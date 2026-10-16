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
	// Shader, Notes and Source are set when the draft came from a pasted
	// share code: the shader is saved with the arena and the notes say what
	// the user should check first.
	Shader *ShaderDraft `json:"-"`
	Notes  []string     `json:"-"`
	Source string       `json:"-"`
}

func interviewInstructions() string {
	text := `You are the permanent arena-orchestrator. Interview the user about what to build and its acceptance criteria, then propose a NEW workflow.

Choices and delegation:
- Each worker's provider, model and supported reasoning effort is the user's choice. The user can make it by delegating explicitly ("you pick", "come up with the efforts for me", "whatever fits"). When they do, choose concrete values yourself from the supplied model catalog and supported efforts, apply them, and list each value you chose in your reply. Never ask again about a delegated choice.
- Apply shorthand as the user wrote it: "all", "every agent" or "same as 1" applies that value to every matching agent. If a list could be read more than one way, pick the most reasonable reading, say which you used, and continue.
- Your own control runtime belongs only to you. Assign it to a worker only when the user names it or says to reuse yours.
- For everything else (roles, profiles, verdict labels, edges, teams, router fallback, on-call limits, workflow policies) propose sensible values that satisfy the request instead of asking. Ask only when a choice depends on intent you cannot infer, and ask every open question together in one message.
- Do not ask about worker readiness; Arena checks each runtime when the graph opens and before every run.
- Project settings (approval policies, limits, shared-context storage, retention) are listed below and are not part of the draft. Do not ask about them or call them unsupported. If the request differs from them, say so once and name ` + "`arena settings edit`" + `.

Graph rules (Arena rejects drafts that break any of them):
- "delegates" relationships are the work path. A node starts after every node that delegates to it and receives their results. Every agent, team and router, except on-call agents and team members, must be reachable from headAgent through delegates edges.
- Delegation must advance levels: level(to) > level(from). A team has its members' level. Routers have no level.
- A team is one node: delegate into the team ID (never to its members) and from the team to whatever consumes its verdict.
- A decision router receives packets only through an incoming delegates edge. Its outgoing paths come only from routes and fallback; never add delegates edges from a router. Route targets are path agents, teams or routers, never on-call agents. A team-result router reads /verdict.
- The delegates graph has no cycles, with one exception: a route with "loop": true and "maxIterations" (1-5) may point back to an ancestor of its router (a node that leads to the router through delegates edges), e.g. ChangesRequested back to the builder. When it fires, Arena reruns everything below that node in new worktrees built on the previous changes and hands the verdict packet to the target as feedback; once maxIterations revisions are spent, the router's fallback applies instead. Propose one when the user wants work revised until reviewers approve; never add any other back edge.
- "communicates" links are peer messaging between two standalone path agents. They make nothing reachable.
- On-call agents have no level and run only through onCall triggers.
Example: lead(0) delegates builder(1) and tester(1); builder and tester each delegate review-board (members at level 2); review-board delegates review-gate (router: inputKind team-result, payloadPointer /verdict, routes Approved->docs(3), ChangesRequested->builder with "loop": true and "maxIterations": 2, fallback {"action":"ask-user"}); builder communicates two-way with tester. Without the loop, ChangesRequested would need a separate forward target such as fixer(3).

Arena score: Arena grades every draft S+ to F with the score sheet below and shows the grade to the user before /save. Design toward a high grade from the start: no agent both coordinates and does the work, every writer has a reviewer downstream of it on a different provider or model, reviewers cannot write, ChangesRequested loops back to the writer with a small cap, and an on-call agent catches failures. Proportionality is part of the grade: never add an agent only to gain points, and keep the arena as small as the task allows; one agent is right for one-agent work. When an explicit user choice costs points (for example one provider for every agent), keep the choice and name the deduction in one sentence. When the user asks about or wants to improve the score, use the findings Arena reports for your previous draft.

Output: return question, complete and builderRequestJson. Set complete=true once every required choice is supplied or delegated; builderRequestJson must then contain workflow and profiles objects following the schemas. If Arena reports a validation error for your draft, fix the draft and return it complete again without asking the user. In this embedded interview, the Arena host performs skill installation, runtime readiness checks and configuration authoring on your behalf. Do not run CLI/status commands or use tools to edit configuration or run workers. Use only the supplied readiness evidence; never invent failures or access claims. Arena previews, validates and saves the draft after explicit /save. Provide concise public progress summaries, never private reasoning. Apply the skill below within this host-owned interview flow.
` + string(assets.Skill())
	if sheet, err := assets.Files.ReadFile("docs/ARENA-SCORING.md"); err == nil {
		text += "\nScore sheet:\n" + string(sheet)
	}
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
	in := Invocation{Runtime: s.DefaultModel, Dir: p.Root, AgentID: "arena-orchestrator", Prompt: "Role profile:\n" + string(SafeJSON(profile)) + "\n" + interviewInstructions() + "\nProject settings (settings.arena.json):\n" + string(SafeJSON(s)) + "\nArena envelope:\n" + string(SafeJSON(packet)), Schema: objectSchema(map[string]any{"question": map[string]any{"type": "string"}, "complete": map[string]any{"type": "boolean"}, "builderRequestJson": map[string]any{"type": "string"}})}
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
	Run       Run
	Err       error
	Commit    string
	CommitErr error
}

