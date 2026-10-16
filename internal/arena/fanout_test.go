package arena

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	c "github.com/Kayyo321/agent-arena/internal/contract"
)

// fanOutWorkflow is lead -> split (planner plans, fixer works per item) ->
// reviewer, with fixer writing in its own worktree per child.
func fanOutWorkflow(s Settings, maxChildren int, approval string) (Workflow, map[string]Profile) {
	w := NewWorkflow("parallel", "Parallel fixes")
	w.HeadAgent = "lead"
	w.Settings.MaxConcurrentAgents = 4
	profiles := map[string]Profile{}
	for _, a := range []struct {
		id    string
		level int
		caps  []string
	}{{"lead", 0, []string{"read"}}, {"planner", 1, []string{"read"}}, {"fixer", 1, []string{"read", "write"}}, {"reviewer", 2, []string{"read"}}} {
		w.Agents = append(w.Agents, Agent{Id: a.id, Level: Int(a.level), Profile: ".agents/parallel/" + a.id + ".agent.json", Runtime: s.DefaultModel})
		profiles[a.id] = NewProfile(a.id, "research", "Do the "+a.id+" work", "Use evidence", a.caps)
	}
	w.FanOuts = []FanOut{{Id: "split", Name: "Parallel fixes", Planner: "planner", Worker: "fixer", MaxChildren: maxChildren, Approval: approval}}
	w.Relationships = []Relationship{{Type: "delegates", From: "lead", To: "split"}, {Type: "delegates", From: "split", To: "reviewer"}}
	return w, profiles
}

// exampleFanOut loads the parallel-fixes example arena.
func exampleFanOut(t *testing.T) (Workflow, map[string]Profile, Settings) {
	t.Helper()
	base := "../../examples/project/.arena"
	var w Workflow
	var s Settings
	for path, v := range map[string]any{"parallel-fixes.arena.json": &w, "settings.arena.json": &s} {
		b, err := os.ReadFile(filepath.Join(base, path))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatal(err)
		}
	}
	profiles := map[string]Profile{}
	for _, a := range w.Agents {
		b, err := os.ReadFile(filepath.Join(base, a.Profile))
		if err != nil {
			t.Fatal(err)
		}
		var p Profile
		if err := json.Unmarshal(b, &p); err != nil {
			t.Fatal(err)
		}
		profiles[a.Id] = p
	}
	return w, profiles, s
}

func TestFanOutExampleFixtureValidates(t *testing.T) {
	w, profiles, s := exampleFanOut(t)
	if err := ValidateWorkflow(w, profiles, s); err != nil {
		t.Fatal(err)
	}
}

func TestFanOutCardFollowsTheJournal(t *testing.T) {
	unicodeTerminal(t)
	w, _, _ := exampleFanOut(t)
	view := func(events []Event) string {
		return strings.Join(wideGraph(w, RunState{Agents: map[string]string{}}, events, 140, false, ""), "\n")
	}
	idle := view(nil)
	for _, part := range []string{"FAN-OUT", "planner → fixer ×4", "max 4 · approval required"} {
		if !strings.Contains(idle, part) {
			t.Fatalf("idle card misses %q:\n%s", part, idle)
		}
	}
	if strings.Contains(idle, "╭─ fixer") || strings.Contains(idle, "role planner") {
		t.Fatalf("planner and worker must stay inside the fan-out card:\n%s", idle)
	}
	event := func(kind string, data map[string]any) Event {
		data["fanOutId"] = "split"
		return Event{AgentID: "split", Type: kind, Data: data}
	}
	events := []Event{event("fanout.started", map[string]any{}), event("decision.requested", map[string]any{})}
	if !strings.Contains(view(events), "waiting for you") {
		t.Fatal("pending approval not shown")
	}
	events = append(events, event("fanout.approved", map[string]any{"count": 3, "dropped": 1}), event("fanout.child.completed", map[string]any{"childId": "split.1"}), event("fanout.child.failed", map[string]any{"childId": "split.2"}))
	if running := view(events); !strings.Contains(running, "1/3 done · 1 failed · 1 dropped") {
		t.Fatalf("progress not shown:\n%s", running)
	}
	// A child's task packet moves along the edge into the fan-out card.
	sent := Event{Type: "message.sent", Data: map[string]any{"messageId": "m1", "kind": "task", "from": map[string]any{"agentId": "coordinator"}, "to": map[string]any{"agentId": "split.3"}}}
	if a, b := wideGraph(w, RunState{Agents: map[string]string{}}, append(events, sent), 140, false, "", 0), wideGraph(w, RunState{Agents: map[string]string{}}, append(events, sent), 140, false, "", 1); strings.Join(a, "\n") == strings.Join(b, "\n") {
		t.Fatal("a child's packet does not animate on the fan-out edge")
	}
	lines := renderGraph(w, RunState{Agents: map[string]string{}}, events, nil, 120, false, false)
	if text := strings.Join(lines, "\n"); !strings.Contains(text, "[FAN-OUT split] fanout.approved; planner planner, worker fixer, max 4") || strings.Contains(text, "fixer · claude-code") {
		t.Fatalf("text graph:\n%s", text)
	}
}

