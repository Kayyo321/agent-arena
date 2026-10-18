package arena

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	assets "github.com/Kayyo321/agent-arena"
)

func TestStateDirHonorsXDGAndFallsBackToHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", "")
	got, err := stateDir()
	if err != nil || got != filepath.Join(home, ".local", "state", "arena") {
		t.Fatalf("stateDir() = %q, %v", got, err)
	}
	t.Setenv("XDG_STATE_HOME", "relative/state")
	if got, _ := stateDir(); got != filepath.Join(home, ".local", "state", "arena") {
		t.Fatalf("a relative XDG_STATE_HOME must be ignored, got %q", got)
	}
	xdg := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdg)
	if got, _ := stateDir(); got != filepath.Join(xdg, "arena") {
		t.Fatalf("stateDir() = %q, want under XDG_STATE_HOME", got)
	}
}

func TestUserSkillManifestRecordAndForget(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdg)
	m, err := loadUserSkills()
	if err != nil || len(m.Skills) != 0 {
		t.Fatalf("a missing manifest must load empty: %+v %v", m, err)
	}
	if err := recordUserSkill("/home/u/a/SKILL.md", []byte("one")); err != nil {
		t.Fatal(err)
	}
	if err := recordUserSkill("/home/u/b/SKILL.md", []byte("two")); err != nil {
		t.Fatal(err)
	}
	// Recording the same path again replaces its hash instead of adding a row.
	if err := recordUserSkill("/home/u/a/SKILL.md", []byte("three")); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(xdg, "arena", "user-skills.json")
	info, err := os.Stat(path)
	if err != nil || info.Mode().Perm() != 0o644 {
		t.Fatalf("manifest file: %v %v", info, err)
	}
	m, err = loadUserSkills()
	if err != nil || m.SchemaVersion != 1 || len(m.Skills) != 2 {
		t.Fatalf("manifest after record: %+v %v", m, err)
	}
	if m.Skills[0].Path != "/home/u/a/SKILL.md" || m.Skills[0].SHA256 != Hash([]byte("three")) || m.Skills[1].SHA256 != Hash([]byte("two")) {
		t.Fatalf("manifest rows: %+v", m.Skills)
	}
	raw, _ := os.ReadFile(path)
	for _, want := range []string{`"schemaVersion": 1`, `"skills"`, `"path"`, `"sha256"`} {
		if !strings.Contains(string(raw), want) {
			t.Fatalf("manifest JSON lacks %s:\n%s", want, raw)
		}
	}
	if err := forgetUserSkill("/home/u/a/SKILL.md"); err != nil {
		t.Fatal(err)
	}
	if err := forgetUserSkill("/never/tracked"); err != nil {
		t.Fatal(err)
	}
	m, _ = loadUserSkills()
	if len(m.Skills) != 1 || m.Skills[0].Path != "/home/u/b/SKILL.md" {
		t.Fatalf("manifest after forget: %+v", m.Skills)
	}
}

func TestForgetDoesNotCreateTheManifest(t *testing.T) {
	xdg := t.TempDir()
	t.Setenv("XDG_STATE_HOME", xdg)
	if err := forgetUserSkill("/x/SKILL.md"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(xdg, "arena", "user-skills.json")); !os.IsNotExist(err) {
		t.Fatalf("forget created a manifest: %v", err)
	}
}

func TestUserSkillInstallRecordsAndRemoveForgets(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_STATE_HOME", filepath.Join(home, "state"))
	p := &Project{Root: t.TempDir()}

	// A project install is not tracked.
	if _, err := p.InstallSkill("claude", false, false); err != nil {
		t.Fatal(err)
	}
	if m, _ := loadUserSkills(); len(m.Skills) != 0 {
		t.Fatalf("a project install must not be recorded: %+v", m.Skills)
	}

	path, err := p.InstallSkill("claude", true, false)
	if err != nil {
		t.Fatal(err)
	}
	m, _ := loadUserSkills()
	if len(m.Skills) != 1 || m.Skills[0].Path != path || m.Skills[0].SHA256 != Hash(assets.Skill()) {
		t.Fatalf("user install must record the path and the source hash: %+v (path %s)", m.Skills, path)
	}
	if _, err := p.InstallSkill("claude", true, false); err != nil {
		t.Fatal(err)
	}
	if m, _ := loadUserSkills(); len(m.Skills) != 1 {
		t.Fatalf("reinstalling must not add a second row: %+v", m.Skills)
	}

	cli := NewCLI(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{})
	remove := Options{"agent": {"claude"}, "user": {"true"}, "yes": {"true"}}
	if err := cli.skill(p, []string{"remove"}, remove); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("skill file still present: %v", err)
	}
	if m, _ := loadUserSkills(); len(m.Skills) != 0 {
		t.Fatalf("remove --user must forget the path: %+v", m.Skills)
	}
}
