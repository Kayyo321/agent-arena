package arena

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	c "github.com/Kayyo321/agent-arena/internal/contract"
)

// Exercises the actual CLI/MCP bridge without making an inference request.
func TestLiveCodexMCPBridgeDiscovery(t *testing.T) {
	exe := os.Getenv("ARENA_INTEGRATION_BINARY")
	model := os.Getenv("ARENA_LIVE_CODEX_MODEL")
	if exe == "" || model == "" {
		t.Skip("explicit Codex/binary integration selection required")
	}
	p := committedProject(t)
	r := teamRun(t, p)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	argv, _ := json.Marshal([]string{"mcp", "--project", p.Root, "--run", r.ID, "--actor", "head"})
	rpc, err := StartRPC(ctx, p.Root, "-c", "mcp_servers.arena.command="+fmt.Sprintf("%q", exe), "-c", "mcp_servers.arena.args="+string(argv))
	if err != nil {
		t.Fatal(err)
	}
	defer rpc.Close()
	thread, err := rpc.Call(ctx, "thread/start", map[string]any{"model": model, "cwd": p.Root, "sandbox": "read-only", "approvalPolicy": "never"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var started struct {
		Thread struct {
			ID string `json:"id"`
		} `json:"thread"`
	}
	if err := json.Unmarshal(thread, &started); err != nil {
		t.Fatal(err)
	}
	response, err := rpc.Call(ctx, "mcpServer/tool/call", map[string]any{"threadId": started.Thread.ID, "server": "arena", "tool": "arena_context_search", "arguments": map[string]any{"query": "baseline"}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Log("real MCP response", Redact(string(response)))
	events, err := p.Events(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	for _, event := range events {
		if event.Type == "context.searched" {
			return
		}
	}
	t.Fatal("CLI MCP call did not reach workflow context manager")
}

// This opt-in test spends provider usage. Fixtures and normal tests never run it.
func TestLiveCodexEmbeddedInterview(t *testing.T) {
	model, effort := os.Getenv("ARENA_LIVE_CODEX_MODEL"), os.Getenv("ARENA_LIVE_CODEX_EFFORT")
	if model == "" || effort == "" {
		t.Skip("explicitly select a live Codex model and effort")
	}
	p := committedProject(t)
	s := DefaultSettings(Runtime{Provider: "codex", Model: model, ReasoningEffort: effort})
	if _, err := p.Commit("live interview runtime", map[string][]byte{"settings.arena.json": JSON(s)}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	session := newInterviewSession(ctx, p, s)
	defer session.close()
	cmd := session.turn("I want to create a new project. I have not selected any worker roles, models or effort. Ask a concise question for missing required choices. Do not complete or save a draft.", setupDesign)
	for {
		msg := cmd().(interviewEvent)
		if msg.Question != nil {
			msg.Question.Reply <- "Ask for missing choices in your structured question response."
		}
		if msg.Result != nil {
			if msg.Result.Err != nil {
				t.Fatal(msg.Result.Err)
			}
			if msg.Result.Question == "" || msg.Result.Draft != nil {
				t.Fatal("interview guessed missing choices")
			}
			t.Log("REAL EMBEDDED CODEX INTERVIEW", model, effort, msg.Result.Question)
			break
		}
		cmd = waitInterview(msg.Next)
	}
	ids, err := p.List()
	if err != nil || len(ids) != 0 {
		t.Fatal("read-only interview changed workflows")
	}
	assertNoLaunchRun(t, p)
}

func TestLiveCodexReadOnlyWorkflow(t *testing.T) {
	model := os.Getenv("ARENA_LIVE_CODEX_MODEL")
	effort := os.Getenv("ARENA_LIVE_CODEX_EFFORT")
	if model == "" || effort == "" {
		t.Skip("set both ARENA_LIVE_CODEX_MODEL and ARENA_LIVE_CODEX_EFFORT to explicitly authorize a real Codex invocation")
	}
	p := committedProject(t)
	runtime := Runtime{Provider: "codex", Model: model, ReasoningEffort: effort}
	s := DefaultSettings(runtime)
	s.Limits.MaxRetriesPerAgent = 0
	if _, e := p.Commit("live test settings", map[string][]byte{"settings.arena.json": JSON(s)}); e != nil {
		t.Fatal(e)
	}
	w := NewWorkflow("live-read", "Live read test")
	w.HeadAgent = "reader"
	w.Agents = []Agent{{Id: "reader", Profile: ".agents/live-read/reader.agent.json", Level: Int(0), Runtime: runtime}}
	profiles := map[string]Profile{"reader": NewProfile("reader", "research", "Verify read-only handoff", "Read base.txt in the assigned workspace. Return a concise structured result mentioning its actual content. Do not write or run validation commands; use empty validation and changedFiles arrays, and an empty packets array.", []string{"read"})}
	if os.Getenv("ARENA_INTEGRATION_BINARY") != "" {
		profile := profiles["reader"]
		profile.Instructions += " Before returning, call Arena's arena_context_search MCP tool with query baseline to verify the live workflow context channel. An empty context result is valid."
		profiles["reader"] = profile
	}
	if _, e := p.Save(w, profiles, ""); e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	r, _, e := prepareNewExecution(ctx, p, w.Id, "Read base.txt and report its content without changing files.")
	if e != nil {
		t.Fatal(e)
	}
	engine := NewEngine(p, r, Providers{})
	engine.Observe = func(e Event) {
		t.Log(e.Sequence, e.Type, e.AgentID)
		if strings.HasPrefix(e.Type, "provider.") {
			t.Log(string(SafeJSON(e.Data)))
		}
	}
	if e := engine.Execute(ctx, false, false); e != nil {
		t.Fatal(e)
	}
	state, e := p.State(r.ID)
	if e != nil || state.Status != "succeeded" {
		t.Fatalf("state %v %v", state, e)
	}
	events, e := p.Events(r.ID)
	if e != nil {
		t.Fatal(e)
	}
	found := false
	contextSearched := false
	for _, event := range events {
		if event.Type == "context.searched" {
			contextSearched = true
		}
		if event.Type == "node.completed" {
			packet, e := p.ReadPacket(r.ID, str(event.Data["messageId"]))
			if e != nil {
				t.Fatal(e)
			}
			payload, _ := packet["payload"].(map[string]any)
			t.Log("real result", string(SafeJSON(payload)))
			if !strings.Contains(strings.ToLower(str(payload["summary"])), "baseline") {
				t.Fatalf("provider did not report actual workspace content: %v", payload)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("no retained provider result")
	}
	if os.Getenv("ARENA_INTEGRATION_BINARY") != "" && !contextSearched {
		t.Fatal("real Codex invocation did not exercise the Arena MCP context tool")
	}
	t.Log("REAL CODEX INFERENCE VERIFIED", model, effort)
}

// This exit check intentionally remains skipped until a second approved runtime
// is supplied. Its JSON contains model/options only, never credential values.
func TestLiveMultiProviderTeamWorkflow(t *testing.T) {
	model, effort, route := os.Getenv("ARENA_LIVE_CODEX_MODEL"), os.Getenv("ARENA_LIVE_CODEX_EFFORT"), os.Getenv("ARENA_LIVE_SECOND_RUNTIME")
	if model == "" || effort == "" || route == "" {
		t.Skip("live M3 exit pending: explicitly select Codex model/effort and ARENA_LIVE_SECOND_RUNTIME")
	}
	var second Runtime
	if err := DecodeJSON([]byte(route), &second); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRuntime(second); err != nil {
		t.Fatal(err)
	}
	if second.Provider == "codex" {
		t.Fatal("M3 exit requires a different approved provider/backend")
	}
	first := Runtime{Provider: "codex", Model: model, ReasoningEffort: effort}
	p := committedProject(t)
	s := DefaultSettings(first)
	s.Limits.MaxRetriesPerAgent = 0
	s.ApprovalPolicies.CrossProviderContextSharing = "task-scoped"
	if _, err := p.Commit("live multi-provider test settings", map[string][]byte{"settings.arena.json": JSON(s)}); err != nil {
		t.Fatal(err)
	}
	w := NewWorkflow("live-team", "Live multi-provider verification")
	w.HeadAgent = "head"
	profiles := map[string]Profile{}
	for _, id := range []string{"head", "one", "two"} {
		runtime := first
		level := 1
		if id == "head" {
			level = 0
		}
		if id == "two" {
			runtime = second
		}
		w.Agents = append(w.Agents, Agent{Id: id, Level: Int(level), Profile: ".agents/" + w.Id + "/" + id + ".agent.json", Runtime: runtime})
		profiles[id] = NewProfile(id, "research", "Verify a cross-provider handoff", "Read base.txt in the assigned workspace. Report its actual content. Do not write or run validation commands. Return the requested schema. For team positions choose Verified only when the file contains baseline; otherwise NeedsMoreInformation. Affirm only a supported verdict. Use empty evidence and packets arrays when applicable.", []string{"read"})
	}
	w.Teams = []Team{{Id: "verification", Name: "Verification", Members: []string{"one", "two"}, Facilitator: "one", Consensus: c.WorkflowTeamConsensus{Mode: "unanimous-deliberation", MaxRounds: 1}, OutputContract: c.WorkflowTeamOutputContract{Format: "arena.team-verdict.v1", Verdicts: []string{"Verified", "NeedsMoreInformation"}}}}
	w.Relationships = []Relationship{{Type: "delegates", From: "head", To: "verification"}}
	if _, err := p.Save(w, profiles, ""); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	for _, runtime := range []Runtime{first, second} {
		if ready := Probe(ctx, p.Root, runtime); !ready.CanStart {
			t.Fatalf("real adapter not ready: %+v", ready)
		}
	}
	head, err := Git(p.Root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	r, err := p.CreateRun(w, profiles, s, "Read base.txt, pass its finding through the two-provider verification team, and return one structured team verdict.", head)
	if err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(p, r, Providers{})
	if err := engine.Execute(ctx, false, false); err != nil {
		t.Fatal(err)
	}
	events, err := p.Events(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, event := range events {
		if event.Type != "team.consensus" && event.Type != "team.no-consensus" {
			continue
		}
		packet, err := p.ReadPacket(r.ID, str(event.Data["messageId"]))
		if err != nil {
			t.Fatal(err)
		}
		if err := ValidatePacket(packet, w); err != nil {
			t.Fatal(err)
		}
		payload, _ := packet["payload"].(map[string]any)
		t.Logf("team packet: %s outcome=%v verdict=%v", event.Type, payload["outcome"], payload["verdict"])
		for _, position := range anySlice(payload["memberPositions"]) {
			entry, _ := position.(map[string]any)
			t.Logf("member %v (%v, verdict %v): %v", entry["agentId"], entry["status"], entry["verdict"], Redact(short(str(entry["summary"]), 200)))
			if entry["status"] == "unavailable" {
				t.Fatalf("a real adapter did not return a position: %v", entry)
			}
			if !strings.Contains(strings.ToLower(str(entry["summary"])), "baseline") {
				t.Fatalf("provider did not verify actual content: %v", entry)
			}
		}
		found = true
	}
	if !found {
		t.Fatal("missing auditable team verdict")
	}
	t.Log("REAL MULTI-PROVIDER WORKFLOW VERIFIED", model, effort, second.Provider, second.Model)
}
