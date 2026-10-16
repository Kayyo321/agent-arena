package arena

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
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

func TestBudgetGatesFanOutChildrenNotJustTheFanOutNode(t *testing.T) {
	p := committedProject(t)
	s, _ := p.Settings()
	w, profiles := fanOutWorkflow(s, 3, "auto-within-cap")
	r := splitRun(t, p, w, profiles)
	r.Settings.Limits.MaxRunTokens = Int(50)
	f := newFanOutFixture(3)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- NewEngine(p, r, testInvoker(func(ctx context.Context, in Invocation) (map[string]any, error) {
			// Only the planner reports usage, so the scheduler's check before the
			// fan-out node passes and the budget trips with the children pending.
			if in.AgentID == "planner" {
				if err := in.Usage(map[string]any{"input_tokens": 60, "output_tokens": 40}); err != nil {
					return nil, err
				}
			}
			return f.invoke(ctx, in)
		})).Execute(ctx, false, false)
	}()
	d := awaitDecision(t, p, r.ID)
	if d.Kind != "budget" || d.Context["budget"] != "tokens" {
		t.Fatalf("decision %+v", d)
	}
	time.Sleep(500 * time.Millisecond)
	f.mu.Lock()
	started := f.ran["split.1"] + f.ran["split.2"] + f.ran["split.3"]
	f.mu.Unlock()
	if started != 0 {
		t.Fatalf("%d fan-out children started past a tripped budget", started)
	}
	if all, _ := p.Decisions(r.ID); len(all) != 1 {
		t.Fatalf("one tripped budget must raise one question, got %d", len(all))
	}
	if err := p.Answer(r.ID, d.ID, "continue"); err != nil {
		t.Fatal(err)
	}
	events := finished(t, p, r, done)
	if n := countEvents(events, "budget.exceeded", ""); n != 1 {
		t.Fatalf("%d budget.exceeded events", n)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range []string{"split.1", "split.2", "split.3"} {
		if f.ran[id] != 1 {
			t.Fatalf("%s ran %d times", id, f.ran[id])
		}
	}
}

// budgetFanOut runs the fan-out fixture with four children. Only the planner
// reports usage (100 tokens), so a 50-token budget trips with every child
// waiting at the gate at once, which is the contention these tests are about.
func budgetFanOut(t *testing.T, limits func(*Settings), now func() time.Time) (*Project, Run, *fanOutFixture, context.CancelFunc, chan error) {
	t.Helper()
	p := committedProject(t)
	s, _ := p.Settings()
	w, profiles := fanOutWorkflow(s, 4, "auto-within-cap")
	r := splitRun(t, p, w, profiles)
	limits(&r.Settings)
	f := newFanOutFixture(4)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	t.Cleanup(cancel)
	e := NewEngine(p, r, testInvoker(reportingPlanner(f)))
	e.Now = now
	done := make(chan error, 1)
	go func() { done <- e.Execute(ctx, false, false) }()
	return p, r, f, cancel, done
}

func reportingPlanner(f *fanOutFixture) func(context.Context, Invocation) (map[string]any, error) {
	return func(ctx context.Context, in Invocation) (map[string]any, error) {
		if in.AgentID == "planner" {
			if err := in.Usage(map[string]any{"input_tokens": 60, "output_tokens": 40}); err != nil {
				return nil, err
			}
		}
		return f.invoke(ctx, in)
	}
}

// childrenStarted counts the fan-out children whose provider has started.
func childrenStarted(f *fanOutFixture) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for id, ran := range f.ran {
		if strings.HasPrefix(id, "split.") {
			n += ran
		}
	}
	return n
}

// settle gives the four waiting children time to reach the gate and to launch if
// the gate were broken.
func settle() { time.Sleep(600 * time.Millisecond) }

