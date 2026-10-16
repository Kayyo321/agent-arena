package arena

import (
	"bytes"
	"compress/flate"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/Kayyo321/agent-arena/internal/shader"
)

// packShare builds a code around arbitrary JSON, bypassing EncodeShare's checks.
func packShare(t *testing.T, kind string, data []byte) string {
	t.Helper()
	var packed bytes.Buffer
	w, _ := flate.NewWriter(&packed, flate.BestCompression)
	w.Write(data)
	w.Close()
	payload := base64.RawURLEncoding.EncodeToString(packed.Bytes())
	return "arena1." + kind + "." + shareCheck(payload) + "." + payload
}

func readyProbe(t *testing.T) {
	t.Helper()
	previous := shareProbe
	shareProbe = func(_ context.Context, _ string, r Runtime) Readiness {
		return Readiness{Provider: r.Provider, Model: r.Model, CanStart: r.Provider == "codex", Detail: "not signed in"}
	}
	t.Cleanup(func() { shareProbe = previous })
}

// savedIdeal saves the S+ fixture with a custom shader into a test project.
func savedIdeal(t *testing.T) (*Project, string) {
	t.Helper()
	p := testProject(t)
	w, profiles := idealArena()
	source := strings.Replace(shader.DefaultSource, "//", "// shared\n//", 1)
	cfg := defaultShader(w.Id)
	cfg.Glyphs = " .:-=+*#"
	w.Settings.BackgroundShader = cfg
	if _, err := p.saveWithShader(w, profiles, "", &source); err != nil {
		t.Fatal(err)
	}
	return p, source
}

func TestShareCodeRoundTripsArenaAndShader(t *testing.T) {
	p, source := savedIdeal(t)
	code, err := p.ExportArena("ideal")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(code, "arena1.a.") || strings.ContainsAny(code, " \n") {
		t.Fatalf("not a one-line arena code: %.40s", code)
	}
	b, err := DecodeShare(code)
	if err != nil {
		t.Fatal(err)
	}
	w, profiles, _ := p.Load("ideal")
	if string(JSON(*b.Workflow)) != string(JSON(w)) || string(JSON(b.Profiles)) != string(JSON(profiles)) {
		t.Fatal("arena changed in transit")
	}
	if b.Shader == nil || b.Shader.Source != source || b.Shader.Glyphs != " .:-=+*#" {
		t.Fatalf("shader changed in transit: %+v", b.Shader)
	}
	// Terminals wrap long lines and people paste codes inside chat messages.
	wrapped := "here you go: "
	for i := 0; i < len(code); i += 60 {
		wrapped += code[i:min(len(code), i+60)] + "\n  "
	}
	if _, err := DecodeShare(wrapped + " enjoy!"); err != nil {
		t.Fatalf("wrapped code: %v", err)
	}
	shaderCode, err := p.ExportShader("ideal")
	if err != nil {
		t.Fatal(err)
	}
	s, err := DecodeShare(shaderCode)
	if err != nil || s.Kind != "shader" || s.Workflow != nil || s.Shader.Source != source || s.From != "Ideal arena" {
		t.Fatalf("shader code: %+v %v", s, err)
	}
	if len(shaderCode) >= len(code) {
		t.Fatal("shader code should be smaller than the arena code")
	}
}

func TestShareCodeRejectsDamagedOrHostileInput(t *testing.T) {
	p, _ := savedIdeal(t)
	code, _ := p.ExportArena("ideal")
	w, profiles := idealArena()
	extra := map[string]Profile{"stowaway": scoreProfile("stowaway", "research", "read")}
	for id, profile := range profiles {
		extra[id] = profile
	}
	secret := w
	secret.Agents = append([]Agent{}, w.Agents...)
	secret.Agents[0].Runtime.Options = map[string]any{"apiKey": "sk-live-abcdefghijklmnopqrstuvwxyz123456"}
	cases := map[string]struct {
		text string
		want string
	}{
		"empty":     {"hello", "no Arena share code"},
		"truncated": {code[:len(code)-7], "cut off or altered"},
		"altered":   {code[:20] + strings.Repeat("A", 5) + code[25:], "cut off or altered"},
		"bomb":      {packShare(t, "a", bytes.Repeat([]byte(" "), maxShareJSON+10)), "expands beyond"},
		"kind":      {packShare(t, "s", JSON(ShareBundle{Format: shareFormat, Kind: "arena", Workflow: &w, Profiles: profiles})), "prefix says shader"},
		"extra":     {packShare(t, "a", JSON(ShareBundle{Format: shareFormat, Kind: "arena", Workflow: &w, Profiles: extra})), "not in the arena"},
		"secret":    {packShare(t, "a", JSON(ShareBundle{Format: shareFormat, Kind: "arena", Workflow: &secret, Profiles: profiles})), "secret"},
		"hlsl":      {packShare(t, "s", JSON(ShareBundle{Format: shareFormat, Kind: "shader", Shader: &ShareShader{"float4 main( {", "."}})), "shared shader"},
		"format":    {packShare(t, "s", []byte(`{"format":"arena.share.v9","kind":"shader","shader":null}`)), "share code"},
	}
	for name, tc := range cases {
		if _, err := DecodeShare(tc.text); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: got %v, want %q", name, err, tc.want)
		}
	}
	if _, err := EncodeShare(ShareBundle{Format: shareFormat, Kind: "arena", Workflow: &secret, Profiles: profiles, Shader: nil}); err == nil {
		t.Error("exported a secret-like runtime option")
	}
}

