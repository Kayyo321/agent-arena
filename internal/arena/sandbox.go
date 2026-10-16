//go:build darwin || linux

package arena

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// SandboxedCommand prepares a mediated command. It can read only system toolchain
// roots, the assigned worktree, its private scratch and a few resolved toolchain
// directories (the executable's own directory, GOROOT, the module cache and PATH
// entries outside the home directory). Host home directories, other Arena trees and
// other processes stay hidden; networking is denied.
func SandboxedCommand(dir string, write bool, executable string, args []string) (*exec.Cmd, func(), error) {
	path, e := exec.LookPath(executable)
	if e != nil {
		return nil, nil, e
	}
	if dir, e = filepath.Abs(dir); e != nil {
		return nil, nil, e
	}
	dir, e = filepath.EvalSymlinks(dir)
	if e != nil {
		return nil, nil, e
	}
	scratch, e := os.MkdirTemp("", "arena-command-")
	if e != nil {
		return nil, nil, e
	}
	createdScratch := scratch
	cleanup := func() { _ = os.RemoveAll(createdScratch) }
	scratch, e = filepath.EvalSymlinks(scratch)
	if e != nil {
		cleanup()
		return nil, nil, e
	}
	for _, name := range []string{"tmp", "cache", "go"} {
		if err := os.MkdirAll(filepath.Join(scratch, name), 0700); err != nil {
			cleanup()
			return nil, nil, err
		}
	}
	fail := func(err error) (*exec.Cmd, func(), error) { cleanup(); return nil, nil, err }
	moduleCache := os.Getenv("GOMODCACHE")
	if moduleCache == "" {
		if home, err := os.UserHomeDir(); err == nil {
			moduleCache = filepath.Join(home, "go", "pkg", "mod")
		}
	}
	reads := commandReadRoots(dir, scratch, path, moduleCache)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		sandbox, e := exec.LookPath("sandbox-exec")
		if e != nil {
			return fail(fmt.Errorf("sandbox-exec is required for mediated command execution"))
		}
		cmd = exec.Command(sandbox, append([]string{"-p", darwinSandboxProfile(dir, scratch, write, reads), path}, args...)...)
	case "linux":
		sandbox, e := exec.LookPath("bwrap")
		if e != nil {
			return fail(fmt.Errorf("bubblewrap is required for mediated command execution"))
		}
		cmd = exec.Command(sandbox, append(bwrapArgs(dir, scratch, write, reads, path), args...)...)
	default:
		return fail(fmt.Errorf("unsupported command sandbox platform"))
	}
	cmd.Dir = dir
	cmd.Env = commandEnv()
	// The repository's Git directory is outside the read boundary, so Go must not try to stamp VCS data.
	cmd.Env = append(cmd.Env, "HOME="+scratch, "TMPDIR="+filepath.Join(scratch, "tmp"), "TMP="+filepath.Join(scratch, "tmp"), "TEMP="+filepath.Join(scratch, "tmp"), "XDG_CACHE_HOME="+filepath.Join(scratch, "cache"), "GOCACHE="+filepath.Join(scratch, "cache", "go-build"), "GOPATH="+filepath.Join(scratch, "go"), "GOFLAGS=-buildvcs=false")
	if moduleCache != "" {
		cmd.Env = append(cmd.Env, "GOMODCACHE="+moduleCache)
	}
	return cmd, cleanup, nil
}

// commandReadRoots resolves the extra read-only directories a command may need beyond
// the fixed system roots. Roots that would expose the home directory, an ancestor of
// the worktree (and so sibling worktrees and .arena/.state), or credential/control
// directories are dropped.
func commandReadRoots(dir, scratch, executable, moduleCache string) []string {
	home, _ := os.UserHomeDir()
	if home != "" {
		if resolved, err := filepath.EvalSymlinks(home); err == nil {
			home = resolved
		}
	}
	explicit := []string{filepath.Dir(executable), moduleCache}
	goroot := os.Getenv("GOROOT")
	if goroot == "" {
		if goTool, err := exec.LookPath("go"); err == nil {
			if resolved, err := filepath.EvalSymlinks(goTool); err == nil {
				goroot = filepath.Dir(filepath.Dir(resolved))
				if _, err := os.Stat(filepath.Join(goroot, "pkg", "tool")); err != nil {
					goroot = ""
				}
			}
		}
	}
	explicit = append(explicit, goroot)
	seen, roots := map[string]bool{}, []string{}
	add := func(candidate string, allowHome bool) {
		if candidate == "" {
			return
		}
		candidate, err := filepath.Abs(candidate)
		if err != nil {
			return
		}
		candidate, err = filepath.EvalSymlinks(candidate)
		if err != nil {
			return
		}
		if info, err := os.Stat(candidate); err != nil || !info.IsDir() {
			return
		}
		switch {
		case seen[candidate], candidate == string(filepath.Separator), protectedPath(candidate):
			return
		case within(candidate, dir), within(candidate, scratch), within(dir, candidate):
			return // Inside a tree that is already mounted, or an ancestor of the worktree.
		case home != "" && within(home, candidate):
			return
		case home != "" && !allowHome && within(candidate, home):
			return
		}
		seen[candidate] = true
		roots = append(roots, candidate)
	}
	for _, candidate := range explicit {
		add(candidate, true)
	}
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.IsAbs(entry) {
			add(entry, false)
		}
	}
	// Mount parents before children so nested roots are not hidden.
	sort.Slice(roots, func(i, j int) bool { return len(roots[i]) < len(roots[j]) })
	return roots
}

