package arena

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

// A run is stalled when nothing is executing and it either waits on a
// decision only the user can answer, or has been silent for stallIdle.
const (
	stallDecisionAge = 15 * time.Second
	stallIdle        = 60 * time.Second
	stallMaxPerRun   = 3
)

type stallDiagnosis struct {
	RunID, Key                                  string
	Summary, Recommendation, DecisionID, Answer string
	// Applied is set when the orchestrator answered a router fallback itself.
	Applied bool
	Err     error
}

// stallRecovery is the project's orchestrator-stall-recovery policy. Absent
// means answer-routing: the orchestrator may resolve router fallbacks, which
// only choose among the workflow's own declared branches.
func stallRecovery(s Settings) string {
	if policy := s.ApprovalPolicies.OrchestratorStallRecovery; policy != "" {
		return policy
	}
	return "answer-routing"
}

// orchestratorMayAnswer limits autonomous answers to routing choices; agent
// questions and permission approvals always stay with the user.
func orchestratorMayAnswer(s Settings, d Decision) bool {
	return d.Kind == "router.fallback" && stallRecovery(s) == "answer-routing"
}

// attentionLines pin pending decisions and the orchestrator's diagnosis
// directly above the composer, where the answer is typed.
func (m LiveModel) attentionLines(width int, color bool) []string {
	lines := []string{}
	if m.ChatMode || m.Run.ID == "" {
		return lines
	}
	for i, d := range m.Decisions {
		if i == 2 {
			lines = append(lines, styled(short(fmt.Sprintf("  +%d more pending · /decisions", len(m.Decisions)-2), width), sgrMuted, color))
			break
		}
		text := "⚑ " + d.AgentID + " needs you: " + d.Prompt
		if len(d.Choices) > 0 {
			text += " · choices: " + strings.Join(d.Choices, " / ")
		}
		hint := "type a choice"
		if len(m.Decisions) > 1 || len(d.Choices) == 0 {
			hint = "/answer " + d.ID + " ANSWER"
		}
		if orchestratorMayAnswer(m.Run.Settings, d) {
			if created, err := time.Parse(time.RFC3339Nano, d.CreatedAt); err == nil {
				if wait := stallDecisionAge - time.Since(created); wait > 0 {
					hint += fmt.Sprintf(", or arena-orchestrator decides in %ds", int(wait.Seconds())+1)
				} else {
					hint += "; arena-orchestrator is deciding"
				}
			}
		}
		lines = append(lines, styled(short(text, width), "1;38;5;214", color), styled(short("  "+hint, width), sgrMuted, color))
	}
	if m.StallBusy {
		lines = append(lines, short(m.spinner(color)+" arena-orchestrator is diagnosing why the run stopped", width+20))
	} else if m.Stall != nil && m.Stall.RunID == m.Run.ID {
		if m.Stall.Err != nil {
			lines = append(lines, styled(short("Orchestrator diagnosis failed: "+Redact(m.Stall.Err.Error()), width), "38;5;203", color))
		} else if m.Stall.Applied {
			lines = append(lines, styled(short("◆ orchestrator answered "+m.Stall.DecisionID+" with "+m.Stall.Answer+": "+m.Stall.Summary, width), "38;5;141", color))
			if m.Stall.Recommendation != "" {
				lines = append(lines, short("  "+m.Stall.Recommendation, width))
			}
		} else {
			lines = append(lines, styled(short("◆ orchestrator: "+m.Stall.Summary, width), "38;5;141", color))
			if m.Stall.Recommendation != "" {
				lines = append(lines, short("  "+m.Stall.Recommendation, width))
			}
			if m.Stall.DecisionID != "" && m.pendingDecision(m.Stall.DecisionID) != nil {
				lines = append(lines, styled(short("  /accept answers "+m.Stall.DecisionID+" with "+m.Stall.Answer, width), sgrMuted, color))
			}
		}
	}
	return lines
}

func (m LiveModel) pendingDecision(id string) *Decision {
	for i := range m.Decisions {
		if m.Decisions[i].ID == id {
			return &m.Decisions[i]
		}
	}
	return nil
}

// stallKey identifies the current stall, or reports that the run is moving.
func (m LiveModel) stallKey(now time.Time) (string, bool) {
	if m.Run.ID == "" || m.State.Status != "running" {
		return "", false
	}
	for _, status := range m.State.Agents {
		if status == "running" || status == "queued" {
			return "", false
		}
	}
	if len(m.Decisions) > 0 {
		ids := []string{}
		oldest := now
		for _, d := range m.Decisions {
			ids = append(ids, d.ID)
			if created, err := time.Parse(time.RFC3339Nano, d.CreatedAt); err == nil && created.Before(oldest) {
				oldest = created
			}
		}
		return "decisions:" + strings.Join(ids, ","), now.Sub(oldest) >= stallDecisionAge
	}
	if len(m.Events) == 0 {
		return "", false
	}
	last := m.Events[len(m.Events)-1]
	at, err := time.Parse(time.RFC3339Nano, last.At)
	return "idle:" + last.ID, err == nil && now.Sub(at) >= stallIdle
}

