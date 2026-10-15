package arena

import (
	"context"
	"fmt"
	"strings"
	"sync"

	tea "charm.land/bubbletea/v2"
	assets "github.com/Kayyo321/agent-arena"
)

type WorkflowDraft struct {
	Workflow Workflow           `json:"workflow"`
	Profiles map[string]Profile `json:"profiles"`
}

func interviewInstructions() string {
	text := "You are the permanent arena-orchestrator. Interview the user about what to build and its acceptance criteria, then propose a NEW workflow. Ask concise questions for every missing provider, model, supported effort, role, edge, team, router and policy choice. The default runtime belongs only to you; never assign it to workers without the user's choice. Return question, complete and builderRequestJson. Set complete=true only when all required choices are supplied. builderRequestJson must contain workflow and profiles objects following the schemas. In this embedded interview, the Arena host performs skill installation, runtime readiness checks and configuration authoring on your behalf. Do not run CLI/status commands or use tools to edit configuration or run workers. Use only the supplied readiness evidence; never invent failures or access claims. Arena previews, validates and saves the draft after explicit /save. Provide concise public progress summaries, never private reasoning. Apply the skill below within this host-owned interview flow.\n" + string(assets.Skill())
	for _, name := range []string{"workflow", "agent"} {
		b, _ := assets.Files.ReadFile("schemas/" + name + ".schema.json")
		text += "\nSchema: " + string(b)
	}
	return text
}

func setupInvocation(p *Project, s Settings, transcript string) (Invocation, Packet, error) {
	if len(transcript) > 2<<20 {
		return Invocation{}, nil, fmt.Errorf("setup context exceeds 2 MiB; start a new conversation")
	}
	head, err := Git(p.Root, "rev-parse", "HEAD")
	if err != nil {
		head = "unborn"
	}
	packet := NewPacket("project", "task", "user", "arena", "arena-orchestrator", s.DefaultModel.Provider, "", taskPayload("Interview the user and propose the requested workflow", p.Root, head, []string{Redact(transcript)}, false), s.DefaultModel)
	if err := ValidatePacket(packet, Workflow{}); err != nil {
		return Invocation{}, nil, err
	}
	var profile Profile
	if err := ReadJSON(p.Path(s.PermanentOnCallAgent.Profile), &profile); err != nil {
		return Invocation{}, nil, err
	}
	in := Invocation{Runtime: s.DefaultModel, Dir: p.Root, AgentID: "arena-orchestrator", Prompt: "Role profile:\n" + string(SafeJSON(profile)) + "\n" + interviewInstructions() + "\nArena envelope:\n" + string(SafeJSON(packet)), Schema: objectSchema(map[string]any{"question": map[string]any{"type": "string"}, "complete": map[string]any{"type": "boolean"}, "builderRequestJson": map[string]any{"type": "string"}})}
	return in, packet, nil
}

func decodeWorkflowDraft(p *Project, s Settings, text string) (*WorkflowDraft, error) {
	if len(text) > 2<<20 {
		return nil, fmt.Errorf("workflow draft exceeds 2 MiB")
	}
	var draft WorkflowDraft
	if err := DecodeJSON([]byte(text), &draft); err != nil {
		return nil, fmt.Errorf("invalid workflow draft: %w", err)
	}
	if err := ValidateWorkflow(draft.Workflow, draft.Profiles, s); err != nil {
		return nil, err
	}
	if ids, err := p.List(); err != nil {
		return nil, err
	} else if Has(ids, draft.Workflow.Id) {
		return nil, fmt.Errorf("workflow %s already exists; choose a new ID or use arena workflow edit", draft.Workflow.Id)
	}
	return &draft, nil
}

type chatLine struct{ Role, Text string }
type interviewResult struct {
	Question string
	Draft    *WorkflowDraft
	Err      error
}
type interviewEvent struct {
	Event    *Event
	Result   *interviewResult
	Question *interviewQuestion
	Next     <-chan interviewEvent
}
type interviewQuestion struct {
	Prompt string
	Reply  chan string
}
type draftSaved struct {
	Run Run
	Err error
}

type interviewSession struct {
	ctx      context.Context
	cancel   context.CancelFunc
	workers  sync.WaitGroup
	project  *Project
	settings Settings
	invoker  Invoker
	probe    func(context.Context, string, Runtime) Readiness
}

