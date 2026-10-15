package arena

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Kayyo321/agent-arena/internal/shader"
	"github.com/charmbracelet/x/ansi"
)

func TestShaderCLILifecycleAndIsolation(t *testing.T) {
	p := testProject(t)
	launchWorkflow(t, p, "alpha")
	launchWorkflow(t, p, "beta")
	w, _, err := p.Load("alpha")
	if err != nil {
		t.Fatal(err)
	}
	if cfg := w.Settings.BackgroundShader; cfg == nil || !cfg.Enabled || cfg.Source != "alpha.background.hlsl" {
		t.Fatalf("missing default: %+v", cfg)
	}
	original, err := os.ReadFile(p.Path("beta.background.hlsl"))
	if err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	cli := NewCLI(strings.NewReader(""), &out, &out)
	run := func(args ...string) error { return cli.Run(context.Background(), append(args, "--project", p.Root)) }
	for _, verb := range []string{"show", "validate", "preview"} {
		if err := run("shader", verb, "alpha"); err != nil {
			t.Fatal(err)
		}
	}
	path := p.Root + "/custom.hlsl"
	source := `float time; float4 main(float2 uv){float v=0.5+0.5*sin(time+uv.x*8.0);return float4(v,v,v,1);}`
	if err := os.WriteFile(path, []byte(source), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run("shader", "set", "alpha", "--input", path, "--glyphs", " .:-+*#@", "--yes"); err != nil {
		t.Fatal(err)
	}
	saved, _ := os.ReadFile(p.Path("alpha.background.hlsl"))
	if string(saved) != source {
		t.Fatal("source was not saved")
	}
	unchanged, _ := os.ReadFile(p.Path("beta.background.hlsl"))
	if !bytes.Equal(original, unchanged) {
		t.Fatal("another arena's shader changed")
	}
	if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := run("shader", "set", "alpha", "--input", path, "--yes"); err == nil {
		t.Fatal("invalid source accepted")
	}
	after, _ := os.ReadFile(p.Path("alpha.background.hlsl"))
	if !bytes.Equal(saved, after) {
		t.Fatal("invalid source destroyed prior shader")
	}
	if err := run("shader", "off", "alpha", "--yes"); err != nil {
		t.Fatal(err)
	}
	w, _, _ = p.Load("alpha")
	if w.Settings.BackgroundShader.Enabled {
		t.Fatal("not disabled")
	}
	if err := run("shader", "default", "alpha", "--yes"); err != nil {
		t.Fatal(err)
	}
	restored, _ := os.ReadFile(p.Path("alpha.background.hlsl"))
	if string(restored) != shader.DefaultSource {
		t.Fatal("default not restored")
	}
	txs, err := os.ReadDir(p.Path(".state", "transactions"))
	if err != nil || len(txs) < 6 {
		t.Fatal("missing recoverable transactions")
	}
}
func TestShaderSourceConflictsAndRecovery(t *testing.T) {
	p := testProject(t)
	launchWorkflow(t, p, "alpha")
	w, profiles, version, err := p.LoadVersion("alpha")
	if err != nil {
		t.Fatal(err)
	}
	changed := shader.DefaultSource + "\n// another editor\n"
	if err = os.WriteFile(p.Path("alpha.background.hlsl"), []byte(changed), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = p.Save(w, profiles, version); err == nil {
		t.Fatal("lost concurrent shader edit")
	}
	w, profiles, version, err = p.LoadVersion("alpha")
	if err != nil {
		t.Fatal(err)
	}
	w.Settings.BackgroundShader.Source = "../other.hlsl"
	if _, err = p.Save(w, profiles, version); err == nil {
		t.Fatal("accepted escaping path")
	}
	if err = os.Remove(p.Path("alpha.background.hlsl")); err != nil {
		t.Fatal(err)
	}
	w, profiles, version, err = p.LoadVersion("alpha")
	if err != nil {
		t.Fatal(err)
	}
	m := LiveModel{Project: p, Run: Run{Workflow: w}}
	loaded := m.loadShader()().(shaderLoaded)
	if loaded.Err == nil || loaded.Program != nil {
		t.Fatal("missing shader did not fail closed")
	}
	source := shader.DefaultSource
	if _, err = p.saveWithShader(w, profiles, version, &source); err != nil {
		t.Fatalf("cannot repair missing source: %v", err)
	}
	if err = os.Remove(p.Path("alpha.background.hlsl")); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(p.Root+"/outside.hlsl", p.Path("alpha.background.hlsl")); err != nil {
		t.Fatal(err)
	}
	if _, err = p.shaderSource(w); err == nil {
		t.Fatal("followed shader symlink")
	}
}
func TestShaderGenerationUsesDefaultRuntimeAndAuditedEnvelopes(t *testing.T) {
	p := testProject(t)
	launchWorkflow(t, p, "alpha")
	w, _, _ := p.Load("alpha")
	before, _ := os.ReadFile(p.Path("alpha.background.hlsl"))
	calls := 0
	invoker := testInvoker(func(ctx context.Context, in Invocation) (map[string]any, error) {
		calls++
		if in.Write || in.Commands || in.Runtime.Model != "explicit-test-model" || in.Runtime.ReasoningEffort != "high" || in.AgentID != "arena-orchestrator" || !strings.Contains(in.Prompt, "Task envelope:") {
			return nil, fmt.Errorf("lost default runtime, permissions or envelope")
		}
		if _, ok := ctx.Deadline(); !ok {
			return nil, fmt.Errorf("no invocation deadline")
		}
		if err := in.Delivered(); err != nil {
			return nil, err
		}
		return map[string]any{"source": shader.DefaultSource, "glyphs": "."}, nil
	})
	probe := func(context.Context, string, Runtime) Readiness { return Readiness{CanStart: true, State: "fixture"} }
	d, err := generateShader(context.Background(), p, w, "fading dots", invoker, probe)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 1 || d.Source != shader.DefaultSource {
		t.Fatal("unexpected generation")
	}
	after, _ := os.ReadFile(p.Path("alpha.background.hlsl"))
	if !bytes.Equal(before, after) {
		t.Fatal("generation applied without review")
	}
	events, err := p.Events("project")
	if err != nil {
		t.Fatal(err)
	}
	kinds := map[string]int{}
	for _, e := range events {
		kinds[e.Type]++
	}
	if kinds["message.sent"] != 2 || kinds["message.delivered"] != 2 || kinds["shader.generated"] != 1 {
		t.Fatalf("missing lifecycle: %v", kinds)
	}
	_, err = generateShader(context.Background(), p, w, "invalid", testInvoker(func(context.Context, Invocation) (map[string]any, error) {
		return map[string]any{"source": "bad", "glyphs": "."}, nil
	}), probe)
	if err == nil {
		t.Fatal("accepted invalid generation")
	}
	_, err = generateShader(context.Background(), p, w, "unavailable", invoker, func(context.Context, string, Runtime) Readiness { return Readiness{State: "unavailable"} })
	if err == nil || calls != 1 {
		t.Fatal("unavailable model invoked")
	}
}
func TestShaderProtectsForegroundSpansAndDisplayPreferences(t *testing.T) {
	w := NewWorkflow("test", "test")
	w.HeadAgent = "head"
	m := LiveModel{Run: Run{Workflow: w, Settings: DefaultSettings(Runtime{})}}
	program, err := shader.Compile(`float4 main(float2 uv){return float4(1);}`)
	if err != nil {
		t.Fatal(err)
	}
	m.ShaderProgram = program
	for _, color := range []bool{false, true} {
		text := "        ╭──────────╮        "
		label := "        │ café 界  │"
		if color {
			label = "        \x1b[31m│ café 界  │\x1b[0m"
		}
		lines := []string{"HEADER", text, label, "        ╰──────────╯", "", "FOOTER"}
		out := m.background(append([]string{}, lines...), 1, 5, 60, 24, color)
		if out[0] != lines[0] || out[5] != lines[5] {
			t.Fatal("header/footer changed")
		}
		for y := 1; y < 4; y++ {
			right := displayWidth(strings.TrimRight(ansi.Strip(lines[y]), " ")) + 1
			if ansi.Strip(ansi.Cut(out[y], 7, right)) != ansi.Strip(ansi.Cut(pad(lines[y], 60), 7, right)) || !strings.Contains(out[y], strings.TrimSpace(lines[y])) {
				t.Fatalf("foreground span changed color=%v: %q vs %q", color, out[y], lines[y])
			}
		}
		if strings.TrimSpace(ansi.Strip(out[4])) == "" {
			t.Fatal("empty graph background not shaded")
		}
		for y := 1; y < 5; y++ {
			if ansi.StringWidth(out[y]) != 60 {
				t.Fatalf("wrong width: %d", ansi.StringWidth(out[y]))
			}
		}
		if !color && strings.Contains(strings.Join(out, ""), "\x1b") {
			t.Fatal("monochrome emitted ANSI")
		}
	}
	m.ShaderTime = 5
	m.ShaderFrame = 40
	m.ShaderDelta = .15
	m.Run.Settings.Display.Animation = "off"
	in := m.shaderInputs(120, 40)
	if in.Time != 0 || in.Frame != 0 || in.DeltaTime != 0 || in.Aspect != 1.5 {
		t.Fatalf("wrong disabled inputs: %+v", in)
	}
	m.ChatMode = true
	plain := []string{"   text", ""}
	if got := m.background(append([]string{}, plain...), 0, 2, 60, 24, true); strings.Join(got, "\n") != strings.Join(plain, "\n") {
		t.Fatal("chat was shaded")
	}
	next, cmd := m.Update(shaderTick{"other", time.Now()})
	if cmd != nil || next.(LiveModel).ShaderFrame != m.ShaderFrame {
		t.Fatal("stale tick changed workflow")
	}
}
func TestShaderUniformsUseActualActivity(t *testing.T) {
	m := LiveModel{Run: Run{Workflow: Workflow{Agents: []Agent{{Id: "one"}, {Id: "two"}}}}, State: RunState{Status: "running", Agents: map[string]string{"one": "running", "two": "idle"}}, Decisions: []Decision{{ID: "decision"}}, Events: []Event{{Type: "message.sent", Data: map[string]any{"messageId": "a"}}, {Type: "message.sent", Data: map[string]any{"messageId": "b"}}, {Type: "message.delivered", Data: map[string]any{"messageId": "b"}}}}
	m.Run.Settings = DefaultSettings(Runtime{})
	in := m.shaderInputs(80, 24)
	if in.AgentCount != 2 || in.RunningAgents != 1 || in.PendingPackets != 1 || in.PendingDecisions != 1 || in.RunActive != 1 {
		t.Fatalf("wrong uniforms: %+v", in)
	}
}

func TestShaderViewPreservesForegroundAndPopupsAcrossSizes(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("LC_ALL", "en_US.UTF-8")
	w, profiles, settings := fixture(t)
	m := LiveModel{Run: Run{Workflow: w, Profiles: profiles, Settings: settings}, State: RunState{Status: "idle"}}
	for _, size := range [][2]int{{140, 40}, {100, 24}, {60, 18}, {30, 8}, {8, 4}} {
		m.Width, m.Height = size[0], size[1]
		for _, popup := range []string{"", "arena-orchestrator"} {
			m.PopupAgent = popup
			decorated := strings.Split(ansi.Strip(m.View().Content), "\n")
			off := defaultShader(w.Id)
			off.Enabled = false
			m.ShaderConfig = off
			plain := strings.Split(ansi.Strip(m.View().Content), "\n")
			m.ShaderConfig = nil
			if len(decorated) != len(plain) {
				t.Fatal("shader changed viewport height")
			}
			for y, line := range plain {
				for x, r := range []rune(line) {
					if r != ' ' && (x >= len([]rune(decorated[y])) || []rune(decorated[y])[x] != r) {
						t.Fatalf("foreground moved at %v popup=%s row=%d col=%d", size, popup, y, x)
					}
				}
			}
		}
	}
}

func TestShaderTUIDraftRequiresApplyAndReloads(t *testing.T) {
	p := testProject(t)
	launchWorkflow(t, p, "alpha")
	w, profiles, version, err := p.LoadVersion("alpha")
	if err != nil {
		t.Fatal(err)
	}
	s, _ := p.Settings()
	m := LiveModel{Project: p, Run: Run{Workflow: w, Profiles: profiles, Settings: s}, Context: context.Background()}
	source := `float4 main(float2 uv){return float4(uv.x,uv.x,uv.x,1);}`
	next, _ := m.Update(shaderGenerated{Workflow: w, Profiles: profiles, Version: version, Draft: ShaderDraft{source, " .:-+#"}})
	m = next.(LiveModel)
	before, _ := os.ReadFile(p.Path("alpha.background.hlsl"))
	if string(before) == source {
		t.Fatal("preview already applied")
	}
	if m.ShaderDraft == nil || m.Panel != "Background Shader Creation" {
		t.Fatal("missing preview")
	}
	next, cmd := m.submit("/shader-save")
	m = next.(LiveModel)
	if cmd == nil {
		t.Fatal("apply missing")
	}
	next, cmd = m.Update(cmd())
	m = next.(LiveModel)
	if m.Error != "" || cmd == nil {
		t.Fatalf("apply failed: %s", m.Error)
	}
	next, _ = m.Update(cmd())
	m = next.(LiveModel)
	saved, _ := os.ReadFile(p.Path("alpha.background.hlsl"))
	if string(saved) != source || m.ShaderProgram == nil || m.ShaderDraft != nil || m.Panel != "" {
		t.Fatal("applied shader did not reload")
	}
	next, cmd = m.submit("/shader-off")
	m = next.(LiveModel)
	next, cmd = m.Update(cmd())
	m = next.(LiveModel)
	next, _ = m.Update(cmd())
	m = next.(LiveModel)
	if m.ShaderConfig == nil || m.ShaderConfig.Enabled || m.ShaderProgram != nil {
		t.Fatal("shader-off did not remove background")
	}
}

func TestShaderMidtonesRemainVisibleOnCharcoalBackground(t *testing.T) {
	shades := []int{}
	for _, light := range []float64{0.02, 0.25, 0.5, 1} {
		program, err := shader.Compile(fmt.Sprintf("float4 main(float2 uv){return float4(%f,%f,%f,1);}", light, light, light))
		if err != nil {
			t.Fatal(err)
		}
		cell := shaderCell(program, ".", shader.Inputs{}, 40, 12, 80, 24, true)
		var r, g, b int
		if _, err := fmt.Sscanf(cell, "\x1b[38;2;%d;%d;%dm", &r, &g, &b); err != nil {
			t.Fatal(err)
		}
		if r != g || g != b {
			t.Fatal("background lost neutral shading")
		}
		shades = append(shades, r)
	}
	// Midtones must stand out against #1e1e1e while remaining quieter than text.
	if shades[1] < 95 || shades[2] < 120 || shades[3] > 165 {
		t.Fatalf("insufficient contrast or excessive highlights: %v", shades)
	}
	for i := 1; i < len(shades); i++ {
		if shades[i] <= shades[i-1] {
			t.Fatalf("edge fade is not monotonic: %v", shades)
		}
	}
	black, err := shader.Compile("float4 main(float2 uv){return float4(0,0,0,1);}")
	if err != nil {
		t.Fatal(err)
	}
	if shaderCell(black, ".", shader.Inputs{}, 40, 12, 80, 24, true) != " " {
		t.Fatal("zero luminance must remain empty")
	}
}
