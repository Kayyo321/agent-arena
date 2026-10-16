package arena

import (
	"context"
	"sync"
	"testing"
	"time"
)

// budgetRun executes the revision-loop fixture with run budgets set. Every
// agent reports 100 tokens, and invoked counts the providers that started.
func budgetRun(t *testing.T, limits func(*Settings), now func() time.Time) (*Project, Run, *Engine, *sync.Map, chan error) {
	t.Helper()
	p, r := loopRun(t, 2)
	limits(&r.Settings)
	f := &loopFixture{verdicts: func(int) string { return "Approved" }, ran: map[string]int{}}
	invoked := &sync.Map{}
	e := NewEngine(p, r, testInvoker(func(ctx context.Context, in Invocation) (map[string]any, error) {
		invoked.Store(in.AgentID, true)
		if err := in.Usage(map[string]any{"input_tokens": 60, "output_tokens": 40}); err != nil {
			return nil, err
		}
		return f.invoke(ctx, in)
	}))
	e.Now = now
	done := make(chan error, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	go func() { done <- e.Execute(ctx, false, false) }()
	return p, r, e, invoked, done
}

func awaitDecision(t *testing.T, p *Project, run string) Decision {
	t.Helper()
	for deadline := time.Now().Add(30 * time.Second); time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		if d, err := p.Decisions(run); err == nil && len(d) > 0 {
			return d[0]
		}
	}
	t.Fatal("no decision was raised")
	return Decision{}
}

func TestTokenBudgetPausesAsksAndResumesOnContinue(t *testing.T) {
	p, r, _, invoked, done := budgetRun(t, func(s *Settings) { s.Limits.MaxRunTokens = Int(50) }, nil)
	d := awaitDecision(t, p, r.ID)
	if d.Kind != "budget" || d.Context["budget"] != "tokens" || len(d.Choices) != 2 {
		t.Fatalf("decision %+v", d)
	}
	state, err := p.State(r.ID)
	if err != nil || state.Status != "paused" {
		t.Fatalf("a tripped budget must pause, not kill: %v %+v", err, state)
	}
	if _, ran := invoked.Load("builder"); ran {
		t.Fatal("a node launched after the budget tripped")
	}
	if err := p.Answer(r.ID, d.ID, "continue"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	events, _ := p.Events(r.ID)
	state, err = Reduce(events)
	if err != nil || state.Status != "succeeded" {
		t.Fatalf("run did not resume to success: %v %+v", err, state)
	}
	if n := countEvents(events, "budget.exceeded", ""); n != 1 || countEvents(events, "budget.continued", "") != 1 {
		t.Fatalf("continuing must lift the budget once: %d exceeded", n)
	}
}

func TestMinutesBudgetStopLeavesRunPausedAndAsksAgainOnResume(t *testing.T) {
	var mu sync.Mutex
	offset := time.Duration(0)
	now := func() time.Time { mu.Lock(); defer mu.Unlock(); return time.Now().Add(offset) }
	p, r, _, _, done := budgetRun(t, func(s *Settings) { s.Limits.MaxRunMinutes = Int(30) }, now)
	// Nothing trips until the clock passes the limit.
	time.Sleep(50 * time.Millisecond)
	mu.Lock()
	offset = 2 * time.Hour
	mu.Unlock()
	first := awaitDecision(t, p, r.ID)
	if first.Context["budget"] != "minutes" {
		t.Fatalf("decision %+v", first)
	}
	if err := p.Answer(r.ID, first.ID, "stop"); err != nil {
		t.Fatal(err)
	}
	// The run stays paused and the engine keeps waiting rather than failing.
	time.Sleep(500 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("stop ended the run: %v", err)
	default:
	}
	if state, _ := p.State(r.ID); state.Status != "paused" {
		t.Fatalf("status %s", state.Status)
	}
	if err := p.Transition(r.ID, "resumed"); err != nil {
		t.Fatal(err)
	}
	second := awaitDecision(t, p, r.ID)
	if second.ID == first.ID {
		t.Fatal("resuming past an unlifted budget must ask again")
	}
	if err := p.Answer(r.ID, second.ID, "continue"); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

func TestRunMinutesExcludesPausedTime(t *testing.T) {
	at := func(m int) string { return time.Date(2026, 1, 1, 0, m, 0, 0, time.UTC).Format(time.RFC3339Nano) }
	events := []Event{{At: at(0), Type: "workflow.started"}, {At: at(10), Type: "workflow.paused"}, {At: at(50), Type: "workflow.resumed"}}
	if got := runMinutes(events, time.Date(2026, 1, 1, 0, 55, 0, 0, time.UTC)); got != 15 {
		t.Fatalf("got %v minutes", got)
	}
}

func TestRunBudgetsAreOptionalAndBounded(t *testing.T) {
	s := DefaultSettings(Runtime{Provider: "codex", Model: "m", ReasoningEffort: "high"})
	if s.Limits.MaxRunTokens != nil || s.Limits.MaxRunMinutes != nil {
		t.Fatal("run budgets must default to off")
	}
	if err := ValidateSettings(s); err != nil {
		t.Fatalf("settings without budgets (every existing file) must validate: %v", err)
	}
	s.Limits.MaxRunTokens, s.Limits.MaxRunMinutes = Int(1000), Int(60)
	if err := ValidateSettings(s); err != nil {
		t.Fatal(err)
	}
	s.Limits.MaxRunMinutes = Int(0)
	if ValidateSettings(s) == nil {
		t.Fatal("a zero-minute budget was accepted")
	}
}
