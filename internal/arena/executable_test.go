package arena

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPinnableExecutableResolvesSymlinkIntoStore(t *testing.T) {
	dir := t.TempDir()
	store := filepath.Join(dir, "lib", "arena", "0.1.0", "arena")
	link := filepath.Join(dir, "bin", "arena")
	if err := os.MkdirAll(filepath.Dir(store), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store, []byte("binary"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(store, link); err != nil {
		t.Fatal(err)
	}
	// t.TempDir can itself sit behind a symlink (macOS /var), so compare resolved paths.
	want, err := filepath.EvalSymlinks(store)
	if err != nil {
		t.Fatal(err)
	}
	if got := pinnableExecutable(link); got != want {
		t.Fatalf("pinnableExecutable(link) = %q, want store path %q", got, want)
	}
}

func TestPinnableExecutableFallsBackWhenMissing(t *testing.T) {
	missing := filepath.Join(t.TempDir(), "gone", "arena")
	if got := pinnableExecutable(missing); got != missing {
		t.Fatalf("pinnableExecutable(missing) = %q, want unchanged %q", got, missing)
	}
}