func TestFanOutValidatorRejections(t *testing.T) {
	s := DefaultSettings(Runtime{Provider: "codex", Model: "gpt-test", ReasoningEffort: "high"})
	w, profiles := fanOutWorkflow(s, 4, "required")
	if err := ValidateWorkflow(w, profiles, s); err != nil {
		t.Fatal(err)
	}
	if *s.Limits.MaxFanOutChildrenPerRun != 8 {
		t.Fatalf("default fan-out limit = %d", *s.Limits.MaxFanOutChildrenPerRun)
	}
	older := convert[Settings](s)
	older.Limits.MaxFanOutChildrenPerRun = nil
	if fanOutLimit(older) != defaultFanOutChildren {
		t.Fatal("settings without the limit must fall back to the default")
	}
	tests := map[string]struct {
		mutate func(*Workflow, map[string]Profile, *Settings)
		want   string
	}{
		"over project limit":      {func(w *Workflow, _ map[string]Profile, s *Settings) { s.Limits.MaxFanOutChildrenPerRun = Int(3) }, "exceeds project maxFanOutChildrenPerRun"},
		"same planner and worker": {func(w *Workflow, _ map[string]Profile, _ *Settings) { w.FanOuts[0].Worker = "planner" }, "distinct planner and worker"},
		"missing worker":          {func(w *Workflow, _ map[string]Profile, _ *Settings) { w.FanOuts[0].Worker = "ghost" }, "invalid planner or worker"},
		"level mismatch": {func(w *Workflow, _ map[string]Profile, _ *Settings) {
			w.Agents[2].Level = Int(2)
		}, "same level"},
		"worker as team member": {func(w *Workflow, p map[string]Profile, _ *Settings) {
			w.Agents = append(w.Agents, Agent{Id: "other", Level: Int(1), Profile: ".agents/parallel/other.agent.json", Runtime: w.Agents[0].Runtime})
			p["other"] = NewProfile("other", "research", "o", "o", []string{"read"})
			w.Teams = []Team{{Id: "pair", Name: "Pair", Members: []string{"fixer", "other"}, Facilitator: "other"}}
			w.Teams[0].Consensus.Mode, w.Teams[0].Consensus.MaxRounds = "unanimous-deliberation", 1
			w.Teams[0].OutputContract.Format, w.Teams[0].OutputContract.Verdicts = "arena.team-verdict.v1", []string{"Approved"}
		}, "cannot be a team member"},
		"worker in two fan-outs": {func(w *Workflow, p map[string]Profile, _ *Settings) {
			w.Agents = append(w.Agents, Agent{Id: "planner2", Level: Int(1), Profile: ".agents/parallel/planner2.agent.json", Runtime: w.Agents[0].Runtime})
			p["planner2"] = NewProfile("planner2", "research", "o", "o", []string{"read"})
			w.FanOuts = append(w.FanOuts, FanOut{Id: "split2", Name: "Again", Planner: "planner2", Worker: "fixer", MaxChildren: 2})
			w.Relationships = append(w.Relationships, Relationship{Type: "delegates", From: "lead", To: "split2"})
		}, "serve another fan-out"},
		"on-call worker": {func(w *Workflow, _ map[string]Profile, _ *Settings) {
			w.Agents[2].Activation, w.Agents[2].Level = "on-call", nil
		}, "invalid planner or worker"},
		"edge to worker": {func(w *Workflow, _ map[string]Profile, _ *Settings) {
			w.Relationships = append(w.Relationships, Relationship{Type: "delegates", From: "lead", To: "fixer"})
		}, "cannot reference team members"},
		"worker as head": {func(w *Workflow, _ map[string]Profile, _ *Settings) {
			w.HeadAgent = "planner"
			w.Agents[1].Level = Int(0)
			w.Agents[2].Level = Int(0)
		}, "head must be a standalone path agent"},
		"reserved child ID": {func(w *Workflow, p map[string]Profile, _ *Settings) {
			w.Agents = append(w.Agents, Agent{Id: "split.2", Level: Int(2), Profile: ".agents/parallel/split.2.agent.json", Runtime: w.Agents[0].Runtime})
			p["split.2"] = NewProfile("split.2", "research", "o", "o", []string{"read"})
			w.Relationships = append(w.Relationships, Relationship{Type: "delegates", From: "split", To: "split.2"})
		}, "reserved for a child of fan-out split"},
		"long ID": {func(w *Workflow, _ map[string]Profile, _ *Settings) {
			id := strings.Repeat("f", 56)
			w.FanOuts[0].Id = id
			w.Relationships = []Relationship{{Type: "delegates", From: "lead", To: id}, {Type: "delegates", From: id, To: "reviewer"}}
		}, "longer than 55"},
		"unreachable": {func(w *Workflow, _ map[string]Profile, _ *Settings) {
			w.Relationships = []Relationship{{Type: "delegates", From: "lead", To: "reviewer"}}
		}, "path node split is unreachable"},
		"on-call trigger on worker": {func(w *Workflow, p map[string]Profile, _ *Settings) {
			w.Agents = append(w.Agents, Agent{Id: "medic", Activation: "on-call", Profile: ".agents/parallel/medic.agent.json", Runtime: w.Agents[0].Runtime})
			p["medic"] = NewProfile("medic", "research", "o", "o", []string{"read"})
			w.OnCall = []c.WorkflowOnCallRule{{AgentId: "medic", MaxInvocationsPerRun: 1, Triggers: []Trigger{{Type: "event", Event: "agent.failed", AgentId: "fixer"}}}}
		}, "agent.failed triggers do not fire for fan-out children"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			w, profiles := fanOutWorkflow(s, 4, "required")
			settings := convert[Settings](s)
			test.mutate(&w, profiles, &settings)
			err := ValidateWorkflow(w, profiles, settings)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("got %v, want %q", err, test.want)
			}
		})
	}
	if fanOutChildOf(w, "split.12") != "split" || fanOutChildOf(w, "split.") != "" || fanOutChildOf(w, "split.1a") != "" || fanOutChildOf(w, "splitx.1") != "" {
		t.Fatal("child ID recognition")
	}
}

