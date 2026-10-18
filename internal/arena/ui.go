package arena

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
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
type resolutionPanelMsg struct {
	lines []string
	err   error
}
type uiReadinessResult struct {
	Required string
	Results  []Readiness
}
type LiveModel struct {
	ClipboardBusy           bool
	ShaderProgram           *shader.Program
	ShaderConfig            *c.WorkflowSettingsBackgroundShader
	ShaderError             string
	ShaderFrame             int
	ShaderTime, ShaderDelta float64
	ShaderLast              time.Time
	ShaderBusy              bool
	ShaderDraft             *shaderGenerated
	ShaderSignals           shaderSignals
	ShaderEnv               shaderEnvelopes
	ShaderInputAt           time.Time

	Attachments        []Attachment
	ChatAttachments    []Attachment
	SendingPrompt      string
	SendingAttachments []Attachment
	input              promptEditor
	GitDetail          bool
	Project            *Project
	Run                Run
	Readiness          []Readiness
	Width, Height      int
	Offset, Selected   int
	Panning            bool
	PanX, PanY         int
	SuggestIndex       int
	SuggestFor         string
	Prompt, Error      string
	Events             []Event
	State              RunState
	Trees              []Workspace
	DiffStats          map[string]diffStat
	DiffBusy           bool
	DiffAt             time.Time
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
	Consulting         bool   // chat messages consult about the loaded arena
	ConsultLog         string // the consultation so far, sent with each question
	ConsultTurns       int
	Walkthrough        string // setup mode while walking the user through ideas
	Chat               []chatLine
	Transcript         string
	Draft              *WorkflowDraft
	DraftScore         *Scorecard
	PastedShader       *ShaderDraft // a pasted arena's shader, kept across revisions
	ControlEvents      []Event
	Panel              string
	PanelText          []string
	PopupAgent         string
	PopupContext       bool
	PopupOffset        int
	PopupLines         []string
	PendingReply       chan string
	Invoker            Invoker
	StallKey           string
	StallCount         int
	StallBusy          bool
	Stall              *stallDiagnosis
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
	case clipboardResult:
		m.ClipboardBusy = false
		if v.Err != nil {
			m.Error = v.Err.Error()
			return m, nil
		}
		m.Error = ""
		if v.Attachment != nil {
			items := append(append([]Attachment(nil), m.Attachments...), *v.Attachment)
			if err := attachmentLimits(items); err != nil {
				m.Error = err.Error()
			} else {
				m.Attachments = items
			}
		} else if next, cmd, ok := m.pasteShare(v.Text); ok {
			return next, cmd
		} else if v.Text != "" {
			m.insertPrompt(v.Text)
		} else {
			m.Error = "Clipboard has no image or text"
		}

	case shaderTick:
		if v.Workflow != m.Run.Workflow.Id {
			return m, nil
		}
		m.ShaderFrame++
		if !m.ShaderLast.IsZero() {
			m.ShaderDelta = v.At.Sub(m.ShaderLast).Seconds()
			m.ShaderTime += m.ShaderDelta
			m.advanceShader(min(m.ShaderDelta, 1))
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
		if m.ShaderSignals.key != eventsKey(m.Events) {
			m.ShaderSignals = summarizeEvents(m.Events)
		}
		m.loadChangePatch()
		for _, event := range m.Events {
			if event.Sequence > m.LastProbeEvent && (event.Type == "agent.failed" || event.Type == "workflow.config-updated") {
				refreshReadiness = true
			}
			m.LastProbeEvent = max(m.LastProbeEvent, event.Sequence)
		}
		cmds := []tea.Cmd{tickUI(m.Run.ID)}
		// Worktrees only change while the run works; a finished run is measured once.
		if now := shaderClock(); m.Project != nil && !m.DiffBusy && len(m.Trees) > 0 && now.Sub(m.DiffAt) >= diffInterval && (m.DiffStats == nil || m.State.Status == "running" || len(m.DiffStats) != countWriting(m.Trees)) {
			m.DiffBusy, m.DiffAt = true, now
			cmds = append(cmds, m.refreshDiffStats())
		}
		if refreshReadiness {
			cmds = append(cmds, m.probeReadiness())
		}
		if stall := m.watchStall(); stall != nil {
			cmds = append(cmds, stall)
		}
		return m, tea.Batch(cmds...)
	case diffStatsResult:
		m.DiffBusy = false
		if v.RunID == m.Run.ID {
			m.DiffStats = v.Stats
		}
	case stallDiagnosis:
		m.StallBusy = false
		if v.RunID == m.Run.ID {
			m.Stall = &v
		}
	case shellResult:
		lines := []string{"$ " + v.Command}
		if output := strings.TrimRight(Redact(v.Output), "\n"); output != "" {
			lines = append(lines, strings.Split(output, "\n")...)
		}
		status := fmt.Sprintf("exit %d", v.Code)
		if v.Err != nil {
			status = "error: " + Redact(v.Err.Error())
		}
		m.Panel, m.PanelText, m.Offset = "Shell · "+status, lines, 1<<30
		m.ChatMode = false
	case resolutionPanelMsg:
		if v.err != nil {
			m.Error = v.err.Error()
			return m, nil
		}
		m.Panel, m.PanelText = "Resolution", v.lines
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
			if v.Result.Kind == "consult" {
				return m.consultReply(*v.Result), nil
			}
			m.Draft, m.DraftScore = v.Result.Draft, nil
			if m.Draft != nil && m.PastedShader != nil {
				d := *m.Draft
				d.Shader, d.Source = m.PastedShader, "share-import"
				if d.Workflow.Settings.BackgroundShader == nil {
					d.Workflow.Settings.BackgroundShader = defaultShader(d.Workflow.Id)
				}
				d.Workflow.Settings.BackgroundShader.Glyphs = m.PastedShader.Glyphs
				m.Draft = &d
			}
			if m.Draft != nil {
				score := ScoreArena(m.Draft.Workflow, m.Draft.Profiles)
				m.DraftScore = &score
			}
			if v.Result.Question != "" {
				m.Chat = append(m.Chat, chatLine{"arena-orchestrator", Redact(v.Result.Question)})
				m.Transcript += "\nAssistant: " + Redact(v.Result.Question)
			}
			if len(v.Result.Ideas) > 0 {
				m.showIdeas(v.Result.Ideas)
			}
			if m.Draft != nil {
				m.Chat = append(m.Chat, chatLine{"Arena", "Draft ready · " + m.DraftScore.Summary() + ". /score for the breakdown. Review the graph below, then /save to create it. Continue typing to revise."})
			}
			m.Offset = 1 << 30
		}
	case draftSaved:
		m.ChatBusy = false
		if v.Err != nil {
			m.Error = v.Err.Error()
			return m, nil
		}
		m.Run, m.Draft, m.DraftScore, m.PastedShader, m.ChatMode = v.Run, nil, nil, nil, false
		saved := "Saved " + v.Run.Workflow.Id + ". The graph is open; type a task or /run TASK to begin."
		if v.Revised {
			saved = "Applied the revision to " + v.Run.Workflow.Id + ". The graph is open; type a task or /run TASK to begin."
		}
		m.Walkthrough = setupDesign
		switch {
		case v.CommitErr != nil:
			saved += " Scaffold not committed: " + Redact(v.CommitErr.Error()) + " — commit before running writing agents."
		case v.Commit != "":
			saved += " Committed the scaffold as " + v.Commit + " (Arena scaffold)."
		}
		m.Detail, m.GitDetail, m.ChangeDetail = false, false, false
		m.State = RunState{Status: "idle", Agents: map[string]string{}}
		m.Readiness, m.Panel, m.PopupAgent, m.Offset = unknownReadiness(v.Run), "", "", 0
		m.Chat = append(m.Chat, chatLine{"Arena", saved})
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
			draft := m.SendingPrompt
			if m.Prompt != "" && draft != "" {
				draft += "\n\n"
			}
			draft += m.Prompt
			m.setPrompt(draft, len(draft))
			m.Attachments = append(append([]Attachment(nil), m.SendingAttachments...), m.Attachments...)
		}
		m.SendingPrompt, m.SendingAttachments = "", nil
		if v.Err != nil {
			m.Error = v.Err.Error()
		}
		if v.Run.ID == "" {
			return m, nil
		}
		m.Run, m.Readiness = v.Run, v.Readiness
		m.StallKey, m.StallCount, m.StallBusy, m.Stall = "", 0, false, nil
		m.State = RunState{Status: "created", Agents: map[string]string{}}
		m.Events, m.Trees, m.Decisions, m.Changes, m.DiffStats = nil, nil, nil, nil, nil
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
	case tea.MouseWheelMsg:
		// Capturing the wheel stops terminals from translating it into Up/Down,
		// which would recall prompt history instead of scrolling.
		switch v.Button {
		case tea.MouseWheelUp:
			m.scrollBy(-3)
		case tea.MouseWheelDown:
			m.scrollBy(3)
		}
	case shareCopied, shareImported:
		return m.updateShare(msg)
	case tea.PasteMsg:
		m.noteShaderInput()
		if next, cmd, ok := m.pasteShare(v.Content); ok {
			return next, cmd
		}
		if !m.pasteAttachments(v.Content) {
			m.insertPrompt(v.Content)
		}
	case tea.KeyPressMsg:
		m.noteShaderInput()
		m.syncPrompt()
		s := v.String()
		if m.Panning {
			if m.panKey(s) {
				return m, nil
			}
		}
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
			cmd := m.pasteClipboard()
			m.ClipboardBusy = true
			return m, cmd
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
			} else if m.input.recalled == 0 && len(m.slashSuggestions()) > 0 {
				// The menu owns ↑/↓ unless history is being browsed.
				if s == "up" {
					m.moveSuggestion(-1)
				} else {
					m.moveSuggestion(1)
				}
			} else if strings.Contains(m.Prompt, "\n") || displayWidth(m.Prompt) > max(1, m.Width-3) && m.Width > 0 {
				if s == "up" {
					m.movePromptVertical(-1)
				} else {
					m.movePromptVertical(1)
				}
			} else if s == "up" {
				m.recallPrompt(1)
			} else {
				m.recallPrompt(-1)
			}
		case "ctrl+p":
			m.recallPrompt(1)
		case "ctrl+n":
			m.recallPrompt(-1)
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
			m.scrollBy(-10)
		case "pgdown":
			m.scrollBy(10)
		case "home", "ctrl+a":
			if m.PopupAgent != "" && s == "home" {
				m.PopupOffset = 0
			} else {
				if s == "ctrl+a" {
					m.input.cursor = 0
				} else {
					m.input.cursor = strings.LastIndex(m.Prompt[:m.input.cursor], "\n") + 1
				}
			}
		case "end":
			if m.PopupAgent != "" {
				m.PopupOffset = 1 << 30
			} else {
				end := strings.Index(m.Prompt[m.input.cursor:], "\n")
				if end < 0 {
					m.input.cursor = len(m.Prompt)
				} else {
					m.input.cursor += end
				}
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
			// A partial command fills in the highlighted suggestion rather
			// than running, so a typo like /ca never cancels a run.
			if command := strings.TrimSpace(m.Prompt); !strings.Contains(command, " ") && !Has(slashCommands, command) && command != "/exit" && len(m.slashSuggestions()) > 0 {
				m.completePrompt()
				return m, nil
			}
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

// scrollBy moves the agent mini-window when open, otherwise the body.
func (m *LiveModel) scrollBy(lines int) {
	popup := m.PopupAgent != ""
	offset := &m.Offset
	if popup {
		offset = &m.PopupOffset
	}
	if *offset >= 1<<29 {
		*offset = m.scrollEnd(popup)
	}
	*offset = max(0, min(m.scrollEnd(popup), *offset+lines))
}

// graphShown reports whether the body is the workflow graph.
func (m LiveModel) graphShown() bool {
	return m.Panel == "" && !m.ChatMode && m.PopupAgent == "" && !m.Detail && !m.GitDetail && !m.ChangeDetail
}

// panKey moves the graph camera while /pan is active. Arrows pan, Space
// recenters, and Esc or Enter leave the mode; any other key leaves it and is
// handled normally, so typing resumes without an extra keystroke.
func (m *LiveModel) panKey(key string) bool {
	if !m.graphShown() {
		m.Panning = false
		return false
	}
	switch key {
	case "left":
		m.PanX -= panStepX
	case "right":
		m.PanX += panStepX
	case "up":
		m.PanY -= panStepY
	case "down":
		m.PanY += panStepY
	case "space", " ":
		m.PanX, m.PanY = 0, 0
	case "esc", "enter":
		m.Panning = false
	default:
		m.Panning = false
		return false
	}
	return true
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
		// With a single pending decision the ID may be omitted.
		if len(parts) >= 2 && len(m.Decisions) == 1 && m.pendingDecision(parts[1]) == nil {
			return m.Project.Answer(m.Run.ID, m.Decisions[0].ID, strings.Join(parts[1:], " "))
		}
		if len(parts) < 3 {
			return fmt.Errorf("decision ID ANSWER")
		}
		return m.Project.Answer(m.Run.ID, parts[1], strings.Join(parts[2:], " "))
	case "integrate":
		if len(parts) >= 2 && parts[1] == "all" {
			skip, err := resolutionSkip(parts[2:])
			if err != nil {
				return err
			}
			_, err = m.Project.ApplyResolution(m.Run.ID, "user", Approval{Channel: ApprovalTUI}, skip)
			return err
		}
		if len(parts) != 2 {
			return fmt.Errorf("inspect with /integrate, apply with /integrate all [skip CHANGE_ID...], or /integrate CHANGE_ID")
		}
		_, err := m.Project.IntegrateBy(m.Run.ID, parts[1], Approval{Channel: ApprovalTUI})
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
	prompt, cursorX, cursorY := m.promptView(width, height, color)
	footerHeight := 1 + len(prompt)
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
	lines = append(lines, promptRule(width, color))
	promptY := len(lines)
	lines = append(lines, prompt...)
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], width, "")
	}
	lines = m.background(lines, backgroundStart, height-footerHeight, width, height, color)
	lines = m.overlay(lines, width, height)
	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = true
	view.MouseMode = tea.MouseModeCellMotion
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
		// The styled transcript is appended after plain lines are wrapped.
	} else if m.ChangeDetail {
		body = append(body, "Review queue · Alt+↑/↓ select a change · PageUp/PageDown scroll · /close to return")
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
		wide := width >= 100 && !m.Expanded
		graph, legend := []string{}, []string{}
		if wide {
			graph = graphView(m.Run.Workflow, m.State, m.Events, m.agentDiffs(), width, color, m.spinner(false), frame)
			// The legend is the wide graph's last row; it stays put while the canvas pans.
			graph, legend = graph[:len(graph)-1], graph[len(graph)-1:]
		} else {
			graph = renderGraph(m.Run.Workflow, m.State, m.Events, m.Trees, width, color, m.Expanded, m.spinner(false))
		}
		// While panning, the key hint takes the wide graph's spacer row.
		if m.Panning {
			body = append(body, fmt.Sprintf("Pan · ←↑↓→ move · Space center · Esc done · offset %+d,%+d", m.PanX, m.PanY))
		} else if wide {
			body = append(body, "")
		}
		body = append(body, panLines(graph, m.PanX, m.PanY, width)...)
		body = append(body, legend...)
		body = append(body, packetActivity(m.Events, frame)...)
		pending := 0
		for _, change := range m.Changes {
			if change.State == "proposed" || change.State == "conflicted" {
				pending++
			}
		}
		if ready, applicable, ok := latestResolutionReady(m.Events); ok && applicable {
			body = append(body, fmt.Sprintf("Resolution prepared: %d ready · /integrate to review, /integrate all to apply", ready))
		} else if ok {
			body = append(body, "Resolution not ready · /integrate to see why")
		} else {
			body = append(body, fmt.Sprintf("Review queue: %d pending · /changes for patches and integration", pending))
		}
		body = append(body, foldReadiness(m.Readiness)...)
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
	if m.Starting {
		body = append([]string{m.spinner(color) + " Starting task · checking readiness, policies and Git isolation"}, body...)
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
	if m.ChatMode && m.Panel == "" {
		body = append(body, m.chatBody(width, color)...)
	}
	return body
}

const panStepX, panStepY = 4, 2

// panLines shows the graph through a camera offset by (dx, dy) cells from
// its centered layout. The block keeps its height so the rows below it stay
// put; the offset is clamped so some of the graph always remains in view.
func panLines(lines []string, dx, dy, width int) []string {
	if dx == 0 && dy == 0 || len(lines) == 0 {
		return lines
	}
	cols := width
	for _, line := range lines {
		cols = max(cols, ansi.StringWidth(line))
	}
	dx = max(-(width - 1), min(cols-1, dx))
	dy = max(-(len(lines) - 1), min(len(lines)-1, dy))
	out := make([]string, len(lines))
	for i := range out {
		line := ""
		if row := i + dy; row >= 0 && row < len(lines) {
			line = lines[row]
		}
		if dx > 0 {
			line = ansi.Cut(line, dx, dx+width)
		} else if dx < 0 && line != "" {
			line = strings.Repeat(" ", -dx) + line
		}
		out[i] = line
	}
	return out
}

func renderGraph(w Workflow, state RunState, events []Event, trees []Workspace, width int, color, expanded bool, spin ...string) []string {
	lines := []string{}
	members := map[string]bool{}
	for _, t := range w.Teams {
		for _, id := range t.Members {
			members[id] = true
		}
	}
	for _, f := range w.FanOuts {
		members[f.Planner], members[f.Worker] = true, true
	}
	for _, a := range w.Agents {
		if a.Activation == "on-call" || members[a.Id] && !expanded {
			continue
		}
		status := state.Agents[a.Id]
		if status == "" {
			status = "waiting"
		}
		if (status == "running" || status == "queued") && len(spin) > 0 {
			status = withSpinner(spin[0], status)
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
		kind := "agent"
		if a.Id == w.HeadAgent {
			kind, label = "head", "HEAD · "+label
		}
		lines = append(lines, kindCard(kind, a.Id, label, []string{runtime, status}, min(width, 70), color)...)
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
	for _, f := range w.FanOuts {
		state := "waiting"
		for _, event := range events {
			if event.AgentID == f.Id && strings.HasPrefix(event.Type, "fanout.") && !strings.HasPrefix(event.Type, "fanout.child.") {
				state = event.Type
			}
		}
		lines = append(lines, fmt.Sprintf("[FAN-OUT %s] %s; planner %s, worker %s, max %d", f.Id, state, f.Planner, f.Worker, f.MaxChildren))
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
	return "\x1b[" + agentSGR(id) + "m" + s + "\x1b[0m"
}
func countWriting(trees []Workspace) int {
	n := 0
	for _, tree := range trees {
		if tree.Write {
			n++
		}
	}
	return n
}
func agentSGR(id string) string {
	h := fnv.New32a()
	h.Write([]byte(id))
	return fmt.Sprintf("38;5;%d", agentPalette[h.Sum32()%uint32(len(agentPalette))])
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

// resolutionSkip parses the optional "skip CHANGE_ID..." tail of /integrate all.
func resolutionSkip(rest []string) ([]string, error) {
	if len(rest) == 0 {
		return nil, nil
	}
	if rest[0] != "skip" || len(rest) == 1 {
		return nil, fmt.Errorf("usage: /integrate all [skip CHANGE_ID...]")
	}
	return rest[1:], nil
}

// latestResolutionReady reports the ready count when the newest resolution
// event is resolution.ready, meaning nothing has been applied or stopped since.
func latestResolutionReady(events []Event) (ready int, applicable bool, ok bool) {
	for i := len(events) - 1; i >= 0; i-- {
		switch events[i].Type {
		case "resolution.ready":
			applicable, _ = events[i].Data["applicable"].(bool)
			return eventCount(events[i].Data["ready"]), applicable, true
		case "resolution.applied", "resolution.stopped":
			return 0, false, false
		}
	}
	return 0, false, false
}

// eventCount reads a count from journal data. Journal reads use UseNumber, so
// counts arrive as json.Number; float64, int and int64 cover in-process events.
func eventCount(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	case json.Number:
		i, err := n.Int64()
		if err != nil {
			return 0
		}
		return int(i)
	}
	return 0
}

// resolutionLines describes the prepared resolution for the /integrate panel.
// It runs Git, so it is called from a tea.Cmd and never from View() or Update.
func (m LiveModel) resolutionLines() ([]string, error) {
	resolution, err := m.Project.PrepareResolution(m.Run.ID, nil)
	if err != nil {
		return nil, err
	}
	ready, blocked, conflict := 0, 0, 0
	for _, item := range resolution.Items {
		switch item.Status {
		case "ready":
			ready++
		case "blocked":
			blocked++
		case "conflict":
			conflict++
		}
	}
	lines := []string{fmt.Sprintf("Resolution · %d ready · %d blocked · %d conflict", ready, blocked, conflict)}
	if !resolution.Ready && resolution.Reason != "" {
		lines = append(lines, "Not ready: "+resolution.Reason)
	}
	if resolution.Ready {
		lines = append(lines, "Apply the whole set with /integrate all, or /integrate all skip CHANGE_ID to leave one out.")
	} else {
		lines = append(lines, "Nothing applies while any change is blocked or conflicting. /integrate all skip CHANGE_ID leaves those out.")
	}
	for _, item := range resolution.Items {
		line := fmt.Sprintf("%d. %s · %s · %s", item.Order, item.ChangeID, item.AgentID, item.Status)
		if len(item.Paths) > 0 {
			line += " · " + strings.Join(item.Paths, ", ")
		}
		if item.Reason != "" {
			line += " · " + item.Reason
		}
		lines = append(lines, line)
	}
	return lines, nil
}
