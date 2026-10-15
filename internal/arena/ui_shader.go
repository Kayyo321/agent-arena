package arena

import (
	"fmt"
	"hash/fnv"
	"math"
	"os"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	c "github.com/Kayyo321/agent-arena/internal/contract"
	"github.com/Kayyo321/agent-arena/internal/shader"
	"github.com/charmbracelet/x/ansi"
)

type shaderTick struct {
	Workflow string
	At       time.Time
}
type shaderLoaded struct {
	Workflow string
	Program  *shader.Program
	Config   c.WorkflowSettingsBackgroundShader
	Err      error
}
type shaderGenerated struct {
	Workflow Workflow
	Profiles map[string]Profile
	Version  string
	Draft    ShaderDraft
	Err      error
}
type shaderSaved struct{ Err error }

func tickShader(id string) tea.Cmd {
	return tea.Tick(150*time.Millisecond, func(t time.Time) tea.Msg { return shaderTick{id, t} })
}
func (m LiveModel) loadShader() tea.Cmd {
	return func() tea.Msg {
		w := m.Run.Workflow
		if m.Project != nil && w.HeadAgent != "" {
			loaded, _, err := m.Project.Load(w.Id)
			if err != nil {
				return shaderLoaded{Workflow: w.Id, Err: err}
			}
			w = loaded
		}
		cfg := shaderSettings(w)
		if !cfg.Enabled {
			return shaderLoaded{Workflow: w.Id, Config: cfg}
		}
		source := shader.DefaultSource
		if m.Project != nil {
			var err error
			source, err = m.Project.shaderSource(w)
			if err != nil {
				return shaderLoaded{Workflow: w.Id, Config: cfg, Err: err}
			}
		}
		program, err := shader.Compile(source)
		return shaderLoaded{w.Id, program, cfg, err}
	}
}

// shaderClock is replaced in tests that need deterministic event ages.
var shaderClock = time.Now

// shaderSignals is a numeric summary of the journal. It is recomputed only when
// the event list changes, and carries no text from events or packets.
type shaderSignals struct {
	key                                      string
	events                                   int
	pending, delivered, failed, total        int
	questions, tasks, results, userQuestions int
	teamRounds                               int
	started                                  time.Time
	last                                     map[string]time.Time
	agentSeq                                 map[string]int
	failedAgent                              string
}

// shaderEnvelopes are host-smoothed levels advanced on every shader tick.
type shaderEnvelopes struct {
	Energy, Activity, Attention, Tension, Success, Progress, Typing float64
	FocusStrength, WorkTime                                         float64
	activityImpulse, typingImpulse                                  float64
	focusAgent                                                      string
	events                                                          int
}

func eventsKey(events []Event) string {
	if len(events) == 0 {
		return "0"
	}
	last := events[len(events)-1]
	return fmt.Sprintf("%d/%d/%s", len(events), last.Sequence, last.ID)
}

// eventCategory maps journal event types to the age channels shaders receive.
func eventCategories(kind string) []string {
	out := []string{}
	switch {
	case strings.HasPrefix(kind, "message."):
		out = append(out, "message")
	case strings.HasPrefix(kind, "change.") || strings.HasPrefix(kind, "checkpoint.") || strings.HasPrefix(kind, "rollback.") || strings.HasPrefix(kind, "worktree.") || strings.HasPrefix(kind, "integration."):
		out = append(out, "change")
	case strings.HasPrefix(kind, "context."):
		out = append(out, "context")
	case kind == "decision.requested":
		out = append(out, "decisionRequest")
	case kind == "decision.answered":
		out = append(out, "decisionAnswer")
	}
	if strings.HasPrefix(kind, "team.") {
		out = append(out, "team")
	}
	switch kind {
	case "agent.progress", "agent.plan", "provider.activity", "provider.requested", "process.output", "usage.reported":
		out = append(out, "progress")
	case "workflow.started", "workflow.resumed", "agent.started", "team.started":
		out = append(out, "start")
	case "agent.succeeded", "node.completed", "workflow.succeeded", "team.consensus", "rollback.completed":
		out = append(out, "success")
	case "team.no-consensus", "coworker.refused", "workflow.interrupted":
		out = append(out, "failure")
	}
	if strings.HasSuffix(kind, ".failed") {
		out = append(out, "failure")
	}
	return out
}

