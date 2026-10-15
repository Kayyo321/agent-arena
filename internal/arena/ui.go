package arena

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	c "github.com/Kayyo321/agent-arena/internal/contract"
	"github.com/Kayyo321/agent-arena/internal/shader"
	"github.com/charmbracelet/x/ansi"
)

type uiTick struct{ RunID string }
type uiCommandResult struct{ Err error }
type uiReadinessResult struct {
	Required string
	Results  []Readiness
}
type LiveModel struct {
	ShaderProgram           *shader.Program
	ShaderConfig            *c.WorkflowSettingsBackgroundShader
	ShaderError             string
	ShaderFrame             int
	ShaderTime, ShaderDelta float64
	ShaderLast              time.Time
	ShaderBusy              bool
	ShaderDraft             *shaderGenerated

	input              promptEditor
	GitDetail          bool
	Project            *Project
	Run                Run
	Readiness          []Readiness
	Width, Height      int
	Offset, Selected   int
	Prompt, Error      string
	Events             []Event
	State              RunState
	Trees              []Workspace
	Decisions          []Decision
	Detail             bool
	Expanded           bool
	Frame              int
	Changes            []Change
	ChangeDetail       bool
	ChangePatch        string
	SelectedChange     string
	LastProbeEvent     int
	Context            context.Context
	Session            *uiSession
	Starting           bool
	Executing          bool
	Interview          *interviewSession
	ChatMode, ChatBusy bool
	ChatTurns          int
	Chat               []chatLine
	Transcript         string
	Draft              *WorkflowDraft
	ControlEvents      []Event
	Panel              string
	PanelText          []string
	PopupAgent         string
	PopupContext       bool
	PopupOffset        int
	PopupLines         []string
	PendingReply       chan string
}

