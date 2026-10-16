package arena

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"
)

// maxInboxWait bounds how long one inbox call may block for a peer reply.
const maxInboxWait = 120

func ServeMCP(ctx context.Context, p *Project, r Run, actor string, in io.Reader, out io.Writer) error {
	agent, ok := r.agent(actor)
	if !ok {
		return fmt.Errorf("MCP actor is not in this workflow")
	}
	scanner := bufio.NewScanner(in)
	scanner.Buffer(make([]byte, 64<<10), 8<<20)
	initialized := false
	calls := 0
	returned := map[string]bool{}
	peers := []string{}
	for _, a := range r.Workflow.Agents {
		if a.Id != actor && AllowedPeer(r.Workflow, actor, a.Id) {
			peers = append(peers, a.Id)
		}
	}
	peerNote := "No peer links are configured for you."
	if len(peers) > 0 {
		peerNote = "Your peers: " + strings.Join(peers, ", ") + "."
	}
	for scanner.Scan() {
		var request struct {
			JSONRPC string          `json:"jsonrpc"`
			ID      json.RawMessage `json:"id"`
			Method  string          `json:"method"`
			Params  map[string]any  `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &request); err != nil {
			return fmt.Errorf("invalid MCP request")
		}
		if len(request.ID) == 0 {
			continue
		}
		response := map[string]any{"jsonrpc": "2.0", "id": request.ID}
		var result any
		var err error
		switch request.Method {
		case "initialize":
			version := str(request.Params["protocolVersion"])
			if !Has([]string{"2025-06-18", "2025-11-25"}, version) {
				version = "2025-06-18"
			}
			result = map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{}}, "serverInfo": map[string]any{"name": "Agent Arena", "version": Version}}
			initialized = true
		case "ping":
			result = map[string]any{}
		case "tools/list":
			if !initialized {
				err = fmt.Errorf("initialize first")
				break
			}
			result = map[string]any{"tools": []any{map[string]any{"name": "arena_send", "description": "Route an Arena packet under the active workflow's permissions and policy. To talk to a peer agent use kind \"message\" with to=<peer id> and payloadJson like {\"text\":\"...\"}; text is required and structured fields may ride alongside it. If the user must first approve sharing with that peer's provider, the result has status \"awaiting-approval\" and Arena delivers the message once approved: do not resend it. " + peerNote + " Use \"context-proposal\" (to=context-manager) for durable workflow context, payload {\"workflowId\":\"" + r.Workflow.Id + "\",\"operation\":\"add\",\"title\":\"...\",\"content\":\"...\",\"tags\":[\"...\"],\"confidence\":\"verified|inferred|unverified\",\"evidence\":[{\"kind\":\"file|url|decision|message|other\",\"reference\":\"...\"}],\"entryId\":null}, and \"coworker-request\" only to ask the orchestrator for a NEW agent (payload needs workflowId \"" + r.Workflow.Id + "\", role, objective, reason and runtime).", "inputSchema": objectSchema(map[string]any{"to": map[string]any{"type": "string"}, "kind": map[string]any{"type": "string", "enum": []string{"message", "context-proposal", "coworker-request"}}, "payloadJson": map[string]any{"type": "string"}})}, map[string]any{"name": "arena_inbox", "description": "Read peer messages delivered to this agent in the current run, oldest first. To wait for a peer's reply before continuing, pass newOnly=true and waitSeconds (up to " + fmt.Sprint(maxInboxWait) + "): the call returns as soon as a message you have not seen arrives, or an empty list on timeout.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"waitSeconds": map[string]any{"type": "integer", "minimum": 0, "maximum": maxInboxWait}, "newOnly": map[string]any{"type": "boolean"}}, "additionalProperties": false}}, map[string]any{"name": "arena_context_search", "description": "Search durable context for the active workflow only. Matches entries sharing any meaningful word with the query, best matches first; an empty query lists every entry. Search before proposing an entry so you revise instead of duplicating.", "inputSchema": objectSchema(map[string]any{"query": map[string]any{"type": "string"}})}, map[string]any{"name": "arena_ask_user", "description": "Ask the user a decision through an audited Arena question envelope and wait for their answer.", "inputSchema": objectSchema(map[string]any{"question": map[string]any{"type": "string"}})}}}
		case "tools/call":
			if !initialized {
				err = fmt.Errorf("initialize first")
				break
			}
			calls++
			if calls > 1000 {
				err = fmt.Errorf("Arena MCP invocation limit reached")
				break
			}
			name := str(request.Params["name"])
			args, _ := request.Params["arguments"].(map[string]any)
			var value any
			switch name {
			case "arena_send":
				payload := map[string]any{}
				if err = json.Unmarshal([]byte(str(args["payloadJson"])), &payload); err == nil {
					value, err = p.AgentPacket(ctx, r, actor, map[string]any{"to": args["to"], "kind": args["kind"], "payload": payload})
				}
			case "arena_inbox":
				wait, _ := args["waitSeconds"].(float64)
				newOnly, _ := args["newOnly"].(bool)
				var packets []Packet
				packets, err = p.WaitInbox(ctx, r, actor, time.Duration(min(max(wait, 0), maxInboxWait))*time.Second, returned, newOnly)
				for _, packet := range packets {
					returned[str(packet["messageId"])] = true
				}
				value = packets
			case "arena_context_search":
				var entries []ContextEntry
				entries, err = p.ContextEntries(r.Workflow.Id, false)
				if err == nil {
					entries = ContextSearch(entries, str(args["query"]))
					if len(entries) > 20 {
						entries = entries[:20]
					}
					for _, entry := range entries {
						if entry.CreatedBy.Provider != nil {
							err = p.ShareContext(ctx, r, actor, *entry.CreatedBy.Provider, actor, agent.Runtime.Provider, "shared context entry", map[string]any{"entryId": entry.Id, "createdBy": entry.CreatedBy})
							if err != nil {
								break
							}
						}
					}
					if err != nil {
						break
					}
					value = entries
					_, err = p.Append(r.ID, r.Workflow.Id, actor, "context.searched", map[string]any{"entryCount": len(entries)})
				}
			case "arena_ask_user":
				question := str(args["question"])
				packet := NewPacket(r.ID, "question", actor, agent.Runtime.Provider, "user", "arena", "", map[string]any{"question": question, "decisionNeeded": "user answer"}, agent.Runtime)
				if _, err = p.Deliver(r, packet); err == nil {
					value, err = p.Decide(ctx, r, actor, "question", question, nil, nil)
					if err == nil {
						reply := NewPacket(r.ID, "message", "user", "arena", actor, agent.Runtime.Provider, str(packet["correlationId"]), map[string]any{"text": value}, Runtime{Provider: "arena"})
						reply["replyTo"] = packet["messageId"]
						_, err = p.Deliver(r, reply)
					}
				}
			default:
				err = fmt.Errorf("unknown Arena tool")
			}
			text := string(SafeJSON(value))
			isError := err != nil
			if err != nil {
				text = Redact(err.Error())
			}
			result = map[string]any{"content": []map[string]any{{"type": "text", "text": text}}, "isError": isError}
			err = nil
		default:
			err = fmt.Errorf("unsupported MCP method %s", request.Method)
		}
		if err != nil {
			response["error"] = map[string]any{"code": -32602, "message": Redact(err.Error())}
		} else {
			response["result"] = result
		}
		b, err := json.Marshal(response)
		if err != nil {
			return err
		}
		if _, err = out.Write(append(b, '\n')); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return scanner.Err()
}
