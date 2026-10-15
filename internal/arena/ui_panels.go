package arena

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

type agentContextLoaded struct {
	Agent  string
	Lines  []string
	Events []Event
	Err    error
}

func (m LiveModel) loadAgentContext(id string) tea.Cmd {
	return func() tea.Msg {
		lines := []string{}
		runtime := m.Run.Settings.DefaultModel
		if agent, ok := agentByID(m.Run.Workflow, id); ok {
			runtime = agent.Runtime
		}
		lines = append(lines, "SELECTED RUNTIME:", string(SafeJSON(runtime)))
		profile, ok := m.Run.Profiles[id]
		if id == "arena-orchestrator" {
			err := ReadJSON(m.Project.Path(m.Run.Settings.PermanentOnCallAgent.Profile), &profile)
			if err != nil {
				return agentContextLoaded{Agent: id, Err: err}
			}
			ok = true
		}
		if ok {
			lines = append(lines, "ROLE: "+profile.Role, "OBJECTIVE: "+profile.Objective, "INSTRUCTIONS:", profile.Instructions, "CAPABILITIES: "+strings.Join(profile.Capabilities, ", "), "CONSTRAINTS: "+strings.Join(profile.Constraints, "; "))
		}
		if id == "arena-orchestrator" {
			lines = append(lines, "CONTROL CONTEXT: selected default runtime and arena-orchestrator skill; only this setup conversation is sent. No worker context is assumed to be shared.")
			lines = append(lines, "SETUP INSTRUCTIONS:", interviewInstructions(), "SETUP CONVERSATION:", m.Transcript)
			events, err := m.Project.Events("project")
			return agentContextLoaded{Agent: id, Lines: lines, Events: events, Err: err}
		}
		lines = append(lines, "WORKFLOW: "+m.Run.Workflow.Id, "RUN OBJECTIVE: "+m.Run.Objective)
		taskFound := false
		for i := len(m.Events) - 1; i >= 0; i-- {
			e := m.Events[i]
			to, _ := e.Data["to"].(map[string]any)
			if e.Type != "message.sent" || e.Data["kind"] != "task" || to["agentId"] != id {
				continue
			}
			taskFound = true
			packet, err := m.Project.ReadPacket(m.Run.ID, str(e.Data["messageId"]))
			if err != nil {
				lines = append(lines, "ACTUAL TASK CONTEXT: packet not retained or unavailable: "+err.Error())
			} else {
				payload, _ := packet["payload"].(map[string]any)
				lines = append(lines, "ACTUAL TASK ENVELOPE:", string(SafeJSON(payload)))
			}
			break
		}
		if !taskFound {
			lines = append(lines, "ACTUAL TASK CONTEXT: no task has been sent to this agent")
		}
		entries, err := m.Project.ContextEntries(m.Run.Workflow.Id, false)
		if err != nil {
			return agentContextLoaded{Agent: id, Lines: lines, Err: err}
		}
		lines = append(lines, "AVAILABLE WORKFLOW CONTEXT (not necessarily sent to this agent):")
		for _, entry := range entries {
			lines = append(lines, fmt.Sprintf("%s | %s | %s | %s", entry.Id, entry.Title, entry.Confidence, entry.Status), "Provenance: "+strings.TrimSpace(string(SafeJSON(entry.CreatedBy))), entry.Content)
		}
		if len(entries) == 0 {
			lines = append(lines, "No shared entries in this workflow")
		}
		return agentContextLoaded{Agent: id, Lines: lines}
	}
}

func visibleActivity(e Event) string {
	data := e.Data
	switch e.Type {
	case "provider.activity":
		return str(data["type"]) + " " + str(data["status"]) + " " + str(data["command"]) + " " + str(data["tool"])
	case "agent.progress":
		return str(data["summary"])
	case "agent.plan":
		return "Plan: " + strings.TrimSpace(string(SafeJSON(data["plan"])))
	case "agent.started", "agent.queued", "agent.cancelled", "setup.started":
		return e.Type
	case "agent.failed", "setup.failed":
		return e.Type + ": " + str(data["error"])
	case "agent.succeeded":
		result, _ := data["result"].(map[string]any)
		return "Result: " + str(result["summary"])
	case "setup.replied":
		return "Orchestrator: " + str(data["text"])
	case "command.completed":
		return "Command completed: " + strings.TrimSpace(string(SafeJSON(data)))
	case "process.output":
		if output := str(data["output"]); output != "" {
			return output
		}
		return fmt.Sprintf("Output recorded: %v bytes (body omitted by retention policy)", data["bytes"])
	case "provider.arena-tool-completed":
		return "Arena tool: " + str(data["tool"]) + " " + str(data["status"])
	}
	return ""
}