func (m LiveModel) Init() tea.Cmd {
	if m.Run.ID == "" {
		return tea.Batch(m.probeReadiness(), m.loadShader(), tickShader(m.Run.Workflow.Id))
	}
	return tea.Batch(tickUI(m.Run.ID), m.loadShader(), tickShader(m.Run.Workflow.Id))
}
func (m LiveModel) probeReadiness() tea.Cmd {
	runtimes := Required([]Workflow{m.Run.Workflow}, m.Run.Settings)
	return func() tea.Msg {
		results := []Readiness{}
		for _, runtime := range runtimes {
			results = append(results, Probe(m.Context, m.Project.Root, runtime))
		}
		return uiReadinessResult{string(JSON(runtimes)), results}
	}
}
func tickUI(runID string) tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(time.Time) tea.Msg { return uiTick{runID} })
}
func (m LiveModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch v := msg.(type) {
	case shaderTick:
		if v.Workflow != m.Run.Workflow.Id {
			return m, nil
		}
		m.ShaderFrame++
		if !m.ShaderLast.IsZero() {
			m.ShaderDelta = v.At.Sub(m.ShaderLast).Seconds()
			m.ShaderTime += m.ShaderDelta
		}
		m.ShaderLast = v.At
		return m, tickShader(v.Workflow)
	case shaderLoaded:
		if v.Workflow != m.Run.Workflow.Id {
			return m, nil
		}
		m.ShaderProgram, m.ShaderConfig, m.ShaderError = v.Program, &v.Config, ""
		if v.Err != nil {
			m.ShaderError = v.Err.Error()
		}
	case shaderGenerated:
		m.ShaderBusy = false
		if v.Err != nil {
			m.Error = v.Err.Error()
			m.PanelText = []string{"Shader creation failed; the saved shader is unchanged."}
			return m, nil
		}
		m.ShaderDraft = &v
		program, _ := shader.Compile(v.Draft.Source)
		m.Panel, m.PanelText = "Background Shader Creation", append([]string{"Preview · /shader-save to apply · /close to return", "Source: " + v.Workflow.Id + ".background.hlsl"}, strings.Split(shaderPreview(program, v.Draft.Glyphs), "\n")...)
	case shaderSaved:
		m.ShaderBusy = false
		if v.Err != nil {
			m.Error = v.Err.Error()
			return m, nil
		}
		m.ShaderDraft = nil
		m.Panel = ""
		m.ChatMode = false
		return m, m.loadShader()
	case tea.WindowSizeMsg:
		m.Width = v.Width
		m.Height = v.Height
	case uiTick:
		if m.Run.ID == "" || v.RunID != m.Run.ID {
			return m, nil
		}
		refreshReadiness := false
		m.Frame++
		var e error
		m.Events, e = m.Project.Events(m.Run.ID)
		if e != nil {
			m.Error = e.Error()
		} else {
			m.State, e = Reduce(m.Events)
			if e != nil {
				m.Error = e.Error()
			}
		}
		if current, err := m.Project.ReadRun(m.Run.ID); err == nil {
			m.Run = current
			known := map[string]bool{}
			for _, ready := range m.Readiness {
				known[ready.Provider+"/"+ready.Model] = true
			}
			for _, runtime := range Required([]Workflow{current.Workflow}, current.Settings) {
				if !known[runtime.Provider+"/"+runtime.Model] {
					m.Readiness = append(m.Readiness, Readiness{Provider: runtime.Provider, Model: runtime.Model, State: "unknown", Auth: "check arena status for newly added runtime"})
					known[runtime.Provider+"/"+runtime.Model] = true
					refreshReadiness = true
				}
			}
		}
		m.Trees, e = m.Project.Workspaces(m.Run.ID)
		if e != nil {
			m.Error = e.Error()
		}
		m.Decisions, e = m.Project.Decisions(m.Run.ID)
		if e != nil {
			m.Error = e.Error()
		}
		m.Changes, e = m.Project.Changes(m.Run.ID)
		if e != nil {
			m.Error = e.Error()
		}
		m.loadChangePatch()
		for _, event := range m.Events {
			if event.Sequence > m.LastProbeEvent && (event.Type == "agent.failed" || event.Type == "workflow.config-updated") {
				refreshReadiness = true
			}
			m.LastProbeEvent = max(m.LastProbeEvent, event.Sequence)
		}
		if refreshReadiness {
			return m, tea.Batch(tickUI(m.Run.ID), m.probeReadiness())
		}
		return m, tickUI(m.Run.ID)
	case uiReadinessResult:
		if v.Required == string(JSON(Required([]Workflow{m.Run.Workflow}, m.Run.Settings))) {
			m.Readiness = v.Results
			if m.Panel == "Provider status" {
				m.PanelText = readinessLines(v.Results)
			}
		}
	case interviewEvent:
		if v.Question != nil {
			m.PendingReply = v.Question.Reply
			m.Chat = append(m.Chat, chatLine{"arena-orchestrator", v.Question.Prompt})
			m.Offset = 1 << 30
			return m, waitInterview(v.Next)
		}
		if v.Event != nil {
			m.ControlEvents = append(m.ControlEvents, *v.Event)
			return m, waitInterview(v.Next)
		}
		if v.Result != nil {
			m.ChatBusy = false
			m.PendingReply = nil
			if v.Result.Err != nil {
				m.Error = v.Result.Err.Error()
				m.Chat = append(m.Chat, chatLine{"Arena", "Request failed: " + Redact(m.Error) + ". You can retry your message."})
				return m, nil
			}
			m.Draft = v.Result.Draft
			if v.Result.Question != "" {
				m.Chat = append(m.Chat, chatLine{"arena-orchestrator", Redact(v.Result.Question)})
				m.Transcript += "\nAssistant: " + Redact(v.Result.Question)
			}
			if m.Draft != nil {
				m.Chat = append(m.Chat, chatLine{"Arena", "Draft ready. Review the graph below, then /save to create it. Continue typing to revise."})
			}
			m.Offset = 1 << 30
		}
	case draftSaved:
		m.ChatBusy = false
		if v.Err != nil {
			m.Error = v.Err.Error()
			return m, nil
		}
		m.Run, m.Draft, m.ChatMode = v.Run, nil, false
		m.Detail, m.GitDetail, m.ChangeDetail = false, false, false
		m.State = RunState{Status: "idle", Agents: map[string]string{}}
		m.Readiness, m.Panel, m.PopupAgent, m.Offset = unknownReadiness(v.Run), "", "", 0
		m.Chat = append(m.Chat, chatLine{"Arena", "Saved " + v.Run.Workflow.Id + ". The graph is open; type a task or /run TASK to begin."})
		return m, m.Init()
	case agentContextLoaded:
		if v.Agent == m.PopupAgent {
			m.PopupLines = v.Lines
			if v.Err != nil {
				m.PopupLines = append(m.PopupLines, "Error: "+v.Err.Error())
			}
			if v.Agent == "arena-orchestrator" {
				m.ControlEvents = v.Events
			}
		}
	case uiRunStarted:
		m.Starting = false
		if v.Err != nil {
			m.Error = v.Err.Error()
		}
		if v.Run.ID == "" {
			return m, nil
		}
		m.Run, m.Readiness = v.Run, v.Readiness
		m.State = RunState{Status: "created", Agents: map[string]string{}}
		m.Events, m.Trees, m.Decisions, m.Changes = nil, nil, nil, nil
		m.ChangePatch, m.SelectedChange = "", ""
		m.LastProbeEvent, m.Offset, m.Selected = 0, 0, 0
		if v.Err != nil {
			return m, tickUI(m.Run.ID)
		}
		m.Executing = true
		return m, tea.Batch(tickUI(m.Run.ID), func() tea.Msg { return <-v.done })
	case uiRunFinished:
		m.Executing = false
		if v.Err != nil {
			m.Error = v.Err.Error()
		}
	case uiCommandResult:
		if v.Err != nil {
			m.Error = v.Err.Error()
		} else {
			m.Error = ""
		}
	case tea.PasteMsg:
		m.insertPrompt(v.Content)
	case tea.KeyPressMsg:
		m.syncPrompt()
		s := v.String()
		switch s {
		case "ctrl+c":
			return m, tea.Quit
		case "ctrl+q":
			return m, tea.Quit
		case "ctrl+g":
			m.showDetail("git")
		case "ctrl+e":
			m.showDetail("events")
		case "ctrl+t":
			m.Expanded = !m.Expanded
			m.ChatMode, m.Panel, m.Offset = false, "", 0
			m.Detail, m.GitDetail, m.ChangeDetail = false, false, false
		case "ctrl+v":
			m.showDetail("changes")
			m.loadChangePatch()
		case "esc":
			if m.PopupAgent != "" {
				m.PopupAgent = ""
			} else {
				m.setPrompt("", 0)
				m.input.recalled = 0
				m.Detail, m.GitDetail, m.ChangeDetail = false, false, false
				m.Panel = ""
			}
		case "tab":
			if strings.HasPrefix(m.Prompt, "/") {
				m.completePrompt()
			} else if m.PopupAgent != "" {
				m.PopupContext = !m.PopupContext
				m.PopupOffset = 0
			} else {
				m.showDetail("events")
			}
		case "up", "down":
			if m.PopupAgent != "" {
				if m.PopupOffset >= 1<<29 {
					m.PopupOffset = m.scrollEnd(true)
				}
				if s == "up" {
					m.PopupOffset = max(0, m.PopupOffset-1)
				} else {
					m.PopupOffset = min(m.scrollEnd(true), m.PopupOffset+1)
				}
			} else if s == "up" {
				m.recallPrompt(1)
			} else {
				m.recallPrompt(-1)
			}
		case "alt+up", "alt+down":
			if !m.Detail && !m.ChangeDetail {
				return m, nil
			}
			count := len(m.Run.Workflow.Agents) + len(m.Run.Workflow.Teams)
			if m.Detail {
				count = len(m.Events)
			}
			if m.ChangeDetail {
				count = len(m.Changes)
			}
			if s == "alt+up" {
				m.Selected = max(0, m.Selected-1)
			} else {
				m.Selected = min(max(0, count-1), m.Selected+1)
			}
			m.loadChangePatch()
			for row, line := range m.bodyLines(max(8, m.Width), false) {
				if strings.HasPrefix(line, "> ") {
					m.Offset = min(row, m.scrollEnd(false))
					break
				}
			}
		case "left", "ctrl+b":
			m.input.cursor = previousCharacter(m.Prompt, m.input.cursor)
		case "right", "ctrl+f":
			m.input.cursor = nextCharacter(m.Prompt, m.input.cursor)
		case "alt+left", "ctrl+left", "alt+b":
			m.input.cursor = m.wordBoundary(-1)
		case "alt+right", "ctrl+right", "alt+f":
			m.input.cursor = m.wordBoundary(1)
		case "pgup":
			if m.PopupAgent != "" {
				if m.PopupOffset >= 1<<29 {
					m.PopupOffset = m.scrollEnd(true)
				}
				m.PopupOffset = max(0, m.PopupOffset-10)
				return m, nil
			}
			if m.Offset >= 1<<29 {
				m.Offset = m.scrollEnd(false)
			}
			m.Offset = max(0, m.Offset-10)
		case "pgdown":
			if m.PopupAgent != "" {
				m.PopupOffset = min(m.scrollEnd(true), m.PopupOffset+10)
				return m, nil
			}
			m.Offset = min(m.scrollEnd(false), m.Offset+10)
		case "home", "ctrl+a":
			if m.PopupAgent != "" && s == "home" {
				m.PopupOffset = 0
			} else {
				m.input.cursor = 0
			}
		case "end":
			if m.PopupAgent != "" {
				m.PopupOffset = 1 << 30
			} else {
				m.input.cursor = len(m.Prompt)
			}
		case "ctrl+home":
			m.Offset = 0
		case "ctrl+end":
			m.Offset = 1 << 30
		case "backspace":
			m.deletePrompt(previousCharacter(m.Prompt, m.input.cursor), m.input.cursor)
		case "delete", "ctrl+d":
			m.deletePrompt(m.input.cursor, nextCharacter(m.Prompt, m.input.cursor))
		case "ctrl+w", "alt+backspace":
			m.deletePrompt(m.wordBoundary(-1), m.input.cursor)
		case "alt+d", "ctrl+delete":
			m.deletePrompt(m.input.cursor, m.wordBoundary(1))
		case "ctrl+u":
			m.deletePrompt(0, m.input.cursor)
		case "ctrl+k":
			m.deletePrompt(m.input.cursor, len(m.Prompt))
		case "shift+enter", "alt+enter", "ctrl+j":
			m.insertPrompt("\n")
		case "enter":
			input := m.Prompt
			next, cmd := m.submit(input)
			value := next.(LiveModel)
			if value.Prompt == "" {
				value.rememberPrompt(input)
			}
			value.syncPrompt()
			return value, cmd
		default:
			if v.Key().Text != "" {
				m.insertPrompt(v.Key().Text)
			}
		}
	}
	return m, nil
}
func (m *LiveModel) showDetail(panel string) {
	open := panel == "git" && m.GitDetail || panel == "events" && m.Detail || panel == "changes" && m.ChangeDetail
	m.ChatMode, m.Panel, m.PopupAgent, m.Offset = false, "", "", 0
	m.GitDetail, m.Detail, m.ChangeDetail = panel == "git" && !open, panel == "events" && !open, panel == "changes" && !open
}

