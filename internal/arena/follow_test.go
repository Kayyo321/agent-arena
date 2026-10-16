package arena

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// syncBuffer lets a test read what a command in another goroutine has written.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}
func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}
func (s *syncBuffer) lines() int { return strings.Count(s.String(), "\n") }

func startCLI(ctx context.Context, out *syncBuffer, args ...string) <-chan error {
	done := make(chan error, 1)
	go func() { done <- NewCLI(strings.NewReader(""), out, out).Run(ctx, args) }()
	return done
}

func waitFor(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !ok(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
	}
}

func waitDone(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("command did not stop")
		return nil
	}
}

func appendEvents(t *testing.T, p *Project, r Run, agent string, kinds ...string) {
	t.Helper()
	for _, kind := range kinds {
		if _, err := p.Append(r.ID, r.Workflow.Id, agent, kind, map[string]any{}); err != nil {
			t.Fatal(err)
		}
	}
}

// streamed decodes NDJSON output.
func streamed(t *testing.T, out string) []Event {
	t.Helper()
	events := []Event{}
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		var e Event
		if err := DecodeJSON([]byte(line), &e); err != nil {
			t.Fatalf("not NDJSON: %v\n%q", err, line)
		}
		events = append(events, e)
	}
	return events
}

// anotherRun starts a second run of the same workflow.
func anotherRun(t *testing.T, p *Project, r Run) Run {
	t.Helper()
	next, err := p.CreateRun(r.Workflow, r.Profiles, r.Settings, "second run", r.BaseCommit)
	if err != nil {
		t.Fatal(err)
	}
	return next
}

func mirror(t *testing.T, p *Project, r Run) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(p.RunDir(r.ID), "events.ndjson"))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestWatchFollowStreamsEachEventOnceInOrderUntilTerminal(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	out := &syncBuffer{}
	done := startCLI(context.Background(), out, "watch", r.ID, "--follow", "--json", "--project", p.Root)
	// The backlog arrives first; everything after it is appended while the
	// follower polls, across several ticks.
	waitFor(t, "the backlog", func() bool { return out.lines() == 1 })
	appendEvents(t, p, r, "", "workflow.started")
	for i := 0; i < 6; i++ {
		appendEvents(t, p, r, "head", "agent.progress")
		time.Sleep(40 * time.Millisecond)
	}
	select {
	case err := <-done:
		t.Fatalf("stopped before the run ended: %v", err)
	default:
	}
	appendEvents(t, p, r, "", "workflow.succeeded")
	if err := waitDone(t, done); err != nil {
		t.Fatal(err)
	}
	events := streamed(t, out.String())
	if len(events) != 9 {
		t.Fatalf("%d events streamed:\n%s", len(events), out.String())
	}
	for i, e := range events {
		if e.Sequence != i+1 {
			t.Fatalf("event %d has sequence %d: duplicated or reordered", i, e.Sequence)
		}
	}
	if events[0].Type != "workflow.created" || events[1].Type != "workflow.started" || events[8].Type != "workflow.succeeded" {
		t.Fatalf("unexpected order: %s", out.String())
	}
	// The stream is the journal mirror, so a consumer can still check the chain.
	if out.String() != mirror(t, p, r) {
		t.Fatalf("stream differs from events.ndjson:\n%s\n%s", out.String(), mirror(t, p, r))
	}
}

func TestWatchFollowKeepsEventsRecordedJustAfterTheTerminalEvent(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	appendEvents(t, p, r, "", "workflow.started")
	appendEvents(t, p, r, "head", "agent.started")
	out := &syncBuffer{}
	done := startCLI(context.Background(), out, "watch", r.ID, "--follow", "--json", "--project", p.Root)
	waitFor(t, "the backlog", func() bool { return out.lines() == 3 })
	// Cancelling records agent.cancelled for the agents it stops after the
	// run itself is cancelled, so it can land after the follower has already
	// printed the terminal event.
	appendEvents(t, p, r, "", "workflow.cancelled")
	waitFor(t, "the terminal event", func() bool { return out.lines() == 4 })
	appendEvents(t, p, r, "head", "agent.cancelled")
	if err := waitDone(t, done); err != nil {
		t.Fatal(err)
	}
	events := streamed(t, out.String())
	if len(events) != 5 || events[3].Type != "workflow.cancelled" || events[4].Type != "agent.cancelled" {
		t.Fatalf("late event lost:\n%s", out.String())
	}
}

