package arena

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSetupCreatesNewProjectWithExplicitControlRuntime(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "new-project")
	var out bytes.Buffer
	cli := NewCLI(strings.NewReader(""), &out, &out)
	args := []string{"setup", "--project", dir, "--provider", "codex", "--model", "explicit-test-model", "--effort", "high", "--no-bootstrap", "--yes"}
	if err := cli.Run(context.Background(), args); err != nil {
		t.Fatalf("setup: %v\n%s", err, out.String())
	}
	p, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	root, err := Git(p.Root, "rev-parse", "--show-toplevel")
	if err != nil || root != p.Root {
		t.Fatalf("new repository missing: %s %v", root, err)
	}
	s, err := p.Settings()
	if err != nil || s.DefaultModel.Provider != "codex" || s.DefaultModel.Model != "explicit-test-model" || s.DefaultModel.ReasoningEffort != "high" {
		t.Fatalf("explicit control choice changed: %+v %v", s.DefaultModel, err)
	}
	if state := providerSkillState(p.Root, "codex"); !strings.HasPrefix(state, "project: canonical skill present") {
		t.Fatalf("skill not installed: %s", state)
	}
	ids, err := p.List()
	if err != nil || len(ids) != 0 {
		t.Fatalf("setup guessed a workflow: %v %v", ids, err)
	}
	assertNoLaunchRun(t, p)
	before, err := os.ReadFile(p.Path("settings.arena.json"))
	if err != nil {
		t.Fatal(err)
	}
	// Once initialized, bare arena reuses the permanent control runtime.
	if err := cli.Run(context.Background(), []string{"--project", dir, "--no-bootstrap"}); err != nil {
		t.Fatal(err)
	}
	after, err := os.ReadFile(p.Path("settings.arena.json"))
	if err != nil || !bytes.Equal(before, after) {
		t.Fatal("setup rewrote the saved runtime")
	}
	if err := cli.Run(context.Background(), []string{"setup", "--project", dir, "--model", "a-different-model", "--no-bootstrap"}); err == nil {
		t.Fatal("setup silently ignored/replaced a conflicting runtime")
	}
	if _, err := Git(p.Root, "rev-parse", "HEAD"); err == nil {
		t.Fatal("setup committed user files without review")
	}
}

func TestBareArenaInEmptyDirectoryRunsSetupWithoutInventingRuntime(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	var out bytes.Buffer
	cli := NewCLI(strings.NewReader(""), &out, &out)
	if err := cli.Run(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "Provider") {
		t.Fatalf("missing runtime: %v", err)
	}
	if !strings.Contains(out.String(), "Arena setup") {
		t.Fatal("bare arena did not enter setup")
	}
	for _, name := range []string{".git", ".arena"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Fatalf("created %s before required choices", name)
		}
	}
	out.Reset()
	if err := cli.Run(context.Background(), []string{"--provider", "codex", "--model", "explicit-test-model", "--effort", "high", "--no-bootstrap", "--yes"}); err != nil {
		t.Fatalf("bare setup with explicit choices: %v", err)
	}
	if !strings.Contains(out.String(), "Project initialized") {
		t.Fatal("bare arena did not complete setup")
	}
}

func TestSetupCancellationAndExistingUserFiles(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "not-created")
	var out bytes.Buffer
	args := []string{"setup", "--project", dir, "--provider", "codex", "--model", "explicit-test-model", "--effort", "high", "--no-bootstrap"}
	if err := NewCLI(strings.NewReader("no\n"), &out, &out).Run(context.Background(), args); err == nil {
		t.Fatal("ignored setup cancellation")
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("cancelled setup created the project")
	}
	p := committedProject(t)
	file := filepath.Join(p.Root, "user.txt")
	if err := os.WriteFile(file, []byte("user changes\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := Git(p.Root, "add", "user.txt"); err != nil {
		t.Fatal(err)
	}
	before, err := Git(p.Root, "status", "--porcelain")
	if err != nil {
		t.Fatal(err)
	}
	if err := NewCLI(strings.NewReader(""), &out, &out).Run(context.Background(), []string{"setup", "--project", p.Root, "--no-bootstrap"}); err != nil {
		t.Fatal(err)
	}
	after, err := Git(p.Root, "status", "--porcelain")
	if err != nil || before != after {
		t.Fatalf("setup touched user changes: before=%q after=%q %v", before, after, err)
	}
}
