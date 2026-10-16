package arena

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	c "github.com/Kayyo321/agent-arena/internal/contract"
)

// loopWorkflow is the review demo with ChangesRequested sent back to the
// builder through a capped loop route instead of a separate fixer.
func loopWorkflow(s Settings, maxIterations int) (Workflow, map[string]Profile) {
	w := NewWorkflow("revision-loop", "Revision loop")
	w.HeadAgent = "lead"
	w.Settings.MaxConcurrentAgents = 2
	profiles := map[string]Profile{}
	for _, a := range []struct {
		id    string
		level int
	}{{"lead", 0}, {"builder", 1}, {"tester", 1}, {"reviewer-a", 2}, {"reviewer-b", 2}, {"docs", 3}, {"escalate", 3}} {
		w.Agents = append(w.Agents, Agent{Id: a.id, Level: Int(a.level), Profile: ".agents/revision-loop/" + a.id + ".agent.json", Runtime: s.DefaultModel})
		caps := []string{"read"}
		if a.id == "builder" {
			caps = []string{"read", "write"}
		}
		profiles[a.id] = NewProfile(a.id, "research", "Do the "+a.id+" work", "Use evidence", caps)
	}
	w.Teams = []Team{{Id: "review-board", Name: "Review board", Members: []string{"reviewer-a", "reviewer-b"}, Facilitator: "reviewer-a", Consensus: c.WorkflowTeamConsensus{Mode: "unanimous-deliberation", MaxRounds: 2}, OutputContract: c.WorkflowTeamOutputContract{Format: "arena.team-verdict.v1", Verdicts: []string{"Approved", "ChangesRequested"}}}}
	loop := true
	w.DecisionRouters = []Router{{Id: "review-gate", Name: "Review gate", InputKind: "team-result", PayloadPointer: "/verdict", Routes: []c.WorkflowDecisionRoute{{Id: "approved", Equals: "Approved", To: "docs"}, {Id: "changes", Equals: "ChangesRequested", To: "builder", Loop: &loop, MaxIterations: Int(maxIterations)}}, Fallback: c.WorkflowDecisionRouterFallback{Action: "route", To: "escalate"}}}
	w.Relationships = []Relationship{{Type: "delegates", From: "lead", To: "builder"}, {Type: "delegates", From: "lead", To: "tester"}, {Type: "delegates", From: "builder", To: "review-board"}, {Type: "delegates", From: "tester", To: "review-board"}, {Type: "delegates", From: "review-board", To: "review-gate"}}
	return w, profiles
}

func TestLoopRoutesMustBeCappedReturnsToAnAncestor(t *testing.T) {
	p := testProject(t)
	s, err := p.Settings()
	if err != nil {
		t.Fatal(err)
	}
	w, profiles := loopWorkflow(s, 2)
	if err := ValidateWorkflow(w, profiles, s); err != nil {
		t.Fatalf("capped loop route rejected: %v", err)
	}
	cases := map[string]struct {
		mutate func(*c.WorkflowDecisionRoute)
		want   string
	}{
		"uncapped loop":         {func(r *c.WorkflowDecisionRoute) { r.MaxIterations = nil }, "needs maxIterations"},
		"plain back edge":       {func(r *c.WorkflowDecisionRoute) { r.Loop, r.MaxIterations = nil, nil }, "delegation cycle"},
		"cap without loop":      {func(r *c.WorkflowDecisionRoute) { r.Loop = nil }, "without loop: true"},
		"loop to non-ancestor":  {func(r *c.WorkflowDecisionRoute) { r.To = "escalate" }, "ancestor"},
		"loop to team member":   {func(r *c.WorkflowDecisionRoute) { r.To = "reviewer-a" }, "earlier path node"},
		"loop to router itself": {func(r *c.WorkflowDecisionRoute) { r.To = "review-gate" }, "earlier path node"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			copy := convert[Workflow](w)
			tc.mutate(&copy.DecisionRouters[0].Routes[1])
			if err := ValidateWorkflow(copy, profiles, s); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
		})
	}
	tooMany := convert[Workflow](w)
	tooMany.DecisionRouters[0].Routes[1].MaxIterations = Int(6)
	if err := ValidateWorkflow(tooMany, profiles, s); err == nil {
		t.Fatal("maxIterations above the schema cap accepted")
	}
	// Every other cycle stays rejected even when a loop route exists.
	cyclic := convert[Workflow](w)
	cyclic.Relationships = append(cyclic.Relationships, Relationship{Type: "delegates", From: "docs", To: "lead"})
	if err := ValidateWorkflow(cyclic, profiles, s); err == nil {
		t.Fatal("ordinary cycle accepted alongside a loop route")
	}
}

