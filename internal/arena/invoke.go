package arena

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type Invocation struct {
	Attachments                 []Attachment
	AttachmentDir               string
	Diagnostic                  func(string, map[string]any) error
	Delivered                   func() error
	Usage                       func(map[string]any) error
	ProjectRoot, RunID, AgentID string
	Packet                      func(context.Context, map[string]any) (any, error)
	Runtime                     Runtime
	Dir                         string
	Prompt                      string
	Schema                      map[string]any
	Write                       bool
	Commands                    bool
	Approve                     func(context.Context, string, map[string]any) (bool, error)
	Question                    func(context.Context, string) (string, error)
	Output                      func(string) error
}
type Invoker interface {
	Invoke(context.Context, Invocation) (map[string]any, error)
}
type Providers struct{}

func (Providers) Invoke(ctx context.Context, in Invocation) (map[string]any, error) {
	switch in.Runtime.Provider {
	case "codex":
		return invokeCodex(ctx, in)
	case "claude-code":
		if claudeSignIn(in.Runtime) {
			return invokeClaudeNative(ctx, in)
		}
		return invokeClaude(ctx, in)
	case "antigravity":
		return invokeAntigravity(ctx, in)
	case "gemini":
		return invokeGemini(ctx, in)
	case "local":
		return invokeLocal(ctx, in)
	}
	return nil, fmt.Errorf("unsupported provider %s", in.Runtime.Provider)
}
func invokeCodex(ctx context.Context, in Invocation) (map[string]any, error) {
	overrides := []string{}
	if exe, argv, ok := arenaMCPCommand(in); ok {
		b, _ := json.Marshal(argv)
		overrides = append(overrides, "-c", "mcp_servers.arena.command="+fmt.Sprintf("%q", exe), "-c", "mcp_servers.arena.args="+string(b))
		in.Prompt += "\nUse Arena MCP tools for live peer messaging, workflow context proposals, coworker requests, and user questions."
	}
	rpc, e := StartRPC(ctx, in.Dir, overrides...)
	if e != nil {
		return nil, e
	}
	defer rpc.Close()
	sandbox := "read-only"
	if in.Write {
		sandbox = "workspace-write"
	}
	approval := "on-request"
	if in.Approve == nil {
		approval = "never"
	}
	params := map[string]any{"model": in.Runtime.Model, "cwd": in.Dir, "sandbox": sandbox, "approvalPolicy": approval, "developerInstructions": nativeWorkerInstructions, "config": map[string]any{"model_reasoning_effort": in.Runtime.ReasoningEffort, "features.multi_agent": false}}
	// Opaque options are retained in the envelope. Only documented supported options
	// are translated here; they may never override Arena's workspace/approval policy.
	if cfg, ok := in.Runtime.Options["config"].(map[string]any); ok {
		safe := params["config"].(map[string]any)
		for k, v := range cfg {
			if k != "model_reasoning_effort" && k != "service_tier" {
				return nil, fmt.Errorf("Codex config %s is not supported by this adapter", k)
			}
			if k == "model_reasoning_effort" && v != in.Runtime.ReasoningEffort {
				return nil, fmt.Errorf("Codex config effort conflicts with the selected reasoningEffort")
			}
			safe[k] = v
		}
	}
	thread, e := rpc.Call(ctx, "thread/start", params, nil)
	if e != nil {
		return nil, e
	}
	var t struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if e = json.Unmarshal(thread, &t); e != nil || t.Thread.ID == "" {
		return nil, fmt.Errorf("unrecognized Codex thread response")
	}
	if len(overrides) > 0 {
		catalog, err := rpc.Call(ctx, "mcpServerStatus/list", map[string]any{"threadId": t.Thread.ID, "serverName": "arena", "limit": 1}, nil)
		if err != nil {
			return nil, fmt.Errorf("Arena MCP discovery failed before inference: %w", err)
		}
		var status struct {
			Data []struct {
				Name    string         `json:"name"`
				Tools   map[string]any `json:"tools"`
				Error   string         `json:"toolsError"`
				Runtime string         `json:"runtimeStatus"`
			} `json:"data"`
		}
		if err := json.Unmarshal(catalog, &status); err != nil {
			return nil, err
		}
		ready := false
		for _, server := range status.Data {
			if server.Name == "arena" {
				ready = len(server.Tools) >= 4
				if !ready {
					return nil, fmt.Errorf("Arena MCP unavailable before inference: %s (%s)", Redact(server.Error), server.Runtime)
				}
			}
		}
		if !ready {
			return nil, fmt.Errorf("Arena MCP server was not discovered before inference")
		}
	}
	text := ""
	done := false
	var turnErr error
	handle := func(m RPCMessage) error {
		if len(m.ID) > 0 && m.Method != "" {
			if in.Diagnostic != nil {
				if err := in.Diagnostic("provider.requested", map[string]any{"method": m.Method}); err != nil {
					return err
				}
			}
			response := map[string]any{}
			switch m.Method {
			case "mcpServer/elicitation/request":
				response["action"] = "decline"
				if arenaToolConfirmation(m.Params, t.Thread.ID) {
					// The local bridge enforces graph/context/inquiry policy for each
					// call. This confirms that bridge, not external MCP access.
					response["action"], response["content"] = "accept", map[string]any{}
				}
				if in.Diagnostic != nil {
					meta, _ := m.Params["_meta"].(map[string]any)
					if err := in.Diagnostic("provider.mcp-confirmation", map[string]any{"action": response["action"], "server": m.Params["serverName"], "kind": meta["codex_approval_kind"], "tool": meta["tool_name"]}); err != nil {
						return err
					}
				}
			case "item/commandExecution/requestApproval", "item/fileChange/requestApproval":
				allowed := false
				if in.Approve != nil && (in.Commands || m.Method == "item/fileChange/requestApproval" && in.Write) {
					var e error
					allowed, e = in.Approve(ctx, m.Method, m.Params)
					if e != nil {
						return e
					}
				}
				decision := "decline"
				if allowed {
					decision = "accept"
				}
				response["decision"] = decision
			case "item/tool/requestUserInput":
				questions, _ := m.Params["questions"].([]any)
				answers := map[string]any{}
				for _, q := range questions {
					question, _ := q.(map[string]any)
					if question["isSecret"] == true {
						return fmt.Errorf("Arena refuses secret-input requests; use provider-owned credential setup")
					}
					answer := ""
					if in.Question != nil {
						var e error
						answer, e = in.Question(ctx, str(question["question"]))
						if e != nil {
							return e
						}
					}
					answers[str(question["id"])] = map[string]any{"answers": []string{answer}}
				}
				response["answers"] = answers
			default:
				return rpc.Send(map[string]any{"id": json.RawMessage(m.ID), "error": map[string]any{"code": -32601, "message": "Arena cannot authorize this server request"}})
			}
			return rpc.Send(map[string]any{"id": json.RawMessage(m.ID), "result": response})
		}
		switch m.Method {
		case "turn/plan/updated":
			if in.Diagnostic != nil {
				return in.Diagnostic("agent.plan", map[string]any{"plan": m.Params["plan"]})
			}
		case "item/started", "item/completed":
			item, _ := m.Params["item"].(map[string]any)
			if in.Diagnostic != nil {
				if activity := codexPublicActivity(item, m.Method); activity != nil {
					if err := in.Diagnostic("provider.activity", activity); err != nil {
						return err
					}
				}
				if item["type"] == "agentMessage" && item["phase"] == "commentary" && m.Method == "item/completed" {
					if err := in.Diagnostic("agent.progress", map[string]any{"summary": item["text"]}); err != nil {
						return err
					}
				}
			}
			if m.Method == "item/started" {
				return nil
			}
			if item["type"] == "mcpToolCall" && item["server"] == "arena" && in.Diagnostic != nil {
				data := map[string]any{"tool": item["tool"], "status": item["status"], "error": item["error"]}
				if item["status"] == "failed" {
					// Arena's own refusal text tells the user (and the
					// stall diagnosis) why the call failed.
					if message := toolResultText(item["result"]); message != "" {
						data["message"] = short(Redact(message), 500)
					}
				}
				if err := in.Diagnostic("provider.arena-tool-completed", data); err != nil {
					return err
				}
			}
			if item["type"] == "agentMessage" {
				text = str(item["text"])
				if in.Output != nil {
					if e := in.Output(text); e != nil {
						return e
					}
				}
			}
		case "thread/tokenUsage/updated":
			if in.Usage != nil {
				return in.Usage(m.Params)
			}
		case "turn/completed":
			turn, _ := m.Params["turn"].(map[string]any)
			done = true
			if turn["status"] != "completed" {
				turnErr = fmt.Errorf("Codex turn ended with status %s", str(turn["status"]))
			}
		case "error":
			if m.Params["willRetry"] != true {
				turnErr = fmt.Errorf("Codex turn error: %s", Redact(string(JSON(m.Params["error"]))))
			}
		}
		return nil
	}
	turn := map[string]any{"threadId": t.Thread.ID, "input": codexInput(in), "model": in.Runtime.Model, "effort": in.Runtime.ReasoningEffort}
	if in.Schema != nil {
		turn["outputSchema"] = in.Schema
	}
	if _, e := rpc.Call(ctx, "turn/start", turn, handle); e != nil {
		return nil, e
	}
	if in.Delivered != nil {
		if err := in.Delivered(); err != nil {
			return nil, err
		}
	}
	for !done {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case e := <-rpc.errors:
			return nil, e
		case m := <-rpc.messages:
			if e := handle(m); e != nil {
				return nil, e
			}
		}
	}
	if turnErr != nil {
		return nil, turnErr
	}
	return parseOutput(text)
}