// within reports whether path is root or below it.
func within(path, root string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Sensitive files under /etc are hidden even when host permissions would allow reading them.
var sandboxMaskedSystemPaths = []string{"/etc/shadow", "/etc/gshadow", "/etc/sudoers", "/etc/sudoers.d", "/etc/ssh", "/etc/ssl/private", "/etc/NetworkManager/system-connections", "/etc/wireguard"}

func bwrapArgs(dir, scratch string, write bool, reads []string, executable string) []string {
	argv := []string{"--die-with-parent", "--unshare-net", "--unshare-pid", "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp"}
	for _, root := range []string{"/bin", "/sbin", "/lib", "/lib32", "/lib64", "/libx32"} {
		info, err := os.Lstat(root)
		switch {
		case err != nil:
		case info.Mode()&os.ModeSymlink != 0:
			if target, err := os.Readlink(root); err == nil {
				argv = append(argv, "--symlink", target, root)
			}
		case info.IsDir():
			argv = append(argv, "--ro-bind", root, root)
		}
	}
	system := []string{"/usr", "/etc", "/opt", "/nix/store"}
	for _, root := range system {
		argv = append(argv, "--ro-bind-try", root, root)
	}
	for _, masked := range sandboxMaskedSystemPaths {
		if info, err := os.Lstat(masked); err == nil {
			if info.IsDir() {
				argv = append(argv, "--tmpfs", masked)
			} else if info.Mode().IsRegular() {
				argv = append(argv, "--ro-bind", "/dev/null", masked)
			}
		}
	}
next:
	for _, root := range reads {
		for _, covered := range system {
			if within(root, covered) {
				continue next
			}
		}
		argv = append(argv, "--ro-bind", root, root)
	}
	if write {
		argv = append(argv, "--bind", dir, dir)
	} else {
		argv = append(argv, "--ro-bind", dir, dir)
	}
	argv = append(argv, "--bind", scratch, scratch, "--chdir", dir, "--", executable)
	return argv
}

func darwinSandboxProfile(dir, scratch string, write bool, reads []string) string {
	subpaths := []string{"/usr", "/bin", "/sbin", "/System", "/Library", "/opt", "/nix", "/private/etc", "/private/var/db/dyld", "/private/var/db/timezone", "/private/var/select", "/dev/fd", dir, scratch}
	subpaths = append(subpaths, reads...)
	profile := `(version 1)(deny default)(allow process*)(allow sysctl-read)(allow mach-lookup)(allow file-read-metadata)(allow file-read*`
	for _, p := range subpaths {
		profile += ` (subpath ` + strconv.Quote(p) + `)`
	}
	for _, p := range []string{"/", "/etc", "/tmp", "/var", "/private", "/dev/null", "/dev/zero", "/dev/random", "/dev/urandom", "/dev/tty", "/dev/dtracehelper"} {
		profile += ` (literal ` + strconv.Quote(p) + `)`
	}
	profile += ` (regex #"^/Applications/Xcode[^/]*\.app/"))`
	profile += `(deny file-read* (subpath "/Library/Keychains") (subpath "/private/etc/ssh") (literal "/private/etc/master.passwd") (literal "/private/etc/sudoers") (subpath "/private/etc/sudoers.d"))`
	profile += `(deny network*)`
	if write {
		profile += `(allow file-write* (subpath ` + strconv.Quote(dir) + `))`
	}
	profile += `(allow file-write* (subpath ` + strconv.Quote(scratch) + `) (literal "/dev/null"))`
	return profile
}
