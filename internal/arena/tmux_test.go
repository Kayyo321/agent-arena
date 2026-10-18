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
	return fakeTmuxVersion(t, "tmux 3.4")
}

func fakeTmuxVersion(t *testing.T, version string) (calls func() [][]string) {
	t.Helper()
	bin, rec := t.TempDir(), filepath.Join(t.TempDir(), "calls")
	script := "#!/bin/sh\nif [ \"$1\" = -V ]; then printf '%s\\n' '" + version + "'; exit 0; fi\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done >> " + rec + "\nprintf -- '--END--\\n' >> " + rec + "\nif [ -n \"$FAKE_TMUX_FAIL\" ] && [ \"$1\" = \"$FAKE_TMUX_FAIL\" ]; then exit 1; fi\n"
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

func indexOf(a []string, s string) int {
	for i, v := range a {
		if v == s {
			return i
		}
	}
	return -1
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
	// The command resolves --project to the project's real path (Open follows
	// symlinks), and on macOS t.TempDir() is reached through one (/var is
	// /private/var), so the panes are told the resolved root, not p.Root. The
	// executable is pinned to its resolved path (pinnableExecutable) the same
	// way, so a symlinked test binary location is expected resolved too.
	root := realPath(t, p.Root)
	exe = pinnableExecutable(exe)
	wantTail := []string{"--", exe, "--project", root, "resume", r.ID, "--retry-interrupted"}
	if !reflect.DeepEqual(first[len(first)-len(wantTail):], wantTail) {
		t.Fatalf("supervisor command: %q", first)
	}
	i := indexOf(first, "-c")
	if i < 0 || i+1 >= len(first) {
		t.Fatalf("new-session must pass -c followed by the project's real path %s: %q", root, first)
	}
	if first[i+1] != root {
		t.Fatalf("new-session must start in the project's real path %s: %q", root, first)
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

func TestTmuxObserverLayoutFailureNeverKillsTheLiveRun(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	calls := fakeTmux(t)
	t.Setenv("TMUX", "")
	t.Setenv("FAKE_TMUX_FAIL", "split-window")
	var out bytes.Buffer
	if err := NewCLI(strings.NewReader(""), &out, &out).Run(context.Background(), []string{"tmux", "resume", r.ID, "--project", p.Root}); err != nil {
		t.Fatalf("a failed observer pane must not fail the command: %v", err)
	}
	for _, c := range calls() {
		if c[0] == "kill-session" {
			t.Fatalf("the supervisor was already running; the session must survive: %q", c)
		}
	}
	if !strings.Contains(out.String(), "Warning: optional tmux step skipped, the run is unaffected: tmux split-window failed") {
		t.Fatalf("the skipped step must be reported: %s", out.String())
	}
}

func TestTmuxNewSessionFailureIsAnError(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	fakeTmux(t)
	t.Setenv("FAKE_TMUX_FAIL", "new-session")
	err := NewCLI(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}).Run(context.Background(), []string{"tmux", "resume", r.ID, "--project", p.Root})
	if err == nil || !strings.Contains(err.Error(), "tmux new-session failed") {
		t.Fatalf("error: %v", err)
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
	// Every pane runs this script, so each one records to its own file (by PID);
	// the test then looks for the supervisor's argv among them.
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\0' \"$a\"; done > " + record + ".$$\nsleep 30\n"
	if err := os.WriteFile(fake, []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	session := "arena-smoke-" + ID("")
	t.Cleanup(func() { _ = exec.Command(bin, "kill-session", "-t", session).Run() })
	task := "$(touch " + dir + "/pwned); echo 'x'\nsecond \"line\""
	hostile := "$(touch " + dir + "/pwned)"
	cmds := tmuxCommands(fake, dir, session, []string{"run", "--run", "r", "--", "wf", task}, "r", []string{"head", hostile})
	for _, argv := range cmds {
		if out, err := exec.Command(bin, argv...).CombinedOutput(); err != nil {
			t.Fatalf("tmux %q: %v: %s", argv, err, out)
		}
	}
	want := []string{"--project", dir, "run", "--run", "r", "--", "wf", task}
	// The layout is four panes: the supervisor, the watch pane and two view
	// panes. Wait for all four to record, so the pane that gets the hostile
	// agent name has started before anything is checked. A file that is still
	// being written reads as short, so the supervisor's and the hostile pane's
	// argv must also be complete before the wait ends.
	const panes = 4
	endsWith := func(argvs [][]string, last string) bool {
		for _, argv := range argvs {
			if len(argv) > 0 && argv[len(argv)-1] == last {
				return true
			}
		}
		return false
	}
	var argvs [][]string
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline); time.Sleep(50 * time.Millisecond) {
		argvs = recordedArgvs(record)
		if len(argvs) >= panes && containsArgv(argvs, want) && endsWith(argvs, hostile) {
			break
		}
	}
	if len(argvs) < panes {
		t.Fatalf("%d panes recorded their argv, want %d: %q", len(argvs), panes, argvs)
	}
	if !containsArgv(argvs, want) {
		t.Fatalf("no pane got the supervisor argv %q; panes got %q", want, argvs)
	}
	if !endsWith(argvs, hostile) {
		t.Fatalf("no pane got the agent name %q as its last argument; panes got %q", hostile, argvs)
	}
	if _, err := os.Stat(filepath.Join(dir, "pwned")); err == nil {
		t.Fatal("a shell interpreted the task")
	}
	if out, err := exec.Command(bin, "list-windows", "-t", session, "-F", "#{window_index}").Output(); err != nil || strings.TrimSpace(string(out)) != "0\n1" {
		t.Fatalf("windows %q: %v", out, err)
	}
}

// recordedArgvs reads the argv each fake pane wrote next to record.
func recordedArgvs(record string) [][]string {
	paths, _ := filepath.Glob(record + ".*")
	argvs := [][]string{}
	for _, path := range paths {
		if b, err := os.ReadFile(path); err == nil && len(b) > 0 {
			argvs = append(argvs, strings.Split(strings.TrimSuffix(string(b), "\x00"), "\x00"))
		}
	}
	return argvs
}

func containsArgv(argvs [][]string, want []string) bool {
	for _, argv := range argvs {
		if reflect.DeepEqual(argv, want) {
			return true
		}
	}
	return false
}

func TestTmuxVersionGate(t *testing.T) {
	for version, want := range map[string]struct{ ok, parsed bool }{
		"tmux 1.9a": {false, true}, "tmux 1.8": {false, true}, "tmux 2.0": {true, true}, "tmux 3.3a": {true, true}, "tmux 3.5": {true, true},
		"tmux next-3.5": {false, false}, "tmux master": {false, false}, "": {false, false}, "screen 4.9": {false, false},
	} {
		if ok, parsed := tmuxVersionSupported(version); ok != want.ok || parsed != want.parsed {
			t.Errorf("%q = %v,%v want %v,%v", version, ok, parsed, want.ok, want.parsed)
		}
	}
}

func TestTmuxRefusesUnsupportedVersionBeforeCreatingAnything(t *testing.T) {
	for _, version := range []string{"tmux 1.9a", "tmux next-3.5", "tmux master"} {
		p := committedProject(t)
		r := testRun(t, p)
		calls := fakeTmuxVersion(t, version)
		err := NewCLI(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}).Run(context.Background(), []string{"tmux", "resume", r.ID, "--project", p.Root})
		if err == nil || !strings.Contains(err.Error(), "2.0 or newer") {
			t.Fatalf("%s: %v", version, err)
		}
		if got := calls(); len(got) != 0 {
			t.Fatalf("%s: tmux was driven after refusal: %q", version, got)
		}
	}
	// A refused `tmux run` must not reserve a run either.
	p := committedProject(t)
	fakeTmuxVersion(t, "tmux 1.8")
	if err := NewCLI(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}).Run(context.Background(), []string{"tmux", "run", "wf", "task", "--project", p.Root}); err == nil || !strings.Contains(err.Error(), "too old") {
		t.Fatalf("run: %v", err)
	}
	if runs, _ := filepath.Glob(p.Path(".state", "run-*")); len(runs) != 0 {
		t.Fatalf("a run was created: %v", runs)
	}
}

// What the run depends on is explicit, not inferred from position: the session
// whose pane 0 supervises the run and the option that keeps its output on
// screen are required; everything that only places observers or focus is not.
func TestTmuxLayoutMarksExactlyWhatTheRunDependsOn(t *testing.T) {
	required := []string{}
	for _, step := range tmuxLayout("/bin/arena", "/proj", "arena-run-1", []string{"run", "--run", "run-1", "--", "wf", "task"}, "run-1", []string{"head", "rev"}) {
		if step.required {
			required = append(required, step.argv[0])
		}
	}
	if strings.Join(required, ",") != "new-session,set-option" {
		t.Fatalf("required steps: %v", required)
	}
}

// A required step that fails after new-session is an error that says the run is
// already live. Nothing is killed (that would interrupt the run), no observer
// layout is attempted, and the user is told how to reach or stop the session.
func TestTmuxRequiredStepFailingAfterNewSessionIsAnErrorThatLeavesTheRunAlone(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	calls := fakeTmux(t)
	t.Setenv("TMUX", "")
	t.Setenv("FAKE_TMUX_FAIL", "set-option")
	err := NewCLI(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}).Run(context.Background(), []string{"tmux", "resume", r.ID, "--project", p.Root})
	session := "arena-" + r.ID
	for _, want := range []string{"required tmux setup step failed", "tmux set-option failed", "already live in tmux session " + session, "tmux attach -t " + session, "tmux kill-session -t " + session, "arena resume " + r.ID} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("error %v lacks %q", err, want)
		}
	}
	for _, c := range calls() {
		switch c[0] {
		case "kill-session", "split-window", "new-window", "attach-session", "switch-client":
			t.Fatalf("after a required step failed, tmux was asked for %q", c)
		}
	}
}