// A stop answer holds every waiting child, a resume asks once for all of them,
// and a continue releases all of them together.
func TestBudgetStopHoldsEveryWaiterAndResumeAsksOnce(t *testing.T) {
	p, r, f, _, done := budgetFanOut(t, func(s *Settings) { s.Limits.MaxRunTokens = Int(50) }, nil)
	first := awaitDecision(t, p, r.ID)
	settle()
	if open, _ := p.Decisions(r.ID); len(open) != 1 || childrenStarted(f) != 0 {
		t.Fatalf("%d open questions and %d children started with four waiters", len(open), childrenStarted(f))
	}
	if err := p.Answer(r.ID, first.ID, "stop"); err != nil {
		t.Fatal(err)
	}
	settle()
	if open, _ := p.Decisions(r.ID); len(open) != 0 || childrenStarted(f) != 0 {
		t.Fatalf("after stop: %d open questions, %d children started", len(open), childrenStarted(f))
	}
	if state, _ := p.State(r.ID); state.Status != "paused" {
		t.Fatalf("a stop answer leaves the run %s, want paused", state.Status)
	}
	select {
	case err := <-done:
		t.Fatalf("the run ended on a stop answer: %v", err)
	default:
	}

	if err := p.Transition(r.ID, "resumed"); err != nil {
		t.Fatal(err)
	}
	second := awaitDecision(t, p, r.ID)
	settle()
	if second.ID == first.ID {
		t.Fatal("resuming past an unlifted budget must ask again")
	}
	if open, _ := p.Decisions(r.ID); len(open) != 1 || childrenStarted(f) != 0 {
		t.Fatalf("after resume: %d open questions, %d children started; four waiters must share one question", len(open), childrenStarted(f))
	}
	if err := p.Answer(r.ID, second.ID, "continue"); err != nil {
		t.Fatal(err)
	}
	events := finished(t, p, r, done)
	if countEvents(events, "budget.exceeded", "") != 2 || countEvents(events, "budget.continued", "") != 1 {
		t.Fatalf("%d questions and %d continues journaled; want 2 and 1", countEvents(events, "budget.exceeded", ""), countEvents(events, "budget.continued", ""))
	}
	for i := 1; i <= 4; i++ {
		if id := fmt.Sprintf("split.%d", i); f.ran[id] != 1 {
			t.Fatalf("%s ran %d times", id, f.ran[id])
		}
	}
}

// Stopping the supervisor frees every waiter without launching any of them, and
// the resumed run waits on the question the old supervisor left open.
func TestBudgetCancellationReleasesEveryWaiterAndResumeReusesTheQuestion(t *testing.T) {
	p, r, f, cancel, done := budgetFanOut(t, func(s *Settings) { s.Limits.MaxRunTokens = Int(50) }, nil)
	first := awaitDecision(t, p, r.ID)
	settle()
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("a cancelled supervisor reported success")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("waiters at the budget gate were not released by the cancellation")
	}
	if childrenStarted(f) != 0 {
		t.Fatalf("%d children started while the budget stood", childrenStarted(f))
	}
	events, err := p.Events(r.ID)
	if err != nil || events[len(events)-1].Type != "workflow.interrupted" {
		t.Fatalf("the journal must end interrupted: %v", err)
	}

	resumed := newFanOutFixture(4)
	ctx, stop := context.WithTimeout(context.Background(), 60*time.Second)
	defer stop()
	resumedDone := make(chan error, 1)
	go func() {
		resumedDone <- NewEngine(p, r, testInvoker(reportingPlanner(resumed))).Execute(ctx, true, false)
	}()
	second := awaitDecision(t, p, r.ID)
	settle()
	if open, _ := p.Decisions(r.ID); len(open) != 1 || childrenStarted(resumed) != 0 {
		t.Fatalf("after resume: %d open questions, %d children started", len(open), childrenStarted(resumed))
	}
	if r.Retention == "full-local" && second.ID != first.ID {
		t.Fatalf("the resumed run asked again (%s) instead of waiting on the open question %s", second.ID, first.ID)
	}
	if err := p.Answer(r.ID, second.ID, "continue"); err != nil {
		t.Fatal(err)
	}
	events = finished(t, p, r, resumedDone)
	if r.Retention == "full-local" && countEvents(events, "budget.exceeded", "") != 1 {
		t.Fatalf("the reused question was journaled %d times", countEvents(events, "budget.exceeded", ""))
	}
	for i := 1; i <= 4; i++ {
		if id := fmt.Sprintf("split.%d", i); resumed.ran[id] != 1 {
			t.Fatalf("%s ran %d times", id, resumed.ran[id])
		}
	}
}