func (m *LiveModel) loadChangePatch() {
	if !m.ChangeDetail || len(m.Changes) == 0 {
		return
	}
	m.Selected = min(m.Selected, len(m.Changes)-1)
	id := m.Changes[m.Selected].ID
	if m.SelectedChange == id {
		return
	}
	b, err := os.ReadFile(filepath.Join(m.Project.RunDir(m.Run.ID), "changes", id+".patch"))
	if err != nil {
		m.Error = err.Error()
		return
	}
	m.SelectedChange, m.ChangePatch = id, Redact(string(b))
}
func (m LiveModel) command(command string) error {
	parts := strings.Fields(command)
	if len(parts) == 0 {
		return nil
	}
	if m.Run.ID == "" {
		return fmt.Errorf("no run yet; enter run TASK to start this arena, or q to quit")
	}
	switch parts[0] {
	case "pause":
		return m.Project.Transition(m.Run.ID, "paused")
	case "resume":
		return m.Project.Transition(m.Run.ID, "resumed")
	case "cancel":
		return m.Project.Transition(m.Run.ID, "cancelled")
	case "decision":
		if len(parts) < 3 {
			return fmt.Errorf("decision ID ANSWER")
		}
		return m.Project.Answer(m.Run.ID, parts[1], strings.Join(parts[2:], " "))
	case "integrate":
		if len(parts) != 2 {
			return fmt.Errorf("inspect with /changes, then /integrate CHANGE_ID")
		}
		_, err := m.Project.Integrate(m.Run.ID, parts[1], true)
		return err
	default:
		return fmt.Errorf("use /help for commands; /view NAME for activity, /context NAME for inputs, /quit to exit")
	}
}
func (m LiveModel) View() tea.View {
	width := max(8, m.Width)
	height := max(4, m.Height)
	color := terminalColor(m.Run.Settings.Display.ColorMode)
	lines := m.header(width, height, color)
	body := m.bodyLines(width, color)
	status := []string{}
	seen := map[string]bool{}
	for _, r := range m.Readiness {
		if seen[r.Provider] {
			continue
		}
		seen[r.Provider] = true
		status = append(status, r.Provider+": "+r.State)
	}
	bottom := fmt.Sprintf("arena: %s · %s · %s · decisions: %d", m.Run.Workflow.Id, strings.Join(status, ", "), m.State.Status, len(m.Decisions))
	prompt, cursorX, cursorY := m.promptView(width, height, color)
	footerHeight := 2 + len(prompt)
	lines = lines[:min(len(lines), max(0, height-footerHeight-1))]
	backgroundStart := len(lines)
	budget := max(0, height-len(lines)-footerHeight)
	offset := min(m.Offset, max(0, len(body)-max(1, budget)))
	end := min(len(body), offset+budget)
	if end >= offset {
		lines = append(lines, body[offset:end]...)
	}
	for len(lines) < height-footerHeight {
		lines = append(lines, "")
	}
	lines = append(lines, muted(short(bottom, width), color), promptRule(width, color))
	promptY := len(lines)
	lines = append(lines, prompt...)
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], width, "")
	}
	lines = m.background(lines, backgroundStart, height-footerHeight, width, height, color)
	lines = m.overlay(lines, width, height)
	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = true
	view.Cursor = tea.NewCursor(cursorX, promptY+cursorY)
	view.Cursor.Shape = tea.CursorBar
	return view
}
func (m LiveModel) bodyLines(width int, color bool) []string {
	body := []string{}
	if m.Panel != "" {
		body = append(body, m.Panel+" · /close to return")
		body = append(body, m.PanelText...)
	} else if m.ChatMode {
		body = append(body, m.chatBody()...)
	} else if m.ChangeDetail {
		body = append(body, "Review queue · Alt+↑/↓ select a change · PageUp/PageDown scroll · Ctrl+V close")
		for i, change := range m.Changes {
			marker := " "
			if i == m.Selected {
				marker = ">"
			}
			body = append(body, marker+" "+change.ID+" | "+change.AgentID+" | "+change.State)
			if i == m.Selected {
				body = append(body, "  paths: "+strings.Join(change.Paths, ", "), "  dependencies: "+strings.Join(change.DependsOn, ", "))
			}
		}
		body = append(body, "After reviewing: > integrate CHANGE_ID", m.ChangePatch)
		body = strings.Split(strings.Join(body, "\n"), "\n")
	} else if m.GitDetail {
		for _, tree := range m.Trees {
			body = append(body, fmt.Sprintf("%s | %s | %s | %s", tree.AgentID, tree.Branch, tree.State, tree.Path))
		}
	} else if m.Detail {
		for i, event := range m.Events {
			marker := " "
			if i == m.Selected {
				marker = ">"
			}
			body = append(body, fmt.Sprintf("%s %04d %s %s", marker, event.Sequence, event.AgentID, event.Type))
			if i == m.Selected {
				for _, line := range strings.Split(string(SafeJSON(event.Data)), "\n") {
					body = append(body, "  "+line)
				}
			}
		}
	} else {
		frame := m.Frame
		if m.Run.Settings.Display.Animation == "off" || m.Run.Settings.Display.Animation == "auto" && os.Getenv("TERM") == "dumb" {
			frame = -1
		}
		if width >= 100 && !m.Expanded {
			body = append(body, wideGraph(m.Run.Workflow, m.State, m.Events, width, color, frame)...)
		} else {
			body = append(body, renderGraph(m.Run.Workflow, m.State, m.Events, m.Trees, width, color, m.Expanded)...)
		}
		body = append(body, packetActivity(m.Events, frame)...)
		pending := 0
		for _, change := range m.Changes {
			if change.State == "proposed" || change.State == "conflicted" {
				pending++
			}
		}
		body = append(body, fmt.Sprintf("Review queue: %d pending · /changes for patches and integration", pending))
		for _, ready := range m.Readiness {
			if ready.State != "good" {
				body = append(body, "Provider "+ready.Provider+" / "+ready.Model+": "+ready.State+" · "+ready.Auth)
			}
		}
	}
	if !m.ChatMode && m.Panel == "" && !m.Detail && !m.GitDetail && !m.ChangeDetail {
		body = append(body, "On-call rail: arena-orchestrator (permanent project control · "+m.Run.Settings.DefaultModel.Provider+" / "+m.Run.Settings.DefaultModel.Model+")")
		for _, ready := range m.Readiness {
			if ready.Provider == m.Run.Settings.DefaultModel.Provider && ready.Model == m.Run.Settings.DefaultModel.Model && ready.Skill != "" {
				body = append(body, "  Skill: "+ready.Skill)
				break
			}
		}
		for _, a := range m.Run.Workflow.Agents {
			if a.Activation == "on-call" {
				state := m.State.Agents[a.Id]
				if state == "" {
					state = "idle · configured triggers only"
				}
				body = append(body, "  "+a.Id+" | "+state)
			}
		}
	}
	for _, d := range m.Decisions {
		body = append(body, "DECISION "+d.ID+" "+d.Prompt+" choices="+strings.Join(d.Choices, "/"))
		body = append(body, "  > decision "+d.ID+" ANSWER")
	}
	if m.Run.ID == "" && !m.ChatMode {
		body = append([]string{"Ready · type a task or /run TASK · /help for commands"}, body...)
	}
	if m.Starting {
		body = append([]string{"Starting task · checking readiness, policies and Git isolation…"}, body...)
	}
	if m.ShaderError != "" {
		body = append([]string{"Background shader disabled: " + Redact(m.ShaderError)}, body...)
	}
	if m.Error != "" {
		body = append([]string{"Error: " + Redact(m.Error)}, body...)
	}
	if m.Detail || m.GitDetail || m.ChangeDetail || m.Panel != "" || m.ChatMode {
		body = plainUILines(body, width)
	}
	return body
}
func renderGraph(w Workflow, state RunState, events []Event, trees []Workspace, width int, color, expanded bool) []string {
	lines := []string{}
	members := map[string]bool{}
	for _, t := range w.Teams {
		for _, id := range t.Members {
			members[id] = true
		}
	}
	for _, a := range w.Agents {
		if a.Activation == "on-call" || members[a.Id] && !expanded {
			continue
		}
		status := state.Agents[a.Id]
		if status == "" {
			status = "waiting"
		}
		label := a.Id + " · " + a.Runtime.Provider
		runtime := a.Runtime.Model
		if a.Runtime.ReasoningEffort != "" {
			runtime += " · " + a.Runtime.ReasoningEffort
		}
		if width < 65 {
			lines = append(lines, agentColor(a.Id, "["+a.Id+"] "+runtime+" | "+status, color))
			continue
		}
		cardWidth := min(width, 70)
		b := borders()
		lines = append(lines, muted(b.top(cardWidth, ""), color), b.row(agentColor(a.Id, label, color), cardWidth), b.row(runtime, cardWidth), b.row(status, cardWidth), muted(b.bottom(cardWidth), color))
	}
	for _, t := range w.Teams {
		roundState := "waiting"
		for _, event := range events {
			if event.AgentID == t.Id && strings.HasPrefix(event.Type, "team.") {
				roundState = event.Type
			}
		}
		lines = append(lines, "[TEAM "+t.Id+"] "+roundState+"; members "+strings.Join(t.Members, ", "))
	}
	for _, r := range w.DecisionRouters {
		label := "[ROUTER " + r.Id + "] " + r.PayloadPointer
		for _, event := range events {
			if event.AgentID == r.Id && event.Type == "router.selected" {
				label += " selected=" + str(event.Data["branch"]) + " -> " + str(event.Data["target"])
			}
		}
		lines = append(lines, label)
		for _, route := range r.Routes {
			lines = append(lines, "  ="+strings.TrimSpace(string(JSON(route.Equals)))+" --> "+route.To)
		}
		lines = append(lines, "  fallback "+r.Fallback.Action+" "+r.Fallback.To)
	}
	for _, edge := range w.Relationships {
		arrow := "--delegates-->"
		if edge.Type == "communicates" {
			arrow = "..peer..>"
			if edge.Direction == "two-way" {
				arrow = "<..peer..>"
			}
		}
		lines = append(lines, edge.From+" "+arrow+" "+edge.To)
	}
	if len(trees) > 0 {
		lines = append(lines, "Worktree details:")
		for _, tree := range trees {
			lines = append(lines, "  "+tree.AgentID+" | "+tree.Branch+" | "+tree.Path)
		}
	}
	return lines
}
func short(s string, width int) string {
	return ansi.Truncate(strings.ReplaceAll(s, "\n", " "), max(0, width), "…")
}
func pad(s string, width int) string {
	s = short(s, width)
	return s + strings.Repeat(" ", max(0, width-ansi.StringWidth(s)))
}
func agentColor(id, s string, on bool) string {
	if !on || id == "" {
		return s
	}
	return fmt.Sprintf("\x1b[3%sm%s\x1b[0m", string('1'+rune(id[0]%6)), s)
}
func Watch(ctx context.Context, p *Project, r Run, readiness []Readiness) error {
	if readiness == nil {
		for _, runtime := range Required([]Workflow{r.Workflow}, r.Settings) {
			readiness = append(readiness, Probe(ctx, p.Root, runtime))
		}
	}
	session := newUISession(ctx, p)
	defer session.close()
	model := LiveModel{Project: p, Run: r, Readiness: readiness, Width: 80, Height: 24, Context: session.ctx, Session: session}
	program := tea.NewProgram(model, tea.WithContext(ctx))
	_, err := program.Run()
	return err
}
