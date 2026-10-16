package arena

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	c "github.com/Kayyo321/agent-arena/internal/contract"
)

type testInvoker func(context.Context, Invocation) (map[string]any, error)

func (f testInvoker) Invoke(ctx context.Context, in Invocation) (map[string]any, error) {
	return f(ctx, in)
}
func ordinaryResult() map[string]any {
	return map[string]any{"summary": "validated fixture result", "changedFiles": []string{}, "validation": []any{}, "limitations": []string{}, "nextAction": "continue", "packets": []any{}}
}

func TestWritingTeamPreservesProposalsAndComposesReadOnly(t *testing.T) {
	p := committedProject(t)
	r := teamRun(t, p)
	w, profiles, version, err := p.LoadVersion(r.Workflow.Id)
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"one", "two"} {
		profile := profiles[id]
		profile.Capabilities = []string{"read", "write"}
		profiles[id] = profile
	}
	if _, err := p.Save(w, profiles, version); err != nil {
		t.Fatal(err)
	}
	r, err = p.CreateRun(w, profiles, r.Settings, "Writing team fixture", r.BaseCommit)
	if err != nil {
		t.Fatal(err)
	}
	composition := false
	fake := testInvoker(func(ctx context.Context, in Invocation) (map[string]any, error) {
		if strings.HasSuffix(in.Dir, "-verdict") {
			composition = true
			if in.Write || in.Commands {
				return nil, fmt.Errorf("final composition has writing capabilities")
			}
			for _, id := range []string{"one", "two"} {
				if _, err := os.Stat(filepath.Join(in.Dir, id+".txt")); err != nil {
					return nil, err
				}
			}
		} else if in.AgentID == "one" || in.AgentID == "two" {
			if err := os.WriteFile(filepath.Join(in.Dir, in.AgentID+".txt"), []byte("independent proposal\n"), 0644); err != nil {
				return nil, err
			}
		}
		if in.Schema["properties"].(map[string]any)["verdict"] == nil {
			return ordinaryResult(), nil
		}
		return map[string]any{"verdict": "Approved", "acceptTeamVerdict": true, "summary": "fixture position", "evidence": []any{}, "rationale": "fixture evidence", "nextAction": "inspect"}, nil
	})
	engine := NewEngine(p, r, fake)
	if _, err := p.Append(r.ID, w.Id, "", "workflow.started", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	packet, err := engine.team(context.Background(), w.Teams[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	if !composition || len(stringSlice(packet["arenaChangeIds"])) != 2 {
		t.Fatalf("team lost writing artifacts: %v", packet)
	}
	payload := packet["payload"].(map[string]any)
	if payload["outcome"] != "consensus" {
		t.Fatalf("unexpected verdict: %v", payload)
	}
}

func TestUnavailableTeamMemberCannotProduceConsensus(t *testing.T) {
	p := committedProject(t)
	r := teamRun(t, p)
	fake := testInvoker(func(ctx context.Context, in Invocation) (map[string]any, error) {
		if in.AgentID == "two" {
			return nil, fmt.Errorf("fixture adapter unavailable")
		}
		if in.Schema["properties"].(map[string]any)["verdict"] == nil {
			return ordinaryResult(), nil
		}
		return map[string]any{"verdict": "Approved", "acceptTeamVerdict": true, "summary": "fixture position", "evidence": []any{}, "rationale": "fixture evidence", "nextAction": "inspect"}, nil
	})
	r.Settings.Limits.MaxRetriesPerAgent = 0
	if _, err := p.Append(r.ID, r.Workflow.Id, "", "workflow.started", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	packet, err := NewEngine(p, r, fake).team(context.Background(), r.Workflow.Teams[0], nil)
	if err != nil {
		t.Fatal(err)
	}
	if packet["payload"].(map[string]any)["outcome"] != "no-consensus" {
		t.Fatal("unavailable member was counted as consensus")
	}
}
func teamRun(t *testing.T, p *Project) Run {
	w := NewWorkflow("team-test", "Team test")
	w.HeadAgent = "head"
	profiles := map[string]Profile{}
	for _, id := range []string{"head", "one", "two", "specialist"} {
		level := Int(1)
		activation := "path"
		if id == "head" {
			level = Int(0)
		}
		if id == "specialist" {
			level = nil
			activation = "on-call"
		}
		w.Agents = append(w.Agents, Agent{Id: id, Profile: ".agents/" + w.Id + "/" + id + ".agent.json", Level: level, Activation: activation, Runtime: Runtime{Provider: "codex", Model: "explicit-test-model", ReasoningEffort: "high"}})
		profiles[id] = NewProfile(id, "research", "Inspect evidence", "Return the requested structured result", []string{"read"})
	}
	w.Settings.MaxConcurrentAgents = 1
	w.Teams = []Team{{Id: "review-team", Name: "Review team", Members: []string{"one", "two"}, Facilitator: "one", Consensus: c.WorkflowTeamConsensus{Mode: "unanimous-deliberation", MaxRounds: 2}, OutputContract: c.WorkflowTeamOutputContract{Format: "arena.team-verdict.v1", Verdicts: []string{"Approved", "ChangesRequested"}}}}
	w.Relationships = []Relationship{{Type: "delegates", From: "head", To: "review-team"}}
	w.OnCall = []c.WorkflowOnCallRule{{AgentId: "specialist", MaxInvocationsPerRun: 1, Triggers: []Trigger{{Type: "event", Event: "team.no-consensus", TeamId: "review-team"}, {Type: "manual"}}}}
	s, e := p.Settings()
	if e != nil {
		t.Fatal(e)
	}
	if e := ValidateWorkflow(w, profiles, s); e != nil {
		t.Fatal(e)
	}
	if _, e := p.Save(w, profiles, ""); e != nil {
		t.Fatal(e)
	}
	head, e := Git(p.Root, "rev-parse", "HEAD")
	if e != nil {
		t.Fatal(e)
	}
	r, e := p.CreateRun(w, profiles, s, "Fixture task", head)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func TestTeamsRequireEveryAffirmationAndRespectOnCallCaps(t *testing.T) {
	for _, unanimous := range []bool{true, false} {
		t.Run(fmt.Sprint(unanimous), func(t *testing.T) {
			p := committedProject(t)
			r := teamRun(t, p)
			fake := testInvoker(func(ctx context.Context, in Invocation) (map[string]any, error) {
				props := in.Schema["properties"].(map[string]any)
				if props["verdict"] == nil {
					return ordinaryResult(), nil
				}
				verdict := "Approved"
				if !unanimous && filepath.Base(in.Dir) == "two" {
					verdict = "ChangesRequested"
				}
				return map[string]any{"verdict": verdict, "acceptTeamVerdict": true, "summary": "fixture position", "evidence": []any{}, "rationale": "fixture evidence", "nextAction": "inspect"}, nil
			})
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			engine := NewEngine(p, r, fake)
			if e := engine.Execute(ctx, false, false); e != nil {
				t.Fatal(e)
			}
			events, e := p.Events(r.ID)
			if e != nil {
				t.Fatal(e)
			}
			outcome := "no-consensus"
			if unanimous {
				outcome = "consensus"
			}
			found := false
			onCall := 0
			for _, event := range events {
				if event.Type == "team."+outcome {
					packet, e := p.ReadPacket(r.ID, str(event.Data["messageId"]))
					if e != nil {
						t.Fatal(e)
					}
					if e := ValidatePacket(packet, r.Workflow); e != nil {
						t.Fatal(e)
					}
					found = true
				}
				if event.Type == "on-call.started" {
					onCall++
				}
			}
			if !found {
				t.Fatal("no structured team result")
			}
			if unanimous && onCall != 0 || !unanimous && onCall != 1 {
				t.Fatalf("wrong on-call count %d", onCall)
			}
			if !unanimous {
				newEngine := NewEngine(p, r, fake)
				if e := newEngine.onCall(ctx, "specialist", Event{ID: "manual-event", Type: "manual", AgentID: "user"}, true); e != nil {
					t.Fatal(e)
				}
				events, e := p.Events(r.ID)
				if e != nil {
					t.Fatal(e)
				}
				count := 0
				for _, event := range events {
					if event.Type == "on-call.started" {
						count++
					}
				}
				if count != 1 {
					t.Fatal("fresh process bypassed invocation cap")
				}
			}
		})
	}
}
func TestRouterMissingFieldDoesNotMatchNull(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	router := Router{Id: "route", InputKind: "result", PayloadPointer: "/missing", Routes: []c.WorkflowDecisionRoute{{Id: "null-branch", Equals: nil, To: "head"}}, Fallback: c.WorkflowDecisionRouterFallback{Action: "stop"}}
	packet := NewPacket(r.ID, "result", "head", "codex", "route", "arena", "", ordinaryResult(), Runtime{Provider: "codex"})
	engine := NewEngine(p, r, nil)
	_, targets, e := engine.route(context.Background(), router, []Packet{packet})
	if e != nil {
		t.Fatal(e)
	}
	if len(targets) != 0 {
		t.Fatal("missing value matched explicit null")
	}
	value, exists, e := LookupPointer(map[string]any{"a": []any{map[string]any{"b/c": true}}}, "/a/0/b~1c")
	if e != nil || !exists || value != true {
		t.Fatal("JSON pointer array/escape resolution failed")
	}
}
func TestPacketPreservesOpaqueProviderValues(t *testing.T) {
	p := Packet{"providerContext": map[string]any{"model": "provider/model:variant", "reasoningEffort": "custom-effort", "permissions": map[string]any{"providerField": "verbatim"}}}
	copy := convert[Packet](p)
	if string(JSON(copy)) != string(JSON(p)) {
		t.Fatal("opaque provider context changed")
	}
}
func TestMCPScopesContextAndRejectsForeignActor(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}
{"jsonrpc":"2.0","id":2,"method":"tools/call","params":{"name":"arena_context_search","arguments":{"query":"fact"}}}
`
	var output strings.Builder
	if e := ServeMCP(context.Background(), p, r, "head", strings.NewReader(input), &output); e != nil {
		t.Fatal(e)
	}
	if !strings.Contains(output.String(), `"isError":false`) {
		t.Fatal(output.String())
	}
	if e := ServeMCP(context.Background(), p, r, "foreign", strings.NewReader(input), &output); e == nil {
		t.Fatal("foreign MCP actor accepted")
	}
}

func TestOpaqueLargeNumbersAndExactRouterScalars(t *testing.T) {
	var runtime Runtime
	if e := DecodeJSON([]byte(`{"provider":"local","model":"model","options":{"seed":9007199254740993}}`), &runtime); e != nil {
		t.Fatal(e)
	}
	encoded := string(JSON(runtime))
	if !strings.Contains(encoded, "9007199254740993") {
		t.Fatal("opaque provider number changed")
	}
	if scalarKey(json.Number("1.00")) != scalarKey(json.Number("1e0")) {
		t.Fatal("equivalent JSON numbers differ")
	}
	if scalarKey(json.Number("9007199254740993")) == scalarKey(json.Number("9007199254740992")) {
		t.Fatal("distinct large JSON numbers collapsed")
	}
	if scalarKey(json.Number("1e10000000000")) == scalarKey(json.Number("2e10000000000")) {
		t.Fatal("large exponent numbers collapsed")
	}
}

func TestTeamAgreeingOnChangesRequestedReachesConsensus(t *testing.T) {
	p := committedProject(t)
	r := teamRun(t, p)
	asked := map[string]int{}
	fake := testInvoker(func(ctx context.Context, in Invocation) (map[string]any, error) {
		props := in.Schema["properties"].(map[string]any)
		if props["verdict"] == nil {
			return ordinaryResult(), nil
		}
		if props["affirm"] != nil && props["acceptTeamVerdict"] == nil && props["rationale"] == nil {
			return nil, fmt.Errorf("deliberation still asks for an ambiguous affirm flag")
		}
		reply := map[string]any{"verdict": "ChangesRequested", "summary": "needs fixes", "evidence": []any{}, "rationale": "fixture", "nextAction": "fix", "affirm": false}
		if props["acceptTeamVerdict"] != nil {
			asked[in.AgentID]++
			if !strings.Contains(in.Prompt, "not asking whether you approve the change") {
				return nil, fmt.Errorf("affirmation question does not explain itself")
			}
			// Reproduces the live failure: same verdict, but declined at first.
			reply["acceptTeamVerdict"] = in.AgentID != "two" || asked[in.AgentID] > 1
		}
		return reply, nil
	})
	engine := NewEngine(p, r, fake)
	if _, err := p.Append(r.ID, r.Workflow.Id, "", "workflow.started", map[string]any{}); err != nil {
		t.Fatal(err)
	}
	seed, err := p.Workspace(r, "one", "seed", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(seed.Path, "reviewed.txt"), []byte("code under review\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := engine.finishWorkspace(context.Background(), seed); err != nil {
		t.Fatal(err)
	}
	var finished Workspace
	if err := ReadJSON(filepath.Join(p.RunDir(r.ID), "workspaces", "seed.json"), &finished); err != nil || finished.LastChange == "" {
		t.Fatalf("seed change missing: %v", err)
	}
	reviewed := NewPacket(r.ID, "result", "builder", "codex", "review-team", "arena", "", ordinaryResult(), Runtime{Provider: "codex"})
	reviewed["arenaChangeIds"] = []string{finished.LastChange}
	packet, err := engine.team(context.Background(), r.Workflow.Teams[0], []Packet{reviewed})
	if err != nil {
		t.Fatal(err)
	}
	payload := packet["payload"].(map[string]any)
	if payload["outcome"] != "consensus" || payload["verdict"] != "ChangesRequested" {
		t.Fatalf("agreeing reviewers did not reach consensus: %v", payload)
	}
	if asked["two"] != 2 {
		t.Fatalf("contradictory affirmation was not clarified: %v", asked)
	}
	if !Has(stringSlice(packet["arenaChangeIds"]), finished.LastChange) {
		t.Fatalf("team result dropped the reviewed changes: %v", packet["arenaChangeIds"])
	}
}