func TestWatchFollowEndsWhenTheRunIsInterrupted(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	appendEvents(t, p, r, "", "workflow.started")
	out := &syncBuffer{}
	done := startCLI(context.Background(), out, "watch", r.ID, "--follow", "--json", "--project", p.Root)
	waitFor(t, "the backlog", func() bool { return out.lines() == 2 })
	// The supervisor stopped (a closed tmux pane, a killed process): no one is
	// left to write events, so the follower must not poll forever.
	appendEvents(t, p, r, "", "workflow.interrupted")
	if err := waitDone(t, done); err != nil {
		t.Fatal(err)
	}
	events := streamed(t, out.String())
	if len(events) != 3 || events[2].Type != "workflow.interrupted" {
		t.Fatalf("stream:\n%s", out.String())
	}
}

func TestWatchJSONOnAFinishedRunPrintsTheJournalOnceAndStops(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	appendEvents(t, p, r, "", "workflow.started", "workflow.succeeded")
	for _, flags := range [][]string{{"--json"}, {"--json", "--follow"}} {
		out := &syncBuffer{}
		args := append([]string{"watch", r.ID, "--project", p.Root}, flags...)
		if err := waitDone(t, startCLI(context.Background(), out, args...)); err != nil {
			t.Fatalf("%v: %v", flags, err)
		}
		if out.String() != mirror(t, p, r) {
			t.Fatalf("%v printed:\n%s", flags, out.String())
		}
	}
}

func TestWatchFollowStopsOnInterruptWithoutError(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	appendEvents(t, p, r, "", "workflow.started")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &syncBuffer{}
	done := startCLI(ctx, out, "watch", r.ID, "--follow", "--json", "--project", p.Root)
	waitFor(t, "the backlog", func() bool { return out.lines() == 2 })
	cancel()
	if err := waitDone(t, done); err != nil {
		t.Fatalf("interrupt reported as an error: %v", err)
	}
	if events := streamed(t, out.String()); len(events) != 2 {
		t.Fatalf("%d events after interrupt", len(events))
	}
}

