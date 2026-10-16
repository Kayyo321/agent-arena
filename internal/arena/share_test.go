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
	view := m.View().Content
	if notes, graph := strings.Index(view, "Writes code"), strings.Index(view, "Arena Ideal arena"); notes < 0 || graph < 0 || notes > graph {
		t.Fatal("trust notes must come before the graph so a large graph cannot hide them")
	}
	next, cmd = m.Update(launchKey(tea.KeyEnter))
	m = next.(launcherModel)
	next, _ = m.Update(cmd())
	m = next.(launcherModel)
	if m.Live == nil || m.Live.Run.Workflow.Id != "ideal-2" {
		t.Fatalf("imported arena not opened: %q", m.Error)
	}
}

// TestExportRefusesSensitiveFields pins the /copy guarantee: a field that may
// identify the author's machine or hold a credential refuses the export, names
// the field, and never echoes the value.
func TestExportRefusesSensitiveFields(t *testing.T) {
	type edit func(*Workflow, map[string]Profile, *ShareShader)
	instructions := func(text string) edit {
		return func(w *Workflow, p map[string]Profile, s *ShareShader) {
			q := p["implementer"]
			q.Instructions += " " + text
			p["implementer"] = q
		}
	}
	cases := []struct {
		name, secret, path, label string
		edit                      edit
	}{
		{"aws", "AKIAIOSFODNN7EXAMPLE", "profiles.implementer.instructions", "AWS access key", nil},
		{"slack", "xoxb-1234567890-abcdefghij", "profiles.implementer.instructions", "Slack token", nil},
		{"jwt", "eyJhbGciOiJIUzI1.eyJzdWIiOiIxMjM0.SflKxwRJSMeKKF2Q", "profiles.implementer.instructions", "JSON web token", nil},
		{"private key", "-----BEGIN OPENSSH PRIVATE KEY-----", "profiles.implementer.instructions", "private key", nil},
		{"assignment", "token = Zx8rT2pQ9vLk", "profiles.implementer.instructions", "credential", nil},
		{"entropy", "Q2xhdWRlSXNBQ29vbE1vZGVsQW5kSXRJc05pY2U9X3o", "profiles.implementer.instructions", "random-looking token", nil},
		{"ip", "10.0.0.12", "profiles.implementer.instructions", "IP address", nil},
		{"host", "build01.corp", "profiles.implementer.instructions", "internal host name", nil},
		{"email", "alice@example.com", "profiles.implementer.description", "email address", func(w *Workflow, p map[string]Profile, s *ShareShader) {
			q := p["implementer"]
			q.Description = "Ask alice@example.com"
			p["implementer"] = q
		}},
		{"url userinfo", "alice:hunter22", "description", "user name or password", func(w *Workflow, p map[string]Profile, s *ShareShader) {
			w.Description = "Mirror of https://alice:hunter22@git.example.com/repo"
		}},
		{"home path", "/home/alice", "profiles.implementer.constraints[0]", "home directory", func(w *Workflow, p map[string]Profile, s *ShareShader) {
			q := p["implementer"]
			q.Constraints = []string{"Only touch /home/alice/projects/app"}
			p["implementer"] = q
		}},
		{"windows home", `C:\Users\alice`, "profiles.implementer.instructions", "home directory", instructions(`Build in C:\Users\alice\code`)},
		{"project path", "/srv/projects/acme", "profiles.implementer.instructions", "path on this machine", nil},
		{"control chars", "\x1b]52;c;aGk=\x07", "name", "terminal control", func(w *Workflow, p map[string]Profile, s *ShareShader) {
			w.Name = "Arena \x1b]52;c;aGk=\x07"
		}},
		{"shader comment", "ghp_abcdefghijklmnopqrstuvwxyz0123", "shader.source", "GitHub token", func(w *Workflow, p map[string]Profile, s *ShareShader) {
			s.Source = "// key ghp_abcdefghijklmnopqrstuvwxyz0123\n" + s.Source
		}},
		{"unknown option", "127.0.0.1:8080", "runtime.options.proxy", "does not document", func(w *Workflow, p map[string]Profile, s *ShareShader) {
			w.Agents[0].Runtime.Options = map[string]any{"proxy": "http://127.0.0.1:8080"}
		}},
		{"codex config", "danger-full-access", "options.config.sandbox_mode", "not documented", func(w *Workflow, p map[string]Profile, s *ShareShader) {
			w.Agents[0].Runtime.Options = map[string]any{"config": map[string]any{"sandbox_mode": "danger-full-access"}}
		}},
		{"secret map key", "sk-abcdefghijklmnopqrstu", "<key>", "API key", func(w *Workflow, p map[string]Profile, s *ShareShader) {
			w.Agents[0].Runtime = Runtime{Provider: "local", Model: "m", Options: map[string]any{"backend": "ollama", "generation": map[string]any{"sk-abcdefghijklmnopqrstu": 1}}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, profiles := idealArena()
			s := &ShareShader{shader.DefaultSource, "."}
			if tc.edit == nil {
				instructions("Note: "+tc.secret)(&w, profiles, s)
			} else {
				tc.edit(&w, profiles, s)
			}
			_, err := EncodeShare(ShareBundle{Format: shareFormat, Kind: "arena", Workflow: &w, Profiles: profiles, Shader: s}, "/srv/projects/acme")
			if err == nil {
				t.Fatal("exported a sensitive field")
			}
			if !strings.Contains(err.Error(), tc.path) || !strings.Contains(err.Error(), tc.label) || strings.Contains(err.Error(), tc.secret) {
				t.Fatalf("error %q should name %q as %q without the value", err, tc.path, tc.label)
			}
		})
	}
	// Clean arenas and the shipped default shader still share.
	w, profiles := idealArena()
	w.Agents[0].Runtime.Options = map[string]any{"config": map[string]any{"service_tier": "flex"}}
	if _, err := EncodeShare(ShareBundle{Format: shareFormat, Kind: "arena", Workflow: &w, Profiles: profiles, Shader: &ShareShader{shader.DefaultSource, "."}}, "/srv/projects/acme"); err != nil {
		t.Fatal(err)
	}
	if _, err := (&Project{"../../examples/project"}).ExportArena("code-review"); err != nil {
		t.Fatalf("example arena: %v", err)
	}
}

func TestExportRefusesThisProjectsPath(t *testing.T) {
	p, _ := savedIdeal(t)
	w, profiles, version, err := p.LoadVersion("ideal")
	if err != nil {
		t.Fatal(err)
	}
	q := profiles["implementer"]
	q.Instructions += " Scripts live in " + p.Root + "/scripts."
	profiles["implementer"] = q
	if _, err := p.Save(w, profiles, version); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ExportArena("ideal"); err == nil || !strings.Contains(err.Error(), "path on this machine") {
		t.Fatalf("exported the project path: %v", err)
	}
}

func TestShareSizeAndControlCharacterLimits(t *testing.T) {
	w, profiles := idealArena()
	q := profiles["implementer"]
	q.Instructions = strings.Repeat("Keep the change small and tested. ", 40000) // ~1.3 MiB, compresses well
	profiles["implementer"] = q
	if _, err := EncodeShare(ShareBundle{Format: shareFormat, Kind: "arena", Workflow: &w, Profiles: profiles}); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("exported a code its decoder refuses: %v", err)
	}
	code := packShare(t, "s", JSON(ShareBundle{Format: shareFormat, Kind: "shader", From: "x\x1b]52;c;aGk=\x07", Shader: &ShareShader{shader.DefaultSource, "."}}))
	_, err := DecodeShare(code)
	if err == nil || !strings.Contains(err.Error(), "terminal control") || strings.ContainsRune(err.Error(), 0x1b) {
		t.Fatalf("control characters: %q", err)
	}
	if got := terminalSafe("ok\x1b]52;c;aGk=\x07 \x1b[31mred\x9b"); strings.ContainsAny(got, "\x1b\x07\u009b") {
		t.Fatalf("terminalSafe kept controls: %q", got)
	}
}