func TestImportRenamesOnCollisionAndSavesThroughTransaction(t *testing.T) {
	readyProbe(t)
	p, source := savedIdeal(t)
	code, _ := p.ExportArena("ideal")
	b, err := DecodeShare(code)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.PrepareImport(context.Background(), b, "ideal", nil); err == nil {
		t.Fatal("--as onto an existing arena was accepted")
	}
	d, err := p.PrepareImport(context.Background(), b, "", shareProbe)
	if err != nil {
		t.Fatal(err)
	}
	w := d.Workflow
	if w.Id != "ideal-2" || w.Agents[1].Profile != ".agents/ideal-2/implementer.agent.json" || w.Settings.BackgroundShader.Source != "ideal-2.background.hlsl" {
		t.Fatalf("not rekeyed: %s %s %+v", w.Id, w.Agents[1].Profile, w.Settings.BackgroundShader)
	}
	notes := strings.Join(d.Notes, "\n")
	for _, want := range []string{"Imported as ideal-2", "Writes code: implementer", "✓ codex", "✗ claude-code / sonnet / high not ready"} {
		if !strings.Contains(notes, want) {
			t.Fatalf("notes miss %q:\n%s", want, notes)
		}
	}
	before, _ := filepath.Glob(p.Path(".state", "transactions", "*", "manifest.json"))
	if _, err := p.SaveDraft(*d); err != nil {
		t.Fatal(err)
	}
	after, _ := filepath.Glob(p.Path(".state", "transactions", "*", "manifest.json"))
	if len(after) != len(before)+1 {
		t.Fatal("import did not go through a recoverable transaction")
	}
	saved, err := os.ReadFile(p.Path("ideal-2.background.hlsl"))
	if err != nil || string(saved) != source {
		t.Fatalf("shared shader not saved: %v", err)
	}
	w2, profiles2, err := p.Load("ideal-2")
	if err != nil {
		t.Fatal(err)
	}
	if got := ScoreArena(w2, profiles2).Grade; got != "S+" {
		t.Fatalf("imported arena scores %s", got)
	}
}

func TestShareCLIExportImportAndShaderInto(t *testing.T) {
	readyProbe(t)
	p, source := savedIdeal(t)
	var out bytes.Buffer
	run := func(stdin string, args ...string) error {
		cli := NewCLI(strings.NewReader(stdin), &out, &out)
		return cli.Run(context.Background(), append(args, "--project", p.Root))
	}
	if err := run("", "share", "export", "ideal"); err != nil {
		t.Fatal(err)
	}
	code := strings.TrimSpace(out.String())
	out.Reset()
	if err := run(code, "share", "import", "-", "--as", "copied", "--yes"); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	for _, want := range []string{"Grade S+", "Imported copied"} {
		if !strings.Contains(out.String(), want) {
			t.Fatalf("import output misses %q:\n%s", want, out.String())
		}
	}
	if err := run("", "score", "copied"); err != nil || !strings.Contains(out.String(), "Trust distribution") {
		t.Fatalf("score: %v", err)
	}
	// A shader code applies to an arena that already exists.
	if err := run("", "workflow", "create", "plain", "--head", "head", "--provider", "codex", "--model", "explicit-test-model", "--effort", "high", "--yes"); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := run("", "share", "export", "ideal", "--shader"); err != nil {
		t.Fatal(err)
	}
	shaderCode := strings.TrimSpace(out.String())
	if err := run("", "share", "import", shaderCode); err == nil || !strings.Contains(err.Error(), "--into") {
		t.Fatalf("shader import without --into: %v", err)
	}
	if err := run("", "share", "import", shaderCode, "--into", "plain", "--yes"); err != nil {
		t.Fatal(err)
	}
	if saved, _ := os.ReadFile(p.Path("plain.background.hlsl")); string(saved) != source {
		t.Fatal("shader code not applied to plain")
	}
}