func summarizeEvents(events []Event) shaderSignals {
	s := shaderSignals{key: eventsKey(events), events: len(events), last: map[string]time.Time{}, agentSeq: map[string]int{}}
	for _, e := range events {
		at, err := time.Parse(time.RFC3339Nano, e.At)
		if err == nil {
			s.last["event"] = at
			for _, category := range eventCategories(e.Type) {
				s.last[category] = at
			}
			if e.Type == "workflow.started" && s.started.IsZero() {
				s.started = at
			}
		}
		if e.AgentID != "" {
			s.agentSeq[e.AgentID] = e.Sequence
		}
		switch e.Type {
		case "agent.failed":
			s.failedAgent = e.AgentID
		case "team.round":
			s.teamRounds++
		}
	}
	for _, p := range packetStates(events) {
		s.total++
		switch p.status {
		case "pending", "delayed":
			s.pending++
			switch p.kind {
			case "question":
				s.questions++
				if p.to == "user" {
					s.userQuestions++
				}
			case "task":
				s.tasks++
			case "result", "review-result", "team-result":
				s.results++
			}
		case "delivered":
			s.delivered++
		case "failed":
			s.failed++
		}
	}
	return s
}

// shaderSignals returns the cached journal summary, recomputing it when stale.
func (m LiveModel) shaderSignals() shaderSignals {
	if m.ShaderSignals.key == eventsKey(m.Events) && m.ShaderSignals.last != nil {
		return m.ShaderSignals
	}
	return summarizeEvents(m.Events)
}

func (m LiveModel) shaderAnimated() bool {
	animation := m.Run.Settings.Display.Animation
	return !(animation == "off" || animation == "auto" && os.Getenv("TERM") == "dumb")
}

