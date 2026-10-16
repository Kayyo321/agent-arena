package arena

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

var slashHelp = []string{
	"/paste                      Paste clipboard image or text (Ctrl+V)",
	"/attach PATH [PATH ...]     Attach images or files (quote paths with spaces)",
	"/attachments /detach N|all  List or remove pending attachments",
	"Files: drop paths into an empty composer; up to 8 files, 10 MiB each, 32 MiB total.",
	"Attachments retain exact file snapshots in full-local history. Images require Codex.",
	"/shader DESCRIPTION         Create a background shader with the default model",
	"/shader-save /shader-off    Apply the preview or disable the background",
	"/help                       Commands and keyboard controls",
	"/agents                     Agents, roles and selected runtimes",
	"/view NAME                  Live agent activity mini-window",
	"/context [NAME]             Profile, task and workflow context",
	"/chat                       Talk to the project orchestrator",
	"/graph                      Return to the workflow graph",
	"/pan                        Pan the graph: ←↑↓→ move, Space recenters, Esc done",
	"/run TASK                   Start an attached workflow run",
	"/pause /resume /cancel      Control the current run",
	"/events /git /changes       Audit, worktrees and review queue",
	"/status /refresh            Required-provider readiness",
	"/teams                      Toggle team member expansion",
	"/decisions                  Pending user questions and approvals",
	"/answer [ID] TEXT           Answer a pending decision (ID optional when only one)",
	"/accept                     Apply the orchestrator's recommended answer",
	"$ COMMAND                   Run a shell command in the project root",
	"/integrate CHANGE_ID        Integrate a reviewed change",
	"/save [no-commit]           Save the previewed draft and commit the scaffold",
	"/clear                      Clear the current view's scroll/error",
	"/close                      Close the mini-window or panel",
	"/quit                       Exit; preserve attached run history",
	"↑/↓ edit multiline text (recall history on one line); Ctrl+P/N always recall.",
	"←/→ move the cursor; Home/End move to the start/end of the current line.",
	"Alt+←/→ move by word; Ctrl+W deletes a word; Ctrl+U/K clear before/after.",
	"Enter sends; Shift+Enter inserts a newline. Alt+Enter/Ctrl+J work on legacy terminals.",
	"PgUp/PgDn scroll; Ctrl+Home/End: first/latest; Alt+↑/↓ select events/changes.",
	"Typing / suggests commands: ↑/↓ pick, Tab/Enter fill in. In a mini-window: Tab context/activity,",
	"PageUp/PageDown scroll, Esc closes. No private reasoning is shown.",
}

var slashCommands = []string{"/paste", "/attach", "/attachments", "/detach", "/shader", "/shader-save", "/shader-off", "/help", "/agents", "/view", "/context", "/chat", "/graph", "/pan", "/run", "/pause", "/resume", "/cancel", "/events", "/git", "/changes", "/status", "/refresh", "/teams", "/decisions", "/answer", "/accept", "/integrate", "/save", "/clear", "/close", "/quit"}

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
	next, cmd := m.submitInput(input)
	value := next.(LiveModel)
	if value.Error != "" && value.Prompt == "" {
		value.setPrompt(input, len(input))
	}
	return value, cmd
}

