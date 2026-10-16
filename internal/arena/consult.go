package arena

import (
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// consultInstructions frame a consultation: the orchestrator advises on the
// loaded arena and its run, and any edit it proposes returns as a draft that
// only the user's /save applies.
func consultInstructions(id string) string {
	return `You are the permanent arena-orchestrator. The user is consulting you from Arena's live view about the loaded arena ` + id + ` and its latest run: for example why a run stalled, how to raise the grade, or which runtime fits a role. Answer in a few short sentences or a short list, using the evidence in the envelope and the project files, and say when the evidence does not show something.

You only advise. You cannot start, steer or cancel a run, answer pending decisions, run commands or change configuration, and Arena gives you no tool that does. When the user asks for a change, or a change is clearly the best answer, also return the complete revised arena in builderRequestJson: workflow and profiles objects following the schemas, keeping workflow ID ` + id + `. Arena validates and scores it and shows it as a draft that only the user's /save applies; say so instead of claiming the change is made. Otherwise return an empty builderRequestJson. Keep every provider, model and effort the user chose: name a better fit in your answer, and change it in a draft only when the user asks you to or delegates that choice. Provide concise public answers, never private reasoning.

` + arenaDesignRules + "\n" + arenaReference()
}

// consult asks the orchestrator about arena id. evidence is the live view's
// snapshot of the run, and transcript the consultation so far.
func (s *interviewSession) consult(id, evidence, transcript string) tea.Cmd {
	var saved Workflow
	var expected string
	return s.run(orchestratorTurn{
		kind: "consult",
		build: func() (Invocation, Packet, error) {
			if len(transcript)+len(evidence) > 2<<20 {
				return Invocation{}, nil, fmt.Errorf("consultation context exceeds 2 MiB; /chat starts a new conversation")
			}
			w, profiles, version, err := s.project.LoadVersion(id)
			if err != nil {
				return Invocation{}, nil, err
			}
			saved, expected = w, version
			head, err := Git(s.project.Root, "rev-parse", "HEAD")
			if err != nil {
				head = "unborn"
			}
			packet := NewPacket("project", "task", "user", "arena", "arena-orchestrator", s.settings.DefaultModel.Provider, "", taskPayload("Answer the user's consultation about arena "+id+" without changing it", s.project.Root, head, []string{Redact(transcript), evidence}, false), s.settings.DefaultModel)
			if err := ValidatePacket(packet, Workflow{}); err != nil {
				return Invocation{}, nil, err
			}
			var profile Profile
			if err := ReadJSON(s.project.Path(s.settings.PermanentOnCallAgent.Profile), &profile); err != nil {
				return Invocation{}, nil, err
			}
			prompt := "Role profile:\n" + string(SafeJSON(profile)) + "\n" + consultInstructions(id) + "\nProject settings (settings.arena.json):\n" + string(SafeJSON(s.settings)) + "\nSaved arena " + id + " (revise this builderRequestJson):\n" + string(SafeJSON(WorkflowDraft{Workflow: w, Profiles: profiles})) + "\nArena envelope:\n" + string(SafeJSON(packet))
			return Invocation{Runtime: s.settings.DefaultModel, Dir: s.project.Root, AgentID: "arena-orchestrator", Prompt: prompt, Schema: objectSchema(map[string]any{"answer": map[string]any{"type": "string"}, "builderRequestJson": map[string]any{"type": "string"}})}, packet, nil
		},
		decode: func(out map[string]any) (interviewResult, string) {
			result := interviewResult{Question: strings.TrimSpace(str(out["answer"]))}
			if raw := strings.TrimSpace(str(out["builderRequestJson"])); raw != "" {
				result.Draft, result.Err = decodeRevision(s.settings, raw, saved, expected)
				if result.Err != nil {
					return result, "Your proposed revision failed Arena validation: " + Redact(result.Err.Error()) + "\nPrevious revision:\n" + Redact(raw) + "\nFix every rule it breaks and return the corrected revision with your answer. Do not ask the user about this."
				}
			}
			if result.Question == "" && result.Draft == nil {
				result.Err = fmt.Errorf("orchestrator returned no answer")
			}
			return result, ""
		},
	})
}

// decodeRevision accepts a revised saved arena. The draft keeps the arena's
// ID and replaces only the configuration version it was proposed against.
func decodeRevision(s Settings, text string, saved Workflow, expected string) (*WorkflowDraft, error) {
	if len(text) > 2<<20 {
		return nil, fmt.Errorf("workflow draft exceeds 2 MiB")
	}
	var draft WorkflowDraft
	if err := DecodeJSON([]byte(text), &draft); err != nil {
		return nil, fmt.Errorf("invalid workflow draft: %w", err)
	}
	if draft.Workflow.Id != saved.Id {
		return nil, fmt.Errorf("a revision keeps workflow ID %s, got %q; /chat creates a new arena", saved.Id, draft.Workflow.Id)
	}
	// The shader is not the orchestrator's to drop.
	if draft.Workflow.Settings.BackgroundShader == nil {
		draft.Workflow.Settings.BackgroundShader = saved.Settings.BackgroundShader
	}
	if err := ValidateWorkflow(draft.Workflow, draft.Profiles, s); err != nil {
		return nil, err
	}
	draft.Expected, draft.Source = expected, "consult"
	return &draft, nil
}

// consultEvidence snapshots what the live view knows about the loaded arena
// and its run for the orchestrator.
func (m LiveModel) consultEvidence() string {
	var b strings.Builder
	r := m.Run
	fmt.Fprintf(&b, "Arena %s graph:\n%s\n", r.Workflow.Id, GraphSummary(r.Workflow))
	fmt.Fprintf(&b, "Arena score:\n%s\n", strings.Join(r.scorecard().Report(), "\n"))
	if lines := readinessLines(m.Readiness); len(lines) > 0 {
		fmt.Fprintf(&b, "Required runtime readiness:\n%s\n", strings.Join(lines, "\n"))
	}
	if r.ID == "" {
		b.WriteString("No run is loaded: the arena is idle.\n")
		return Redact(b.String())
	}
	fmt.Fprintf(&b, "Run %s · status %s · objective: %s\nAgent states: %s\n", r.ID, m.State.Status, r.Objective, SafeJSON(m.State.Agents))
	for _, d := range m.Decisions {
		fmt.Fprintf(&b, "Pending decision %s from %s (%s): %s choices=%v\n", d.ID, d.AgentID, d.Kind, d.Prompt, d.Choices)
	}
	if m.Stall != nil && m.Stall.RunID == r.ID && m.Stall.Err == nil {
		fmt.Fprintf(&b, "Your earlier stall diagnosis: %s %s\n", m.Stall.Summary, m.Stall.Recommendation)
	}
	// The latest 60 events that say something, oldest first.
	recent := []string{}
	for i := len(m.Events) - 1; i >= 0 && len(recent) < 60; i-- {
		event := m.Events[i]
		if event.Type == "usage.reported" || event.Type == "process.output" {
			continue
		}
		recent = append(recent, fmt.Sprintf("%d %s %s %s", event.Sequence, event.AgentID, event.Type, short(string(JSON(event.Data)), 300)))
	}
	slices.Reverse(recent)
	fmt.Fprintf(&b, "Recent events:\n%s\n", strings.Join(recent, "\n"))
	return Redact(b.String())
}

// consultTurn sends one question to the orchestrator about the loaded arena.
func (m LiveModel) consultTurn(text string) (tea.Model, tea.Cmd) {
	switch {
	case m.ChatBusy:
		m.setPrompt(text, len(text))
		m.Error = "The orchestrator is responding; your input is preserved"
		return m, nil
	case len(m.Attachments) > 0:
		m.Error = "/consult accepts text only; detach the files first"
		return m, nil
	case m.ConsultTurns >= 50:
		m.Error = "Consultation's 50-turn limit reached; /chat or restart arena to continue"
		return m, nil
	}
	m.ensureInterview()
	if m.Interview == nil {
		m.Error = "This observer has no orchestrator session"
		return m, nil
	}
	question := Redact(text)
	m.ConsultLog += "\nUser: " + question
	m.Chat = append(m.Chat, chatLine{"you", question})
	m.ConsultTurns++
	m.ChatBusy, m.Draft, m.DraftScore = true, nil, nil
	m.Offset = 1 << 30 // Follow the newest messages.
	return m, m.Interview.consult(m.Run.Workflow.Id, m.consultEvidence(), m.ConsultLog)
}

// consultReply shows the orchestrator's answer and any revision it proposed
// as a draft; nothing is applied until /save.
func (m LiveModel) consultReply(result interviewResult) LiveModel {
	m.Draft, m.DraftScore = result.Draft, nil
	if result.Question != "" {
		m.Chat = append(m.Chat, chatLine{"arena-orchestrator", Redact(result.Question)})
		m.ConsultLog += "\nAssistant: " + Redact(result.Question)
	}
	if m.Draft != nil {
		score := ScoreArena(m.Draft.Workflow, m.Draft.Profiles)
		m.DraftScore = &score
		m.ConsultLog += "\nArena: your proposed revision is shown as a draft · " + score.Summary()
		m.Chat = append(m.Chat, chatLine{"Arena", "Proposed revision of " + m.Draft.Workflow.Id + " · " + score.Summary() + " (saved arena: " + m.Run.scorecard().Grade + "). Nothing has changed yet: review the preview below, then /save to apply it. Keep typing to ask more or revise it."})
	}
	m.Offset = 1 << 30
	return m
}