// shaderState fills every state-derived input except viewport, clocks, focus
// position and smoothed envelopes. The returned targets are the unsmoothed
// envelope levels, and focus names the agent most worth watching.
func (m LiveModel) shaderState(now time.Time) (in shader.Inputs, targets shaderEnvelopes, focus string) {
	sig := m.shaderSignals()
	in = shader.IdleInputs(1, 1)
	in.Resolution, in.Aspect = shader.Value{}, 0
	in.AgentCount = float64(len(m.Run.Workflow.Agents))
	for _, state := range m.State.Agents {
		switch state {
		case "running":
			in.RunningAgents++
		case "queued":
			in.QueuedAgents++
		case "waiting":
			in.WaitingAgents++
		case "succeeded":
			in.SucceededAgents++
		case "failed":
			in.FailedAgents++
		case "cancelled":
			in.CancelledAgents++
		case "interrupted":
			in.InterruptedAgents++
		}
	}
	in.RunIdle = 0
	switch m.State.Status {
	case "running":
		in.RunActive = 1
	case "paused":
		in.RunPaused = 1
	case "interrupted":
		in.RunInterrupted = 1
	case "failed":
		in.RunFailed = 1
	case "succeeded":
		in.RunSucceeded = 1
	case "cancelled":
		in.RunCancelled = 1
	default:
		in.RunIdle = 1
	}
	if !sig.started.IsZero() {
		in.RunTime = max(0, now.Sub(sig.started).Seconds())
	}
	in.PendingPackets, in.DeliveredPackets, in.FailedPackets, in.TotalPackets = float64(sig.pending), float64(sig.delivered), float64(sig.failed), float64(sig.total)
	in.PendingQuestions, in.PendingTasks, in.PendingResults = float64(sig.questions), float64(sig.tasks), float64(sig.results)
	in.PendingDecisions = float64(len(m.Decisions))
	oldest := time.Time{}
	for _, d := range m.Decisions {
		if at, err := time.Parse(time.RFC3339Nano, d.CreatedAt); err == nil && (oldest.IsZero() || at.Before(oldest)) {
			oldest, focus = at, d.AgentID
		} else if focus == "" {
			focus = d.AgentID
		}
	}
	if !oldest.IsZero() {
		in.DecisionAge = max(0, now.Sub(oldest).Seconds())
	}
	if len(m.Decisions) > 0 || m.PendingReply != nil || sig.userQuestions > 0 {
		in.AwaitingUser = 1
	}
	for _, change := range m.Changes {
		in.Changes++
		switch change.State {
		case "integrated":
			in.ChangesIntegrated++
		case "conflicted":
			in.ChangesConflicted++
		case "superseded", "refused":
		default:
			in.ChangesPending++
		}
	}
	for _, tree := range m.Trees {
		in.Worktrees++
		if tree.Write {
			in.WritingWorktrees++
		}
	}
	for _, ready := range m.Readiness {
		if ready.CanStart {
			in.ProvidersReady++
		} else if ready.State != "unknown" {
			in.ProvidersBlocked++
		}
	}
	in.TeamRounds, in.EventCount = float64(sig.teamRounds), float64(sig.events)
	age := func(category string) float64 {
		at, ok := sig.last[category]
		if !ok {
			return shader.NeverAge
		}
		return min(shader.NeverAge, max(0, now.Sub(at).Seconds()))
	}
	in.EventAge, in.MessageAge, in.ProgressAge, in.StartAge = age("event"), age("message"), age("progress"), age("start")
	in.SuccessAge, in.FailureAge, in.ChangeAge, in.ContextAge, in.TeamAge = age("success"), age("failure"), age("change"), age("context"), age("team")
	in.DecisionRequestAge, in.DecisionAnswerAge = age("decisionRequest"), age("decisionAnswer")
	if !m.ShaderInputAt.IsZero() {
		in.InputAge = min(shader.NeverAge, max(0, now.Sub(m.ShaderInputAt).Seconds()))
	}
	in.PromptLength = float64(len([]rune(m.Prompt)))
	in.Attachments = float64(len(m.Attachments))
	if m.Starting || m.ChatBusy || m.ShaderBusy || m.ClipboardBusy {
		in.Busy = 1
	}
	if m.Error != "" || m.ShaderError != "" {
		in.ErrorActive = 1
	}
	if m.Run.Workflow.Id != "" {
		h := fnv.New32a()
		h.Write([]byte(m.Run.Workflow.Id))
		in.Seed = float64(h.Sum32()) / (1 << 32)
	}

	// Focus: the agent asking the user, else the most recently active running
	// agent, else the agent that failed most recently.
	strength := 0.0
	if focus != "" {
		strength = 1
	} else {
		best := -1
		for id, state := range m.State.Agents {
			if state != "running" {
				continue
			}
			// Ties are broken by ID so map order never moves the highlight.
			if seq := sig.agentSeq[id]; seq > best || seq == best && id < focus {
				focus, best = id, seq
			}
		}
		if focus != "" {
			strength = .8
		} else if sig.failedAgent != "" && (in.RunFailed == 1 || in.FailedAgents > 0) {
			focus, strength = sig.failedAgent, .7
		}
	}
	agents := max(1, in.AgentCount)
	active := .35 + .65*in.RunActive
	targets.Energy = clamp01((in.RunningAgents/agents*.8+in.QueuedAgents/agents*.2+min(in.PendingPackets, 4)/4*.25)*active + in.Busy*.2)
	targets.Attention = clamp01(in.AwaitingUser + min(in.PendingQuestions, 1)*.35)
	targets.Tension = clamp01(in.RunFailed*.7 + min(in.FailedAgents, 2)*.25 + min(in.ChangesConflicted, 1)*.35 + min(in.FailedPackets, 1)*.2 + in.ErrorActive*.3 + min(in.ProvidersBlocked, 1)*.15 + in.RunInterrupted*.2)
	targets.Success = clamp01(in.RunSucceeded*.8 + math.Exp(-in.SuccessAge/4)*.5)
	targets.Progress = clamp01((in.SucceededAgents + in.FailedAgents + in.CancelledAgents) / agents)
	if in.RunSucceeded == 1 {
		targets.Progress = 1
	}
	targets.FocusStrength = strength
	targets.events = sig.events
	return in, targets, focus
}
func clamp01(x float64) float64 { return math.Max(0, math.Min(1, x)) }

