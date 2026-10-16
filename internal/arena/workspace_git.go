package arena

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// gitRepo is a checkout that coordinator Git commands run against. A writing
// worker's sandbox can rewrite anything in its worktree, including the .git
// pointer, so commands for worker worktrees never rediscover the repository:
// they name the administrative gitdir Arena verified from the main repository
// and disable configuration that makes Git start other programs.
type gitRepo struct {
	dir    string
	gitDir string // the worktree's .git/worktrees/<name>; empty for the user's checkout
}

// workerGitConfig overrides every config scope. Filter and textconv drivers can
// only come from trusted config once the gitdir is pinned; diffs also pass
// --no-ext-diff --no-textconv so stored patches stay byte-exact.
var workerGitConfig = []string{
	"core.fsmonitor=false",
	"core.hooksPath=" + os.DevNull,
	"protocol.allow=never",
	"submodule.recurse=false",
	"commit.gpgSign=false",
}

func (g gitRepo) git(args ...string) (string, error) { return g.gitEnv(nil, args...) }
func (g gitRepo) gitEnv(env []string, args ...string) (string, error) {
	if g.gitDir == "" {
		return gitEnv(g.dir, env, args...)
	}
	prefix := []string{"--git-dir=" + g.gitDir, "--work-tree=" + g.dir}
	for _, setting := range workerGitConfig {
		prefix = append(prefix, "-c", setting)
	}
	return gitEnv(g.dir, env, append(prefix, args...)...)
}

type workspaceIntegrityError struct{ dir, reason string }

func (e *workspaceIntegrityError) Error() string {
	return fmt.Sprintf("refusing to run Git in worker worktree %s: %s; the worktree is preserved for inspection", e.dir, e.reason)
}

// repo returns the Git view for dir: the user's checkout as-is, anything else
// only as a verified Arena worktree.
func (p *Project) repo(dir string) (gitRepo, error) {
	if sameDir(dir, p.Root) {
		return gitRepo{dir: p.Root}, nil
	}
	return p.workerRepo(dir, "")
}

// workerRepo verifies that dir/.git is still the pointer Git wrote and that it
// names the administrative directory the main repository records for dir. The
// returned gitdir comes from the main repository, never from the worktree.
func (p *Project) workerRepo(dir, recorded string) (gitRepo, error) {
	reject := func(format string, args ...any) (gitRepo, error) {
		return gitRepo{}, &workspaceIntegrityError{dir, fmt.Sprintf(format, args...)}
	}
	target, e := readGitPointer(dir)
	if e != nil {
		return reject("%v", e)
	}
	if recorded != "" && target != filepath.Clean(recorded) {
		return reject(".git points at %s instead of Arena's recorded gitdir %s", target, recorded)
	}
	admin, e := p.worktreeAdmin(dir)
	if e != nil {
		return reject("%v", e)
	}
	if !sameDir(admin, target) {
		return reject(".git points at %s, not this worktree's administrative gitdir %s", target, admin)
	}
	return gitRepo{dir: dir, gitDir: admin}, nil
}

func readGitPointer(dir string) (string, error) {
	path := filepath.Join(dir, ".git")
	info, e := os.Lstat(path)
	if e != nil {
		return "", fmt.Errorf("worktree .git pointer is missing: %w", e)
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf(".git was replaced (mode %v); expected the gitdir pointer file Git created", info.Mode().Type())
	}
	if info.Size() > 4096 {
		return "", fmt.Errorf(".git is too large to be a gitdir pointer")
	}
	b, e := os.ReadFile(path)
	if e != nil {
		return "", e
	}
	target, ok := strings.CutPrefix(strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r"), "gitdir: ")
	if !ok || target == "" || strings.ContainsAny(target, "\r\n\x00") {
		return "", fmt.Errorf(".git does not contain a single gitdir pointer")
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(dir, target)
	}
	return filepath.Clean(target), nil
}

// worktreeAdmin finds the main repository's administrative directory whose
// gitdir back-pointer names dir. Workers cannot write the main repository.
func (p *Project) worktreeAdmin(dir string) (string, error) {
	common, e := Git(p.Root, "rev-parse", "--git-common-dir")
	if e != nil {
		return "", e
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(p.Root, common)
	}
	entries, e := os.ReadDir(filepath.Join(common, "worktrees"))
	if e != nil && !os.IsNotExist(e) {
		return "", e
	}
	match := ""
	for _, entry := range entries {
		admin := filepath.Join(common, "worktrees", entry.Name())
		b, err := os.ReadFile(filepath.Join(admin, "gitdir"))
		if err != nil {
			continue
		}
		back := strings.TrimSuffix(strings.TrimSuffix(string(b), "\n"), "\r")
		if !filepath.IsAbs(back) {
			back = filepath.Join(admin, back)
		}
		if filepath.Base(back) == ".git" && sameDir(filepath.Dir(back), dir) {
			if match != "" {
				return "", fmt.Errorf("multiple Git worktree records claim this directory")
			}
			match = admin
		}
	}
	if match == "" {
		return "", fmt.Errorf("the repository has no Git worktree record for this directory")
	}
	return match, nil
}

func sameDir(a, b string) bool {
	x, e := os.Stat(a)
	if e != nil || !x.IsDir() {
		return false
	}
	y, e := os.Stat(b)
	return e == nil && y.IsDir() && os.SameFile(x, y)
}