// splitRun saves w and creates a run of it in a committed project.
func splitRun(t *testing.T, p *Project, w Workflow, profiles map[string]Profile) Run {
	t.Helper()
	s, err := p.Settings()
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateWorkflow(w, profiles, s); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Save(w, profiles, ""); err != nil {
		t.Fatal(err)
	}
	head, err := Git(p.Root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	r, err := p.CreateRun(w, profiles, s, "Fan-out fixture", head)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestFanOutChildrenAndCoworkersShareOneConfigWriter(t *testing.T) {
	p := committedProject(t)
	s, err := p.Settings()
	if err != nil {
		t.Fatal(err)
	}
	w, profiles := fanOutWorkflow(s, 4, "required")
	for _, id := range []string{"planner2", "fixer2"} {
		w.Agents = append(w.Agents, Agent{Id: id, Level: Int(1), Profile: ".agents/parallel/" + id + ".agent.json", Runtime: s.DefaultModel})
		profiles[id] = NewProfile(id, "research", "Do the "+id+" work", "Use evidence", []string{"read", "write"})
	}
	w.FanOuts = append(w.FanOuts, FanOut{Id: "split2", Name: "Second split", Planner: "planner2", Worker: "fixer2", MaxChildren: 3})
	w.Relationships = append(w.Relationships, Relationship{Type: "delegates", From: "lead", To: "split2"}, Relationship{Type: "delegates", From: "split2", To: "reviewer"})
	r := splitRun(t, p, w, profiles)
	e := NewEngine(p, r, nil)
	var wg sync.WaitGroup
	errs := make(chan error, 32)
	for i := 0; i < 8; i++ {
		wg.Add(3)
		go func(n int) {
			defer wg.Done()
			_, err := e.registerChildren(w.FanOuts[0], 1+n%4)
			errs <- err
		}(i)
		go func(n int) {
			defer wg.Done()
			_, err := e.registerChildren(w.FanOuts[1], 1+n%3)
			errs <- err
		}(i)
		go func() {
			defer wg.Done()
			_ = e.profileOf("split.1")
			_, _ = e.agentByID("split2.1")
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	read, err := p.ReadRun(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(read.FanOutChildren) != 7 || len(read.Workflow.Agents) != len(w.Agents) {
		t.Fatalf("children %d, agents %d", len(read.FanOutChildren), len(read.Workflow.Agents))
	}
	child, ok := read.agent("split2.3")
	if !ok || child.Runtime.Model != w.Agents[5].Runtime.Model {
		t.Fatalf("child not resolvable from the persisted run: %v", child)
	}
	if e.profileOf("split.4").Id != "split.4" || !Has(e.profileOf("split.4").Capabilities, "write") {
		t.Fatal("child profile must copy the worker's under the child ID")
	}
	// A coworker approved later goes through the same writer and keeps them.
	helper := Agent{Id: "helper", Level: Int(2), Profile: ".agents/parallel/helper.agent.json", Runtime: s.DefaultModel}
	if err := p.QueueCoworker(r.ID, ConfigUpdate{RequestID: "request-1", Parent: "split", Agent: helper, Profile: NewProfile("helper", "research", "Help", "Help", []string{"read"}), Transaction: "tx"}); err != nil {
		t.Fatal(err)
	}
	if added, err := e.applyCoworkers(); err != nil || added["helper"] != "split" {
		t.Fatalf("coworker not applied: %v %v", added, err)
	}
	if read, err = p.ReadRun(r.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := read.agent("helper"); !ok || len(read.FanOutChildren) != 7 {
		t.Fatalf("coworker write dropped children or the coworker: %d", len(read.FanOutChildren))
	}
	// Each child's MCP bridge resolves its actor from that file.
	in := strings.NewReader(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}` + "\n")
	var out strings.Builder
	if err := ServeMCP(context.Background(), p, read, "split.2", in, &out); err != nil || !strings.Contains(out.String(), "Agent Arena") {
		t.Fatalf("child MCP actor refused: %v %s", err, out.String())
	}
	if err := ServeMCP(context.Background(), p, read, "split.9", strings.NewReader(""), &out); err == nil {
		t.Fatal("unregistered child ID accepted as an MCP actor")
	}
	// Nothing else names the file.
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		b, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if n := strings.Count(string(b), `"current-config.json"`); n > 0 && !strings.HasSuffix(file, "_test.go") && (file != "updates.go" || n != 1) {
			t.Fatalf("%s names current-config.json directly; use writeConfig", file)
		}
	}
	// A redacted run keeps no objective in the file, as in run.json.
	redacted := convert[Run](r)
	redacted.Retention = "redacted-local"
	if err := NewEngine(p, redacted, nil).writeConfig(redacted); err != nil {
		t.Fatal(err)
	}
	if read, err = p.ReadRun(r.ID); err != nil || read.Objective != "" {
		t.Fatalf("objective persisted for a redacted run: %q %v", read.Objective, err)
	}
}

func TestFanOutChildCannotRequestCoworkers(t *testing.T) {
	p := committedProject(t)
	s, err := p.Settings()
	if err != nil {
		t.Fatal(err)
	}
	w, profiles := fanOutWorkflow(s, 4, "required")
	splitRun(t, p, w, profiles)
	_, err = p.RequestCoworker(context.Background(), CoworkerRequest{WorkflowID: w.Id, From: "split.1", AgentID: "extra", Role: "research", Objective: "o", Reason: "r", Capabilities: []string{"read"}, WorkspaceMode: "read-only", Runtime: s.DefaultModel}, true, nil)
	if err == nil || !strings.Contains(err.Error(), "fan-out children cannot request coworkers") {
		t.Fatalf("got %v", err)
	}
}

// fanOutFixture plays every agent of a fan-out run. Children write
// <childId>.txt into their worktree; the reviewer records which of those it
// can see in its own.
type fanOutFixture struct {
	mu       sync.Mutex
	items    []fanOutItem
	ran      map[string]int
	dirs     []string
	saw      []string
	prompts  map[string]string
	planWrit bool
	fail     func(id string) bool
	// block, when set, may hold a child until the test interrupts the run.
	block   func(ctx context.Context, id string) bool
	verdict func(pass int) string
}

func newFanOutFixture(n int) *fanOutFixture {
	f := &fanOutFixture{ran: map[string]int{}, prompts: map[string]string{}}
	for i := 1; i <= n; i++ {
		f.items = append(f.items, fanOutItem{Title: fmt.Sprintf("item %d", i), Instructions: fmt.Sprintf("Fix part %d.", i)})
	}
	return f
}

func (f *fanOutFixture) invoke(ctx context.Context, in Invocation) (map[string]any, error) {
	props := in.Schema["properties"].(map[string]any)
	f.mu.Lock()
	f.ran[in.AgentID]++
	f.prompts[in.AgentID] = in.Prompt
	f.mu.Unlock()
	pass := 1
	if _, n, ok := strings.Cut(filepath.Base(in.Dir), "-iter"); ok {
		fmt.Sscan(n, &pass)
	}
	switch {
	case props["items"] != nil:
		f.mu.Lock()
		f.planWrit = f.planWrit || in.Write
		f.mu.Unlock()
		return map[string]any{"items": convert[[]any](f.items), "rationale": "fixture"}, nil
	case strings.Contains(in.AgentID, "."):
		f.mu.Lock()
		f.dirs = append(f.dirs, filepath.Base(in.Dir))
		f.mu.Unlock()
		if f.block != nil && f.block(ctx, in.AgentID) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		if f.fail != nil && f.fail(in.AgentID) {
			return nil, fmt.Errorf("fixture failure for %s", in.AgentID)
		}
		if err := os.WriteFile(filepath.Join(in.Dir, in.AgentID+".txt"), []byte("child output\n"), 0644); err != nil {
			return nil, err
		}
	case in.AgentID == "reviewer":
		matches, _ := filepath.Glob(filepath.Join(in.Dir, "split*.txt"))
		f.mu.Lock()
		for _, m := range matches {
			f.saw = append(f.saw, filepath.Base(m))
		}
		f.mu.Unlock()
		if props["outcome"] != nil {
			return map[string]any{"format": "arena.review-verdict.v1", "outcome": f.verdict(pass), "summary": "fixture", "rationale": "fixture", "findings": []any{}, "openQuestions": []any{}, "nextAction": "route", "packets": []any{}}, nil
		}
	}
	return ordinaryResult(), nil
}

// startFanOut executes r in the background and reports its result.
func startFanOut(ctx context.Context, p *Project, r Run, f *fanOutFixture, resume bool) chan error {
	done := make(chan error, 1)
	go func() { done <- NewEngine(p, r, testInvoker(f.invoke)).Execute(ctx, resume, false) }()
	return done
}

func fanOutResult(t *testing.T, p *Project, r Run, events []Event, node string) map[string]any {
	t.Helper()
	for i := len(events) - 1; i >= 0; i-- {
		if v := events[i]; v.Type == "node.completed" && str(v.Data["nodeId"]) == node {
			packet, err := p.ReadPacket(r.ID, str(v.Data["messageId"]))
			if err != nil {
				t.Fatal(err)
			}
			if packet["kind"] != "fanout-result" {
				t.Fatalf("fan-out returned %v", packet["kind"])
			}
			return packet["payload"].(map[string]any)
		}
	}
	t.Fatalf("%s never completed", node)
	return nil
}

func finished(t *testing.T, p *Project, r Run, done chan error) []Event {
	t.Helper()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	events, err := p.Events(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := Reduce(events); err != nil || state.Status != "succeeded" {
		t.Fatalf("run ended %s: %v", state.Status, err)
	}
	return events
}

func TestFanOutApprovalRequiredGatesChildrenAndPinsTheirChanges(t *testing.T) {
	p := committedProject(t)
	s, _ := p.Settings()
	w, profiles := fanOutWorkflow(s, 4, "required")
	r := splitRun(t, p, w, profiles)
	f := newFanOutFixture(3)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	done := startFanOut(ctx, p, r, f, false)
	d := awaitDecision(t, p, r.ID)
	if d.Kind != "fanout" || d.AgentID != "split" || strings.Join(d.Choices, ",") != "approve,stop" || !strings.Contains(d.Prompt, "3. item 3") {
		t.Fatalf("approval decision %+v", d)
	}
	f.mu.Lock()
	ran := f.ran["split.1"] + f.ran["split.2"] + f.ran["split.3"]
	f.mu.Unlock()
	if ran != 0 {
		t.Fatal("a child started before the plan was approved")
	}
	if err := p.Answer(r.ID, d.ID, "approve"); err != nil {
		t.Fatal(err)
	}
	events := finished(t, p, r, done)
	for _, id := range []string{"split.1", "split.2", "split.3"} {
		if f.ran[id] != 1 || !strings.Contains(f.prompts[id], "Do only this item") {
			t.Fatalf("%s ran %d times", id, f.ran[id])
		}
	}
	if f.ran["planner"] != 1 || f.planWrit || f.ran["fixer"] != 0 {
		t.Fatalf("planner must run once read-only and the template never: %v write=%v", f.ran, f.planWrit)
	}
	sort.Strings(f.dirs)
	if strings.Join(f.dirs, ",") != "split.1,split.2,split.3" {
		t.Fatalf("children need their own worktrees: %v", f.dirs)
	}
	sort.Strings(f.saw)
	if strings.Join(f.saw, ",") != "split.1.txt,split.2.txt,split.3.txt" {
		t.Fatalf("reviewer must start from every child's change: %v", f.saw)
	}
	result := fanOutResult(t, p, r, events, "split")
	if len(anySlice(result["results"])) != 3 || len(anySlice(result["dropped"])) != 0 || intValue(result["planned"]) != 3 {
		t.Fatalf("fan-out result %v", result)
	}
	for _, v := range anySlice(result["results"]) {
		if m := v.(map[string]any); m["status"] != "succeeded" || len(anySlice(m["changeIds"])) != 1 {
			t.Fatalf("child result %v", m)
		}
	}
	state, _ := Reduce(events)
	if state.Agents["split.2"] != "succeeded" || countEvents(events, "fanout.child.completed", "split") != 3 {
		t.Fatalf("child state %v", state.Agents)
	}
}

func TestFanOutAutoWithinCapStartsWithoutAsking(t *testing.T) {
	p := committedProject(t)
	s, _ := p.Settings()
	w, profiles := fanOutWorkflow(s, 4, "auto-within-cap")
	r := splitRun(t, p, w, profiles)
	f := newFanOutFixture(2)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	events := finished(t, p, r, startFanOut(ctx, p, r, f, false))
	if n := countEvents(events, "decision.requested", ""); n != 0 {
		t.Fatalf("auto-within-cap asked %d questions", n)
	}
	if f.ran["split.1"] != 1 || f.ran["split.2"] != 1 {
		t.Fatalf("children %v", f.ran)
	}
}

func TestFanOutCapsTheRunTotalAndOnlyTruncatesWithConsent(t *testing.T) {
	p := committedProject(t)
	s, _ := p.Settings()
	s.Limits.MaxFanOutChildrenPerRun = Int(4)
	w, profiles := fanOutWorkflow(s, 3, "auto-within-cap")
	for _, id := range []string{"planner2", "fixer2"} {
		w.Agents = append(w.Agents, Agent{Id: id, Level: Int(2), Profile: ".agents/parallel/" + id + ".agent.json", Runtime: s.DefaultModel})
		profiles[id] = NewProfile(id, "research", "Do the "+id+" work", "Use evidence", []string{"read", "write"})
	}
	w.Agents[3].Level = Int(3)
	w.FanOuts = append(w.FanOuts, FanOut{Id: "again", Name: "Second split", Planner: "planner2", Worker: "fixer2", MaxChildren: 3, Approval: "auto-within-cap"})
	w.Relationships = []Relationship{{Type: "delegates", From: "lead", To: "split"}, {Type: "delegates", From: "split", To: "again"}, {Type: "delegates", From: "again", To: "reviewer"}}
	if err := ValidateWorkflow(w, profiles, s); err != nil {
		t.Fatal(err)
	}
	r := splitRun(t, p, w, profiles)
	r.Settings = s
	f := newFanOutFixture(3)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	done := startFanOut(ctx, p, r, f, false)
	// split fits (3 of 4) and starts unasked; again has one slot left.
	d := awaitDecision(t, p, r.ID)
	if d.AgentID != "again" || strings.Join(d.Choices, ",") != "truncate,stop" || intValue(d.Context["cap"]) != 1 {
		t.Fatalf("over-cap decision %+v", d)
	}
	if err := p.Answer(r.ID, d.ID, "truncate"); err != nil {
		t.Fatal(err)
	}
	events := finished(t, p, r, done)
	if f.ran["again.1"] != 1 || f.ran["again.2"] != 0 || startedFanOutChildren(events) != 4 {
		t.Fatalf("run total exceeded: %v", f.ran)
	}
	result := fanOutResult(t, p, r, events, "again")
	if len(anySlice(result["results"])) != 1 || len(anySlice(result["dropped"])) != 2 || !strings.Contains(str(result["summary"]), "2 planned items were dropped") {
		t.Fatalf("truncated result %v", result)
	}
}

func TestFanOutStopFailsTheNodeWithoutChildren(t *testing.T) {
	p := committedProject(t)
	s, _ := p.Settings()
	w, profiles := fanOutWorkflow(s, 2, "auto-within-cap")
	r := splitRun(t, p, w, profiles)
	f := newFanOutFixture(3)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	done := startFanOut(ctx, p, r, f, false)
	d := awaitDecision(t, p, r.ID)
	if strings.Join(d.Choices, ",") != "truncate,stop" {
		t.Fatalf("a plan over maxChildren must ask even in auto-within-cap: %+v", d)
	}
	if err := p.Answer(r.ID, d.ID, "stop"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil || !strings.Contains(err.Error(), "stopped by the user") {
		t.Fatalf("got %v", err)
	}
	if f.ran["split.1"] != 0 || f.ran["reviewer"] != 0 {
		t.Fatalf("work ran after stop: %v", f.ran)
	}
}

func TestFanOutEmptyPlanCompletesWithAnEmptyResult(t *testing.T) {
	p := committedProject(t)
	s, _ := p.Settings()
	w, profiles := fanOutWorkflow(s, 4, "required")
	r := splitRun(t, p, w, profiles)
	f := newFanOutFixture(0)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	events := finished(t, p, r, startFanOut(ctx, p, r, f, false))
	result := fanOutResult(t, p, r, events, "split")
	if intValue(result["planned"]) != 0 || len(anySlice(result["results"])) != 0 || countEvents(events, "decision.requested", "") != 0 || f.ran["reviewer"] != 1 {
		t.Fatalf("empty plan: %v %v", result, f.ran)
	}
}

func TestFanOutChildFailureKeepsSiblingsUnlessAllFail(t *testing.T) {
	p := committedProject(t)
	s, _ := p.Settings()
	w, profiles := fanOutWorkflow(s, 4, "auto-within-cap")
	r := splitRun(t, p, w, profiles)
	// A failed attempt would otherwise ask whether to retry.
	r.Settings.Limits.MaxRetriesPerAgent = 0
	f := newFanOutFixture(3)
	f.fail = func(id string) bool { return id == "split.2" }
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	events := finished(t, p, r, startFanOut(ctx, p, r, f, false))
	result := fanOutResult(t, p, r, events, "split")
	statuses := []string{}
	for _, v := range anySlice(result["results"]) {
		statuses = append(statuses, str(v.(map[string]any)["status"]))
	}
	if strings.Join(statuses, ",") != "succeeded,failed,succeeded" || countEvents(events, "fanout.child.failed", "split") != 1 || f.ran["reviewer"] != 1 {
		t.Fatalf("one failed child must not stop its siblings: %v", statuses)
	}

	p = committedProject(t)
	r = splitRun(t, p, w, profiles)
	r.Settings.Limits.MaxRetriesPerAgent = 0
	f = newFanOutFixture(2)
	f.fail = func(string) bool { return true }
	if err := <-startFanOut(ctx, p, r, f, false); err == nil || !strings.Contains(err.Error(), "every child of fan-out split failed") {
		t.Fatalf("got %v", err)
	}
}

func TestFanOutResumeKeepsFinishedChildrenAndTheApproval(t *testing.T) {
	p := committedProject(t)
	s, _ := p.Settings()
	w, profiles := fanOutWorkflow(s, 4, "required")
	r := splitRun(t, p, w, profiles)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	first := newFanOutFixture(2)
	// split.2 waits until split.1 is journaled as completed, then the
	// supervisor stops mid-fan-out.
	first.block = func(_ context.Context, id string) bool {
		if id != "split.2" {
			return false
		}
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if events, err := p.Events(r.ID); err == nil && countEvents(events, "fanout.child.completed", "split") == 1 {
				break
			}
		}
		cancel()
		return true
	}
	done := startFanOut(ctx, p, r, first, false)
	d := awaitDecision(t, p, r.ID)
	if err := p.Answer(r.ID, d.ID, "approve"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err == nil {
		t.Fatal("interrupted run reported success")
	}
	resumed := newFanOutFixture(2)
	ctx, cancel = context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	events := finished(t, p, r, startFanOut(ctx, p, r, resumed, true))
	if resumed.ran["planner"] != 0 || resumed.ran["split.1"] != 0 || resumed.ran["split.2"] != 1 || resumed.ran["lead"] != 0 {
		t.Fatalf("resume reran finished work: %v", resumed.ran)
	}
	if countEvents(events, "decision.requested", "") != 1 || countEvents(events, "fanout.approved", "split") != 1 {
		t.Fatal("resume asked for approval again")
	}
	result := fanOutResult(t, p, r, events, "split")
	if len(anySlice(result["results"])) != 2 || !strings.Contains(str(result["summary"]), "2 of 2 children succeeded") {
		t.Fatalf("resumed result %v", result)
	}
}

func TestFanOutResumeDuringApprovalNeverAsksTwice(t *testing.T) {
	for _, answeredWhileDown := range []bool{false, true} {
		t.Run(fmt.Sprintf("answeredWhileDown=%v", answeredWhileDown), func(t *testing.T) {
			p := committedProject(t)
			s, _ := p.Settings()
			w, profiles := fanOutWorkflow(s, 4, "required")
			r := splitRun(t, p, w, profiles)
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			done := startFanOut(ctx, p, r, newFanOutFixture(2), false)
			stale := awaitDecision(t, p, r.ID)
			cancel()
			if err := <-done; err == nil {
				t.Fatal("interrupted run reported success")
			}
			if answeredWhileDown {
				if err := p.Answer(r.ID, stale.ID, "approve"); err != nil {
					t.Fatal(err)
				}
			}
			resumed := newFanOutFixture(2)
			ctx, cancel = context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			done = startFanOut(ctx, p, r, resumed, true)
			if !answeredWhileDown {
				d := awaitDecision(t, p, r.ID)
				if d.ID != stale.ID {
					t.Fatalf("resume asked a new question %s instead of reusing %s", d.ID, stale.ID)
				}
				if err := p.Answer(r.ID, d.ID, "approve"); err != nil {
					t.Fatal(err)
				}
			}
			events := finished(t, p, r, done)
			if countEvents(events, "decision.requested", "") != 1 || resumed.ran["planner"] != 0 || resumed.ran["split.1"] != 1 {
				t.Fatalf("resume asked again or replanned: %v", resumed.ran)
			}
			if state, _ := Reduce(events); state.Pending != 0 {
				t.Fatalf("a stale question is still pending: %d", state.Pending)
			}
		})
	}
}

func TestFanOutInsideALoopReplansEachPass(t *testing.T) {
	p := committedProject(t)
	s, _ := p.Settings()
	w, profiles := fanOutWorkflow(s, 4, "auto-within-cap")
	profiles["reviewer"] = NewProfile("reviewer", "review", "Review", "Review", []string{"read"})
	w.Agents = append(w.Agents, Agent{Id: "done", Level: Int(3), Profile: ".agents/parallel/done.agent.json", Runtime: s.DefaultModel})
	profiles["done"] = NewProfile("done", "research", "Finish", "Finish", []string{"read"})
	loop := true
	w.DecisionRouters = []Router{{Id: "gate", Name: "Gate", InputKind: "review-result", PayloadPointer: "/outcome", Routes: []c.WorkflowDecisionRoute{{Id: "approved", Equals: "Approved", To: "done"}, {Id: "again", Equals: "ChangesRequested", To: "split", Loop: &loop, MaxIterations: Int(1)}}, Fallback: c.WorkflowDecisionRouterFallback{Action: "stop"}}}
	w.Relationships = append(w.Relationships, Relationship{Type: "delegates", From: "reviewer", To: "gate"})
	r := splitRun(t, p, w, profiles)
	f := newFanOutFixture(2)
	f.verdict = func(pass int) string {
		if pass == 1 {
			return "ChangesRequested"
		}
		return "Approved"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	events := finished(t, p, r, startFanOut(ctx, p, r, f, false))
	sort.Strings(f.dirs)
	if f.ran["planner"] != 2 || strings.Join(f.dirs, ",") != "split.1,split.1-iter2,split.2,split.2-iter2" || f.ran["done"] != 1 {
		t.Fatalf("loop pass must replan and rerun children in new worktrees: %v %v", f.ran, f.dirs)
	}
	if countEvents(events, "fanout.completed", "split") != 2 || startedFanOutChildren(events) != 4 {
		t.Fatal("each pass must be journaled separately")
	}
	state, _ := Reduce(events)
	if state.Iterations["split.1"] != 2 {
		t.Fatalf("children must show the second pass: %v", state.Iterations)
	}
}

func TestFanOutScoreCountsEveryAllowedChild(t *testing.T) {
	s := DefaultSettings(Runtime{Provider: "codex", Model: "gpt-test", ReasoningEffort: "high"})
	s.Limits.MaxFanOutChildrenPerRun = Int(64)
	w, profiles := fanOutWorkflow(s, 4, "required")
	// lead, reviewer, planner, then fixer at maxChildren 4.
	if g := analyzeArena(w, profiles); g.pathAgents() != 7 || g.worstCase() != 7 || !Has(g.writers, "split") {
		t.Fatalf("path agents %d, worst case %d, writers %v", g.pathAgents(), g.worstCase(), g.writers)
	}
	// Six slots fit seven possible instances; v2 would have flagged them
	// against four agents.
	w.Settings.MaxConcurrentAgents = 6
	if Has(findingIDs(ScoreArena(w, profiles)), "P4") {
		t.Fatal("P4 must count fan-out children as path agents")
	}
	wide := convert[Workflow](w)
	wide.Settings.MaxConcurrentAgents = 4
	wide.FanOuts[0].MaxChildren = 8
	if !Has(findingIDs(ScoreArena(wide, profiles)), "P4") {
		t.Fatal("P4 must flag slots below the widest fan-out")
	}
	single := convert[Workflow](w)
	single.Settings.MaxConcurrentAgents = 1
	if !Has(findingIDs(ScoreArena(single, profiles)), "R3") {
		t.Fatal("R3 must flag a fan-out that can only run one child at a time")
	}
	// A loop around a 32-child fan-out reaches R4; a 1-child one does not.
	looped := convert[Workflow](w)
	profiles["reviewer"] = NewProfile("reviewer", "review", "Review", "Review", []string{"read"})
	looped.Agents = append(looped.Agents, Agent{Id: "done", Level: Int(3), Profile: ".agents/parallel/done.agent.json", Runtime: s.DefaultModel})
	profiles["done"] = NewProfile("done", "research", "Finish", "Finish", []string{"read"})
	loop := true
	looped.DecisionRouters = []Router{{Id: "gate", Name: "Gate", InputKind: "review-result", PayloadPointer: "/outcome", Routes: []c.WorkflowDecisionRoute{{Id: "approved", Equals: "Approved", To: "done"}, {Id: "again", Equals: "ChangesRequested", To: "split", Loop: &loop, MaxIterations: Int(1)}}, Fallback: c.WorkflowDecisionRouterFallback{Action: "stop"}}}
	looped.Relationships = append(looped.Relationships, Relationship{Type: "delegates", From: "reviewer", To: "gate"})
	looped.FanOuts[0].MaxChildren = 32
	looped.Settings.MaxConcurrentAgents = 4
	if err := ValidateWorkflow(looped, profiles, s); err != nil {
		t.Fatal(err)
	}
	// 3 + 32 + done on the path, then the loop body again: split (1 + 32),
	// reviewer and done.
	if worst := analyzeArena(looped, profiles).worstCase(); worst != 71 {
		t.Fatalf("worst case %d", worst)
	}
	card := ScoreArena(looped, profiles)
	if !Has(findingIDs(card), "R4") || card.Rubric != 3 {
		t.Fatalf("R4 missing at 71 invocations: %+v", card.Findings)
	}
	looped.FanOuts[0].MaxChildren = 1
	if Has(findingIDs(ScoreArena(looped, profiles)), "R4") {
		t.Fatal("R4 at one child")
	}
	// The fan-out is the writer review must receive.
	unreviewed := convert[Workflow](w)
	profiles["reviewer"] = NewProfile("reviewer", "research", "Do the reviewer work", "Use evidence", []string{"read"})
	if f := ScoreArena(unreviewed, profiles).Findings; len(f) == 0 || !Has(findingIDs(ScoreArena(unreviewed, profiles)), "T1") {
		t.Fatalf("unreviewed fan-out writer: %+v", f)
	}
	if !strings.Contains(roughCost(w), "7 agent turns") {
		t.Fatalf("rough cost must count every allowed child: %s", roughCost(w))
	}
}
