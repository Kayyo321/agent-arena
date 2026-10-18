package arena

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func testProject(t *testing.T) *Project {
	t.Helper()
	return testProjectIn(t, t.TempDir())
}

// symlinkedTempDir is a new empty directory whose path runs through a symlink,
// as macOS reaches t.TempDir() (/var is a symlink to /private/var). The path a
// test holds is then not the real path that Git, filepath.EvalSymlinks and Open
// report, which is how the macOS-only failures appear on other systems.
func symlinkedTempDir(t *testing.T) string {
	t.Helper()
	real, link := t.TempDir(), filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("cannot create a symlink: %v", err)
	}
	dir := filepath.Join(link, "project")
	if err := os.Mkdir(dir, 0755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func testProjectIn(t *testing.T, dir string) *Project {
	t.Helper()
	cmd := exec.Command("git", "init", "-q", dir)
	if b, e := cmd.CombinedOutput(); e != nil {
		t.Fatalf("git init: %v %s", e, b)
	}
	p := &Project{dir}
	s := DefaultSettings(Runtime{Provider: "codex", Model: "explicit-test-model", ReasoningEffort: "high"})
	if _, e := p.Commit("test init", map[string][]byte{"settings.arena.json": JSON(s), s.PermanentOnCallAgent.Profile: JSON(NewProfile("arena-orchestrator", "arena-control", "Manage workflows", "Use Arena CLI and its installed skill", []string{"read", "run_commands"}))}); e != nil {
		t.Fatal(e)
	}
	return p
}
func TestAuthoringLifecycleDoesNotNeedHandWrittenConfig(t *testing.T) {
	p := testProject(t)
	var out bytes.Buffer
	cli := NewCLI(strings.NewReader(""), &out, &out)
	run := func(args ...string) {
		t.Helper()
		if e := cli.Run(context.Background(), append(args, "--project", p.Root)); e != nil {
			t.Fatalf("%v: %v\n%s", args, e, out.String())
		}
	}
	run("workflow", "create", "test", "--head", "head", "--provider", "codex", "--model", "explicit-test-model", "--effort", "high", "--yes")
	run("workflow", "validate", "test")
	run("workflow", "show", "test")
	run("workflow", "delete", "test", "--yes")
	if _, e := os.Stat(p.Path("test.arena.json")); !os.IsNotExist(e) {
		t.Fatalf("workflow still exists: %v", e)
	}
	files, e := filepath.Glob(p.Path(".state", "transactions", "*", "manifest.json"))
	if e != nil || len(files) < 3 {
		t.Fatal("missing recovery snapshots")
	}
}
func TestLockExclusionAndOwnership(t *testing.T) {
	p := testProject(t)
	l, e := p.Lock("config")
	if e != nil {
		t.Fatal(e)
	}
	if _, e := p.Lock("config"); e == nil {
		t.Fatal("concurrent lock admitted")
	}
	if e := p.RecoverLock("config"); e == nil {
		t.Fatal("recovered live owner lock")
	}
	if e := l.Close(); e != nil {
		t.Fatal(e)
	}
	l, e = p.Lock("config")
	if e != nil {
		t.Fatal(e)
	}
	if e := l.Close(); e != nil {
		t.Fatal(e)
	}
}
func TestSettingsAndProfilePathsRejectSymlinks(t *testing.T) {
	p := testProject(t)
	outside := t.TempDir()
	if e := os.Symlink(outside, p.Path(".agents", "test")); e != nil {
		t.Fatal(e)
	}
	if _, e := SafePath(p.Path(), ".agents/test/profile.json"); e == nil {
		t.Fatal("accepted symlink escaping profile root")
	}
}

func TestConcurrentProfileEditIsNotOverwritten(t *testing.T) {
	p := testProject(t)
	w := NewWorkflow("profile-edit", "Profile edit")
	w.HeadAgent = "head"
	w.Agents = []Agent{{Id: "head", Profile: ".agents/profile-edit/head.agent.json", Level: Int(0), Runtime: Runtime{Provider: "codex", Model: "explicit-test-model", ReasoningEffort: "high"}}}
	profiles := map[string]Profile{"head": NewProfile("head", "research", "Original objective", "Original instructions", []string{"read"})}
	if _, e := p.Save(w, profiles, ""); e != nil {
		t.Fatal(e)
	}
	one, oneProfiles, version, e := p.LoadVersion(w.Id)
	if e != nil {
		t.Fatal(e)
	}
	two, twoProfiles, otherVersion, e := p.LoadVersion(w.Id)
	if e != nil {
		t.Fatal(e)
	}
	profile := oneProfiles["head"]
	profile.Instructions = "First edit"
	oneProfiles["head"] = profile
	if _, e := p.Save(one, oneProfiles, version); e != nil {
		t.Fatal(e)
	}
	profile = twoProfiles["head"]
	profile.Instructions = "Second stale edit"
	twoProfiles["head"] = profile
	if _, e := p.Save(two, twoProfiles, otherVersion); e == nil {
		t.Fatal("stale profile edit overwrote accepted change")
	}
	_, actual, e := p.Load(w.Id)
	if e != nil || actual["head"].Instructions != "First edit" {
		t.Fatal("accepted profile edit lost")
	}
}

func TestValidateWarnsAboutCapabilitiesTheAdapterCannotExercise(t *testing.T) {
	p := testProject(t)
	var out bytes.Buffer
	cli := NewCLI(strings.NewReader(""), &out, &out)
	for _, args := range [][]string{
		{"workflow", "create", "agy", "--head", "head", "--provider", "antigravity", "--model", "gemini-test", "--capabilities", "read,run_commands", "--yes"},
		{"workflow", "validate", "agy"},
	} {
		if e := cli.Run(context.Background(), append(args, "--project", p.Root)); e != nil {
			t.Fatalf("%v: %v\n%s", args, e, out.String())
		}
	}
	if n := strings.Count(out.String(), "warning: head has run_commands, but antigravity headless runs cannot execute commands"); n != 2 {
		t.Fatalf("create and validate should each warn once, got %d:\n%s", n, out.String())
	}
}
