//go:build darwin || linux

package arena

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// markerProgram writes an executable that records that Git ran it, then passes
// stdin through so it also works as a clean filter.
func markerProgram(t *testing.T, marker string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "hostile")
	if e := os.WriteFile(path, []byte("#!/bin/sh\ntouch '"+marker+"'\ncat 2>/dev/null\nexit 0\n"), 0755); e != nil {
		t.Fatal(e)
	}
	return path
}
func mustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, e := Git(dir, args...); e != nil {
		t.Fatal(e)
	}
}

// replaceGitDir plays a sandboxed worker that swaps the worktree's .git pointer
// for a repository it controls.
func replaceGitDir(t *testing.T, w Workspace) {
	t.Helper()
	if e := os.Remove(filepath.Join(w.Path, ".git")); e != nil {
		t.Fatal(e)
	}
	mustGit(t, w.Path, "init", "-q")
	mustGit(t, w.Path, "add", "-A")
	mustGit(t, w.Path, "-c", "user.name=Worker", "-c", "user.email=worker@localhost", "commit", "-qm", "worker repo")
}

func TestFinishWorkspaceRefusesTamperedGitMetadata(t *testing.T) {
	cases := []struct {
		name   string
		tamper func(t *testing.T, w Workspace, program string)
		// armed proves the fixture would have run program under the old,
		// discovery-based host Git call.
		armed []string
	}{
		{"fsmonitor in replacement .git directory", func(t *testing.T, w Workspace, program string) {
			replaceGitDir(t, w)
			mustGit(t, w.Path, "config", "core.fsmonitor", program)
		}, []string{"status", "--porcelain=v1"}},
		{"hooks and attribute filter in replacement .git directory", func(t *testing.T, w Workspace, program string) {
			replaceGitDir(t, w)
			hooks := filepath.Join(filepath.Dir(program), "hooks")
			if e := os.MkdirAll(hooks, 0755); e != nil {
				t.Fatal(e)
			}
			for _, hook := range []string{"post-index-change", "reference-transaction"} {
				if e := os.Symlink(program, filepath.Join(hooks, hook)); e != nil {
					t.Fatal(e)
				}
			}
			mustGit(t, w.Path, "config", "core.hooksPath", hooks)
			mustGit(t, w.Path, "config", "filter.evil.clean", program)
			if e := os.WriteFile(filepath.Join(w.Path, ".gitattributes"), []byte("* filter=evil\n"), 0644); e != nil {
				t.Fatal(e)
			}
			if e := os.WriteFile(filepath.Join(w.Path, "base.txt"), []byte("worker\n"), 0644); e != nil {
				t.Fatal(e)
			}
		}, []string{"add", "-A"}},
		{"pointer redirected to worker gitdir", func(t *testing.T, w Workspace, program string) {
			evil := filepath.Join(w.Path, ".evil")
			mustGit(t, w.Path, "--git-dir="+evil, "--work-tree="+w.Path, "init", "-q")
			mustGit(t, w.Path, "--git-dir="+evil, "--work-tree="+w.Path, "add", "-A")
			mustGit(t, w.Path, "--git-dir="+evil, "--work-tree="+w.Path, "-c", "user.name=Worker", "-c", "user.email=worker@localhost", "commit", "-qm", "worker repo")
			mustGit(t, w.Path, "--git-dir="+evil, "config", "core.fsmonitor", program)
			if e := os.WriteFile(filepath.Join(w.Path, ".git"), []byte("gitdir: .evil\n"), 0644); e != nil {
				t.Fatal(e)
			}
		}, []string{"status", "--porcelain=v1"}},
		{"pointer replaced by symlink", func(t *testing.T, w Workspace, program string) {
			dotGit := filepath.Join(w.Path, ".git")
			if e := os.Remove(dotGit); e != nil {
				t.Fatal(e)
			}
			if e := os.Symlink(w.GitDir, dotGit); e != nil {
				t.Fatal(e)
			}
		}, nil},
		{"pointer deleted so Git would discover the user checkout", func(t *testing.T, w Workspace, program string) {
			if e := os.Remove(filepath.Join(w.Path, ".git")); e != nil {
				t.Fatal(e)
			}
		}, nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := committedProject(t)
			r := testRun(t, p)
			w, e := p.Workspace(r, "writer", "writer", true)
			if e != nil {
				t.Fatal(e)
			}
			if w.GitDir == "" {
				t.Fatal("workspace did not record its gitdir")
			}
			if e := os.WriteFile(filepath.Join(w.Path, "proposal.txt"), []byte("change\n"), 0644); e != nil {
				t.Fatal(e)
			}
			marker := filepath.Join(t.TempDir(), "host-code-ran")
			program := markerProgram(t, marker)
			tc.tamper(t, w, program)
			if _, e := os.Stat(marker); !os.IsNotExist(e) {
				t.Fatal("fixture ran the hostile program during setup")
			}

			event, e := p.FinishWorkspace(r, w)
			var integrity *workspaceIntegrityError
			if !errors.As(e, &integrity) || event != nil {
				t.Fatalf("tampered worktree was not refused: %v %v", event, e)
			}
			if _, e := p.Checkpoint(w.Path, "retry"); !errors.As(e, &integrity) {
				t.Fatalf("retry checkpoint accepted tampered worktree: %v", e)
			}
			if _, e := p.FinishWorkspace(r, w); e == nil {
				t.Fatal("rejected worktree was finished on a second attempt")
			}
			if _, e := p.Workspace(r, "writer", "writer", true); e == nil {
				t.Fatal("rejected worktree was reused")
			}
			if _, e := os.Stat(marker); !os.IsNotExist(e) {
				t.Fatal("host Git ran a worker-controlled program")
			}
			if _, e := os.Stat(filepath.Join(w.Path, "proposal.txt")); e != nil {
				t.Fatal("rejected worktree was not preserved")
			}
			workspaces, e := p.Workspaces(r.ID)
			if e != nil || len(workspaces) != 1 || workspaces[0].State != "rejected" {
				t.Fatalf("workspace not marked rejected: %+v %v", workspaces, e)
			}
			events, e := p.Events(r.ID)
			if e != nil {
				t.Fatal(e)
			}
			rejected := 0
			for _, event := range events {
				if event.Type == "workspace.rejected" {
					rejected++
				}
				if event.Type == "change.proposed" {
					t.Fatal("tampered worktree produced a proposal")
				}
			}
			if rejected != 1 {
				t.Fatalf("expected one workspace.rejected event, got %d", rejected)
			}

			if tc.armed != nil {
				// The legacy call that trusted the worktree's .git runs the program.
				_, _ = Git(w.Path, tc.armed...)
				if _, e := os.Stat(marker); e != nil {
					t.Fatal("fixture is not armed; the test would not detect the vulnerability")
				}
			}
		})
	}
}