type loopFixture struct {
	mu       sync.Mutex
	verdicts func(pass int) string
	builder  []string
	prompts  []string
	ran      map[string]int
	// interrupt, when set, runs before a builder pass and can stop the run.
	interrupt func(pass int) bool
}

func (f *loopFixture) invoke(ctx context.Context, in Invocation) (map[string]any, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	props := in.Schema["properties"].(map[string]any)
	pass := 1
	if _, n, ok := strings.Cut(filepath.Base(in.Dir), "-iter"); ok {
		fmt.Sscan(n, &pass)
	}
	if props["verdict"] != nil {
		verdict := f.verdicts(pass)
		return map[string]any{"verdict": verdict, "acceptTeamVerdict": true, "affirm": true, "summary": "pass " + fmt.Sprint(pass) + " " + verdict, "evidence": []any{"fixture"}, "rationale": "fixture", "nextAction": "follow route"}, nil
	}
	f.ran[in.AgentID]++
	if in.AgentID == "builder" {
		f.builder = append(f.builder, in.Dir)
		if f.interrupt != nil && f.interrupt(pass) {
			<-ctx.Done()
			return nil, ctx.Err()
		}
		f.prompts = append(f.prompts, in.Prompt)
		for earlier := 1; earlier < pass; earlier++ {
			if _, err := os.Stat(filepath.Join(in.Dir, fmt.Sprintf("pass%d.txt", earlier))); err != nil {
				return nil, fmt.Errorf("revision %d does not build on pass %d: %w", pass, earlier, err)
			}
		}
		if err := os.WriteFile(filepath.Join(in.Dir, fmt.Sprintf("pass%d.txt", pass)), []byte("builder output\n"), 0644); err != nil {
			return nil, err
		}
	}
	return ordinaryResult(), nil
}