// Each optional step failing, alone, is a warning on stderr; the run, the rest
// of the layout and the attach go on.
func TestTmuxEachOptionalStepFailingIsOnlyAWarning(t *testing.T) {
	for _, failing := range []string{"split-window", "new-window", "select-layout", "select-window", "select-pane"} {
		t.Run(failing, func(t *testing.T) {
			p := committedProject(t)
			r := testRun(t, p)
			calls := fakeTmux(t)
			t.Setenv("TMUX", "")
			t.Setenv("FAKE_TMUX_FAIL", failing)
			var stdout, stderr bytes.Buffer
			if err := NewCLI(strings.NewReader(""), &stdout, &stderr).Run(context.Background(), []string{"tmux", "resume", r.ID, "--project", p.Root}); err != nil {
				t.Fatalf("a failed optional step must not fail the command: %v", err)
			}
			if !strings.Contains(stderr.String(), "Warning: optional tmux step skipped, the run is unaffected: tmux "+failing+" failed") || strings.Contains(stdout.String(), "Warning") {
				t.Fatalf("warning belongs on stderr.\nstdout: %s\nstderr: %s", stdout.String(), stderr.String())
			}
			sawLast, attached := false, false
			for _, c := range calls() {
				sawLast = sawLast || c[0] == "select-pane"
				attached = attached || c[0] == "attach-session"
				if c[0] == "kill-session" {
					t.Fatal("the session was killed")
				}
			}
			if !sawLast || !attached {
				t.Fatalf("the layout stopped at the failed step (reached the last step: %v, attached: %v)", sawLast, attached)
			}
		})
	}
}