type interviewSession struct {
	ctx      context.Context
	cancel   context.CancelFunc
	workers  sync.WaitGroup
	project  *Project
	settings Settings
	invoker  Invoker
	probe    func(context.Context, string, Runtime) Readiness
	discover func(context.Context, string, Runtime) ([]ModelOption, error)
	catalog  []ModelOption
	// lastScore is Arena's grade of the previous draft, shown to the
	// orchestrator on the next turn so it can explain or improve it.
	lastScore *Scorecard
}

// setupRepairAttempts bounds how often an invalid draft goes back to the
// orchestrator with Arena's validation error before the user sees it.
const setupRepairAttempts = 2

func newInterviewSession(ctx context.Context, p *Project, s Settings) *interviewSession {
	ctx, cancel := context.WithCancel(ctx)
	return &interviewSession{ctx: ctx, cancel: cancel, project: p, settings: s, invoker: Providers{}, probe: Probe, discover: DiscoverModels}
}
func (s *interviewSession) close() { s.cancel(); s.workers.Wait() }
func waitInterview(ch <-chan interviewEvent) tea.Cmd {
	return func() tea.Msg { v := <-ch; v.Next = ch; return v }
}
func (s *interviewSession) turn(transcript string, attachments ...Attachment) tea.Cmd {
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
		if err := attachmentPolicy(attachments, s.settings.Audit.Retention, []Runtime{s.settings.DefaultModel}); err != nil {
			result.Err = err
			return
		}
		if len(attachments) > 0 {
			saved, err := s.project.saveAttachments("project", attachments)
			if err != nil {
				result.Err = err
				return
			}
			packet["payload"].(map[string]any)["attachments"] = saved
			in.Attachments, in.AttachmentDir = attachments, s.project.RunDir("project")
			in.Prompt += attachmentManifest(saved)
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
		if s.catalog == nil && s.discover != nil {
			if models, err := s.discover(bounded, s.project.Root, s.settings.DefaultModel); err == nil {
				s.catalog = models
			} else {
				s.catalog = []ModelOption{}
			}
		}
		if len(s.catalog) > 0 {
			in.Prompt += "\nModel catalog reported by " + s.settings.DefaultModel.Provider + " (exact IDs and supported efforts; listing is not proof of access). Use these IDs when the user delegates a " + s.settings.DefaultModel.Provider + " model or effort:\n" + string(SafeJSON(s.catalog))
		}
		if s.lastScore != nil {
			in.Prompt += "\nArena's score of your previous draft (rubric v" + fmt.Sprint(s.lastScore.Rubric) + "):\n" + strings.Join(s.lastScore.Report(), "\n")
		}
		run := Run{ID: "project", Settings: s.settings, Retention: s.settings.Audit.Retention}
		if _, err := s.project.Send(run, packet); err != nil {
			result.Err = err
			return
		}
		var delivered sync.Once
		var deliveredErr error
		in.Delivered = func() error {
			delivered.Do(func() { deliveredErr = s.project.Acknowledge(run, packet) })
			return deliveredErr
		}
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
		prompt := in.Prompt
		for attempt := 0; ; attempt++ {
			out, err := s.invoker.Invoke(bounded, in)
			if err != nil {
				result.Err = err
				_ = emit("setup.failed", map[string]any{"error": err.Error()})
				return
			}
			result.Question = strings.TrimSpace(str(out["question"]))
			result.Draft, result.Err = nil, nil
			if out["complete"] == true {
				result.Draft, result.Err = decodeWorkflowDraft(s.project, s.settings, str(out["builderRequestJson"]))
			} else if result.Question == "" {
				result.Err = fmt.Errorf("orchestrator returned no question or complete workflow")
			}
			if out["complete"] != true || result.Err == nil || attempt >= setupRepairAttempts {
				break
			}
			if err := emit("setup.repair", map[string]any{"attempt": attempt + 1, "error": Redact(result.Err.Error())}); err != nil {
				result.Err = err
				return
			}
			in.Prompt = prompt + "\nYour previous draft failed Arena validation: " + Redact(result.Err.Error()) + "\nPrevious draft:\n" + Redact(str(out["builderRequestJson"])) + "\nFix every graph rule it breaks and return the corrected draft with complete=true. Do not ask the user about this."
		}
		if result.Err != nil && result.Draft == nil && strings.Contains(in.Prompt, "failed Arena validation") {
			result.Err = fmt.Errorf("draft still invalid after %d automatic repairs: %w", setupRepairAttempts, result.Err)
		}
		replied := map[string]any{"text": result.Question, "draftReady": result.Draft != nil}
		if result.Draft != nil {
			score := ScoreArena(result.Draft.Workflow, result.Draft.Profiles)
			s.lastScore = &score
			replied["grade"], replied["points"] = score.Grade, score.Points
		}
		_ = emit("setup.replied", replied)
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