func (m LiveModel) agentActivity(id string) []string {
	state := m.State.Agents[id]
	if state == "" {
		state = "idle"
	}
	runtime := m.Run.Settings.DefaultModel
	if a, ok := agentByID(m.Run.Workflow, id); ok {
		runtime = a.Runtime
	}
	if id == "arena-orchestrator" && m.ChatBusy {
		state = "responding"
	}
	lines := []string{id + " | " + state, runtime.Provider + " / " + runtime.Model + " / " + runtime.ReasoningEffort, "Public progress, tools and results · Tab: context", ""}
	events := m.Events
	if id == "arena-orchestrator" {
		events = m.ControlEvents
	}
	count := 0
	for _, e := range events {
		if e.AgentID != id {
			continue
		}
		if text := visibleActivity(e); text != "" {
			lines = append(lines, e.At+"  "+text)
			count++
		}
	}
	if count == 0 {
		lines = append(lines, "No recorded activity yet")
	}
	if result := m.State.Completed[id]; result != nil {
		lines = append(lines, "STRUCTURED RESULT:", string(SafeJSON(result)))
	}
	return lines
}

func plainUILines(lines []string, width int) []string {
	out := []string{}
	for _, line := range lines {
		// Provider text cannot inject terminal control sequences into the UI.
		line = ansi.Strip(Redact(line))
		out = append(out, strings.Split(ansi.Hardwrap(line, max(1, width), true), "\n")...)
	}
	return out
}

func (m LiveModel) overlay(lines []string, width, height int) []string {
	if m.PopupAgent == "" {
		return lines
	}
	content := m.agentActivity(m.PopupAgent)
	title := "Activity · " + m.PopupAgent
	if m.PopupContext {
		content = m.PopupLines
		title = "Context · " + m.PopupAgent
		if len(content) == 0 {
			content = []string{"Loading context…"}
		}
	}
	w := min(88, width-2)
	prompt, _, _ := m.promptView(width, height, false)
	footer := 2 + len(prompt)
	h := min(22, height-footer-1)
	if w < 10 || h < 4 {
		content = plainUILines(append([]string{title}, content...), width)
		start := min(m.PopupOffset, max(0, len(content)-max(1, height-footer)))
		for i := range max(0, height-footer) {
			if start+i < len(content) && i < len(lines) {
				lines[i] = short(content[start+i], width)
			}
		}
		return lines
	}
	content = plainUILines(content, w-4)
	visible := h - 3
	start := min(m.PopupOffset, max(0, len(content)-visible))
	b := borders()
	box := []string{b.top(w, title)}
	for i := range visible {
		text := ""
		if start+i < len(content) {
			text = content[start+i]
		}
		box = append(box, b.row(text, w))
	}
	box = append(box, b.row("PgUp/PgDn scroll · Tab switch · Esc close", w), b.bottom(w))
	left, top := (width-w)/2, max(0, (height-footer-len(box))/2)
	for i, line := range box {
		y := top + i
		if y >= height-footer || y >= len(lines) {
			break
		}
		lines[y] = pad(ansi.Cut(lines[y], 0, left), left) + line + pad(ansi.Cut(lines[y], left+w, width), width-left-w)
	}
	return lines
}

func (m LiveModel) chatBody() []string {
	lines := []string{"Project orchestrator · describe your project · /help commands"}
	for _, line := range m.Chat {
		role := line.Role
		if role == "you" {
			role = "You"
		}
		lines = append(lines, role, line.Text, "")
	}
	if m.Draft != nil {
		lines = append(lines, "WORKFLOW PREVIEW:", GraphSummary(m.Draft.Workflow), "Review runtimes and policy above. /save applies this draft; typing revises it.")
	}
	if m.ChatBusy {
		lines = append(lines, "Working… · /view arena-orchestrator for activity")
	}
	return lines
}

func (m LiveModel) scrollEnd(popup bool) int {
	width, height := max(8, m.Width), max(4, m.Height)
	prompt, _, _ := m.promptView(width, height, false)
	footer := 2 + len(prompt)
	if popup {
		body := m.agentActivity(m.PopupAgent)
		if m.PopupContext {
			body = m.PopupLines
		}
		w, h := min(88, width-2), min(22, height-footer-1)
		if w < 10 || h < 4 {
			title := "Activity · " + m.PopupAgent
			if m.PopupContext {
				title = "Context · " + m.PopupAgent
			}
			return max(0, len(plainUILines(append([]string{title}, body...), width))-max(1, height-footer))
		}
		return max(0, len(plainUILines(body, w-4))-max(1, h-3))
	}
	header := min(len(m.header(width, height, false)), max(0, height-footer-1))
	budget := max(1, height-header-footer)
	return max(0, len(m.bodyLines(width, false))-budget)
}