// advanceShader eases every envelope toward its target over dt seconds.
func (m *LiveModel) advanceShader(dt float64) {
	_, target, focus := m.shaderState(shaderClock())
	env := &m.ShaderEnv
	if focus != env.focusAgent {
		// Fade the highlight in at its new location instead of jumping there.
		env.focusAgent, env.FocusStrength = focus, 0
	}
	if fresh := target.events - env.events; fresh > 0 && env.events > 0 {
		env.activityImpulse = min(1.5, env.activityImpulse+.3*float64(fresh))
	}
	env.events = target.events
	ease := func(x *float64, to, tau float64) { *x += (to - *x) * (1 - math.Exp(-dt/tau)) }
	env.activityImpulse *= math.Exp2(-dt / 2)
	env.typingImpulse *= math.Exp2(-dt / .8)
	ease(&env.Energy, target.Energy, 1.2)
	ease(&env.Attention, target.Attention, .6)
	ease(&env.Tension, target.Tension, .8)
	ease(&env.Success, target.Success, 1.5)
	ease(&env.Progress, target.Progress, 1.5)
	ease(&env.FocusStrength, target.FocusStrength, .5)
	ease(&env.Activity, min(1, env.activityImpulse), .35)
	ease(&env.Typing, min(1, env.typingImpulse), .25)
	env.WorkTime += dt * (.25 + 1.2*env.Energy + .6*env.Activity)
}

// noteShaderInput records composer activity for the typing envelope.
func (m *LiveModel) noteShaderInput() {
	m.ShaderInputAt = shaderClock()
	m.ShaderEnv.typingImpulse = min(1.2, m.ShaderEnv.typingImpulse+.2)
}

func (m LiveModel) shaderInputs(width, height int) shader.Inputs {
	in, target, _ := m.shaderState(shaderClock())
	in.Resolution, in.Aspect = shader.Vec2(float64(width), float64(height)), float64(width)/float64(height*2)
	in.Time, in.DeltaTime, in.Frame = m.ShaderTime, m.ShaderDelta, float64(m.ShaderFrame)
	env := m.ShaderEnv
	in.Energy, in.Activity, in.Attention, in.Tension = env.Energy, env.Activity, env.Attention, env.Tension
	in.Success, in.Progress, in.Typing, in.FocusStrength, in.WorkTime = env.Success, env.Progress, env.Typing, env.FocusStrength, env.WorkTime
	if !m.shaderAnimated() {
		// Without animation, levels snap to state and transient channels rest.
		in.Time, in.DeltaTime, in.Frame, in.WorkTime = 0, 0, 0, 0
		in.Energy, in.Attention, in.Tension, in.Success, in.Progress = target.Energy, target.Attention, target.Tension, target.Success, target.Progress
		in.Activity, in.Typing, in.FocusStrength = 0, 0, target.FocusStrength
		for _, a := range []*float64{&in.EventAge, &in.MessageAge, &in.ProgressAge, &in.StartAge, &in.SuccessAge, &in.FailureAge, &in.ChangeAge, &in.DecisionRequestAge, &in.DecisionAnswerAge, &in.ContextAge, &in.TeamAge, &in.InputAge} {
			*a = shader.NeverAge
		}
	}
	return in
}

// focusPosition finds the focus agent's card label in the rendered rows and
// returns its approximate centre in uv coordinates.
func focusPosition(lines []string, agent string, start, end, width, height int) (shader.Value, bool) {
	if agent == "" {
		return shader.Vec2(.5, .5), false
	}
	word := func(r byte) bool {
		return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' || r == '.'
	}
	for y := start; y < end && y < len(lines); y++ {
		plain := ansi.Strip(lines[y])
		for from := 0; from < len(plain); {
			i := strings.Index(plain[from:], agent)
			if i < 0 {
				break
			}
			i += from
			j := i + len(agent)
			if (i == 0 || !word(plain[i-1])) && (j == len(plain) || !word(plain[j])) {
				x := float64(displayWidth(plain[:i])) + float64(displayWidth(agent))/2
				return shader.Vec2(x/float64(width), (float64(y)+1.5)/float64(height)), true
			}
			from = j
		}
	}
	return shader.Vec2(.5, .5), false
}