func (m LiveModel) submitInput(input string) (tea.Model, tea.Cmd) {
	m.Prompt, m.Error = "", ""
	text := strings.TrimSpace(input)
	if m.ClipboardBusy && (!strings.HasPrefix(text, "/") || strings.HasPrefix(text, "/run ")) {
		m.Error = "Clipboard paste is still in progress; your draft is preserved"
		return m, nil
	}
	if text == "" {
		if len(m.Attachments) == 0 {
			return m, nil
		}
		text, input = "Inspect the attached files.", "Inspect the attached files."
	}
	if strings.HasPrefix(text, "$") {
		command := strings.TrimSpace(strings.TrimPrefix(text, "$"))
		if command == "" {
			m.Error = "type a shell command after $"
			return m, nil
		}
		return m, m.runShell(command)
	}
	parts := strings.Fields(text)
	if strings.HasPrefix(text, "/") {
		name := strings.TrimPrefix(parts[0], "/")
		arg := strings.TrimSpace(strings.TrimPrefix(text, parts[0]))
		switch name {
		case "paste":
			cmd := m.pasteClipboard()
			m.ClipboardBusy = true
			return m, cmd
		case "attach", "attachments", "detach":
			m.attachmentCommand(name, arg)
		case "shader", "shader-save", "shader-off":
			if name == "shader" && len(m.Attachments) > 0 {
				m.Error = "Shader commands accept text only; detach the files or send them as a workflow/chat message"
				return m, nil
			}
			return m.shaderCommand(name, arg)
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
			m.Panning, m.PanX, m.PanY = false, 0, 0
		case "pan":
			if m.Run.Workflow.HeadAgent == "" {
				m.Error = "Create and /save a workflow first"
				break
			}
			m.ChatMode, m.Panel, m.PopupAgent = false, "", ""
			m.Detail, m.GitDetail, m.ChangeDetail = false, false, false
			m.Panning = true
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
		case "accept":
			if m.Stall == nil || m.Stall.DecisionID == "" || m.pendingDecision(m.Stall.DecisionID) == nil {
				m.Error = "no orchestrator recommendation is waiting; use /answer ID TEXT"
				break
			}
			answer := "decision " + m.Stall.DecisionID + " " + m.Stall.Answer
			operation := func() tea.Msg { return uiCommandResult{m.command(answer)} }
			if m.Session != nil {
				return m, m.Session.action(operation)
			}
			return m, operation
		case "save":
			commit := arg != "no-commit"
			if arg != "" && arg != "no-commit" {
				m.Error = "usage: /save [no-commit]"
				break
			}
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
				saved := draftSaved{Run: r, Err: err}
				if err == nil && commit {
					saved.Commit, saved.CommitErr = commitScaffold(m.Project)
				}
				return saved
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
			return m, tea.Batch(m.probeReadiness(), m.loadShader())
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
			next, cmd := m.submitInput(name + " " + arg)
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
			if len(m.Attachments) > 0 {
				m.Error = "Finish this text reply first; attachments can be sent in the next chat message"
				return m, nil
			}
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
		combined := append(append([]Attachment(nil), m.ChatAttachments...), m.Attachments...)
		if err := attachmentPolicy(combined, m.Run.Settings.Audit.Retention, []Runtime{m.Run.Settings.DefaultModel}); err != nil {
			m.Error = err.Error()
			return m, nil
		}
		shownText := Redact(text)
		if len(m.Attachments) > 0 {
			shownText += "\nAttachments: " + m.attachmentSummary()
		}
		m.ChatAttachments, m.Attachments = combined, nil
		m.Transcript += "\nUser: " + Redact(text)
		m.Chat = append(m.Chat, chatLine{"you", shownText})
		m.ChatTurns++
		m.ChatBusy, m.Draft = true, nil
		m.Offset = 1 << 30 // Follow the newest messages.
		return m, m.Interview.turn(m.Transcript, m.ChatAttachments...)
	}
	if answer, ok := m.decisionShortcut(text); ok {
		text = answer
		parts = strings.Fields(text)
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
			retention := m.Run.Settings.Audit.Retention
			if m.Run.Workflow.Settings.Audit.Retention == "redacted-local" {
				retention = "redacted-local"
			}
			runtimes := []Runtime{}
			for _, a := range m.Run.Workflow.Agents {
				runtimes = append(runtimes, a.Runtime)
			}
			if err := attachmentPolicy(m.Attachments, retention, runtimes); err != nil {
				m.Error = err.Error()
				return m, nil
			}
			m.Starting = true
			attachments := m.Attachments
			m.SendingPrompt, m.SendingAttachments = input, attachments
			m.Attachments = nil
			return m, m.Session.start(m.Run.Workflow.Id, strings.TrimSpace(strings.TrimPrefix(text, "run")), attachments...)
		}
		return m, nil
	}
	operation := func() tea.Msg { return uiCommandResult{m.command(text)} }
	if m.Session != nil {
		return m, m.Session.action(operation)
	}
	return m, operation
}

// foldReadiness reports each provider/model once, keeping the first
// not-ready state, and omits runtimes that are ready.
func foldReadiness(results []Readiness) []string {
	lines, seen := []string{}, map[string]bool{}
	for _, ready := range results {
		key := ready.Provider + "/" + ready.Model
		if ready.State == "good" || seen[key] {
			continue
		}
		seen[key] = true
		lines = append(lines, "Provider "+ready.Provider+" / "+ready.Model+": "+ready.State+" · "+ready.Auth)
	}
	return lines
}

func readinessLines(results []Readiness) []string {
	lines := []string{}
	for _, r := range results {
		lines = append(lines, r.Provider+" / "+r.Model+": "+r.State+" · "+r.Auth+" · "+r.Detail)
	}
	return lines
}

