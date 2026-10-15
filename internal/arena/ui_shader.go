package arena

import (
	"fmt"
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
func (m LiveModel) shaderInputs(width, height int) shader.Inputs {
	in := shader.Inputs{Resolution: shader.Vec2(float64(width), float64(height)), Aspect: float64(width) / float64(height*2), AgentCount: float64(len(m.Run.Workflow.Agents)), PendingDecisions: float64(len(m.Decisions)), Time: m.ShaderTime, DeltaTime: m.ShaderDelta, Frame: float64(m.ShaderFrame)}
	for _, state := range m.State.Agents {
		if state == "running" {
			in.RunningAgents++
		}
	}
	for _, p := range packetStates(m.Events) {
		if p.status == "pending" || p.status == "delayed" {
			in.PendingPackets++
		}
	}
	if m.State.Status == "running" {
		in.RunActive = 1
	}
	if m.Run.Settings.Display.Animation == "off" || m.Run.Settings.Display.Animation == "auto" && os.Getenv("TERM") == "dumb" {
		in.Time, in.DeltaTime, in.Frame = 0, 0, 0
	}
	return in
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
	if width*height > 32768 || width*height*program.Operations() > 1500000 {
		return lines
	}
	in := m.shaderInputs(width, height)
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
			row.WriteString(shaderCell(program, cfg.Glyphs, in, x, y, width, height, color))
		}
		if right > left {
			row.WriteString(ansi.Cut(line, left, right))
			row.WriteString(strings.Repeat(" ", max(0, right-left-displayWidth(ansi.Cut(line, left, right)))))
		}
		for x := right; x < width; x++ {
			row.WriteString(shaderCell(program, cfg.Glyphs, in, x, y, width, height, color))
		}
		lines[y] = row.String()
	}
	return lines
}
func shaderCell(program *shader.Program, glyphs string, in shader.Inputs, x, y, width, height int, color bool) string {
	in.Cell = shader.Vec2(float64(x), float64(y))
	in.UV = shader.Vec2((float64(x)+.5)/float64(width), (float64(y)+.5)/float64(height))
	light := shader.Luminance(program.Eval(in))
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
		shade := int(math.Round(30 + 135*math.Sqrt(light)))
		return fmt.Sprintf("\x1b[38;2;%d;%d;%dm%c\x1b[0m", shade, shade, shade, glyph)
	}
	// Ordered dither keeps low contrast and brightness variation without ANSI.
	threshold := [4][4]float64{{0, 8, 2, 10}, {12, 4, 14, 6}, {3, 11, 1, 9}, {15, 7, 13, 5}}
	if light*.65 < (threshold[y%4][x%4]+.5)/16 {
		return " "
	}
	return string(glyph)
}
func shaderPreview(program *shader.Program, glyphs string) string {
	const width, height = 72, 18
	in := shader.Inputs{Resolution: shader.Vec2(width, height), Aspect: 2}
	lines := make([]string, height)
	for y := range height {
		var row strings.Builder
		for x := range width {
			row.WriteString(shaderCell(program, glyphs, in, x, y, width, height, false))
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
