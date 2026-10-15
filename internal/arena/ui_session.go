package arena

import (
	"context"
	"sync"

	tea "charm.land/bubbletea/v2"
)

type uiRunStarted struct {
	Run       Run
	Readiness []Readiness
	Err       error
	done      <-chan tea.Msg
}
type uiRunFinished struct{ Err error }

// The launcher owns every run it starts. Exiting cancels its context and waits
// for the attached supervisor to stop and preserve the run before returning.
type uiSession struct {
	ctx        context.Context
	cancel     context.CancelFunc
	project    *Project
	workers    sync.WaitGroup
	prepare    func(context.Context, *Project, string, string) (Run, []Readiness, error)
	execute    func(context.Context, *Project, Run) error
	interviews []*interviewSession
}

func newUISession(ctx context.Context, p *Project) *uiSession {
	ctx, cancel := context.WithCancel(ctx)
	return &uiSession{ctx: ctx, cancel: cancel, project: p, prepare: prepareNewExecution, execute: func(ctx context.Context, p *Project, r Run) error {
		return NewEngine(p, r, Providers{}).Execute(ctx, false, false)
	}}
}

func (s *uiSession) close() {
	s.cancel()
	for _, interview := range s.interviews {
		interview.close()
	}
	s.workers.Wait()
}

func (s *uiSession) action(f tea.Cmd) tea.Cmd {
	ch := make(chan tea.Msg, 1)
	s.workers.Add(1)
	go func() { defer s.workers.Done(); ch <- f() }()
	return func() tea.Msg { return <-ch }
}

func (s *uiSession) start(workflow, task string) tea.Cmd {
	messages := make(chan tea.Msg, 2)
	// Add and launch synchronously, so closing the UI cannot race a not-yet-run
	// Bubble Tea command and leave an invocation outside attached supervision.
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		r, readiness, err := s.prepare(s.ctx, s.project, workflow, task)
		messages <- uiRunStarted{r, readiness, err, messages}
		if err == nil {
			messages <- uiRunFinished{s.execute(s.ctx, s.project, r)}
		}
	}()
	return func() tea.Msg { return <-messages }
}