func TestWatchFollowReportsAJournalItCannotFollow(t *testing.T) {
	var sink syncBuffer
	p := committedProject(t)
	r := testRun(t, p)
	cli := NewCLI(strings.NewReader(""), &sink, &sink)
	if err := cli.Run(context.Background(), []string{"watch", r.ID, "--follow", "--project", p.Root}); err == nil || !strings.Contains(err.Error(), "--json") {
		t.Fatalf("--follow without --json: %v", err)
	}
	if err := cli.Run(context.Background(), []string{"watch", "run-missing", "--json", "--project", p.Root}); err == nil {
		t.Fatal("unknown run followed")
	}

	// History that disappears under a follower is an error, not a hang.
	appendEvents(t, p, r, "", "workflow.started")
	out := &syncBuffer{}
	done := startCLI(context.Background(), out, "watch", r.ID, "--follow", "--json", "--project", p.Root)
	waitFor(t, "the backlog", func() bool { return out.lines() == 2 })
	if err := p.PruneRun(r.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := waitDone(t, done); err == nil || !strings.Contains(err.Error(), "retired") {
		t.Fatalf("retired run: %v", err)
	}

	// Events that do not replay still stream, then the follower says why it stopped.
	r = anotherRun(t, p, r)
	appendEvents(t, p, r, "", "workflow.started", "workflow.succeeded", "workflow.succeeded")
	out = &syncBuffer{}
	err := waitDone(t, startCLI(context.Background(), out, "watch", r.ID, "--follow", "--json", "--project", p.Root))
	if err == nil || !strings.Contains(err.Error(), "do not replay") || len(streamed(t, out.String())) != 4 {
		t.Fatalf("invalid journal: %v\n%s", err, out.String())
	}
}

// viewRun builds a run whose agent "head" has public activity and whose
// "reviewer" has some that head's view must not show.
func viewRun(t *testing.T, p *Project) Run {
	t.Helper()
	r := testRun(t, p)
	appendEvents(t, p, r, "", "workflow.started")
	appendEvents(t, p, r, "head", "agent.started")
	record := func(agent, kind string, data map[string]any) {
		t.Helper()
		if _, err := p.Append(r.ID, r.Workflow.Id, agent, kind, data); err != nil {
			t.Fatal(err)
		}
	}
	record("head", "agent.progress", map[string]any{"summary": "reading base.txt"})
	record("reviewer", "agent.progress", map[string]any{"summary": "someone else's step"})
	record("head", "provider.activity", map[string]any{"type": "command", "status": "completed", "command": "go test"})
	record("head", "process.output", map[string]any{"bytes": 42, "contentHash": "abc"})
	record("head", "agent.progress", map[string]any{"summary": "\x1b]52;c;aGk=\x07\x1b[31mred\x1b[0m done"})
	return r
}

func finishHead(t *testing.T, p *Project, r Run) {
	t.Helper()
	if _, err := p.Append(r.ID, r.Workflow.Id, "head", "agent.succeeded", map[string]any{"result": map[string]any{"summary": "all good"}}); err != nil {
		t.Fatal(err)
	}
	appendEvents(t, p, r, "", "workflow.succeeded")
}

func TestViewPrintsOneAgentsActivityAndSaysWhenOutputIsNotRetained(t *testing.T) {
	p := committedProject(t)
	r := viewRun(t, p)
	finishHead(t, p, r)
	out := &syncBuffer{}
	if err := waitDone(t, startCLI(context.Background(), out, "view", "head", "--run", r.ID, "--project", p.Root)); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	if strings.ContainsAny(text, "\x1b\x07") {
		t.Fatalf("provider text drove the terminal: %q", text)
	}
	want := []string{
		"head | succeeded",
		"codex / explicit-test-model / high",
		"Raw process output is not retained for this run (retention full-local, process output summary)",
		"agent.started",
		"reading base.txt",
		"command completed go test",
		"Output recorded: 42 bytes (body omitted by retention policy)",
		"red done",
		"Result: all good",
		"STRUCTURED RESULT:",
	}
	at := 0
	for _, fragment := range want {
		i := strings.Index(text[at:], fragment)
		if i < 0 {
			t.Fatalf("missing or out of order %q in:\n%s", fragment, text)
		}
		at += i + len(fragment)
	}
	if strings.Contains(text, "someone else") || strings.Contains(text, "Tab: context") {
		t.Fatalf("view leaked another agent or a live-view hint:\n%s", text)
	}
}

func TestViewShowsRawOutputWhenTheRunKeptIt(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	r.Settings.Audit.ProcessOutput = "full"
	if err := Atomic(filepath.Join(p.RunDir(r.ID), "run.json"), SafeJSON(r), 0600); err != nil {
		t.Fatal(err)
	}
	appendEvents(t, p, r, "", "workflow.started")
	if _, err := p.Append(r.ID, r.Workflow.Id, "head", "process.output", map[string]any{"bytes": 12, "output": "tests passed\n"}); err != nil {
		t.Fatal(err)
	}
	out := &syncBuffer{}
	if err := waitDone(t, startCLI(context.Background(), out, "view", "head", "--run", r.ID, "--project", p.Root)); err != nil {
		t.Fatal(err)
	}
	if text := out.String(); !strings.Contains(text, "tests passed") || strings.Contains(text, "not retained") {
		t.Fatalf("raw output:\n%s", text)
	}

	// An agent with nothing recorded still gets a usable window.
	out = &syncBuffer{}
	if err := waitDone(t, startCLI(context.Background(), out, "view", "reviewer", "--run", r.ID, "--project", p.Root)); err == nil {
		t.Fatal("an agent the run does not know was viewed")
	}
	out = &syncBuffer{}
	r2 := anotherRun(t, p, r)
	if err := waitDone(t, startCLI(context.Background(), out, "view", "head", "--run", r2.ID, "--project", p.Root)); err != nil || !strings.Contains(out.String(), "No recorded activity yet") {
		t.Fatalf("%v\n%s", err, out.String())
	}
}

func TestViewFollowAppendsNewActivityOnceUntilTheRunEnds(t *testing.T) {
	p := committedProject(t)
	r := viewRun(t, p)
	out := &syncBuffer{}
	done := startCLI(context.Background(), out, "view", "head", "--run", r.ID, "--follow", "--project", p.Root)
	waitFor(t, "the backlog", func() bool { return strings.Contains(out.String(), "red done") })
	for i := 0; i < 4; i++ {
		if _, err := p.Append(r.ID, r.Workflow.Id, "head", "agent.progress", map[string]any{"summary": fmt.Sprintf("step %d", i)}); err != nil {
			t.Fatal(err)
		}
		if _, err := p.Append(r.ID, r.Workflow.Id, "reviewer", "agent.progress", map[string]any{"summary": "someone else's step"}); err != nil {
			t.Fatal(err)
		}
		time.Sleep(40 * time.Millisecond)
	}
	select {
	case err := <-done:
		t.Fatalf("stopped before the run ended: %v", err)
	default:
	}
	finishHead(t, p, r)
	if err := waitDone(t, done); err != nil {
		t.Fatal(err)
	}
	text := out.String()
	for _, once := range []string{"reading base.txt", "step 0", "step 1", "step 2", "step 3", "Result: all good", "STRUCTURED RESULT:", "Run succeeded"} {
		if n := strings.Count(text, once); n != 1 {
			t.Fatalf("%q appears %d times:\n%s", once, n, text)
		}
	}
	if !(strings.Index(text, "step 0") < strings.Index(text, "step 3") && strings.Index(text, "step 3") < strings.Index(text, "Result: all good")) {
		t.Fatalf("activity out of order:\n%s", text)
	}
	if strings.Contains(text, "someone else") {
		t.Fatalf("another agent's activity:\n%s", text)
	}
}

func TestViewRequiresARunAndAKnownAgent(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	cli := NewCLI(strings.NewReader(""), &syncBuffer{}, &syncBuffer{})
	if err := cli.Run(context.Background(), []string{"view", "head", "--project", p.Root}); err == nil || !strings.Contains(err.Error(), "--run") {
		t.Fatalf("no run: %v", err)
	}
	if err := cli.Run(context.Background(), []string{"view", "--run", r.ID, "--project", p.Root}); err == nil {
		t.Fatal("no agent")
	}
	if err := cli.Run(context.Background(), []string{"view", "nobody", "--run", r.ID, "--project", p.Root}); err == nil || !strings.Contains(err.Error(), "agents: head") {
		t.Fatalf("unknown agent: %v", err)
	}
}

// `arena tmux resume` starts its observer panes at the same moment as the
// supervisor, so they begin on a run whose journal still ends in
// workflow.interrupted: the supervisor probes providers before it records
// workflow.resumed. A follower started in that state must wait for the resume,
// not decide there is no supervisor and quit.
func TestWatchFollowOfAnAlreadyInterruptedRunWaitsForItToBeResumed(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	appendEvents(t, p, r, "", "workflow.started", "workflow.interrupted")
	out := &syncBuffer{}
	done := startCLI(context.Background(), out, "watch", r.ID, "--follow", "--json", "--project", p.Root)
	waitFor(t, "the backlog", func() bool { return out.lines() == 3 })
	time.Sleep(5 * followInterval)
	select {
	case err := <-done:
		t.Fatalf("quit while the run could still be resumed: %v", err)
	default:
	}
	appendEvents(t, p, r, "", "workflow.resumed")
	appendEvents(t, p, r, "head", "agent.progress")
	appendEvents(t, p, r, "", "workflow.succeeded")
	if err := waitDone(t, done); err != nil {
		t.Fatal(err)
	}
	if out.String() != mirror(t, p, r) || len(streamed(t, out.String())) != 6 {
		t.Fatalf("the resumed run was not followed to its end:\n%s", out.String())
	}
}

// A real run is interrupted under both kinds of follower, then resumed. Followers
// attached when the supervisor stops exit with nothing missing; one started
// while the run is interrupted rides through the resume to completion.
func TestFollowersExitWhenARealRunIsInterruptedAndSurviveItsResume(t *testing.T) {
	p := committedProject(t)
	s, _ := p.Settings()
	w, profiles := fanOutWorkflow(s, 4, "auto-within-cap")
	r := splitRun(t, p, w, profiles)
	watching, viewing := &syncBuffer{}, &syncBuffer{}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	watchDone := startCLI(ctx, watching, "watch", r.ID, "--follow", "--json", "--project", p.Root)
	viewDone := startCLI(ctx, viewing, "view", "lead", "--run", r.ID, "--follow", "--project", p.Root)
	waitFor(t, "the followers to attach", func() bool { return watching.lines() >= 1 && viewing.lines() >= 1 })

	// split.2 is held until the supervisor is stopped.
	engineCtx, stop := context.WithCancel(ctx)
	first := newFanOutFixture(2)
	first.block = func(_ context.Context, id string) bool {
		if id == "split.2" {
			stop()
			return true
		}
		return false
	}
	if err := <-startFanOut(engineCtx, p, r, first, false); err == nil {
		t.Fatal("the stopped supervisor reported success")
	}
	atInterrupt, err := p.Events(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if last := atInterrupt[len(atInterrupt)-1]; last.Type != "workflow.interrupted" {
		t.Fatalf("the journal ends in %s, want workflow.interrupted", last.Type)
	}

	// Both attached followers end cleanly, having delivered the whole journal.
	if err := waitDone(t, watchDone); err != nil {
		t.Fatalf("watch after the interrupt: %v", err)
	}
	if err := waitDone(t, viewDone); err != nil {
		t.Fatalf("view after the interrupt: %v", err)
	}
	if streamedEvents := streamed(t, watching.String()); len(streamedEvents) != len(atInterrupt) {
		t.Fatalf("watch delivered %d of %d events", len(streamedEvents), len(atInterrupt))
	}
	if !strings.Contains(viewing.String(), "Run interrupted") {
		t.Fatalf("view did not end on the interrupted run:\n%s", viewing.String())
	}

	// A follower started now waits for the resume and follows it to the end.
	late := &syncBuffer{}
	lateDone := startCLI(ctx, late, "watch", r.ID, "--follow", "--json", "--project", p.Root)
	waitFor(t, "the late follower's backlog", func() bool { return late.lines() == len(atInterrupt) })
	time.Sleep(4 * followInterval)
	select {
	case err := <-lateDone:
		t.Fatalf("a follower of an interrupted run quit before the resume: %v", err)
	default:
	}
	events := finished(t, p, r, startFanOut(ctx, p, r, newFanOutFixture(2), true))
	if err := waitDone(t, lateDone); err != nil {
		t.Fatal(err)
	}
	if late.String() != mirror(t, p, r) || len(streamed(t, late.String())) != len(events) {
		t.Fatalf("the late follower missed part of the resumed run: %d of %d events", len(streamed(t, late.String())), len(events))
	}
}
