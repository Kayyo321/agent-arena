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
	return "Blanket command approval (opt-in): " + strings.Join(agents, ", ") + " run Antigravity with every tool request auto-approved, inside Arena's command fence. Each command runs with an emptied home, no credentials and no network; Antigravity itself keeps the network it needs for inference, so a prompt-injected reviewer could still describe what it reads (its config, the project, the worktree) in a model turn."
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
	RuntimeDir string   // the user's XDG runtime directory (sockets, auth cookies)
	State      string   // the CLI's own state directory, overlaid
	Root       string   // project root, read-only
	Dir        string   // assigned worktree
	Write      bool     // the worktree is writable
	BusSocket  string   // relay socket mounted as the session bus, or ""
	Executable string   // the resolved agy binary
	BindBack   []string // read-only paths restored under the tmpfs home (agy config, toolchains)
	CmdWrapDir string   // host directory holding the bash/sh command wrappers, or ""
	RealBash   string   // the real bash the wrapper re-execs inside the nested fence
	RealSh     string   // the real sh the wrapper re-execs inside the nested fence
}

// cmdWrapMount is where the command wrappers are bound inside the outer fence.
// It sits under the private /tmp so a nested command (fresh /tmp) cannot see or
// tamper with it, and it is bound read-only so neither agy nor a command can
// rewrite the wrapper between commands.
const cmdWrapMount = "/tmp/.arena-cmdwrap"

// extraReadPaths are host-side absolute directories the user allows the command
// fence to bind read-only on top of the tmpfs home, set through the
// ARENA_ANTIGRAVITY_EXTRA_READS environment variable (os.PathListSeparator
// separated). It is deliberately host-side, not a workflow runtime option, so a
// shared arena can never widen another person's fence.
func extraReadPaths() []string {
	return filepath.SplitList(os.Getenv("ARENA_ANTIGRAVITY_EXTRA_READS"))
}

// homeDirToolchains are directory-valued environment variables whose target,
// when it lives under the home directory, a command needs to build or test.
var homeDirToolchains = []string{"GOROOT", "GOPATH", "GOMODCACHE", "CARGO_HOME", "RUSTUP_HOME", "JAVA_HOME", "NVM_DIR", "PYENV_ROOT", "VIRTUAL_ENV"}

// homeBindBacks resolves the read-only paths under home that must survive the
// tmpfs: agy's own ~/.gemini configuration, PATH and toolchain directories, and
// the user's extra reads. The agy state overlay and binary are mounted
// separately. Paths outside home are dropped (they are visible through the
// read-only root already) and duplicates and nested children are removed.
func homeBindBacks(home string) []string {
	if home == "" {
		return nil
	}
	candidates := []string{filepath.Join(home, ".gemini"), filepath.Join(home, ".config", "gemini")}
	for _, entry := range filepath.SplitList(os.Getenv("PATH")) {
		candidates = append(candidates, entry)
	}
	for _, name := range homeDirToolchains {
		candidates = append(candidates, os.Getenv(name))
	}
	candidates = append(candidates, extraReadPaths()...)
	// Candidates are compared after resolving symlinks, so home must be too
	// (macOS temp dirs live under /var -> /private/var).
	if resolved, err := filepath.EvalSymlinks(home); err == nil {
		home = resolved
	}
	seen, out := map[string]bool{}, []string{}
	for _, candidate := range candidates {
		if candidate == "" || !filepath.IsAbs(candidate) {
			continue
		}
		resolved, err := filepath.EvalSymlinks(candidate)
		if err != nil || !within(resolved, home) || resolved == home {
			continue // outside the tmpfs home, or home itself
		}
		if info, err := os.Stat(resolved); err != nil || !info.IsDir() {
			continue
		}
		if !seen[resolved] {
			seen[resolved] = true
			out = append(out, resolved)
		}
	}
	return out
}

