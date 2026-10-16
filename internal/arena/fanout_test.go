package arena

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
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
	// edit, when set, replaces the default file a child writes in its worktree.
	edit func(id, dir string) error
	// base is what the reviewer saw in base.txt.
	base string
	// files lists every file in the reviewer's workspace.
	files []string
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
		if f.edit != nil {
			if err := f.edit(in.AgentID, in.Dir); err != nil {
				return nil, err
			}
		} else if err := os.WriteFile(filepath.Join(in.Dir, in.AgentID+".txt"), []byte("child output\n"), 0644); err != nil {
			return nil, err
		}
	case in.AgentID == "reviewer":
		matches, _ := filepath.Glob(filepath.Join(in.Dir, "split*.txt"))
		f.mu.Lock()
		for _, m := range matches {
			f.saw = append(f.saw, filepath.Base(m))
		}
		if b, err := os.ReadFile(filepath.Join(in.Dir, "base.txt")); err == nil {
			f.base = string(b)
		}
		if entries, err := os.ReadDir(in.Dir); err == nil {
			for _, entry := range entries {
				f.files = append(f.files, entry.Name())
			}
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
	if countEvents(events, "fanout.conflict", "") != 0 {
		t.Fatal("disjoint children journaled a conflict")
	}
	result := fanOutResult(t, p, r, events, "split")
	if len(anySlice(result["results"])) != 3 || len(anySlice(result["dropped"])) != 0 || intValue(result["planned"]) != 3 {
		t.Fatalf("fan-out result %v", result)
	}
	for _, v := range anySlice(result["results"]) {
		if m := v.(map[string]any); m["status"] != "succeeded" || len(anySlice(m["changeIds"])) != 1 || m["conflict"] != nil || m["conflictsWith"] != nil {
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

func TestFanOutIsCreatableWithoutHandEditing(t *testing.T) {
	p := committedProject(t)
	s, err := p.Settings()
	if err != nil {
		t.Fatal(err)
	}
	// The guided builder adds the node to a draft that has its agents.
	w, profiles := fanOutWorkflow(s, 4, "required")
	w.FanOuts = nil
	w.Relationships = nil
	script := strings.Join([]string{
		"fanout", "split", "Parallel fixes", "planner", "fixer", "3", "auto-within-cap",
		"link", "delegates", "lead", "split",
		"link", "delegates", "split", "reviewer",
		"finish", ""}, "\n")
	var out strings.Builder
	cli := NewCLI(strings.NewReader(script), &out, &out)
	built, builtProfiles, err := cli.buildWorkflow(context.Background(), p, w, profiles)
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if err := ValidateWorkflow(built, builtProfiles, s); err != nil {
		t.Fatalf("guided fan-out does not validate: %v", err)
	}
	if len(built.FanOuts) != 1 || built.FanOuts[0] != (FanOut{Id: "split", Name: "Parallel fixes", Planner: "planner", Worker: "fixer", MaxChildren: 3, Approval: "auto-within-cap"}) {
		t.Fatalf("built %+v", built.FanOuts)
	}
	if !strings.Contains(out.String(), "[fan-out split] planner planner, worker fixer, max 3 children; approval auto-within-cap") {
		t.Fatalf("summary does not show the fan-out:\n%s", out.String())
	}
	// Editing an existing fan-out keeps its values as defaults.
	cli = NewCLI(strings.NewReader("fanout\nsplit\n\n\n\n9\n"), &out, &out)
	if _, _, err := cli.buildWorkflow(context.Background(), p, built, builtProfiles); err == nil || !strings.Contains(err.Error(), "between 1 and 8") {
		t.Fatalf("maxChildren above the project limit must be refused: %v", err)
	}

	// workflow create --input takes the same node from a builder request.
	full, fullProfiles := fanOutWorkflow(s, 4, "required")
	request := filepath.Join(t.TempDir(), "request.json")
	if err := os.WriteFile(request, JSON(WorkflowDraft{Workflow: full, Profiles: fullProfiles}), 0600); err != nil {
		t.Fatal(err)
	}
	cli = NewCLI(strings.NewReader(""), &out, &out)
	if err := cli.Run(context.Background(), []string{"workflow", "create", full.Id, "--input", request, "--yes", "--project", p.Root}); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	saved, _, err := p.Load(full.Id)
	if err != nil || len(saved.FanOuts) != 1 || saved.FanOuts[0].MaxChildren != 4 {
		t.Fatalf("saved %+v %v", saved.FanOuts, err)
	}
}

// sameLine makes the listed children replace base.txt's only line with their
// own text, so any two of them conflict; the others write their own file.
func sameLine(clashing ...string) func(id, dir string) error {
	return func(id, dir string) error {
		if Has(clashing, id) {
			return os.WriteFile(filepath.Join(dir, "base.txt"), []byte("edited by "+id+"\n"), 0644)
		}
		return os.WriteFile(filepath.Join(dir, id+".txt"), []byte("child output\n"), 0644)
	}
}

// changeStates maps each of the run's change IDs to its state.
func changeStates(t *testing.T, p *Project, r Run) map[string]string {
	t.Helper()
	changes, err := p.Changes(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	states := map[string]string{}
	for _, c := range changes {
		states[c.ID] = c.State
	}
	return states
}

func TestFanOutConflictIsSurfacedNotFatal(t *testing.T) {
	p := committedProject(t)
	s, _ := p.Settings()
	w, profiles := fanOutWorkflow(s, 4, "auto-within-cap")
	before := ScoreArena(w, profiles)
	r := splitRun(t, p, w, profiles)
	f := newFanOutFixture(3)
	f.edit = sameLine("split.1", "split.2")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	events := finished(t, p, r, startFanOut(ctx, p, r, f, false))
	result := fanOutResult(t, p, r, events, "split")
	results := anySlice(result["results"])
	if len(results) != 3 {
		t.Fatalf("results %v", results)
	}
	held := []string{}
	changeIDs := []string{}
	for i, v := range results {
		m := v.(map[string]any)
		changeIDs = append(changeIDs, stringSlice(m["changeIds"])...)
		switch i {
		case 0, 1:
			other := fmt.Sprintf("split.%d", 3-(i+1))
			if m["conflict"] != true || strings.Join(stringSlice(m["conflictsWith"]), ",") != other || m["status"] != "succeeded" {
				t.Fatalf("child %d: %v", i+1, m)
			}
			held = append(held, stringSlice(m["changeIds"])...)
		case 2:
			if m["conflict"] != nil || m["conflictsWith"] != nil {
				t.Fatalf("the clean child was marked: %v", m)
			}
		}
	}
	if !strings.Contains(str(result["summary"]), "2 children conflict") || !strings.Contains(str(result["summary"]), "3 of 3 children succeeded") {
		t.Fatalf("summary %q", result["summary"])
	}
	// The reviewer's workspace holds the clean child's file and the untouched line.
	if strings.Join(f.saw, ",") != "split.3.txt" || f.base != "baseline\n" {
		t.Fatalf("reviewer saw %v, base.txt %q", f.saw, f.base)
	}
	// The node's packet pins only the clean change; the held ones are still proposals.
	var packet Packet
	for _, v := range events {
		if v.Type == "node.completed" && str(v.Data["nodeId"]) == "split" {
			packet, _ = p.ReadPacket(r.ID, str(v.Data["messageId"]))
		}
	}
	if pinned := stringSlice(packet["arenaChangeIds"]); len(pinned) != 1 || Has(held, pinned[0]) {
		t.Fatalf("pinned %v, held %v", pinned, held)
	}
	states := changeStates(t, p, r)
	if len(held) != 2 || len(states) != 3 {
		t.Fatalf("held %v states %v", held, states)
	}
	for _, id := range changeIDs {
		if states[id] != "proposed" {
			t.Fatalf("change %s is %q; nothing may be dropped", id, states[id])
		}
	}
	// One journaled conflict names the pair and the held changes.
	var conflict Event
	for _, v := range events {
		if v.Type == "fanout.conflict" {
			conflict = v
		}
	}
	if countEvents(events, "fanout.conflict", "split") != 1 || strings.Join(stringSlice(conflict.Data["children"]), ",") != "split.1,split.2" || len(stringSlice(conflict.Data["changeIds"])) != 2 || len(anySlice(conflict.Data["pairs"])) != 1 {
		t.Fatalf("conflict events %+v", conflict)
	}
	if countEvents(events, "fanout.completed", "split") != 1 || countEvents(events, "node.failed", "") != 0 {
		t.Fatal("the node must complete")
	}
	// The held changes can still be integrated one at a time; the second one
	// then reports its conflict instead of overwriting the first.
	if _, err := Git(p.Root, "add", "."); err != nil {
		t.Fatal(err)
	}
	if _, err := Git(p.Root, "-c", "user.name=Test", "-c", "user.email=test@localhost", "commit", "-qm", "workflow files"); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Integrate(r.ID, held[0], true); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Integrate(r.ID, held[1], true); err == nil {
		t.Fatal("the second conflicting change integrated")
	}
	if after := ScoreArena(w, profiles); !reflect.DeepEqual(before, after) {
		t.Fatalf("the grade moved: %+v -> %+v", before, after)
	}
	// The graph card and the event list both show it.
	view := strings.Join(wideGraph(w, RunState{Agents: map[string]string{}}, events, 140, false, ""), "\n")
	if !strings.Contains(view, "3/3 done · 2 conflicting") {
		t.Fatalf("card:\n%s", view)
	}
}

func TestFanOutTwoChildrenOnTheSameLineStillFinish(t *testing.T) {
	p := committedProject(t)
	s, _ := p.Settings()
	w, profiles := fanOutWorkflow(s, 4, "auto-within-cap")
	r := splitRun(t, p, w, profiles)
	f := newFanOutFixture(2)
	f.edit = sameLine("split.1", "split.2")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	events := finished(t, p, r, startFanOut(ctx, p, r, f, false))
	if f.ran["reviewer"] != 1 || len(f.saw) != 0 || f.base != "baseline\n" {
		t.Fatalf("reviewer ran %d times and saw %v", f.ran["reviewer"], f.saw)
	}
	for _, state := range changeStates(t, p, r) {
		if state != "proposed" {
			t.Fatalf("state %s", state)
		}
	}
	if countEvents(events, "fanout.conflict", "split") != 1 {
		t.Fatal("conflict not journaled")
	}
}

func TestFanOutResumeAfterAConflictJournalsItOnce(t *testing.T) {
	p := committedProject(t)
	s, _ := p.Settings()
	w, profiles := fanOutWorkflow(s, 4, "auto-within-cap")
	r := splitRun(t, p, w, profiles)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	first := newFanOutFixture(3)
	first.edit = sameLine("split.1", "split.2")
	// split.3 waits until both conflicting children are journaled, then the
	// supervisor stops.
	first.block = func(_ context.Context, id string) bool {
		if id != "split.3" {
			return false
		}
		for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
			if events, err := p.Events(r.ID); err == nil && countEvents(events, "fanout.child.completed", "split") == 2 {
				break
			}
		}
		cancel()
		return true
	}
	if err := <-startFanOut(ctx, p, r, first, false); err == nil {
		t.Fatal("interrupted run reported success")
	}
	resumed := newFanOutFixture(3)
	resumed.edit = sameLine("split.1", "split.2")
	ctx, cancel = context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	events := finished(t, p, r, startFanOut(ctx, p, r, resumed, true))
	if resumed.ran["split.1"] != 0 || resumed.ran["split.2"] != 0 || resumed.ran["split.3"] != 1 || resumed.ran["planner"] != 0 {
		t.Fatalf("resume reran finished work: %v", resumed.ran)
	}
	if countEvents(events, "fanout.conflict", "split") != 1 {
		t.Fatalf("conflict journaled %d times", countEvents(events, "fanout.conflict", "split"))
	}
	results := anySlice(fanOutResult(t, p, r, events, "split")["results"])
	if len(results) != 3 || results[0].(map[string]any)["conflict"] != true || results[1].(map[string]any)["conflict"] != true || results[2].(map[string]any)["conflict"] != nil {
		t.Fatalf("results after resume %v", results)
	}
	if len(changeStates(t, p, r)) != 3 || strings.Join(resumed.saw, ",") != "split.3.txt" {
		t.Fatalf("changes %v, reviewer saw %v", changeStates(t, p, r), resumed.saw)
	}
}

func TestFanOutConflictCheckNeedsNoWorktreeAndLeavesNothingBehind(t *testing.T) {
	p := committedProject(t)
	s, _ := p.Settings()
	w, profiles := fanOutWorkflow(s, 4, "auto-within-cap")
	r := splitRun(t, p, w, profiles)
	f := newFanOutFixture(2)
	f.edit = sameLine("split.1", "split.2")
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	finished(t, p, r, startFanOut(ctx, p, r, f, false))
	if left, _ := filepath.Glob(p.Path(".state", "indices", "*")); len(left) != 0 {
		t.Fatalf("scratch indexes left behind: %v", left)
	}
	if status, err := Git(p.Root, "status", "--porcelain=v1"); err != nil || strings.Contains(status, "base.txt") {
		t.Fatalf("the user's checkout was touched: %q %v", status, err)
	}
}

// Edits a few lines apart share patch context, so plain `git apply --check`
// refuses the second one, but the next node's base is built with --3way and
// merges them. The check must agree with that base, not hold clean work back.
func TestFanOutNearbyLineEditsAreNotFlaggedAsConflicts(t *testing.T) {
	p := committedProject(t)
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = fmt.Sprintf("line %d", i+1)
	}
	if err := os.WriteFile(filepath.Join(p.Root, "notes.txt"), []byte(strings.Join(lines, "\n")+"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Git(p.Root, "add", "notes.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := Git(p.Root, "-c", "user.name=Test", "-c", "user.email=test@localhost", "commit", "-qm", "notes"); err != nil {
		t.Fatal(err)
	}
	s, _ := p.Settings()
	w, profiles := fanOutWorkflow(s, 4, "auto-within-cap")
	r := splitRun(t, p, w, profiles)
	f := newFanOutFixture(3)
	line := map[string]int{"split.1": 1, "split.2": 3, "split.3": 20}
	f.edit = func(id, dir string) error {
		b, err := os.ReadFile(filepath.Join(dir, "notes.txt"))
		if err != nil {
			return err
		}
		edited := strings.Replace(string(b), fmt.Sprintf("line %d\n", line[id]), "edited by "+id+"\n", 1)
		return os.WriteFile(filepath.Join(dir, "notes.txt"), []byte(edited), 0644)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	events := finished(t, p, r, startFanOut(ctx, p, r, f, false))
	for _, v := range anySlice(fanOutResult(t, p, r, events, "split")["results"]) {
		if m := v.(map[string]any); m["conflict"] != nil {
			t.Fatalf("a clean child was held out: %v", m)
		}
	}
	if countEvents(events, "fanout.conflict", "split") != 0 {
		t.Fatal("a conflict was journaled for edits that merge")
	}
}

// Only the clashing pair is flagged; a disjoint child stays clean. Called
// directly with patches on disk, so no worktrees are needed.
func TestFanOutConflictsFlagsOnlyTheClashingPair(t *testing.T) {
	p := committedProject(t)
	s, _ := p.Settings()
	w, profiles := fanOutWorkflow(s, 4, "auto-within-cap")
	r := splitRun(t, p, w, profiles)
	first, second, third := proposedChange(t, p, r, "one", "same.txt", "one\n"), proposedChange(t, p, r, "two", "other.txt", "two\n"), proposedChange(t, p, r, "three", "third.txt", "three\n")
	changes := map[string][]string{"split.1": {first.ID}, "split.2": {second.ID}, "split.3": {third.ID}}
	got, err := p.fanOutConflicts(r, []string{"split.1", "split.2", "split.3"}, changes)
	if err != nil || len(got) != 0 {
		t.Fatalf("disjoint children flagged: %v %v", got, err)
	}
	// Make the third child's patch create the file the first child created.
	if err := os.WriteFile(filepath.Join(p.RunDir(r.ID), "changes", third.ID+".patch"), []byte("diff --git a/same.txt b/same.txt\nnew file mode 100644\nindex 0000000..e69de29\n--- /dev/null\n+++ b/same.txt\n@@ -0,0 +1 @@\n+three\n"), 0600); err != nil {
		t.Fatal(err)
	}
	got, err = p.fanOutConflicts(r, []string{"split.1", "split.2", "split.3"}, changes)
	if err != nil {
		t.Fatal(err)
	}
	// split.3 clashes with split.1 as a pair, so both are held, and split.2 is clean.
	if strings.Join(got["split.1"], ",") != "split.3" || strings.Join(got["split.3"], ",") != "split.1" || len(got["split.2"]) != 0 {
		t.Fatalf("conflicts %v", got)
	}
}

// indexFiles lists the paths in a scratch index file.
func indexFiles(t *testing.T, root, index string) []string {
	t.Helper()
	out, err := gitEnv(root, []string{"GIT_INDEX_FILE=" + index}, "ls-files")
	if err != nil {
		t.Fatal(err)
	}
	return strings.Fields(out)
}

// COMBINED-FAILURE HANDLING, WITH AN INJECTED PATCH APPLIER. A conflict that
// only the combination of three or more children shows cannot be built from
// real git patches, so this test stands in for git on exactly one application:
// trial that adds split.3 on top of the accepted split.1 and split.2. Every individual build and pair trial, and every other application,
// still runs real git. The stub also dirties the failed trial index, as a git
// apply that stops half-way would, and checks that the child applied after the
// failure sees the accepted changes and none of that debris.
//
// It asserts the failing child alone is held out and mapped to the accepted
// ones, the accepted changes stay intact, the held change stays a reviewable
// proposal, and the next node's base holds only the accepted changes.
func TestFanOutCombinedOnlyFailureHoldsOutJustThatChildAndLeavesTheBaseClean(t *testing.T) {
	p := committedProject(t)
	s, _ := p.Settings()
	w, profiles := fanOutWorkflow(s, 4, "auto-within-cap")
	r := splitRun(t, p, w, profiles)
	f := newFanOutFixture(4)

	var mu sync.Mutex
	combinedFailures, checkedAfterFailure := 0, 0
	stub := func(root, index, patch string) error {
		body, err := os.ReadFile(patch)
		if err != nil {
			return err
		}
		have := indexFiles(t, root, index)
		mu.Lock()
		defer mu.Unlock()
		if strings.Contains(string(body), "split.3.txt") && Has(have, "split.1.txt") && Has(have, "split.2.txt") {
			combinedFailures++
			// An empty blob, written to the object store, placed in the trial index.
			write := exec.Command("git", "hash-object", "-w", "--stdin")
			write.Dir, write.Stdin = root, strings.NewReader("")
			blob, err := write.Output()
			if err != nil {
				t.Errorf("planting debris: %v", err)
			}
			debris := exec.Command("git", "update-index", "--add", "--cacheinfo", "100644,"+strings.TrimSpace(string(blob))+",contaminated.txt")
			debris.Dir, debris.Env = root, append(os.Environ(), "GIT_INDEX_FILE="+index)
			if out, err := debris.CombinedOutput(); err != nil {
				t.Errorf("planting debris: %v %s", err, out)
			}
			return fmt.Errorf("injected: split.3 does not apply on top of split.1 and split.2")
		}
		if strings.Contains(string(body), "split.4.txt") && Has(have, "split.1.txt") && Has(have, "split.2.txt") {
			// The accumulated index after the failure: both accepted changes
			// are still there and the failed trial left nothing in it.
			checkedAfterFailure++
			if Has(have, "contaminated.txt") || Has(have, "split.3.txt") {
				t.Errorf("the failed application reached the accumulated index: %v", have)
			}
		}
		return gitApplyCached(root, index, patch)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	engine := NewEngine(p, r, testInvoker(f.invoke))
	engine.patchApply = stub
	done := make(chan error, 1)
	go func() { done <- engine.Execute(ctx, false, false) }()
	events := finished(t, p, r, done)

	if combinedFailures != 1 || checkedAfterFailure != 1 {
		t.Fatalf("the injected failure ran %d times and split.4 was checked after it %d times; want once each", combinedFailures, checkedAfterFailure)
	}

	result := fanOutResult(t, p, r, events, "split")
	results := anySlice(result["results"])
	if len(results) != 4 {
		t.Fatalf("results %v", results)
	}
	changeOf := map[string]string{}
	for _, v := range results {
		m := v.(map[string]any)
		id := str(m["childId"])
		changeOf[id] = strings.Join(stringSlice(m["changeIds"]), ",")
		held := m["conflict"] == true
		if m["status"] != "succeeded" || held != (id == "split.3") {
			t.Fatalf("%s: %v (only split.3 may be held)", id, m)
		}
		if held && strings.Join(stringSlice(m["conflictsWith"]), ",") != "split.1,split.2" {
			t.Fatalf("split.3 is held against %v, want the accepted split.1 and split.2", m["conflictsWith"])
		}
	}

	// One journaled conflict: split.3 alone, with a pair for each accepted child.
	var conflict Event
	for _, v := range events {
		if v.Type == "fanout.conflict" {
			conflict = v
		}
	}
	if countEvents(events, "fanout.conflict", "split") != 1 || strings.Join(stringSlice(conflict.Data["children"]), ",") != "split.3" || strings.Join(stringSlice(conflict.Data["changeIds"]), ",") != changeOf["split.3"] || len(anySlice(conflict.Data["pairs"])) != 2 {
		t.Fatalf("conflict event %+v", conflict)
	}

	// The node pins the accepted changes only, in order; every change is still a proposal.
	var packet Packet
	for _, v := range events {
		if v.Type == "node.completed" && str(v.Data["nodeId"]) == "split" {
			packet, _ = p.ReadPacket(r.ID, str(v.Data["messageId"]))
		}
	}
	wantPinned := changeOf["split.1"] + "," + changeOf["split.2"] + "," + changeOf["split.4"]
	if got := strings.Join(stringSlice(packet["arenaChangeIds"]), ","); got != wantPinned {
		t.Fatalf("pinned %s, want %s", got, wantPinned)
	}
	for id, state := range changeStates(t, p, r) {
		if state != "proposed" {
			t.Fatalf("change %s is %q; nothing may be dropped or integrated", id, state)
		}
	}

	// The next node's base, built from the pinned changes, holds the accepted
	// changes and neither the held one nor any debris from the failed trial.
	if strings.Join(f.saw, ",") != "split.1.txt,split.2.txt,split.4.txt" || Has(f.files, "contaminated.txt") || Has(f.files, "split.3.txt") {
		t.Fatalf("the reviewer's workspace holds %v (split files %v)", f.files, f.saw)
	}
}
