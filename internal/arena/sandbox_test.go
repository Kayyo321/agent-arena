//go:build darwin || linux

package arena

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestMediatedCommandsUsePrivateScratchAndCannotWriteOtherTrees(t *testing.T) {
	if !sandboxAvailable() {
		t.Skip("OS command sandbox is not operational on this host")
	}
	assigned, outside := t.TempDir(), t.TempDir()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	run := func(write bool, script string, args ...string) error {
		cmd, cleanup, err := SandboxedCommand(assigned, write, "sh", append([]string{"-c", script, "arena-sandbox-test"}, args...))
		if err != nil {
			return err
		}
		defer cleanup()
		process, err := StartProcess(ctx, cmd)
		if err != nil {
			return err
		}
		return process.Wait()
	}
	if err := run(false, `printf scratch > "$TMPDIR/test"; test -f "$TMPDIR/test"; test -n "$HOME"; test -z "$ANTHROPIC_API_KEY"`); err != nil {
		t.Fatal(err)
	}
	if err := run(true, `printf allowed > "$1"`, filepath.Join(assigned, "allowed.txt")); err != nil {
		t.Fatal(err)
	}
	if err := run(false, `printf denied > "$1"`, filepath.Join(assigned, "denied.txt")); err == nil {
		t.Fatal("read-only command wrote to assigned tree")
	}
	if err := run(true, `printf denied > "$1"`, filepath.Join(outside, "denied.txt")); err == nil {
		t.Fatal("command wrote to another tree")
	}
	if _, err := os.Stat(filepath.Join(outside, "denied.txt")); !os.IsNotExist(err) {
		t.Fatal("outside write was not prevented")
	}
}

func runSandboxed(t *testing.T, timeout time.Duration, dir string, write bool, executable string, args ...string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	cmd, cleanup, err := SandboxedCommand(dir, write, executable, args)
	if err != nil {
		return "", err
	}
	defer cleanup()
	var output bytes.Buffer
	cmd.Stdout, cmd.Stderr = &output, &output
	process, err := StartProcess(ctx, cmd)
	if err != nil {
		return "", err
	}
	err = process.Wait()
	return output.String(), err
}

func TestMediatedCommandsCannotReadHomeOrOtherWorktrees(t *testing.T) {
	if !sandboxAvailable() {
		t.Skip("OS command sandbox is not operational on this host")
	}
	// Keep the fake host tree outside the system temporary directory, which the
	// sandbox replaces wholesale, so the read allowlist itself is exercised.
	base, err := os.MkdirTemp(".", ".sandbox-read-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(base) })
	if base, err = filepath.Abs(base); err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(base, "home")
	state := filepath.Join(base, "project", ".arena", ".state", "run-1")
	assigned, neighbor := filepath.Join(state, "worker-a"), filepath.Join(state, "worker-b")
	planted := map[string]string{
		filepath.Join(home, ".aws", "credentials"):     "aws-secret",
		filepath.Join(home, ".ssh", "id_ed25519"):      "ssh-secret",
		filepath.Join(home, "bin", "home-tool"):        "home-path-secret",
		filepath.Join(neighbor, "notes.txt"):           "neighbor-secret",
		filepath.Join(base, "project", "settings.txt"): "project-secret",
		filepath.Join(assigned, "visible.txt"):         "visible",
	}
	for path, content := range planted {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0600); err != nil {
			t.Fatal(err)
		}
	}
	tools := t.TempDir()
	if err := os.WriteFile(filepath.Join(tools, "arena-hello"), []byte("#!/bin/sh\necho hello-from-tool\n"), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("PATH", filepath.Join(home, "bin")+string(filepath.ListSeparator)+os.Getenv("PATH"))
	for path, content := range planted {
		if content == "visible" {
			continue
		}
		if out, err := runSandboxed(t, 10*time.Second, assigned, true, "cat", path); err == nil || strings.Contains(out, content) {
			t.Fatalf("sandbox read %s: %q", path, out)
		}
	}
	// Linux hides the run state behind an empty tmpfs; macOS denies listing it.
	if out, err := runSandboxed(t, 10*time.Second, assigned, false, "ls", "-a", filepath.Dir(state), filepath.Dir(assigned)); err != nil && !strings.Contains(out, "Operation not permitted") || strings.Contains(out, "worker-b") || strings.Contains(out, "settings.txt") {
		t.Fatalf("sandbox listed neighboring state: %v %q", err, out)
	}
	if out, err := runSandboxed(t, 10*time.Second, assigned, false, "ls"); err != nil || !strings.Contains(out, "visible.txt") {
		t.Fatalf("assigned worktree is not listable: %v %q", err, out)
	}
	if out, err := runSandboxed(t, 10*time.Second, assigned, false, filepath.Join(tools, "arena-hello")); err != nil || !strings.Contains(out, "hello-from-tool") {
		t.Fatalf("requested executable outside the system roots did not run: %v %q", err, out)
	}
}

func TestMediatedCommandsSeeAFreshProcessTable(t *testing.T) {
	if !sandboxAvailable() {
		t.Skip("OS command sandbox is not operational on this host")
	}
	if _, err := os.Stat("/proc/self/environ"); err != nil {
		t.Skip("host has no procfs")
	}
	const canary = "ARENA_SANDBOX_CANARY=host-process-secret"
	helper := exec.Command("sleep", "30")
	helper.Env = append(os.Environ(), canary)
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = helper.Process.Kill(); _ = helper.Wait() })
	visible := false // The child's environ shows the canary only once exec completes.
	for deadline := time.Now().Add(2 * time.Second); !visible && time.Now().Before(deadline); time.Sleep(10 * time.Millisecond) {
		host, err := os.ReadFile("/proc/" + strconv.Itoa(helper.Process.Pid) + "/environ")
		visible = err == nil && bytes.Contains(host, []byte(canary))
	}
	if !visible {
		t.Skip("host does not expose same-user process environments")
	}
	script := `test -r /proc/1/environ || exit 3
for f in /proc/[0-9]*/environ; do
	if grep -q host-process-secret "$f" 2>/dev/null; then echo "leaked $f"; exit 4; fi
done
test ! -e "/proc/$1" || { echo "helper visible"; exit 5; }`
	if out, err := runSandboxed(t, 10*time.Second, t.TempDir(), false, "sh", "-c", script, "arena-sandbox-test", strconv.Itoa(helper.Process.Pid)); err != nil {
		t.Fatalf("sandbox exposed host processes: %v %q", err, out)
	}
}

func TestMediatedCommandsCanBuildInTheWorktree(t *testing.T) {
	if !sandboxAvailable() {
		t.Skip("OS command sandbox is not operational on this host")
	}
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go toolchain is not on PATH")
	}
	assigned := t.TempDir()
	files := map[string]string{
		"go.mod":  "module example.test/hello\n\ngo 1.21\n",
		"main.go": "package main\n\nimport \"fmt\"\n\nfunc main() { fmt.Println(\"built-in-sandbox\") }\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(assigned, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	out, err := runSandboxed(t, 3*time.Minute, assigned, true, "sh", "-c", `GOTOOLCHAIN=local go build -o "$TMPDIR/hello" . && "$TMPDIR/hello"`)
	if err != nil || !strings.Contains(out, "built-in-sandbox") {
		t.Fatalf("go build failed inside the sandbox: %v %q", err, out)
	}
}