// Continuing past the token budget lifts that budget only. A reached minutes
// budget is asked about next, before the child whose question was just answered
// or any waiter launches.
func TestBudgetContinueLiftsOnlyTheBudgetItAnswered(t *testing.T) {
	var skew atomic.Int64
	now := func() time.Time { return time.Now().Add(time.Duration(skew.Load())) }
	p, r, f, _, done := budgetFanOut(t, func(s *Settings) {
		s.Limits.MaxRunTokens = Int(50)
		s.Limits.MaxRunMinutes = Int(5)
	}, now)
	tokens := awaitDecision(t, p, r.ID)
	if tokens.Context["budget"] != "tokens" {
		t.Fatalf("first question %+v", tokens.Context)
	}
	settle()
	// An hour passes while the question is open, so minutes is reached too.
	skew.Store(int64(time.Hour))
	if err := p.Answer(r.ID, tokens.ID, "continue"); err != nil {
		t.Fatal(err)
	}
	minutes := awaitDecision(t, p, r.ID)
	settle()
	if minutes.ID == tokens.ID || minutes.Context["budget"] != "minutes" {
		t.Fatalf("second question %+v", minutes.Context)
	}
	if open, _ := p.Decisions(r.ID); len(open) != 1 || childrenStarted(f) != 0 {
		t.Fatalf("%d open questions, %d children started; continuing tokens must not lift minutes", len(open), childrenStarted(f))
	}
	if err := p.Answer(r.ID, minutes.ID, "continue"); err != nil {
		t.Fatal(err)
	}
	events := finished(t, p, r, done)
	lifted := []string{}
	for _, v := range events {
		if v.Type == "budget.continued" {
			lifted = append(lifted, str(v.Data["budget"]))
		}
	}
	if strings.Join(lifted, ",") != "tokens,minutes" || countEvents(events, "budget.exceeded", "") != 2 {
		t.Fatalf("lifted %v after %d questions; want tokens then minutes after two", lifted, countEvents(events, "budget.exceeded", ""))
	}
	for i := 1; i <= 4; i++ {
		if id := fmt.Sprintf("split.%d", i); f.ran[id] != 1 {
			t.Fatalf("%s ran %d times", id, f.ran[id])
		}
	}
}

// An approved retry starts the provider again, so it waits at the budget gate
// like any launch: the retry answer must not carry it past a reached budget.
func TestBudgetGatesAnApprovedRetry(t *testing.T) {
	p, r := loopRun(t, 2)
	r.Settings.Limits.MaxRunTokens = Int(50)
	r.Settings.Limits.MaxRetriesPerAgent = 1
	f := &loopFixture{verdicts: func(int) string { return "Approved" }, ran: map[string]int{}}
	var mu sync.Mutex
	builds := 0
	invoker := testInvoker(func(ctx context.Context, in Invocation) (map[string]any, error) {
		if in.AgentID == "builder" {
			mu.Lock()
			builds++
			attempt := builds
			mu.Unlock()
			if attempt == 1 {
				// The first attempt spends the budget and then fails.
				if err := in.Usage(map[string]any{"input_tokens": 60, "output_tokens": 40}); err != nil {
					return nil, err
				}
				return nil, fmt.Errorf("provider failed")
			}
		}
		return f.invoke(ctx, in)
	})
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- NewEngine(p, r, invoker).Execute(ctx, false, false) }()

	retry := awaitDecision(t, p, r.ID)
	if retry.Kind != "retry" {
		t.Fatalf("first question %+v", retry)
	}
	if err := p.Answer(r.ID, retry.ID, "retry"); err != nil {
		t.Fatal(err)
	}
	budget := awaitDecision(t, p, r.ID)
	settle()
	mu.Lock()
	started := builds
	mu.Unlock()
	if budget.Kind != "budget" || started != 1 {
		t.Fatalf("after the retry answer: question %q, builder started %d times; the retry must wait at the budget", budget.Kind, started)
	}
	if err := p.Answer(r.ID, budget.ID, "continue"); err != nil {
		t.Fatal(err)
	}
	finished(t, p, r, done)
	if builds != 2 {
		t.Fatalf("builder ran %d times", builds)
	}
}