func (m *LiveModel) watchStall() tea.Cmd {
	key, stalled := m.stallKey(time.Now())
	if !stalled || m.StallBusy || key == m.StallKey || m.StallCount >= stallMaxPerRun {
		return nil
	}
	m.StallKey, m.StallBusy = key, true
	m.StallCount++
	return m.diagnoseStall(key)
}

// diagnoseStall wakes the permanent orchestrator with the run's evidence. It
// only recommends; answering a decision stays with the user.
func (m LiveModel) diagnoseStall(key string) tea.Cmd {
	p, r, settings := m.Project, m.Run, m.Run.Settings
	events, decisions, state := append([]Event(nil), m.Events...), append([]Decision(nil), m.Decisions...), m.State
	invoker := m.Invoker
	if invoker == nil {
		invoker = Providers{}
	}
	ctx := m.Context
	if ctx == nil {
		ctx = context.Background()
	}
	return func() tea.Msg {
		result := stallDiagnosis{RunID: r.ID, Key: key}
		var evidence strings.Builder
		fmt.Fprintf(&evidence, "Workflow %s graph:\n%s\nAgent states: %s\n", r.Workflow.Id, GraphSummary(r.Workflow), SafeJSON(state.Agents))
		for _, d := range decisions {
			fmt.Fprintf(&evidence, "Pending decision %s from %s (%s): %s choices=%v context=%s\n", d.ID, d.AgentID, d.Kind, d.Prompt, d.Choices, SafeJSON(d.Context))
			if id := str(d.Context["messageId"]); id != "" && validID(id) == nil {
				var packet Packet
				if ReadJSON(filepath.Join(p.RunDir(r.ID), "messages", id+".json"), &packet) == nil {
					fmt.Fprintf(&evidence, "Packet under decision: %s\n", short(string(SafeJSON(map[string]any{"kind": packet["kind"], "from": packet["from"], "payload": packet["payload"]})), 4000))
				}
			}
		}
		evidence.WriteString("Recent events:\n")
		for _, event := range events[max(0, len(events)-40):] {
			if event.Type == "usage.reported" || event.Type == "process.output" {
				continue
			}
			fmt.Fprintf(&evidence, "%d %s %s %s\n", event.Sequence, event.AgentID, event.Type, short(string(JSON(event.Data)), 300))
		}
		// The schema only admits real decision IDs and choices, so a routing
		// answer is always structured rather than buried in prose.
		ids, choices := []any{""}, []any{""}
		routing := false
		for _, d := range decisions {
			ids = append(ids, d.ID)
			for _, choice := range d.Choices {
				if !Has(stringSlice(choices), choice) {
					choices = append(choices, choice)
				}
			}
			routing = routing || orchestratorMayAnswer(settings, d)
		}
		authority := "You cannot act yourself: Arena shows your recommendation and the user decides."
		if routing {
			authority = "For a router.fallback decision Arena applies your decisionId and answer directly, so choose the branch that best moves the task toward its acceptance criteria (for example, send unmet acceptance criteria or requested changes to the agent that fixes them). For any other decision Arena only shows your recommendation."
		}
		in := Invocation{Runtime: settings.DefaultModel, Dir: p.Root, AgentID: "arena-orchestrator", Prompt: "You are the permanent arena-orchestrator, the project control agent. This run has stopped making progress. Using only the evidence below, explain in one or two sentences why it is stuck, and give the next step in one sentence. Whenever a pending decision has a sensible answer, set decisionId and answer to that decision and exactly one of its listed choices; leave both empty only when no listed choice fits. " + authority + " Never include private reasoning.\n" + Redact(evidence.String()), Schema: objectSchema(map[string]any{"summary": map[string]any{"type": "string"}, "recommendation": map[string]any{"type": "string"}, "decisionId": map[string]any{"type": "string", "enum": ids}, "answer": map[string]any{"type": "string", "enum": choices}})}
		bounded, cancel := inferenceDeadline(ctx, settings.Limits.MaxAgentRunMinutes)
		defer cancel()
		out, err := invoker.Invoke(bounded, in)
		if err != nil {
			result.Err = err
		} else {
			result.Summary, result.Recommendation = strings.TrimSpace(str(out["summary"])), strings.TrimSpace(str(out["recommendation"]))
			result.DecisionID, result.Answer = strings.TrimSpace(str(out["decisionId"])), strings.TrimSpace(str(out["answer"]))
			if result.DecisionID == "" && len(decisions) == 1 && result.Answer != "" {
				result.DecisionID = decisions[0].ID
			}
			var chosen *Decision
			for i, d := range decisions {
				if d.ID == result.DecisionID && result.Answer != "" && (len(d.Choices) == 0 || Has(d.Choices, result.Answer)) {
					chosen = &decisions[i]
				}
			}
			if chosen == nil {
				result.DecisionID, result.Answer = "", ""
			} else if orchestratorMayAnswer(settings, *chosen) {
				if err := p.Answer(r.ID, chosen.ID, result.Answer); err == nil {
					result.Applied = true
				} else if !strings.Contains(err.Error(), "already answered") {
					result.Err = fmt.Errorf("answer %s: %w", chosen.ID, err)
				}
			}
		}
		data := map[string]any{"key": key, "summary": result.Summary, "recommendation": result.Recommendation, "decisionId": result.DecisionID, "answer": result.Answer, "applied": result.Applied, "policy": stallRecovery(settings)}
		if result.Err != nil {
			data["error"] = Redact(result.Err.Error())
		}
		if _, err := p.Append(r.ID, r.Workflow.Id, "arena-orchestrator", "orchestrator.stall-diagnosed", data); err != nil && result.Err == nil {
			result.Err = err
		}
		return result
	}
}

