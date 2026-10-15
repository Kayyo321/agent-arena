//go:build darwin || linux

package arena

import (
	"context"
	"os"
	"path/filepath"
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