func (m LiveModel) background(lines []string, start, end, width, height int, color bool) []string {
	if m.ChatMode || m.Panel != "" || m.Detail || m.GitDetail || m.ChangeDetail || width < 40 || height < 12 || m.Run.Workflow.HeadAgent == "" || m.ShaderError != "" {
		return lines
	}
	cfg := shaderSettings(m.Run.Workflow)
	program := m.ShaderProgram
	if m.ShaderConfig != nil {
		cfg = *m.ShaderConfig
	}
	if !cfg.Enabled {
		return lines
	}
	if program == nil {
		if m.Run.Workflow.Settings.BackgroundShader != nil {
			return lines
		}
		program = shader.Default
	}
	// Uniform work is hoisted per frame, so only per-cell operations count.
	if width*height > 32768 || width*height*program.VaryingOperations() > 3000000 {
		return lines
	}
	in := m.shaderInputs(width, height)
	focus := m.ShaderEnv.focusAgent
	if !m.shaderAnimated() {
		_, _, focus = m.shaderState(shaderClock())
	}
	if position, ok := focusPosition(lines, focus, start, end, width, height); ok {
		in.Focus = position
	} else {
		in.FocusStrength = 0
	}
	frame := program.Bind(in)
	for y := start; y < end && y < len(lines); y++ {
		line := lines[y]
		plain := ansi.Strip(line)
		// Reserve the full occupied span, not only ink. Internal card padding and
		// spaces in text remain opaque. Extra clearance separates adjacent dots.
		left := displayWidth(plain) - displayWidth(strings.TrimLeft(plain, " "))
		right := displayWidth(strings.TrimRight(plain, " "))
		if right > 0 {
			left = max(0, left-1)
			right = min(width, right+1)
		} else {
			left = width
			right = width
		}
		var row strings.Builder
		for x := 0; x < left; x++ {
			row.WriteString(shaderSample(frame, cfg.Glyphs, x, y, width, height, color))
		}
		if right > left {
			row.WriteString(ansi.Cut(line, left, right))
			row.WriteString(strings.Repeat(" ", max(0, right-left-displayWidth(ansi.Cut(line, left, right)))))
		}
		for x := right; x < width; x++ {
			row.WriteString(shaderSample(frame, cfg.Glyphs, x, y, width, height, color))
		}
		lines[y] = row.String()
	}
	return lines
}
func shaderCell(program *shader.Program, glyphs string, in shader.Inputs, x, y, width, height int, color bool) string {
	return shaderSample(program.Bind(in), glyphs, x, y, width, height, color)
}
func shaderSample(frame *shader.Frame, glyphs string, x, y, width, height int, color bool) string {
	v := frame.Sample(shader.Vec2((float64(x)+.5)/float64(width), (float64(y)+.5)/float64(height)), shader.Vec2(float64(x), float64(y)))
	light := shader.Luminance(v)
	if light < .015 || len(glyphs) == 0 {
		return " "
	}
	glyph := glyphs[min(len(glyphs)-1, int(light*float64(len(glyphs))))]
	if glyph == ' ' {
		return " "
	}
	if color {
		// Lift midtones so sparse dots remain visible on charcoal terminal
		// backgrounds. Keep zero transparent and cap highlights below text.
		shade := 30 + 135*math.Sqrt(light)
		r, g, b := shadeTint(v, shade)
		return fmt.Sprintf("\x1b[38;2;%d;%d;%dm%c\x1b[0m", r, g, b, glyph)
	}
	// Ordered dither keeps low contrast and brightness variation without ANSI.
	threshold := [4][4]float64{{0, 8, 2, 10}, {12, 4, 14, 6}, {3, 11, 1, 9}, {15, 7, 13, 5}}
	if light*.65 < (threshold[y%4][x%4]+.5)/16 {
		return " "
	}
	return string(glyph)
}

