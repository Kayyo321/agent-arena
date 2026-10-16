package arena

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"
)

// DoctorCheck is one preflight result. Status is ok, warn or fail; only a
// failing check marks the result not ok.
type DoctorCheck struct {
	ID     string `json:"id"`
	Status string `json:"status"`
	Detail string `json:"detail"`
}

type DoctorReport struct {
	OK     bool          `json:"ok"`
	Checks []DoctorCheck `json:"checks"`
}

// doctorHost is everything doctor reads from the machine, so tests can fake
// each platform. It never reads credentials and never reports env values.
type doctorHost struct {
	GOOS     string
	Dir      string
	Getenv   func(string) string
	ReadFile func(string) ([]byte, error)
	LookPath func(string) (string, error)
	Output   func(ctx context.Context, dir, name string, args ...string) (string, error)
}

func realDoctorHost(dir string) doctorHost {
	return doctorHost{GOOS: runtime.GOOS, Dir: dir, Getenv: os.Getenv, ReadFile: os.ReadFile, LookPath: exec.LookPath,
		Output: func(ctx context.Context, dir, name string, args ...string) (string, error) {
			ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Dir = dir
			b, err := cmd.Output()
			return strings.TrimSpace(Redact(string(b))), err
		}}
}

var versionNumber = regexp.MustCompile(`(\d+)\.(\d+)(?:\.(\d+))?`)

// bubblewrapAtLeast reports whether `bwrap --version` output is major.minor or newer.
func bubblewrapAtLeast(output string, major, minor int) bool {
	m := versionNumber.FindStringSubmatch(output)
	if m == nil {
		return false
	}
	a, _ := strconv.Atoi(m[1])
	b, _ := strconv.Atoi(m[2])
	return a > major || a == major && b >= minor
}

// Doctor runs the read-only preflight checks.
func Doctor(ctx context.Context, h doctorHost) DoctorReport {
	checks := []DoctorCheck{}
	add := func(id, status, format string, a ...any) {
		checks = append(checks, DoctorCheck{ID: id, Status: status, Detail: fmt.Sprintf(format, a...)})
	}
	switch h.GOOS {
	case "linux", "darwin":
		add("platform", "ok", "%s", h.GOOS)
	default:
		add("platform", "fail", "%s is not supported; on Windows use WSL2 (docs/adr/0001)", h.GOOS)
	}
	wsl := false
	if h.GOOS == "linux" {
		version, _ := h.ReadFile("/proc/version")
		lower := strings.ToLower(string(version))
		wsl = h.Getenv("WSL_DISTRO_NAME") != "" || strings.Contains(lower, "microsoft")
		if wsl {
			if strings.Contains(lower, "wsl2") || strings.Contains(lower, "microsoft-standard") {
				add("wsl", "ok", "WSL2 detected")
			} else {
				add("wsl", "warn", "WSL detected but not clearly WSL2; Arena supports WSL2 only")
			}
			if p := filepath.ToSlash(h.Dir); strings.HasPrefix(p, "/mnt/") {
				add("wsl-filesystem", "warn", "the project is under /mnt/, a Windows drive; Windows and network filesystems are outside the supported scope (worktrees, locks and file watching misbehave). Move it into the WSL filesystem, such as your home directory")
			}
		}
	}

	if _, err := h.LookPath("git"); err != nil {
		add("git", "fail", "git is not installed or not on PATH")
	} else {
		v, _ := h.Output(ctx, h.Dir, "git", "--version")
		add("git", "ok", "%s", v)
		gitRepoChecks(ctx, h, add)
	}

	for _, p := range []struct{ id, bin string }{{"codex", "codex"}, {"claude-code", "claude"}, {"antigravity", "agy"}} {
		if _, err := h.LookPath(p.bin); err != nil {
			add("provider-"+p.id, "warn", "%s not found; needed only by arenas that use %s", p.bin, p.id)
			continue
		}
		v, err := h.Output(ctx, h.Dir, p.bin, "--version")
		if err != nil {
			add("provider-"+p.id, "warn", "%s found but its version probe failed", p.bin)
			continue
		}
		add("provider-"+p.id, "ok", "%s (%s); sign-in is checked by arena status", v, VersionDrift(p.id, v))
	}

	switch h.GOOS {
	case "linux":
		if _, err := h.LookPath("bwrap"); err != nil {
			add("bubblewrap", "warn", "bwrap not found; sandboxed command execution and the Antigravity command fence need bubblewrap 0.11 or newer")
		} else if v, _ := h.Output(ctx, h.Dir, "bwrap", "--version"); !bubblewrapAtLeast(v, 0, 11) {
			add("bubblewrap", "warn", "%s is older than 0.11, which the Antigravity command fence needs (--tmp-overlay)", v)
		} else {
			add("bubblewrap", "ok", "%s", v)
		}
	case "darwin":
		if _, err := h.LookPath("sandbox-exec"); err != nil {
			add("sandbox-exec", "warn", "sandbox-exec not found; sandboxed command execution is unavailable")
		} else {
			add("sandbox-exec", "ok", "found")
		}
	}

	if _, err := h.LookPath("tmux"); err != nil {
		add("tmux", "warn", "tmux not found; optional, only for arena tmux and the devbox workflow (docs/DEVBOX.md)")
	} else {
		v, _ := h.Output(ctx, h.Dir, "tmux", "-V")
		add("tmux", "ok", "%s (optional)", v)
	}

	report := DoctorReport{OK: true, Checks: checks}
	for _, c := range checks {
		report.OK = report.OK && c.Status != "fail"
	}
	return report
}

func gitRepoChecks(ctx context.Context, h doctorHost, add func(id, status, format string, a ...any)) {
	if out, err := h.Output(ctx, h.Dir, "git", "rev-parse", "--is-inside-work-tree"); err != nil || out != "true" {
		add("git-repo", "fail", "not inside a Git work tree; Arena needs a repository to isolate worktrees")
		return
	}
	if _, err := h.Output(ctx, h.Dir, "git", "rev-parse", "--verify", "HEAD"); err != nil {
		add("git-repo", "fail", "the repository has no commit; commit the initial state before running workers")
		return
	}
	status, err := h.Output(ctx, h.Dir, "git", "status", "--porcelain")
	switch {
	case err != nil:
		add("git-repo", "warn", "could not read repository status")
	case status == "":
		add("git-repo", "ok", "repository with a commit; working tree clean")
	default:
		add("git-repo", "ok", "repository with a commit; %d uncommitted path(s) (runs checkpoint them as the baseline)", len(strings.Split(status, "\n")))
	}
}

func (cli *CLI) doctor(ctx context.Context, dir string, o Options) error {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	report := Doctor(ctx, realDoctorHost(abs))
	if o.Bool("json") {
		cli.print(report)
	} else {
		for _, c := range report.Checks {
			fmt.Fprintf(cli.Out, "%-5s %-20s %s\n", strings.ToUpper(c.Status), c.ID, c.Detail)
		}
	}
	if !report.OK {
		return fmt.Errorf("doctor found blocking problems")
	}
	return nil
}