// decisionShortcut turns plain text into an answer when exactly one decision
// is pending and the text is one of its choices.
func (m LiveModel) decisionShortcut(text string) (string, bool) {
	if len(m.Decisions) != 1 || strings.HasPrefix(text, "/") || strings.HasPrefix(text, "$") {
		return "", false
	}
	d := m.Decisions[0]
	if Has(d.Choices, text) {
		return "decision " + d.ID + " " + text, true
	}
	return "", false
}

type shellResult struct {
	Command, Output string
	Code            int
	Err             error
}

// runShell executes a user-typed "$ command" in the project root. It is the
// user's own command, so it runs with their environment, bounded in time and
// output, and its outcome (not its output) is journaled.
func (m LiveModel) runShell(command string) tea.Cmd {
	p := m.Project
	ctx := m.Context
	if ctx == nil {
		ctx = context.Background()
	}
	return func() tea.Msg {
		result := shellResult{Command: command}
		if p == nil {
			result.Err = fmt.Errorf("no project is open")
			return result
		}
		bounded, cancel := context.WithTimeout(ctx, 2*time.Minute)
		defer cancel()
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "/bin/sh"
		}
		cmd := exec.CommandContext(bounded, shell, "-c", command)
		cmd.Dir = p.Root
		cmd.Stdin = nil
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &limitedWriter{&out, 256 << 10}, &limitedWriter{&out, 256 << 10}
		err := cmd.Run()
		result.Output = out.String()
		if exit, ok := err.(*exec.ExitError); ok {
			result.Code = exit.ExitCode()
		} else if err != nil {
			result.Err = err
		}
		if bounded.Err() == context.DeadlineExceeded {
			result.Err = fmt.Errorf("command timed out after 2 minutes")
		}
		_, _ = p.Append("project", "", "user", "shell.command", map[string]any{"command": Redact(command), "exitCode": result.Code, "outputBytes": out.Len()})
		return result
	}
}

type limitedWriter struct {
	buffer *bytes.Buffer
	limit  int
}

func (w *limitedWriter) Write(b []byte) (int, error) {
	if room := w.limit - w.buffer.Len(); room > 0 {
		w.buffer.Write(b[:min(len(b), room)])
	}
	return len(b), nil
}

// commitScaffold records the saved workflow so writing workers have a base
// commit. It refuses secret-like content rather than committing it.
func commitScaffold(p *Project) (string, error) {
	if _, err := Git(p.Root, "add", "-A"); err != nil {
		return "", err
	}
	staged, err := Git(p.Root, "diff", "--cached", "--binary")
	if err != nil {
		return "", err
	}
	if staged == "" {
		return "", nil
	}
	if Redact(staged) != staged {
		return "", fmt.Errorf("staged files contain secret-like values; review them, then commit yourself")
	}
	if _, err := Git(p.Root, "commit", "--quiet", "-m", "Arena scaffold"); err != nil {
		return "", err
	}
	return Git(p.Root, "rev-parse", "--short", "HEAD")
}