func runLoop(t *testing.T, maxIterations int, verdicts func(int) string) (*Project, Run, *loopFixture, []Event) {
	t.Helper()
	p, r := loopRun(t, maxIterations)
	f := &loopFixture{verdicts: verdicts, ran: map[string]int{}}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := NewEngine(p, r, testInvoker(f.invoke)).Execute(ctx, false, false); err != nil {
		t.Fatal(err)
	}
	events, err := p.Events(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	return p, r, f, events
}

func loopRun(t *testing.T, maxIterations int) (*Project, Run) {
	t.Helper()
	p := committedProject(t)
	s, err := p.Settings()
	if err != nil {
		t.Fatal(err)
	}
	w, profiles := loopWorkflow(s, maxIterations)
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
	r, err := p.CreateRun(w, profiles, s, "Revision loop fixture", head)
	if err != nil {
		t.Fatal(err)
	}
	return p, r
}

func countEvents(events []Event, kind, agent string) int {
	n := 0
	for _, v := range events {
		if v.Type == kind && (agent == "" || v.AgentID == agent) {
			n++
		}
	}
	return n
}

func TestChangesRequestedLoopsBackToBuilderThenSucceeds(t *testing.T) {
	unicodeTerminal(t)
	p, r, f, events := runLoop(t, 2, func(pass int) string {
		if pass == 1 {
			return "ChangesRequested"
		}
		return "Approved"
	})
	state, err := Reduce(events)
	if err != nil {
		t.Fatal(err)
	}
	if state.Status != "succeeded" {
		t.Fatalf("run did not succeed: %s", state.Status)
	}
	if len(f.builder) != 2 || filepath.Base(f.builder[0]) != "builder" || filepath.Base(f.builder[1]) != "builder-iter2" {
		t.Fatalf("builder should run twice in separate worktrees: %v", f.builder)
	}
	if !strings.Contains(f.prompts[1], "memberPositions") || !strings.Contains(f.prompts[1], "Revision iteration 2") {
		t.Fatal("second builder pass did not receive the team verdict as feedback")
	}
	if f.ran["tester"] != 1 || f.ran["docs"] != 1 || f.ran["escalate"] != 0 {
		t.Fatalf("loop body or exits ran wrongly: %v", f.ran)
	}
	if countEvents(events, "loop.iteration", "review-gate") != 1 || countEvents(events, "agent.started", "builder") != 2 {
		t.Fatal("loop iteration was not journaled")
	}
	if state.Iterations["builder"] != 2 || state.Agents["builder"] != "succeeded" {
		t.Fatalf("reducer lost the second pass: %v %v", state.Iterations, state.Agents)
	}
	// The second proposal depends on the first: nothing is silently replaced.
	changes, err := p.Changes(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	var first, second Change
	for _, change := range changes {
		switch change.AgentID {
		case "builder":
			if Has(change.Paths, "pass2.txt") {
				second = change
			} else {
				first = change
			}
		}
	}
	if first.ID == "" || second.ID == "" || !Has(second.DependsOn, first.ID) {
		t.Fatalf("revision does not build on the first change: %+v %+v", first, second)
	}
	view := strings.Join(wideGraph(r.Workflow, state, events, 140, false, ""), "\n")
	if !strings.Contains(view, "↺ 1/2") {
		t.Fatalf("graph does not show the used iteration:\n%s", view)
	}
}

func TestLoopCapFallsThroughToRouterFallback(t *testing.T) {
	_, _, f, events := runLoop(t, 1, func(int) string { return "ChangesRequested" })
	if len(f.builder) != 2 {
		t.Fatalf("builder should run once plus one capped revision, ran %d", len(f.builder))
	}
	if countEvents(events, "loop.iteration", "") != 1 || countEvents(events, "loop.capped", "review-gate") != 1 {
		t.Fatal("cap was not journaled")
	}
	if f.ran["escalate"] != 1 || f.ran["docs"] != 0 {
		t.Fatalf("capped loop did not take the fallback: %v", f.ran)
	}
	state, err := Reduce(events)
	if err != nil || state.Status != "succeeded" {
		t.Fatalf("capped run: %s %v", state.Status, err)
	}
}

func TestGraphDrawsLoopRouteAsCountedReturnEdge(t *testing.T) {
	unicodeTerminal(t)
	s := DefaultSettings(Runtime{Provider: "codex", Model: "m", ReasoningEffort: "high"})
	w, _ := loopWorkflow(s, 2)
	events := []Event{{Type: "loop.iteration", AgentID: "review-gate", Data: map[string]any{"routerId": "review-gate", "target": "builder"}}}
	for _, width := range []int{80, 140, 200} {
		lines := wideGraph(w, RunState{Agents: map[string]string{}}, events, width, false, "")
		view := strings.Join(lines, "\n")
		if !strings.Contains(view, "↺ 1/2") || !strings.ContainsAny(view, "◀▶") {
			t.Fatalf("width %d: loop edge missing:\n%s", width, view)
		}
		if !strings.Contains(view, `="Approved"`) {
			t.Fatalf("width %d: forward branch lost:\n%s", width, view)
		}
		for _, line := range lines {
			if ansiWidth := len([]rune(line)); ansiWidth > width {
				t.Fatalf("width %d: line overflows: %q", width, line)
			}
		}
	}
	idle := strings.Join(wideGraph(w, RunState{Agents: map[string]string{}}, nil, 140, false, ""), "\n")
	if !strings.Contains(idle, "↺ 0/2") {
		t.Fatalf("idle graph does not show the loop budget:\n%s", idle)
	}
}

func TestResumeReplaysLoopIterationFromJournal(t *testing.T) {
	p, r := loopRun(t, 2)
	verdicts := func(pass int) string {
		if pass == 1 {
			return "ChangesRequested"
		}
		return "Approved"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	first := &loopFixture{verdicts: verdicts, ran: map[string]int{}}
	first.interrupt = func(pass int) bool {
		if pass == 2 {
			cancel()
			return true
		}
		return false
	}
	if err := NewEngine(p, r, testInvoker(first.invoke)).Execute(ctx, false, false); err == nil {
		t.Fatal("interrupted run reported success")
	}
	// A fresh supervisor must rebuild the second pass from the journal: the
	// builder resumes in its -iter2 worktree with the verdict as feedback.
	resumed := &loopFixture{verdicts: verdicts, ran: map[string]int{}}
	ctx, cancel = context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := NewEngine(p, r, testInvoker(resumed.invoke)).Execute(ctx, true, false); err != nil {
		t.Fatal(err)
	}
	if len(resumed.builder) != 1 || filepath.Base(resumed.builder[0]) != "builder-iter2" || !strings.Contains(resumed.prompts[0], "memberPositions") {
		t.Fatalf("resume did not continue the second pass: %v", resumed.builder)
	}
	if resumed.ran["lead"] != 0 || resumed.ran["tester"] != 0 || resumed.ran["docs"] != 1 {
		t.Fatalf("resume reran completed nodes: %v", resumed.ran)
	}
	events, err := p.Events(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if state, err := Reduce(events); err != nil || state.Status != "succeeded" || countEvents(events, "loop.iteration", "") != 1 {
		t.Fatalf("resumed loop: %s %v", state.Status, err)
	}
}