func TestImportWaitsForActiveRun(t *testing.T) {
	readyProbe(t)
	p, _ := savedIdeal(t)
	code, _ := p.ExportArena("ideal")
	r, _ := loadWorkflowView(p, "ideal")
	m := LiveModel{Project: p, Run: r, Executing: true, Width: 120, Height: 40, State: RunState{Status: "running", Agents: map[string]string{}}}
	next, cmd := m.Update(tea.PasteMsg{Content: code})
	if m2 := next.(LiveModel); cmd != nil || m2.Draft != nil || !strings.Contains(m2.Error, "Finish or cancel") {
		t.Fatalf("pasted over an active run: %q", m2.Error)
	}
	m.Draft = &WorkflowDraft{Workflow: r.Workflow, Profiles: r.Profiles}
	if next, _ = m.submit("/save"); !strings.Contains(next.(LiveModel).Error, "Finish or cancel") {
		t.Fatalf("saved over an active run: %q", next.(LiveModel).Error)
	}
}

func TestHighEntropyTellsKeysFromIdentifiers(t *testing.T) {
	for _, s := range []string{"TestExportRefusesThisProjectsPath1234567", "MyServiceClientFactory2024Implementation", "ValidateWorkflowAgainstProjectSettings2"} {
		if highEntropy(s) {
			t.Errorf("identifier %q flagged as a key", s)
		}
	}
	for _, s := range []string{"Q2xhdWRlSXNBQ29vbE1vZGVsQW5kSXRJc05pY2U9X3o", "x7Kp2QvR9mZt4WbL8nYc3HdJ6sFg1VaE5uTq0Ni", "AbC9dEf2GhI7jKl4MnO1pQr8StU5vWx3YzA6bCd"} {
		if !highEntropy(s) {
			t.Errorf("key %q not flagged", s)
		}
	}
}
