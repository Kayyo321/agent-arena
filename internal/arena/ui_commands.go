package arena

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

var slashHelp = []string{
	"/help                       Commands and keyboard controls",
	"/agents                     Agents, roles and selected runtimes",
	"/view NAME                  Live agent activity mini-window",
	"/context [NAME]             Profile, task and workflow context",
	"/chat                       Talk to the project orchestrator",
	"/graph                      Return to the workflow graph",
	"/run TASK                   Start an attached workflow run",
	"/pause /resume /cancel      Control the current run",
	"/events /git /changes       Audit, worktrees and review queue",
	"/status /refresh            Required-provider readiness",
	"/teams                      Toggle team member expansion",
	"/decisions                  Pending user questions and approvals",
	"/answer ID TEXT             Answer a pending decision",
	"/integrate CHANGE_ID        Integrate a reviewed change",
	"/save                       Save the previewed setup draft",
	"/clear                      Clear the current view's scroll/error",
	"/close                      Close the mini-window or panel",
	"/quit                       Exit; preserve attached run history",
	"↑/↓ recall messages; ←/→ move the cursor. Home/End: start/end.",
	"Alt+←/→ move by word; Ctrl+W deletes a word; Ctrl+U/K clear before/after.",
	"Enter sends; Alt+Enter or Ctrl+J inserts a newline. Paste supports multiple lines.",
	"PgUp/PgDn scroll; Ctrl+Home/End: first/latest; Alt+↑/↓ select events/changes.",
	"Tab completes /commands. In a mini-window: Tab context/activity,",
	"PageUp/PageDown scroll, Esc closes. No private reasoning is shown.",
}

var slashCommands = []string{"/help", "/agents", "/view", "/context", "/chat", "/graph", "/run", "/pause", "/resume", "/cancel", "/events", "/git", "/changes", "/status", "/refresh", "/teams", "/decisions", "/answer", "/integrate", "/save", "/clear", "/close", "/quit"}

func (m LiveModel) resolveAgent(name string) (string, error) {
	if name == "arena-orchestrator" || name == "orchestrator" {
		return "arena-orchestrator", nil
	}
	for _, a := range m.Run.Workflow.Agents {
		if a.Id == name {
			return a.Id, nil
		}
	}
	matches := []string{}
	for id, p := range m.Run.Profiles {
		if strings.EqualFold(p.Name, name) {
			matches = append(matches, id)
		}
	}
	if len(matches) == 1 {
		return matches[0], nil
	}
	return "", fmt.Errorf("agent %q is unknown or ambiguous; use /agents and its exact ID", name)
}

