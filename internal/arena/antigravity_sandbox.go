//go:build darwin || linux

package arena

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// Antigravity's headless mode has no per-command approval: agy either refuses
// terminal commands or, with --dangerously-skip-permissions, approves every
// tool request. Arena runs the second only when the runtime opts in with
// runtime.options.commandApproval = "sandboxed-blanket", and then only inside
// commandFence, which is much tighter than writeFence. It is never a default
// and never chosen on the user's behalf.
const antigravityBlanketApproval = "sandboxed-blanket"

func antigravityBlanket(r Runtime) bool {
	return r.Provider == "antigravity" && str(r.Options["commandApproval"]) == antigravityBlanketApproval
}

// blanketApprovalAgents lists the agents whose provider auto-approves every
// tool request, for consent previews.
func blanketApprovalAgents(w Workflow, profiles map[string]Profile) []string {
	out := []string{}
	for _, a := range w.Agents {
		if antigravityBlanket(a.Runtime) && has(profiles[a.Id], "run_commands") {
			out = append(out, a.Id)
		}
	}
	return out
}

// blanketApprovalNote is the consent line for previews, or "".
func blanketApprovalNote(w Workflow, profiles map[string]Profile) string {
	agents := blanketApprovalAgents(w, profiles)
	if len(agents) == 0 {
		return ""
	}
	return "Blanket command approval (opt-in): " + strings.Join(agents, ", ") + " run Antigravity with every tool request auto-approved, inside Arena's command fence. The fence keeps credentials and the home directory out of reach but not the network: a prompt-injected command can send anything it can read."
}

// hiddenCredentialPaths is the reviewed table of home-relative paths that
// commandFence covers with an empty tmpfs (directories) or /dev/null (files).
// Paths that do not exist are skipped: the root is read-only, so nothing can
// create them. Add a path here rather than special-casing it elsewhere.
var hiddenCredentialPaths = []string{
	// Other providers' sign-ins, settings and app state.
	".codex", ".claude", ".claude.json", ".claude.json.backup", ".config/Claude",
	".gemini/oauth_creds.json", ".gemini/google_accounts.json", ".gemini/.env", ".gemini/antigravity-ide", ".antigravity-ide",
	".config/gcloud", ".config/github-copilot", ".aws", ".azure", ".oci", ".config/gh", ".config/hub", ".config/op",
	// Keys, agents and password stores.
	".ssh", ".gnupg", ".password-store", ".local/share/keyrings", ".local/share/kwalletd", ".pki",
	// Registry, cluster and service tokens.
	".netrc", ".git-credentials", ".config/git/credentials", ".docker", ".kube", ".npmrc", ".yarnrc", ".pypirc",
	".cargo/credentials", ".cargo/credentials.toml", ".gem/credentials", ".m2/settings.xml", ".gradle/gradle.properties",
	".terraform.d", ".vault-token", ".config/rclone", ".pgpass", ".my.cnf",
	// Browser, mail and Flatpak app profiles (cookies, saved sessions).
	".mozilla", ".thunderbird", ".config/google-chrome", ".config/chromium", ".config/BraveSoftware", ".config/microsoft-edge", ".var/app",
	// Shell and REPL history, which often holds pasted tokens.
	".bash_history", ".zsh_history", ".local/share/fish/fish_history", ".python_history", ".psql_history", ".mysql_history", ".node_repl_history",
}