// shadeTint keeps the shader's hue at the lifted brightness. Neutral output
// stays exactly grey; saturated hues are softened and capped below text.
func shadeTint(v shader.Value, shade float64) (int, int, int) {
	grey := int(math.Round(shade))
	if v.N < 3 {
		return grey, grey, grey
	}
	c := [3]float64{max(0, v.V[0]), max(0, v.V[1]), max(0, v.V[2])}
	peak := max(c[0], c[1], c[2])
	if peak <= 0 || c[0] == c[1] && c[1] == c[2] {
		return grey, grey, grey
	}
	const saturation, ceiling = .65, 205.0
	var tint [3]float64
	for i := range c {
		tint[i] = 1 - saturation + saturation*c[i]/peak
	}
	luma := .2126*tint[0] + .7152*tint[1] + .0722*tint[2]
	var out [3]int
	for i := range tint {
		out[i] = int(math.Round(min(ceiling, shade*tint[i]/luma)))
	}
	return out[0], out[1], out[2]
}
func shaderPreview(program *shader.Program, glyphs string) string {
	const width, height = 72, 18
	frame := program.Bind(shader.IdleInputs(width, height))
	lines := make([]string, height)
	for y := range height {
		var row strings.Builder
		for x := range width {
			row.WriteString(shaderSample(frame, glyphs, x, y, width, height, false))
		}
		lines[y] = row.String()
	}
	return strings.Join(lines, "\n")
}
func (m LiveModel) shaderCommand(name, arg string) (tea.Model, tea.Cmd) {
	if m.Project == nil || m.Run.Workflow.HeadAgent == "" {
		m.Error = "Open a saved arena first"
		return m, nil
	}
	if m.ShaderBusy {
		m.Error = "A shader request is already running"
		return m, nil
	}
	var operation tea.Cmd
	switch name {
	case "shader":
		if strings.TrimSpace(arg) == "" {
			m.Error = "Usage: /shader DESCRIPTION; /shader-save applies the preview; /shader-off disables it"
			return m, nil
		}
		id := m.Run.Workflow.Id
		operation = func() tea.Msg {
			w, profiles, version, err := m.Project.LoadVersion(id)
			if err != nil {
				return shaderGenerated{Err: err}
			}
			draft, err := generateShader(m.Context, m.Project, w, arg, Providers{}, Probe)
			return shaderGenerated{w, profiles, version, draft, err}
		}
		m.Panel, m.PanelText = "Background Shader Creation", []string{"Creating a shader with the configured default model…"}
	case "shader-save":
		if m.ShaderDraft == nil {
			m.Error = "Generate a preview with /shader DESCRIPTION first"
			return m, nil
		}
		d := *m.ShaderDraft
		operation = func() tea.Msg {
			cfg := defaultShader(d.Workflow.Id)
			cfg.Glyphs = d.Draft.Glyphs
			d.Workflow.Settings.BackgroundShader = cfg
			tx, err := m.Project.saveWithShader(d.Workflow, d.Profiles, d.Version, &d.Draft.Source)
			if err == nil {
				err = m.Project.Audit(d.Workflow.Id, "shader.saved", map[string]any{"transaction": tx, "source": "tui", "approvedBy": "user"})
			}
			return shaderSaved{err}
		}
	case "shader-off":
		id := m.Run.Workflow.Id
		operation = func() tea.Msg {
			w, profiles, version, err := m.Project.LoadVersion(id)
			if err != nil {
				return shaderSaved{err}
			}
			cfg := shaderSettings(w)
			cfg.Enabled = false
			w.Settings.BackgroundShader = &cfg
			tx, err := m.Project.Save(w, profiles, version)
			if err == nil {
				err = m.Project.Audit(id, "shader.saved", map[string]any{"transaction": tx, "enabled": false})
			}
			return shaderSaved{err}
		}
	}
	m.ShaderBusy = true
	if m.Session != nil {
		return m, m.Session.action(operation)
	}
	return m, operation
}