// bwrapArgs builds the outer Linux fence: a read-only root with private /tmp,
// /dev, /proc and PID/IPC namespaces; the home directory replaced by a tmpfs
// with only agy's configuration, the toolchains and the user's extra reads
// bound back read-only; the user's runtime directory and Arena's run state
// covered; agy's state directory writable through a throwaway overlay; and the
// worktree the only real writable path, and only for writers. agy itself keeps
// the network for inference; each command it runs is re-sandboxed without it
// (see nestedCommandArgs). The hidden credential table stays as defense in
// depth under the tmpfs home.
func (f commandFence) bwrapArgs(wrapper string) []string {
	argv := []string{wrapper, "--die-with-parent", "--new-session", "--unshare-pid", "--unshare-ipc", "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp"}
	argv = append(argv, hideRuntimeDirs(f.RuntimeDir)...)
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
	// Home allowlist: empty every home, including other users', then bind back
	// read-only only what agy and commands need.
	for _, other := range otherHomes(f.Home) {
		hide(other)
	}
	if f.Home != "" {
		argv = append(argv, "--tmpfs", f.Home)
		for _, path := range f.BindBack {
			argv = append(argv, "--ro-bind", path, path)
		}
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
	if f.CmdWrapDir != "" {
		argv = append(argv, "--ro-bind", f.CmdWrapDir, cmdWrapMount)
	}
	return append(argv, "--ro-bind", f.Executable, f.Executable, "--chdir", f.Dir, "--", f.Executable)
}

// nestedCommandArgs builds the inner fence the bash/sh wrappers re-exec for each
// command agy runs. It inherits the outer fence's read-only root (so the empty
// home, hidden credentials, read-only project and toolchains all carry over),
// then adds a private network namespace, hides the keyring relay and agy's
// state, and leaves the worktree as the only writable path (for writers). This
// is what keeps a prompt-injected command off the network, away from abstract
// sockets, and unable to reach agy's sign-in. It omits the leading bwrap and the
// trailing `-- <shell> "$@"`, which each wrapper adds.
func (f commandFence) nestedCommandArgs() []string {
	argv := []string{"--die-with-parent", "--new-session", "--unshare-net", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--ro-bind", "/", "/", "--dev", "/dev", "--proc", "/proc", "--tmpfs", "/tmp"}
	argv = append(argv, hideRuntimeDirs(f.RuntimeDir)...) // and the relay bus socket
	if f.State != "" {
		argv = append(argv, "--tmpfs", f.State) // agy's sign-in-bearing state
	}
	if f.Write {
		argv = append(argv, "--bind", f.Dir, f.Dir)
	} else {
		argv = append(argv, "--ro-bind", f.Dir, f.Dir)
	}
	return append(argv, "--chdir", f.Dir)
}

// hideRuntimeDirs empties every user's runtime directory (/run/user, where the
// real session bus and other sockets live) and the fence's own one: a socket on
// a read-only bind is still connectable, so read-only is not enough.
func hideRuntimeDirs(runtimeDir string) []string {
	argv := []string{}
	if info, err := os.Stat("/run/user"); err == nil && info.IsDir() {
		argv = append(argv, "--tmpfs", "/run/user")
	}
	if runtimeDir != "" {
		argv = append(argv, "--tmpfs", runtimeDir)
	}
	return argv
}

// otherHomes lists home roots other than the fence's own that exist on this
// host, so the fence can empty them: every /home/* and /Users/* entry plus
// /root, and the stray mount roots /mnt, /media and /srv.
func otherHomes(home string) []string {
	out := []string{}
	for _, parent := range []string{"/home", "/Users"} {
		entries, err := os.ReadDir(parent)
		if err != nil {
			continue
		}
		for _, entry := range entries {
			if path := filepath.Join(parent, entry.Name()); path != home {
				out = append(out, path)
			}
		}
	}
	for _, path := range []string{"/root", "/mnt", "/media", "/srv"} {
		if path != home {
			out = append(out, path)
		}
	}
	return out
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
	f.BindBack = homeBindBacks(f.Home)
	f.RealBash, err = exec.LookPath("bash")
	if err != nil {
		return nil, noop, fmt.Errorf("Antigravity's sandboxed blanket approval needs bash for the command wrapper: %w", err)
	}
	if f.RealSh, err = exec.LookPath("sh"); err != nil {
		f.RealSh = f.RealBash
	}
	f.RuntimeDir = resolve(os.Getenv("XDG_RUNTIME_DIR"))
	if f.RuntimeDir == "" {
		return nil, noop, fmt.Errorf("Antigravity's sandboxed blanket approval needs XDG_RUNTIME_DIR to hide the session bus and relay the keyring")
	}
	var relay *keyringRelay
	busAddress := ""
	if upstream := sessionBusSocket(); upstream != "" && f.RuntimeDir != "" {
		if relay, err = startKeyringRelay(upstream); err != nil {
			return nil, noop, err
		}
		f.BusSocket = relay.Socket
		busAddress = "unix:path=" + filepath.Join(f.RuntimeDir, "bus")
	}
	wrapDir, err := writeCommandWrappers(f, wrapper)
	if err != nil {
		if relay != nil {
			relay.Close()
		}
		return nil, noop, err
	}
	f.CmdWrapDir = wrapDir
	cleanup := func() {
		if relay != nil {
			relay.Close()
		}
		_ = os.RemoveAll(wrapDir)
	}
	env := cmd.Env
	if env == nil {
		env = os.Environ()
	}
	// Commands resolve bash/sh through PATH (agy launches `bash -c`), so the
	// wrapper mount leads, re-sandboxing each command without the network.
	cmd.Env = prependPath(commandFenceEnv(env, busAddress), cmdWrapMount)
	argv := f.bwrapArgs(wrapper)
	cmd.Args = append(argv, cmd.Args[1:]...)
	cmd.Path = wrapper
	return relay, cleanup, nil
}

// writeCommandWrappers creates a private directory holding bash and sh scripts
// that re-exec the real interpreter inside the nested command fence. The
// directory is bound read-only into the outer fence at cmdWrapMount.
func writeCommandWrappers(f commandFence, bwrap string) (string, error) {
	dir, err := os.MkdirTemp("", "arena-cmdwrap-")
	if err != nil {
		return "", err
	}
	nested := f.nestedCommandArgs()
	for name, real := range map[string]string{"bash": f.RealBash, "sh": f.RealSh} {
		argv := append([]string{bwrap}, nested...)
		argv = append(argv, "--", real, `"$@"`)
		script := "#!/bin/sh\nexec " + shellJoin(argv[:len(argv)-1]) + ` "$@"` + "\n"
		if err := os.WriteFile(filepath.Join(dir, name), []byte(script), 0o555); err != nil {
			_ = os.RemoveAll(dir)
			return "", err
		}
	}
	return dir, nil
}

// shellJoin renders argv as a POSIX command line, single-quoting each word.
func shellJoin(argv []string) string {
	quoted := make([]string, len(argv))
	for i, a := range argv {
		quoted[i] = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
	}
	return strings.Join(quoted, " ")
}

// prependPath puts prefix first on the PATH entry of env, adding one if absent.
func prependPath(env []string, prefix string) []string {
	for i, kv := range env {
		if value, ok := strings.CutPrefix(kv, "PATH="); ok {
			env[i] = "PATH=" + prefix + string(os.PathListSeparator) + value
			return env
		}
	}
	return append(env, "PATH="+prefix)
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