// Executable settings from any config scope stay inert for worker worktrees,
// even with an intact gitdir pointer.
func TestWorkerGitIgnoresExecutableConfig(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	w, e := p.Workspace(r, "writer", "writer", true)
	if e != nil {
		t.Fatal(e)
	}
	marker := filepath.Join(t.TempDir(), "host-code-ran")
	program := markerProgram(t, marker)
	hooks := filepath.Join(filepath.Dir(program), "hooks")
	if e := os.MkdirAll(hooks, 0755); e != nil {
		t.Fatal(e)
	}
	for _, hook := range []string{"post-index-change", "reference-transaction"} {
		if e := os.Symlink(program, filepath.Join(hooks, hook)); e != nil {
			t.Fatal(e)
		}
	}
	for _, setting := range [][2]string{{"core.fsmonitor", program}, {"core.hooksPath", hooks}, {"diff.external", program}, {"diff.evil.textconv", program}} {
		mustGit(t, p.Root, "config", setting[0], setting[1])
	}
	if e := os.WriteFile(filepath.Join(w.Path, ".gitattributes"), []byte("* diff=evil\n"), 0644); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(w.Path, "base.txt"), []byte("worker\n"), 0644); e != nil {
		t.Fatal(e)
	}
	event, e := p.FinishWorkspace(r, w)
	if e != nil || event == nil {
		t.Fatalf("intact worktree was not proposed: %v %v", event, e)
	}
	if _, e := os.Stat(marker); !os.IsNotExist(e) {
		t.Fatal("worker Git call ran configured program")
	}
	patch, e := os.ReadFile(filepath.Join(p.RunDir(r.ID), "changes", str(event.Data["changeId"])+".patch"))
	if e != nil || !strings.Contains(string(patch), "-baseline\n+worker\n") {
		t.Fatalf("patch was not the raw diff: %s %v", patch, e)
	}
	_, _ = Git(w.Path, "status")
	if _, e := os.Stat(marker); e != nil {
		t.Fatal("fixture is not armed; the test would not detect the vulnerability")
	}
}
