package arena

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

var hostile = []string{"$(touch pwned)", "; rm -rf x", "it's \"quoted\"", "line one\nline two", "`id`", "--help", "-x"}

// fakeTmux installs a tmux that appends each invocation's argv to a file, one
// argument per line, invocations separated by a line holding "--END--".
func fakeTmux(t *testing.T) (calls func() [][]string) {
	t.Helper()
	bin, rec := t.TempDir(), filepath.Join(t.TempDir(), "calls")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done >> " + rec + "\nprintf -- '--END--\\n' >> " + rec + "\n"
	if err := os.WriteFile(filepath.Join(bin, "tmux"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return func() [][]string {
		b, _ := os.ReadFile(rec)
		out := [][]string{}
		for _, chunk := range strings.Split(string(b), "--END--\n") {
			if chunk != "" {
				out = append(out, strings.Split(strings.TrimSuffix(chunk, "\n"), "\n"))
			}
		}
		return out
	}
}

func TestTmuxLayoutIsExactAndSupervisorIsPaneZero(t *testing.T) {
	got := tmuxCommands("/bin/arena", "/proj", "arena-run-1", []string{"run", "--run", "run-1", "--", "wf", "task"}, "run-1", []string{"head", "rev"})
	want := [][]string{
		{"new-session", "-d", "-s", "arena-run-1", "-n", "run", "-c", "/proj", "--", "/bin/arena", "--project", "/proj", "run", "--run", "run-1", "--", "wf", "task"},
		{"set-option", "-t", "arena-run-1", "remain-on-exit", "on"},
		{"split-window", "-h", "-t", "arena-run-1:0", "-c", "/proj", "--", "/bin/arena", "watch", "run-1", "--json", "--follow", "--project", "/proj"},
		{"new-window", "-t", "arena-run-1:", "-n", "agents", "-c", "/proj", "--", "/bin/arena", "view", "--run", "run-1", "--follow", "--project", "/proj", "--", "head"},
		{"select-layout", "-t", "arena-run-1:1", "tiled"},
		{"split-window", "-t", "arena-run-1:1", "-c", "/proj", "--", "/bin/arena", "view", "--run", "run-1", "--follow", "--project", "/proj", "--", "rev"},
		{"select-layout", "-t", "arena-run-1:1", "tiled"},
		{"select-window", "-t", "arena-run-1:0"},
		{"select-pane", "-t", "arena-run-1:0.0"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("layout:\n%q\nwant\n%q", got, want)
	}
}

func TestTmuxHostileValuesStayWholeArgvElements(t *testing.T) {
	for _, h := range hostile {
		cmds := tmuxCommands("/bin/arena", "/p", "arena-r", []string{"run", "--run", "r", "--", h, h}, "r", []string{h})
		var seen int
		for _, argv := range cmds {
			for i, a := range argv {
				if strings.Contains(a, h) && a != h {
					t.Fatalf("%q was merged into another argument: %q", h, a)
				}
				if a == h {
					seen++
					// Everything user-controlled sits after a "--".
					if !contains(argv[:i], "--") {
						t.Fatalf("%q precedes the option terminator in %q", h, argv)
					}
				}
			}
		}
		if seen != 3 {
			t.Fatalf("%q appeared %d times, want 3", h, seen)
		}
	}
}

func contains(a []string, s string) bool {
	for _, v := range a {
		if v == s {
			return true
		}
	}
	return false
}

func TestTmuxResumeRunsLayoutThroughFakeTmux(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	calls := fakeTmux(t)
	t.Setenv("TMUX", "")
	var out bytes.Buffer
	if err := NewCLI(strings.NewReader(""), &out, &out).Run(context.Background(), []string{"tmux", "resume", r.ID, "--retry-interrupted", "--project", p.Root}); err != nil {
		t.Fatal(err)
	}
	got := calls()
	exe, _ := os.Executable()
	first := got[0]
	if first[0] != "new-session" {
		t.Fatalf("first call: %q", first)
	}
	wantTail := []string{"--", exe, "--project", p.Root, "resume", r.ID, "--retry-interrupted"}
	if !reflect.DeepEqual(first[len(first)-len(wantTail):], wantTail) {
		t.Fatalf("supervisor command: %q", first)
	}
	last := got[len(got)-1]
	if !reflect.DeepEqual(last, []string{"attach-session", "-t", "arena-" + r.ID}) {
		t.Fatalf("outside tmux must attach: %q", last)
	}
	if !strings.Contains(out.String(), "pane 0 supervises") {
		t.Fatalf("output: %s", out.String())
	}
}

func TestTmuxInsideTmuxSwitchesClientInsteadOfNesting(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	calls := fakeTmux(t)
	t.Setenv("TMUX", "/tmp/tmux-1000/default,1,0")
	if err := NewCLI(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}).Run(context.Background(), []string{"tmux", "resume", r.ID, "--project", p.Root}); err != nil {
		t.Fatal(err)
	}
	got := calls()
	if last := got[len(got)-1]; !reflect.DeepEqual(last, []string{"switch-client", "-t", "arena-" + r.ID}) {
		t.Fatalf("inside tmux must switch: %q", last)
	}
	for _, c := range got {
		if c[0] == "attach-session" {
			t.Fatal("nested attach")
		}
	}
}

func TestTmuxMissingIsClearAndNeverInstalls(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	t.Setenv("PATH", t.TempDir())
	err := NewCLI(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}).Run(context.Background(), []string{"tmux", "resume", r.ID, "--project", p.Root})
	if err == nil || !strings.Contains(err.Error(), "tmux is not installed") || !strings.Contains(err.Error(), "never installs") {
		t.Fatalf("error: %v", err)
	}
}

func TestTmuxUsage(t *testing.T) {
	p := committedProject(t)
	for _, args := range [][]string{{"tmux"}, {"tmux", "run", "wf"}, {"tmux", "bogus", "x"}, {"tmux", "resume", "a", "b"}} {
		if err := NewCLI(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}).Run(context.Background(), append(args, "--project", p.Root)); err == nil || !strings.Contains(err.Error(), "tmux run WORKFLOW TASK") {
			t.Fatalf("%v: %v", args, err)
		}
	}
}

