package arena

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

// fakeHost is a machine where only the listed binaries exist; outputs maps
// "name args..." to what the probe prints.
func fakeHost(goos, dir string, env map[string]string, procVersion string, outputs map[string]string) doctorHost {
	return doctorHost{GOOS: goos, Dir: dir,
		Getenv: func(k string) string { return env[k] },
		ReadFile: func(path string) ([]byte, error) {
			if path == "/proc/version" && procVersion != "" {
				return []byte(procVersion), nil
			}
			return nil, os.ErrNotExist
		},
		LookPath: func(name string) (string, error) {
			for key := range outputs {
				if strings.Fields(key)[0] == name {
					return "/fake/" + name, nil
				}
			}
			return "", fmt.Errorf("not found")
		},
		Output: func(_ context.Context, _, name string, args ...string) (string, error) {
			if out, ok := outputs[strings.Join(append([]string{name}, args...), " ")]; ok {
				return out, nil
			}
			return "", fmt.Errorf("probe failed")
		}}
}

func status(r DoctorReport, id string) string {
	for _, c := range r.Checks {
		if c.ID == id {
			return c.Status
		}
	}
	return "absent"
}

var goodGit = map[string]string{"git --version": "git version 2.47.3", "git rev-parse --is-inside-work-tree": "true", "git rev-parse --verify HEAD": "abc", "git status --porcelain": ""}

func with(base map[string]string, extra map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func TestDoctorLinuxHealthyHost(t *testing.T) {
	r := Doctor(context.Background(), fakeHost("linux", "/home/u/proj", nil, "Linux version 6.1", with(goodGit, map[string]string{
		"codex --version": "codex-cli 0.162.0-alpha.17.2", "bwrap --version": "bubblewrap 0.11.0", "tmux -V": "tmux 3.4"})))
	if !r.OK {
		t.Fatalf("%+v", r)
	}
	for id, want := range map[string]string{"platform": "ok", "git": "ok", "git-repo": "ok", "provider-codex": "ok", "provider-claude-code": "warn", "bubblewrap": "ok", "tmux": "ok", "wsl": "absent"} {
		if got := status(r, id); got != want {
			t.Errorf("%s = %s, want %s", id, got, want)
		}
	}
}

func TestDoctorOldBubblewrapWarnsWithoutBlocking(t *testing.T) {
	r := Doctor(context.Background(), fakeHost("linux", "/p", nil, "", with(goodGit, map[string]string{"bwrap --version": "bubblewrap 0.9.0"})))
	if !r.OK || status(r, "bubblewrap") != "warn" {
		t.Fatalf("%+v", r)
	}
	r = Doctor(context.Background(), fakeHost("linux", "/p", nil, "", goodGit))
	if status(r, "bubblewrap") != "warn" || !r.OK || status(r, "tmux") != "warn" {
		t.Fatalf("%+v", r)
	}
}

func TestDoctorDarwinChecksSandboxExecNotBubblewrap(t *testing.T) {
	r := Doctor(context.Background(), fakeHost("darwin", "/Users/u/p", nil, "", with(goodGit, map[string]string{"sandbox-exec": ""})))
	if status(r, "sandbox-exec") != "ok" || status(r, "bubblewrap") != "absent" || !r.OK {
		t.Fatalf("%+v", r)
	}
	r = Doctor(context.Background(), fakeHost("darwin", "/p", nil, "", goodGit))
	if status(r, "sandbox-exec") != "warn" {
		t.Fatalf("%+v", r)
	}
}

func TestDoctorWSL2DetectionAndWindowsDriveWarning(t *testing.T) {
	proc := "Linux version 5.15.153.1-microsoft-standard-WSL2"
	for name, host := range map[string]doctorHost{
		"by env":  fakeHost("linux", "/mnt/c/work/p", map[string]string{"WSL_DISTRO_NAME": "Ubuntu"}, "", goodGit),
		"by proc": fakeHost("linux", "/mnt/c/work/p", nil, proc, goodGit),
	} {
		r := Doctor(context.Background(), host)
		if status(r, "wsl") == "absent" || status(r, "wsl-filesystem") != "warn" || !r.OK {
			t.Errorf("%s: %+v", name, r)
		}
	}
	r := Doctor(context.Background(), fakeHost("linux", "/home/u/p", nil, proc, goodGit))
	if status(r, "wsl") != "ok" || status(r, "wsl-filesystem") != "absent" {
		t.Fatalf("%+v", r)
	}
	// /mnt outside WSL is not a Windows drive.
	if r := Doctor(context.Background(), fakeHost("linux", "/mnt/data/p", nil, "Linux", goodGit)); status(r, "wsl-filesystem") != "absent" {
		t.Fatalf("%+v", r)
	}
}

func TestDoctorBlockingFailures(t *testing.T) {
	for name, host := range map[string]doctorHost{
		"no git":     fakeHost("linux", "/p", nil, "", map[string]string{}),
		"not a repo": fakeHost("linux", "/p", nil, "", map[string]string{"git --version": "git version 2", "git rev-parse --is-inside-work-tree": "false"}),
		"no commit":  fakeHost("linux", "/p", nil, "", map[string]string{"git --version": "git version 2", "git rev-parse --is-inside-work-tree": "true"}),
		"windows":    fakeHost("windows", "C:/p", nil, "", goodGit),
	} {
		if r := Doctor(context.Background(), host); r.OK {
			t.Errorf("%s should block: %+v", name, r)
		}
	}
}

func TestDoctorNeverPrintsEnvironmentValues(t *testing.T) {
	secret := "ubuntu-secret-distro-name"
	r := Doctor(context.Background(), fakeHost("linux", "/mnt/c/p", map[string]string{"WSL_DISTRO_NAME": secret, "ANTHROPIC_API_KEY": "sk-hidden"}, "", goodGit))
	if b := string(JSON(r)); strings.Contains(b, secret) || strings.Contains(b, "sk-hidden") {
		t.Fatalf("env value leaked: %s", b)
	}
}

func TestDoctorCommandTextAndJSON(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	err := NewCLI(strings.NewReader(""), &out, &out).Run(context.Background(), []string{"doctor", "--project", dir})
	if err == nil || !strings.Contains(out.String(), "git-repo") || !strings.Contains(out.String(), "FAIL") {
		t.Fatalf("a directory outside a repository must block: %v\n%s", err, out.String())
	}
	out.Reset()
	_ = NewCLI(strings.NewReader(""), &out, &out).Run(context.Background(), []string{"doctor", "--json", "--project", dir})
	var report DoctorReport
	if e := DecodeJSON(out.Bytes(), &report); e != nil || report.OK || len(report.Checks) == 0 {
		t.Fatalf("json: %v\n%s", e, out.String())
	}
}