// commandFenceEnv is what reaches the CLI (and so every command it runs) from
// Arena's environment: locale, terminal, search path and toolchain locations.
// Credentials in the environment, agent sockets (SSH, GPG), the display and
// the real session bus are dropped.
var (
	commandFenceEnvNames    = []string{"PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "LANGUAGE", "TERM", "COLORTERM", "TZ", "NO_COLOR", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_STATE_HOME", "XDG_RUNTIME_DIR", "GOROOT", "GOPATH", "GOMODCACHE", "GOTOOLCHAIN", "CARGO_HOME", "RUSTUP_HOME", "JAVA_HOME", "NVM_DIR", "PYENV_ROOT", "VIRTUAL_ENV"}
	commandFenceEnvPrefixes = []string{"LC_", "AGY_", "ANTIGRAVITY_"}
	commandFenceProxyNames  = []string{"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "ALL_PROXY", "http_proxy", "https_proxy", "no_proxy", "all_proxy"}
)

func commandFenceEnv(environ []string, busAddress string) []string {
	env := []string{}
	for _, kv := range environ {
		name, value, _ := strings.Cut(kv, "=")
		keep := Has(commandFenceEnvNames, name) || Has(commandFenceProxyNames, name) && !strings.Contains(value, "@")
		for _, prefix := range commandFenceEnvPrefixes {
			keep = keep || strings.HasPrefix(name, prefix)
		}
		if keep && Redact(value) == value {
			env = append(env, kv)
		}
	}
	env = append(env, "TMPDIR=/tmp", "XDG_CACHE_HOME=/tmp/.cache")
	if busAddress != "" {
		env = append(env, "DBUS_SESSION_BUS_ADDRESS="+busAddress)
	}
	return env
}

// commandFence describes one fenced CLI run. All paths are resolved.
type commandFence struct {
	Home       string
	RuntimeDir string // the user's XDG runtime directory (sockets, auth cookies)
	State      string // the CLI's own state directory, overlaid
	Root       string // project root, read-only
	Dir        string // assigned worktree
	Write      bool
	BusSocket  string // relay socket mounted as the session bus, or ""
	Executable string
}

// bwrapArgs builds the Linux fence: a read-only root with private /tmp, /dev,
// /proc and PID/IPC namespaces; the user's runtime directory, Arena's run
// state and the hidden credential table covered; the CLI's state directory
// writable through a throwaway overlay; and the worktree the only real
// writable path, and only for writers. The network is shared: agy needs it
// for inference.
func (f commandFence) bwrapArgs(wrapper string) []string {
	argv := []string{wrapper, "--die-with-parent", "--new-session", "--unshare-pid", "--unshare-ipc", "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp"}
	if f.RuntimeDir != "" {
		argv = append(argv, "--tmpfs", f.RuntimeDir)
	}
	hide := func(path string) {
		info, err := os.Lstat(path)
		if err != nil {
			return
		}
		if info.Mode()&os.ModeSymlink != 0 {
			if path, err = filepath.EvalSymlinks(path); err != nil {
				return
			}
			if info, err = os.Stat(path); err != nil {
				return
			}
		}
		if info.IsDir() {
			argv = append(argv, "--tmpfs", path)
		} else {
			argv = append(argv, "--ro-bind", "/dev/null", path)
		}
	}
	for _, masked := range sandboxMaskedSystemPaths {
		hide(masked)
	}
	if f.Home != "" {
		for _, rel := range hiddenCredentialPaths {
			hide(filepath.Join(f.Home, rel))
		}
	}
	if f.State != "" {
		argv = append(argv, "--overlay-src", f.State, "--tmp-overlay", f.State)
	}
	if f.Root != "" {
		argv = append(argv, "--ro-bind", f.Root, f.Root)
		// Other agents' worktrees, journals and attachments live here.
		if info, err := os.Stat(filepath.Join(f.Root, ".arena", ".state")); err == nil && info.IsDir() {
			argv = append(argv, "--tmpfs", filepath.Join(f.Root, ".arena", ".state"))
		}
	}
	if f.Write {
		argv = append(argv, "--bind", f.Dir, f.Dir)
	} else {
		argv = append(argv, "--ro-bind", f.Dir, f.Dir)
	}
	if f.BusSocket != "" && f.RuntimeDir != "" {
		argv = append(argv, "--bind", f.BusSocket, filepath.Join(f.RuntimeDir, "bus"))
	}
	return append(argv, "--ro-bind", f.Executable, f.Executable, "--chdir", f.Dir, "--", f.Executable)
}

var tmpOverlay = sync.OnceValue(func() bool {
	help, _ := exec.Command("bwrap", "--help").CombinedOutput()
	return strings.Contains(string(help), "--tmp-overlay")
})

// antigravityStateDir is agy's own state (logs, conversations, caches). Its
// sign-in lives in the system keyring, not here.
func antigravityStateDir(home string) string {
	return filepath.Join(home, ".gemini", "antigravity-cli")
}

// commandFenceCommand rewrites cmd to run inside the command fence. The
// returned relay is nil when there is no session bus to relay; cleanup is
// always safe to call.
func commandFenceCommand(cmd *exec.Cmd, root, dir string, write bool) (*keyringRelay, func(), error) {
	noop := func() {}
	if err := commandFenceAvailable(); err != nil {
		return nil, noop, err
	}
	wrapper, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, noop, err
	}
	exe, err := exec.LookPath(cmd.Path)
	if err != nil {
		return nil, noop, err
	}
	resolve := func(path string) string {
		if path == "" {
			return ""
		}
		if resolved, err := filepath.EvalSymlinks(path); err == nil {
			return resolved
		}
		return ""
	}
	f := commandFence{Dir: resolve(dir), Root: resolve(root), Write: write, Executable: resolve(exe)}
	if f.Dir == "" || f.Executable == "" {
		return nil, noop, fmt.Errorf("command fence needs an existing worktree and executable")
	}
	if home, err := os.UserHomeDir(); err == nil {
		f.Home = resolve(home)
		if info, err := os.Stat(antigravityStateDir(f.Home)); err == nil && info.IsDir() {
			f.State = antigravityStateDir(f.Home)
		}
	}
	if f.Home == "" || within(f.Home, f.Dir) || f.Root != "" && within(f.Home, f.Root) {
		return nil, noop, fmt.Errorf("command fence refuses a worktree or project that contains the home directory")
	}
	f.RuntimeDir = resolve(os.Getenv("XDG_RUNTIME_DIR"))
	var relay *keyringRelay
	busAddress := ""
	if upstream := sessionBusSocket(); upstream != "" && f.RuntimeDir != "" {
		if relay, err = startKeyringRelay(upstream); err != nil {
			return nil, noop, err
		}
		f.BusSocket = relay.Socket
		busAddress = "unix:path=" + filepath.Join(f.RuntimeDir, "bus")
	}
	cleanup := func() {
		if relay != nil {
			relay.Close()
		}
	}
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	cmd.Env = commandFenceEnv(env, busAddress)
	argv := f.bwrapArgs(wrapper)
	cmd.Args = append(argv, cmd.Args[1:]...)
	cmd.Path = wrapper
	return relay, cleanup, nil
}

// awaitKeyring gives agy time to read its sign-in from the keyring before the
// first turn is sent, then seals the relay so no later search (from a command)
// can learn another service.
func awaitKeyring(relay *keyringRelay, timeout time.Duration) {
	if relay == nil {
		return
	}
	deadline := time.Now().Add(timeout)
	for relay.Reads() == 0 && time.Now().Before(deadline) {
		time.Sleep(20 * time.Millisecond)
	}
	relay.Seal()
}

// commandFenceAvailable reports why sandboxed blanket approval cannot run on
// this host, or nil.
func commandFenceAvailable() error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("commandApproval %q is available on Linux only: on %s Arena cannot let agy reach its Keychain sign-in while keeping commands away from other Keychain items", antigravityBlanketApproval, runtime.GOOS)
	}
	if _, err := exec.LookPath("bwrap"); err != nil {
		return fmt.Errorf("bubblewrap is required for Antigravity's sandboxed blanket approval")
	}
	if !tmpOverlay() {
		return fmt.Errorf("Antigravity's sandboxed blanket approval needs bubblewrap 0.11 or newer (--tmp-overlay)")
	}
	return nil
}