// Only public activity is projected into Arena. Reasoning item content and raw
// thought/summary streams are deliberately excluded from logs and UI callbacks.
func codexPublicActivity(item map[string]any, method string) map[string]any {
	if !Has([]string{"commandExecution", "fileChange", "mcpToolCall", "webSearch"}, str(item["type"])) {
		return nil
	}
	data := map[string]any{"type": item["type"], "status": item["status"], "itemId": item["id"]}
	if method == "item/started" {
		data["status"] = "started"
	}
	for _, key := range []string{"command", "tool", "server", "exitCode"} {
		if value, ok := item[key]; ok {
			data[key] = value
		}
	}
	return cleanValue(data).(map[string]any)
}

func arenaToolConfirmation(params map[string]any, threadID string) bool {
	if params["serverName"] != "arena" || params["threadId"] != threadID || params["mode"] != "form" {
		return false
	}
	schema, ok := params["requestedSchema"].(map[string]any)
	if !ok || schema["type"] != "object" {
		return false
	}
	properties, ok := schema["properties"].(map[string]any)
	if !ok || len(properties) != 0 {
		return false
	}
	if required, exists := schema["required"]; exists && required != nil {
		fields, ok := required.([]any)
		if !ok || len(fields) != 0 {
			return false
		}
	}
	meta, _ := params["_meta"].(map[string]any)
	if meta["codex_approval_kind"] != "mcp_tool_call" {
		return false
	}
	for _, key := range []string{"codex_requires_user_input", "codex_sensitive_action", "codex_strict_auto_review"} {
		if value, exists := meta[key]; exists && value != nil && value != false {
			return false
		}
	}
	if reviewer, exists := meta["approvals_reviewer"]; exists && reviewer != nil && reviewer != "user" {
		return false
	}
	// Inspected CLI builds can omit the tool name for their empty confirmation.
	// The pinned local Arena server exposes only these policy-enforced operations.
	name, exists := meta["tool_name"]
	if !exists {
		return true
	}
	tool, ok := name.(string)
	return ok && Has([]string{"arena_context_search", "arena_inbox", "arena_send", "arena_ask_user"}, tool)
}
func parseOutput(text string) (map[string]any, error) {
	text = strings.TrimSpace(text)
	if strings.HasPrefix(text, "```json") {
		text = strings.TrimPrefix(text, "```json")
		text = strings.TrimSuffix(strings.TrimSpace(text), "```")
	}
	var out map[string]any
	if e := DecodeJSON([]byte(text), &out); e != nil || out == nil {
		return nil, fmt.Errorf("provider returned invalid structured output")
	}
	return out, nil
}

func toolResultText(v any) string {
	result, _ := v.(map[string]any)
	for _, item := range anySlice(result["content"]) {
		if content, ok := item.(map[string]any); ok && content["type"] == "text" {
			return str(content["text"])
		}
	}
	return ""
}