func newInterviewSession(ctx context.Context, p *Project, s Settings) *interviewSession {
	ctx, cancel := context.WithCancel(ctx)
	return &interviewSession{ctx: ctx, cancel: cancel, project: p, settings: s, invoker: Providers{}, probe: Probe}
}
func (s *interviewSession) close() { s.cancel(); s.workers.Wait() }
func waitInterview(ch <-chan interviewEvent) tea.Cmd {
	return func() tea.Msg { v := <-ch; v.Next = ch; return v }
}
func (s *interviewSession) turn(transcript string) tea.Cmd {
	ch := make(chan interviewEvent, 32)
	s.workers.Add(1)
	go func() {
		defer s.workers.Done()
		defer close(ch)
		result := interviewResult{}
		defer func() {
			select {
			case ch <- interviewEvent{Result: &result}:
			case <-s.ctx.Done():
			}
		}()
		emit := func(kind string, data map[string]any) error {
			event, err := s.project.Append("project", "", "arena-orchestrator", kind, data)
			if err != nil {
				return err
			}
			select {
			case ch <- interviewEvent{Event: &event}:
				return nil
			case <-s.ctx.Done():
				return s.ctx.Err()
			}
		}
		in, packet, err := setupInvocation(s.project, s.settings, transcript)
		if err != nil {
			result.Err = err
			return
		}
		bounded, cancel := inferenceDeadline(s.ctx, s.settings.Limits.MaxAgentRunMinutes)
		defer cancel()
		slot, err := s.project.Lock("orchestrator")
		if err != nil {
			result.Err = fmt.Errorf("project orchestrator is busy or needs lock recovery: %w", err)
			return
		}
		defer slot.Close()
		status := s.probe(bounded, s.project.Root, s.settings.DefaultModel)
		if !status.CanStart {
			result.Err = fmt.Errorf("%s: %s (%s)", status.State, status.Detail, status.Auth)
			return
		}
		in.Prompt += "\nHost readiness evidence for the control runtime (not evidence for any worker):\n" + string(SafeJSON(status))
		run := Run{ID: "project", Settings: s.settings, Retention: s.settings.Audit.Retention}
		if _, err := s.project.Send(run, packet); err != nil {
			result.Err = err
			return
		}
		in.Delivered = func() error { return s.project.Acknowledge(run, packet) }
		in.Diagnostic = emit
		in.Question = func(ctx context.Context, prompt string) (string, error) {
			question := &interviewQuestion{Redact(prompt), make(chan string, 1)}
			select {
			case ch <- interviewEvent{Question: question}:
			case <-ctx.Done():
				return "", ctx.Err()
			}
			select {
			case answer := <-question.Reply:
				return Redact(answer), nil
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		in.Usage = func(data map[string]any) error { return emit("usage.reported", map[string]any{"usage": data}) }
		if err := emit("setup.started", map[string]any{"model": in.Runtime.Model}); err != nil {
			result.Err = err
			return
		}
		out, err := s.invoker.Invoke(bounded, in)
		if err != nil {
			result.Err = err
			_ = emit("setup.failed", map[string]any{"error": err.Error()})
			return
		}
		result.Question = strings.TrimSpace(str(out["question"]))
		if out["complete"] == true {
			result.Draft, result.Err = decodeWorkflowDraft(s.project, s.settings, str(out["builderRequestJson"]))
		} else if result.Question == "" {
			result.Err = fmt.Errorf("orchestrator returned no question or complete workflow")
		}
		_ = emit("setup.replied", map[string]any{"text": result.Question, "draftReady": result.Draft != nil})
	}()
	return waitInterview(ch)
}

func (cli *CLI) setupUI(ctx context.Context, p *Project, s Settings) error {
	session := newUISession(ctx, p)
	defer session.close()
	interview := newInterviewSession(session.ctx, p, s)
	defer interview.close()
	profiles := map[string]Profile{}
	var control Profile
	if err := ReadJSON(p.Path(s.PermanentOnCallAgent.Profile), &control); err != nil {
		return err
	}
	profiles["arena-orchestrator"] = control
	r := Run{Workflow: NewWorkflow("setup", "Project setup"), Profiles: profiles, Settings: s, BaseCommit: "unborn"}
	m := LiveModel{Project: p, Run: r, State: RunState{Status: "setup"}, Readiness: unknownReadiness(r), Session: session, Interview: interview, ChatMode: true, Chat: []chatLine{{"arena-orchestrator", "What would you like to build? Describe the goal and acceptance criteria. I’ll ask for the workflow choices, then show a draft for /save."}}, Width: 80, Height: 24, Context: session.ctx}
	_, err := tea.NewProgram(m, tea.WithContext(session.ctx), tea.WithInput(cli.input), tea.WithOutput(cli.Out)).Run()
	return err
}