func TestRunAdoptsOnlyAnUnstartedReservedRun(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	if _, _, err := adoptReservedRun(context.Background(), p, r.ID, "other", "task"); err == nil || !strings.Contains(err.Error(), "belongs to workflow") {
		t.Fatalf("wrong workflow: %v", err)
	}
	if _, _, err := adoptReservedRun(context.Background(), p, r.ID, r.Workflow.Id, " "); err == nil {
		t.Fatal("blank task accepted")
	}
	appendEvents(t, p, r, "", "workflow.started")
	if _, _, err := adoptReservedRun(context.Background(), p, r.ID, r.Workflow.Id, "task"); err == nil || !strings.Contains(err.Error(), "unstarted") {
		t.Fatalf("started run adopted: %v", err)
	}
}

// Real tmux: hostile text reaches the pane command as one argument, and no
// shell interprets it. Skipped when tmux is absent.
func TestTmuxSmokeWithRealTmux(t *testing.T) {
	bin, err := exec.LookPath("tmux")
	if err != nil {
		t.Skip("tmux is not installed")
	}
	dir := t.TempDir()
	record := filepath.Join(dir, "argv")
	fake := filepath.Join(dir, "arena")
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\0' \"$a\"; done > " + record + "\nsleep 30\n"
	if err := os.WriteFile(fake, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	session := "arena-smoke-" + ID("")
	t.Cleanup(func() { _ = exec.Command(bin, "kill-session", "-t", session).Run() })
	task := "$(touch " + dir + "/pwned); echo 'x'\nsecond \"line\""
	cmds := tmuxCommands(fake, dir, session, []string{"run", "--run", "r", "--", "wf", task}, "r", []string{"head", "$(touch " + dir + "/pwned)"})
	for _, argv := range cmds {
		if out, err := exec.Command(bin, argv...).CombinedOutput(); err != nil {
			t.Fatalf("tmux %q: %v: %s", argv, err, out)
		}
	}
	var b []byte
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		if b, _ = os.ReadFile(record); len(b) > 0 {
			break
		}
	}
	got := strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00")
	want := []string{"--project", dir, "run", "--run", "r", "--", "wf", task}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("pane argv %q, want %q", got, want)
	}
	if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
		t.Fatal("a shell interpreted the task")
	}
	if out, err := exec.Command(bin, "list-windows", "-t", session, "-F", "#{window_index}").Output(); err != nil || strings.TrimSpace(string(out)) != "0\n1" {
		t.Fatalf("windows %q: %v", out, err)
	}
}