func TestCopyClipboardFallsBackAcrossTools(t *testing.T) {
	tried := []string{}
	tool, err := copyClipboard(context.Background(), "linux", "arena1.s.x", func(_ context.Context, input []byte, name string, args ...string) error {
		tried = append(tried, name)
		if string(input) != "arena1.s.x" {
			t.Fatalf("input %q", input)
		}
		if name == "wl-copy" {
			return errors.New("no wayland")
		}
		return nil
	})
	if err != nil || tool != "xclip" || strings.Join(tried, ",") != "wl-copy,xclip" {
		t.Fatalf("tool %q err %v tried %v", tool, err, tried)
	}
	if _, err := copyClipboard(context.Background(), "plan9", "x", nil); err == nil {
		t.Fatal("copy on an unsupported platform")
	}
}

func TestPastingAnArenaCodeOpensAnImportDraft(t *testing.T) {
	readyProbe(t)
	p, _ := savedIdeal(t)
	code, _ := p.ExportArena("ideal")
	r, err := loadWorkflowView(p, "ideal")
	if err != nil {
		t.Fatal(err)
	}
	m := LiveModel{Project: p, Run: r, Width: 120, Height: 40, State: RunState{Status: "idle", Agents: map[string]string{}}}
	if !strings.Contains(m.View().Content, "S+") {
		t.Fatal("header does not show the grade")
	}
	next, cmd := m.Update(tea.PasteMsg{Content: code})
	m = next.(LiveModel)
	if cmd == nil || m.Prompt != "" {
		t.Fatalf("share code went into the composer: %q", m.Prompt)
	}
	next, _ = m.Update(cmd())
	m = next.(LiveModel)
	if m.Error != "" || m.Draft == nil || !m.ChatMode || m.Draft.Workflow.Id != "ideal-2" || m.DraftScore.Grade != "S+" {
		t.Fatalf("no import draft: %q %+v", m.Error, m.Draft)
	}
	if !strings.Contains(m.View().Content, "Writes code: implementer") {
		t.Fatal("import preview hides the trust notes")
	}
	next, _ = m.submit("/score")
	if m2 := next.(LiveModel); !strings.Contains(m2.Panel, "draft ideal-2") {
		t.Fatalf("/score panel: %q", m2.Panel)
	}
	next, cmd = m.submit("/save")
	m = next.(LiveModel)
	next, _ = m.Update(cmd())
	m = next.(LiveModel)
	if m.Error != "" || m.Run.Workflow.Id != "ideal-2" {
		t.Fatalf("import not saved: %s", m.Error)
	}
	if _, err := os.Stat(p.Path("ideal-2.background.hlsl")); err != nil {
		t.Fatal(err)
	}

	// A shader code previews on the open arena; /shader-save applies it.
	shaderCode, _ := p.ExportShader("ideal")
	next, cmd = m.Update(tea.PasteMsg{Content: shaderCode})
	m = next.(LiveModel)
	next, _ = m.Update(cmd())
	m = next.(LiveModel)
	if m.ShaderDraft == nil || m.ShaderDraft.Workflow.Id != "ideal-2" {
		t.Fatalf("shader paste did not preview: %q", m.Error)
	}
}

func TestLauncherShowsGradesAndImportsPastedArena(t *testing.T) {
	readyProbe(t)
	p, _ := savedIdeal(t)
	code, _ := p.ExportArena("ideal")
	other, _ := savedIdeal(t)
	ids, _ := other.List()
	s := newUISession(context.Background(), other)
	defer s.close()
	m := launcherModel{Project: other, IDs: append(ids, "missing"), Grades: arenaGrades(other, ids), Session: s, Width: 100, Height: 30}
	if !strings.Contains(m.View().Content, "S+") {
		t.Fatal("launcher hides arena grades")
	}
	next, cmd := m.Update(tea.PasteMsg{Content: "try this " + code})
	m = next.(launcherModel)
	next, _ = m.Update(cmd())
	m = next.(launcherModel)
	if m.Import == nil || m.Import.Workflow.Id != "ideal-2" || !strings.Contains(m.View().Content, "Enter import ideal-2") {
		t.Fatalf("no import preview: %q", m.Error)
	}
	next, cmd = m.Update(launchKey(tea.KeyEnter))
	m = next.(launcherModel)
	next, _ = m.Update(cmd())
	m = next.(launcherModel)
	if m.Live == nil || m.Live.Run.Workflow.Id != "ideal-2" {
		t.Fatalf("imported arena not opened: %q", m.Error)
	}
}