func (m LiveModel) submit(input string) (tea.Model, tea.Cmd) {
	m.Prompt, m.Error = "", ""
	text := strings.TrimSpace(input)
	if text == "" {
		return m, nil
	}
	parts := strings.Fields(text)
	if strings.HasPrefix(text, "/") {
		name := strings.TrimPrefix(parts[0], "/")
		arg := strings.TrimSpace(strings.TrimPrefix(text, parts[0]))
		switch name {
		case "quit", "exit":
			return m, tea.Quit
		case "help":
			m.Panel, m.PanelText = "Help", slashHelp
		case "agents":
			m.Panel, m.PanelText = "Agents", []string{"arena-orchestrator | permanent project control | " + m.Run.Settings.DefaultModel.Provider + " / " + m.Run.Settings.DefaultModel.Model}
			for _, a := range m.Run.Workflow.Agents {
				p := m.Run.Profiles[a.Id]
				m.PanelText = append(m.PanelText, fmt.Sprintf("%s (%s) | %s | %s / %s / %s", a.Id, p.Name, p.Role, a.Runtime.Provider, a.Runtime.Model, a.Runtime.ReasoningEffort))
			}
		case "view", "context":
			if arg == "" && name == "view" {
				m.Error = "usage: /view AGENT_NAME; /agents lists names"
				break
			}
			if arg == "" {
				arg = m.PopupAgent
				if arg == "" {
					arg = m.Run.Workflow.HeadAgent
				}
				if arg == "" {
					arg = "arena-orchestrator"
				}
			}
			id, err := m.resolveAgent(arg)
			if err != nil {
				m.Error = err.Error()
				break
			}
			m.PopupAgent, m.PopupContext, m.PopupOffset, m.PopupLines = id, name == "context", 0, nil
			return m, m.loadAgentContext(id)
		case "close":
			m.PopupAgent, m.Panel = "", ""
		case "graph":
			if m.Run.Workflow.HeadAgent == "" {
				m.Error = "Create and /save a workflow first"
				break
			}
			m.ChatMode, m.Panel, m.PopupAgent = false, "", ""
			m.Detail, m.GitDetail, m.ChangeDetail = false, false, false
		case "chat":
			if m.Interview == nil {
				m.Interview = newInterviewSession(m.Context, m.Project, m.Run.Settings)
				// The hosting session owns this interview and joins it on exit.
				if m.Session != nil {
					m.Session.interviews = append(m.Session.interviews, m.Interview)
				}
			}
			m.ChatMode, m.Panel, m.PopupAgent = true, "", ""
			if len(m.Chat) == 0 {
				m.Chat = append(m.Chat, chatLine{"arena-orchestrator", "Describe the new workflow you want to create. I will ask for all required choices."})
			}
		case "save":
			if m.ChatBusy || m.Draft == nil {
				m.Error = "No validated draft to save; finish the setup interview first"
				break
			}
			if m.Run.Settings.ApprovalPolicies.AgentWorkflowEdit == "auto-refuse" {
				m.Error = "project workflow-edit policy refuses agent proposals"
				break
			}
			draft := *m.Draft
			m.ChatBusy = true
			operation := func() tea.Msg {
				_, err := m.Project.Save(draft.Workflow, draft.Profiles, "")
				if err != nil {
					return draftSaved{Err: err}
				}
				err = m.Project.Audit(draft.Workflow.Id, "workflow.saved", map[string]any{"source": "tui-setup", "approvedBy": "user"})
				if err != nil {
					return draftSaved{Err: fmt.Errorf("workflow saved, audit failed: %w", err)}
				}
				r, err := loadWorkflowView(m.Project, draft.Workflow.Id)
				return draftSaved{r, err}
			}
			if m.Session != nil {
				return m, m.Session.action(operation)
			}
			return m, operation
		case "events":
			m.Detail, m.GitDetail, m.ChangeDetail, m.Panel, m.ChatMode = true, false, false, "", false
		case "git":
			m.Detail, m.GitDetail, m.ChangeDetail, m.Panel, m.ChatMode = false, true, false, "", false
		case "changes":
			m.Detail, m.GitDetail, m.ChangeDetail, m.Panel, m.ChatMode = false, false, true, "", false
			m.loadChangePatch()
		case "teams":
			m.Expanded = !m.Expanded
			m.ChatMode, m.Panel = false, ""
			m.Detail, m.GitDetail, m.ChangeDetail = false, false, false
		case "status":
			m.Panel, m.PanelText = "Provider status", readinessLines(m.Readiness)
		case "refresh":
			if m.PopupAgent != "" {
				return m, m.loadAgentContext(m.PopupAgent)
			}
			return m, m.probeReadiness()
		case "decisions":
			m.Panel, m.PanelText = "Pending decisions", []string{"Use /answer ID TEXT"}
			for _, d := range m.Decisions {
				m.PanelText = append(m.PanelText, d.ID+" | "+d.AgentID+" | "+d.Prompt+" | choices="+strings.Join(d.Choices, ", "))
			}
		case "clear":
			m.Offset, m.PopupOffset = 0, 0
			m.Error = ""
		case "run", "pause", "resume", "cancel", "answer", "integrate":
			if name == "answer" {
				name = "decision"
			}
			if name == "run" && m.Run.Workflow.HeadAgent == "" {
				m.Error = "Create and /save a workflow first"
				break
			}
			chat := m.ChatMode
			m.ChatMode = false
			next, cmd := m.submit(name + " " + arg)
			value := next.(LiveModel)
			if name != "run" {
				value.ChatMode = chat
			}
			return value, cmd
		default:
			m.Error = "Unknown /command; use /help"
		}
		m.Offset = 0
		return m, nil
	}
	if m.ChatMode {
		if m.PendingReply != nil {
			m.PendingReply <- Redact(text)
			m.PendingReply = nil
			m.Chat = append(m.Chat, chatLine{"you", Redact(text)})
			m.Transcript += "\nUser answer: " + Redact(text)
			return m, nil
		}
		if m.ChatBusy {
			m.Prompt = input
			m.Error = "The orchestrator is responding; your input is preserved"
			return m, nil
		}
		if m.ChatTurns >= 50 {
			m.Error = "Setup's 50-turn limit reached; restart arena setup to continue"
			return m, nil
		}
		if len(m.Transcript)+len(text) > 2<<20 {
			m.Error = "Setup context exceeds 2 MiB"
			return m, nil
		}
		if m.Interview == nil {
			m.Error = "This observer has no setup session"
			return m, nil
		}
		m.Transcript += "\nUser: " + Redact(text)
		m.Chat = append(m.Chat, chatLine{"you", Redact(text)})
		m.ChatTurns++
		m.ChatBusy, m.Draft = true, nil
		m.Offset = 1 << 30 // Follow the newest messages.
		return m, m.Interview.turn(m.Transcript)
	}
	if !Has([]string{"run", "pause", "resume", "cancel", "decision", "integrate"}, parts[0]) {
		text = "run " + text
		parts = strings.Fields(text)
	}
	if parts[0] == "run" {
		if len(parts) < 2 {
			m.Error = "usage: /run TASK"
		} else if m.Session == nil {
			m.Error = "start a task from arena or use arena run WORKFLOW TASK"
		} else if m.Starting || m.Executing || m.Run.ID != "" && Has([]string{"created", "running", "paused"}, m.State.Status) {
			m.Error = "a task is already starting or active; finish or cancel it before starting another"
		} else {
			m.Starting = true
			return m, m.Session.start(m.Run.Workflow.Id, strings.TrimSpace(strings.TrimPrefix(text, "run")))
		}
		return m, nil
	}
	operation := func() tea.Msg { return uiCommandResult{m.command(text)} }
	if m.Session != nil {
		return m, m.Session.action(operation)
	}
	return m, operation
}

func readinessLines(results []Readiness) []string {
	lines := []string{}
	for _, r := range results {
		lines = append(lines, r.Provider+" / "+r.Model+": "+r.State+" · "+r.Auth+" · "+r.Detail)
	}
	return lines
}

func (m *LiveModel) completePrompt() {
	parts := strings.Fields(m.Prompt)
	if len(parts) == 0 {
		return
	}
	options := []string{}
	if strings.HasPrefix(m.Prompt, "/view ") || strings.HasPrefix(m.Prompt, "/context ") {
		options = append(options, parts[0]+" arena-orchestrator")
		for _, a := range m.Run.Workflow.Agents {
			options = append(options, parts[0]+" "+a.Id)
		}
	} else {
		options = append(options, slashCommands...)
	}
	matches := []string{}
	for _, option := range options {
		if strings.HasPrefix(option, m.Prompt) {
			matches = append(matches, option)
		}
	}
	if len(matches) == 1 {
		m.Prompt = matches[0] + " "
	} else if len(matches) > 1 {
		m.Error = "Complete: " + strings.Join(matches, ", ")
	}
}