type slashSuggestion struct{ text, usage, about string }

// slashUsage maps each command to the usage and summary shown in /help.
var slashUsage = func() map[string][2]string {
	usage := map[string][2]string{}
	for _, line := range slashHelp {
		split := strings.Index(line, "  ")
		if !strings.HasPrefix(line, "/") || split < 0 {
			continue
		}
		// Grouped lines such as "/attachments /detach N|all" give each
		// command the arguments that follow it.
		about, name := strings.TrimSpace(line[split:]), ""
		for _, field := range strings.Fields(line[:split]) {
			if _, seen := usage[field]; strings.HasPrefix(field, "/") && !seen {
				name = field
				usage[name] = [2]string{name, about}
			} else if name != "" && !strings.HasPrefix(field, "/") {
				usage[name] = [2]string{usage[name][0] + " " + field, about}
			}
		}
	}
	return usage
}()

// slashSuggestions lists the commands, or the /view and /context agent IDs,
// that extend the single-line prompt.
func (m LiveModel) slashSuggestions() []slashSuggestion {
	if !strings.HasPrefix(m.Prompt, "/") || strings.Contains(m.Prompt, "\n") || m.PopupAgent != "" {
		return nil
	}
	options := []slashSuggestion{}
	if command, _, spaced := strings.Cut(m.Prompt, " "); !spaced {
		for _, name := range slashCommands {
			usage := slashUsage[name]
			if usage[0] == "" {
				usage[0] = name
			}
			options = append(options, slashSuggestion{name, usage[0], usage[1]})
		}
	} else if command == "/view" || command == "/context" {
		options = append(options, slashSuggestion{command + " arena-orchestrator", "arena-orchestrator", "permanent project control"})
		for _, a := range m.Run.Workflow.Agents {
			about := a.Runtime.Provider + " / " + a.Runtime.Model
			if p, ok := m.Run.Profiles[a.Id]; ok && p.Role != "" {
				about = p.Role + " · " + about
			}
			options = append(options, slashSuggestion{command + " " + a.Id, a.Id, about})
		}
	}
	matches := []slashSuggestion{}
	for _, option := range options {
		if strings.HasPrefix(option.text, m.Prompt) {
			matches = append(matches, option)
		}
	}
	return matches
}

// suggestion returns the highlighted entry; editing the prompt resets it.
func (m LiveModel) suggestion(count int) int {
	if m.SuggestFor != m.Prompt || count == 0 {
		return 0
	}
	return (m.SuggestIndex%count + count) % count
}

// moveSuggestion steps the highlight, wrapping at either end.
func (m *LiveModel) moveSuggestion(step int) {
	count := len(m.slashSuggestions())
	m.SuggestIndex, m.SuggestFor = m.suggestion(count)+step, m.Prompt
	m.SuggestIndex = m.suggestion(count)
}

// completePrompt fills in the highlighted suggestion and a trailing space.
func (m *LiveModel) completePrompt() {
	options := m.slashSuggestions()
	if len(options) == 0 {
		return
	}
	text := options[m.suggestion(len(options))].text + " "
	m.setPrompt(text, len(text))
}

// suggestionLines draws the menu above the prompt, keeping the highlight in
// view when there are more matches than rows.
func (m LiveModel) suggestionLines(width, rows int, color bool) []string {
	options := m.slashSuggestions()
	if len(options) == 0 || rows < 1 {
		return nil
	}
	selected := m.suggestion(len(options))
	rows = min(rows, len(options))
	start := min(max(0, selected-rows+1), len(options)-rows)
	column := 0
	for _, option := range options {
		column = max(column, displayWidth(option.usage))
	}
	column = min(column, max(8, width/2))
	lines := []string{}
	for i := start; i < start+rows; i++ {
		option, marker := options[i], "  "
		if i == selected {
			marker = "› "
		}
		usage := pad(option.usage, column)
		if i == selected && color {
			usage = "\x1b[1;" + sgrBrand + "m" + usage + "\x1b[0m"
		}
		line := marker + usage
		if room := width - 2 - column - 2; option.about != "" && room > 3 {
			line += "  " + muted(short(option.about, room), color)
		}
		lines = append(lines, line)
	}
	if more := len(options) - rows; more > 0 && rows < len(options) {
		lines[len(lines)-1] += muted(fmt.Sprintf("  +%d more", more), color)
	}
	return lines
}
