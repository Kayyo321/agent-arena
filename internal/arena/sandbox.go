//go:build darwin || linux

package arena

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
)

func SandboxedCommand(dir string, write bool, executable string, args []string) (*exec.Cmd, func(), error) {
	path, e := exec.LookPath(executable)
	if e != nil {
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
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		sandbox, e := exec.LookPath("sandbox-exec")
		if e != nil {
			return fail(fmt.Errorf("sandbox-exec is required for mediated command execution"))
		}
		profile := `(version 1)(deny default)(allow process*)(allow sysctl-read)(allow mach-lookup)(allow file-read*)(deny network*)`
		if write {
			profile += `(allow file-write* (subpath ` + strconv.Quote(dir) + `))`
		}
		profile += `(allow file-write* (subpath ` + strconv.Quote(scratch) + `) (literal "/dev/null"))`
		cmd = exec.Command(sandbox, append([]string{"-p", profile, path}, args...)...)
	case "linux":
		sandbox, e := exec.LookPath("bwrap")
		if e != nil {
			return fail(fmt.Errorf("bubblewrap is required for mediated command execution"))
		}
		argv := []string{"--die-with-parent", "--unshare-net", "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp", "--chdir", dir}
		if write {
			argv = append(argv, "--bind", dir, dir)
		}
		argv = append(argv, "--bind", scratch, scratch)
		argv = append(argv, "--", path)
		argv = append(argv, args...)
		cmd = exec.Command(sandbox, argv...)
	default:
		return fail(fmt.Errorf("unsupported command sandbox platform"))
	}
	cmd.Dir = dir
	cmd.Env = commandEnv()
	cmd.Env = append(cmd.Env, "HOME="+scratch, "TMPDIR="+filepath.Join(scratch, "tmp"), "TMP="+filepath.Join(scratch, "tmp"), "TEMP="+filepath.Join(scratch, "tmp"), "XDG_CACHE_HOME="+filepath.Join(scratch, "cache"), "GOCACHE="+filepath.Join(scratch, "cache", "go-build"), "GOPATH="+filepath.Join(scratch, "go"))
	moduleCache := os.Getenv("GOMODCACHE")
	if moduleCache == "" {
		if home, err := os.UserHomeDir(); err == nil {
			moduleCache = filepath.Join(home, "go", "pkg", "mod")
		}
	}
	if moduleCache != "" {
		cmd.Env = append(cmd.Env, "GOMODCACHE="+moduleCache)
	}
	return cmd, cleanup, nil
}