// An on-call agent runs beside a paused or failed run and cannot wait for an
// answer, so while a budget stands it is skipped and journaled, not launched.
func TestBudgetSkipsOnCallAgentsWhileItStands(t *testing.T) {
	p := committedProject(t)
	r := teamRun(t, p)
	r.Settings.Limits.MaxRunTokens = Int(50)
	if _, err := p.Append(r.ID, r.Workflow.Id, "head", "usage.reported", map[string]any{"provider": "codex", "usage": map[string]any{"input_tokens": 60, "output_tokens": 40}}); err != nil {
		t.Fatal(err)
	}
	var launched atomic.Int32
	engine := NewEngine(p, r, testInvoker(func(context.Context, Invocation) (map[string]any, error) {
		launched.Add(1)
		return ordinaryResult(), nil
	}))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := engine.onCall(ctx, "specialist", Event{ID: "manual-1", Type: "manual", AgentID: "user"}, true); err != nil {
		t.Fatal(err)
	}
	events, _ := p.Events(r.ID)
	var skipped Event
	for _, v := range events {
		if v.Type == "on-call.skipped" {
			skipped = v
		}
	}
	if launched.Load() != 0 || countEvents(events, "on-call.started", "") != 0 || skipped.Data["budget"] != "tokens" {
		t.Fatalf("launched %d, started %d, skipped %+v", launched.Load(), countEvents(events, "on-call.started", ""), skipped.Data)
	}
	if open, _ := p.Decisions(r.ID); len(open) != 0 {
		t.Fatalf("skipping an on-call agent raised %d questions", len(open))
	}

	// Once the user has continued past the budget the agent runs.
	if _, err := p.Append(r.ID, r.Workflow.Id, "", "budget.continued", map[string]any{"budget": "tokens"}); err != nil {
		t.Fatal(err)
	}
	if err := engine.onCall(ctx, "specialist", Event{ID: "manual-2", Type: "manual", AgentID: "user"}, true); err != nil {
		t.Fatal(err)
	}
	if launched.Load() != 1 {
		t.Fatalf("the on-call agent launched %d times after the budget was lifted", launched.Load())
	}
}

// Many launches reach the gate in the same instant while the budget stands. The
// gate is the only thing that makes them share one question: each caller reads
// a running run, so without it every one of them would pause and ask.
func TestBudgetGateSerializesCallersThatArriveTogether(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	r.Settings.Limits.MaxRunTokens = Int(50)
	appendEvents(t, p, r, "", "workflow.started")
	if _, err := p.Append(r.ID, r.Workflow.Id, "head", "usage.reported", map[string]any{"provider": "codex", "usage": map[string]any{"input_tokens": 60, "output_tokens": 40}}); err != nil {
		t.Fatal(err)
	}
	e := NewEngine(p, r, testInvoker(func(context.Context, Invocation) (map[string]any, error) { return ordinaryResult(), nil }))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const callers = 24
	start := make(chan struct{})
	results := make(chan error, callers)
	for i := 0; i < callers; i++ {
		go func() {
			<-start
			launch, err := e.enforceBudget(ctx)
			if err == nil && !launch {
				err = fmt.Errorf("a launch was held after the budget was continued")
			}
			results <- err
		}()
	}
	close(start)
	question := awaitDecision(t, p, r.ID)
	settle()
	if open, _ := p.Decisions(r.ID); len(open) != 1 {
		t.Fatalf("%d callers raised %d questions; want one", callers, len(open))
	}
	select {
	case err := <-results:
		t.Fatalf("a caller got past the open question: %v", err)
	default:
	}
	if err := p.Answer(r.ID, question.ID, "continue"); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < callers; i++ {
		select {
		case err := <-results:
			if err != nil {
				t.Fatal(err)
			}
		case <-time.After(10 * time.Second):
			t.Fatalf("%d of %d callers were released", i, callers)
		}
	}
	events, _ := p.Events(r.ID)
	if countEvents(events, "budget.exceeded", "") != 1 || countEvents(events, "budget.continued", "") != 1 {
		t.Fatalf("%d questions and %d continues journaled; want 1 and 1", countEvents(events, "budget.exceeded", ""), countEvents(events, "budget.continued", ""))
	}
}
