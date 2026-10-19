package arena

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

var blanketRuntime = Runtime{Provider: "antigravity", Model: "fixture", Options: map[string]any{"commandApproval": antigravityBlanketApproval}}

// argRun is the index where want starts as a contiguous run in argv, or -1.
func argRun(argv []string, want ...string) int {
	for i := 0; i+len(want) <= len(argv); i++ {
		if strings.Join(argv[i:i+len(want)], "\x00") == strings.Join(want, "\x00") {
			return i
		}
	}
	return -1
}

func TestCommandApprovalOptionValidation(t *testing.T) {
	cases := []struct {
		runtime Runtime
		ok      bool
	}{
		{blanketRuntime, true},
		{Runtime{Provider: "antigravity", Model: "m"}, true},
		{Runtime{Provider: "antigravity", Model: "m", Options: map[string]any{"commandApproval": "blanket"}}, false},
		{Runtime{Provider: "antigravity", Model: "m", Options: map[string]any{"commandApproval": true}}, false},
		{Runtime{Provider: "antigravity", Model: "m", Options: map[string]any{"commandApproval": antigravityBlanketApproval, "backend": "x"}}, false},
		{Runtime{Provider: "codex", Model: "m", ReasoningEffort: "high", Options: map[string]any{"commandApproval": antigravityBlanketApproval}}, false},
		{Runtime{Provider: "claude-code", Model: "m", ReasoningEffort: "high", Options: map[string]any{"authMethod": claudeSignInAuth, "commandApproval": antigravityBlanketApproval}}, false},
		{Runtime{Provider: "local", Model: "m", Options: map[string]any{"backend": "ollama", "commandApproval": antigravityBlanketApproval}}, false},
		{Runtime{Provider: "opencode", Model: "opencode/m", Options: map[string]any{"commandApproval": antigravityBlanketApproval}}, false},
	}
	for _, tc := range cases {
		if err := ValidateRuntime(tc.runtime); (err == nil) != tc.ok {
			t.Errorf("%v: got %v, want ok=%v", tc.runtime, err, tc.ok)
		}
	}
	if antigravityBlanket(Runtime{Provider: "codex", Options: blanketRuntime.Options}) {
		t.Fatal("the option means nothing outside Antigravity")
	}
}

func TestBlanketApprovalStillRefusedWithoutTheOption(t *testing.T) {
	in := Invocation{Runtime: Runtime{Provider: "antigravity", Model: "fixture"}, Dir: t.TempDir(), Commands: true}
	_, err := invokeAntigravity(context.Background(), in)
	if err == nil || !strings.Contains(err.Error(), "refuses command permissions") || !strings.Contains(err.Error(), "commandApproval") {
		t.Fatalf("commands without the option: %v", err)
	}
}

func TestHiddenCredentialTableCoversTheReviewedMinimum(t *testing.T) {
	for _, required := range []string{".codex", ".claude", ".claude.json", ".ssh", ".gnupg", ".aws", ".config/gh", ".netrc", ".docker", ".kube"} {
		if !Has(hiddenCredentialPaths, required) {
			t.Errorf("hiddenCredentialPaths lacks %s", required)
		}
	}
	for _, rel := range hiddenCredentialPaths {
		// agy's own state directory must stay visible to it.
		if filepath.IsAbs(rel) || strings.Contains(rel, "..") || within(".gemini/antigravity-cli", rel) || within(rel, ".gemini/antigravity-cli") {
			t.Errorf("bad hidden path %q", rel)
		}
	}
}

func TestCommandFenceBwrapArgs(t *testing.T) {
	home := t.TempDir()
	for _, dir := range []string{".codex", ".ssh", ".config/gh", ".gemini/antigravity-cli", "elsewhere/aws"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0700); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{".claude.json", ".netrc"} {
		if err := os.WriteFile(filepath.Join(home, file), nil, 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink(filepath.Join(home, "elsewhere", "aws"), filepath.Join(home, ".aws")); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	tree := filepath.Join(root, ".arena", ".state", "run", "trees", "task")
	if err := os.MkdirAll(tree, 0755); err != nil {
		t.Fatal(err)
	}
	f := commandFence{Home: home, RuntimeDir: "/run/user/4242", State: antigravityStateDir(home), Root: root, Dir: tree, BusSocket: "/tmp/arena-keyring-x/bus", Executable: "/opt/agy/agy"}
	argv := f.bwrapArgs("/usr/bin/bwrap")
	if argv[0] != "/usr/bin/bwrap" || argRun(argv, "--ro-bind", "/", "/") < 0 || argRun(argv, "--bind", "/", "/") >= 0 {
		t.Fatalf("root must be read-only: %q", argv)
	}
	for _, flag := range []string{"--unshare-pid", "--unshare-ipc", "--new-session", "--die-with-parent"} {
		if !Has(argv, flag) {
			t.Errorf("missing %s", flag)
		}
	}
	if Has(argv, "--unshare-net") {
		t.Error("agy needs the network for inference")
	}
	rootAt := argRun(argv, "--ro-bind", "/", "/")
	mustAfterRoot := func(run ...string) int {
		t.Helper()
		at := argRun(argv, run...)
		if at < 0 || at < rootAt {
			t.Errorf("missing or before the root: %q in %q", run, argv)
		}
		return at
	}
	mustAfterRoot("--tmpfs", "/tmp")
	mustAfterRoot("--tmpfs", "/run/user/4242")
	mustAfterRoot("--tmpfs", filepath.Join(home, ".codex"))
	mustAfterRoot("--tmpfs", filepath.Join(home, ".ssh"))
	mustAfterRoot("--tmpfs", filepath.Join(home, ".config/gh"))
	mustAfterRoot("--tmpfs", resolveForTest(filepath.Join(home, "elsewhere", "aws"))) // a symlinked entry hides its target
	mustAfterRoot("--ro-bind", "/dev/null", filepath.Join(home, ".claude.json"))
	mustAfterRoot("--ro-bind", "/dev/null", filepath.Join(home, ".netrc"))
	mustAfterRoot("--overlay-src", f.State, "--tmp-overlay", f.State)
	stateAt := mustAfterRoot("--tmpfs", filepath.Join(root, ".arena", ".state"))
	treeAt := mustAfterRoot("--ro-bind", tree, tree)
	if treeAt < stateAt || argRun(argv, "--bind", tree, tree) >= 0 {
		t.Errorf("a reader's worktree is read-only and mounted over the hidden run state: %q", argv)
	}
	if busAt := mustAfterRoot("--bind", f.BusSocket, "/run/user/4242/bus"); busAt < argRun(argv, "--tmpfs", "/run/user/4242") {
		t.Error("relay socket must be mounted inside the hidden runtime directory")
	}
	if argRun(argv, "--chdir", tree, "--", "/opt/agy/agy") != len(argv)-4 {
		t.Errorf("must end by running the CLI in its worktree: %q", argv)
	}
	for _, absent := range []string{".kube", ".docker", ".gnupg"} {
		if Has(argv, filepath.Join(home, absent)) {
			t.Errorf("absent %s must not be mounted (nothing can create it under a read-only root)", absent)
		}
	}
	// A writer gets its worktree, and only that, writable.
	f.Write = true
	argv = f.bwrapArgs("/usr/bin/bwrap")
	binds := 0
	for i, a := range argv {
		if a == "--bind" {
			binds++
			if argv[i+1] != tree && argv[i+1] != f.BusSocket {
				t.Errorf("unexpected writable mount %q", argv[i+1])
			}
		}
	}
	if binds != 2 || argRun(argv, "--bind", tree, tree) < argRun(argv, "--tmpfs", filepath.Join(root, ".arena", ".state")) {
		t.Errorf("writer binds: %q", argv)
	}
}

func TestCommandFenceEnvDropsCredentialsAndSockets(t *testing.T) {
	env := commandFenceEnv([]string{"PATH=/usr/bin", "HOME=/home/u", "LANG=C.UTF-8", "LC_ALL=C", "AGY_CLI_HIDE_LOGO=1", "SSH_AUTH_SOCK=/run/user/1/ssh", "GH_TOKEN=ghp_x", "OPENAI_API_KEY=sk-x", "DISPLAY=:1", "WAYLAND_DISPLAY=wayland-0", "XAUTHORITY=/run/user/1/xauth", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1/bus", "HTTPS_PROXY=http://user:pw@proxy:3128", "NO_PROXY=localhost"}, "unix:path=/run/user/1/bus")
	got := strings.Join(env, "\n")
	for _, want := range []string{"PATH=/usr/bin", "HOME=/home/u", "LANG=C.UTF-8", "LC_ALL=C", "AGY_CLI_HIDE_LOGO=1", "NO_PROXY=localhost", "TMPDIR=/tmp", "DBUS_SESSION_BUS_ADDRESS=unix:path=/run/user/1/bus"} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %s", want)
		}
	}
	for _, banned := range []string{"SSH_AUTH_SOCK", "GH_TOKEN", "OPENAI_API_KEY", "DISPLAY", "XAUTHORITY", "user:pw"} {
		if strings.Contains(got, banned) {
			t.Errorf("leaked %s", banned)
		}
	}
	if strings.Count(got, "DBUS_SESSION_BUS_ADDRESS") != 1 {
		t.Error("only the relay address may reach the CLI")
	}
}

func TestAntigravityBlanketRunsSandboxedInsideTheCommandFence(t *testing.T) {
	if err := commandFenceAvailable(); err != nil {
		t.Skip(err)
	}
	previous := antigravityKeyringWait
	antigravityKeyringWait = 0
	t.Cleanup(func() { antigravityKeyringWait = previous })
	root := t.TempDir()
	tree := filepath.Join(root, ".arena", ".state", "run", "trees", "mine")
	other := filepath.Join(root, ".arena", ".state", "run", "trees", "other")
	for _, dir := range []string{tree, other} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(other, "notes"), []byte("x"), 0644); err != nil {
		t.Fatal(err)
	}
	// The fake CLI runs inside the fence: it records its argv and what it can
	// reach into the worktree, the only place it may write.
	bin := t.TempDir()
	script := `#!/bin/sh
for a in "$@"; do printf '%s\n' "$a"; done > .agy-args
head -n 1 > .agy-stdin
touch inside && echo inside >> .agy-log
touch "` + root + `/escaped" 2>/dev/null && echo root >> .agy-log
test -e "` + other + `/notes" && echo other >> .agy-log
touch /tmp/private && echo tmp >> .agy-log
test -n "$SSH_AUTH_SOCK$DISPLAY" && echo sockets >> .agy-log
test $(ls /proc | grep -c '^[0-9]') -lt 10 && echo pidns >> .agy-log
cat <<'EOF'
{"event":"step_update","step_update":{"step_index":1,"state":"DONE","step_type":"agent_response","text_delta":"Done."}}
{"event":"result","result":{"status":"SUCCESS","response":"{}","structured_output":{"summary":"fixture"}}}
EOF
`
	if err := os.WriteFile(filepath.Join(bin, "agy"), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("SSH_AUTH_SOCK", "/nonexistent/agent")
	in := Invocation{Runtime: blanketRuntime, ProjectRoot: root, Dir: tree, Prompt: "Return fixture", Write: true, Commands: true, Schema: objectSchema(map[string]any{"summary": map[string]any{"type": "string"}})}
	out, err := invokeAntigravity(context.Background(), in)
	if err != nil || out["summary"] != "fixture" {
		t.Fatalf("result: %v %v", out, err)
	}
	args := strings.Split(strings.TrimSuffix(recorded(t, tree, ".agy-args"), "\n"), "\n")
	want := []string{"--output-format", "stream-json", "--input-format", "stream-json", "--disable-slash-commands", "--model", "fixture", "--json-schema", compactJSON(in.Schema), "--sandbox", "--dangerously-skip-permissions"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("agy argv:\n got %q\nwant %q", args, want)
	}
	if log := recorded(t, tree, ".agy-log"); log != "inside\ntmp\npidns\n" {
		t.Fatalf("fence reach: %q", log)
	}
	if stdin := recorded(t, tree, ".agy-stdin"); !strings.Contains(stdin, "Terminal commands run inside a nested sandbox") {
		t.Fatalf("worker was not told about the fence: %s", stdin)
	}
	if _, err := os.Stat(filepath.Join(root, "escaped")); err == nil {
		t.Fatal("wrote outside the worktree")
	}
	// A reader's worktree is read-only too.
	if err := os.Remove(filepath.Join(tree, ".agy-log")); err != nil {
		t.Fatal(err)
	}
	in.Write = false
	if _, err := invokeAntigravity(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(tree, ".agy-log")); err == nil {
		t.Fatal("a read-only worker wrote its worktree")
	}
}

func TestBlanketApprovalScoringAndStatus(t *testing.T) {
	w, profiles := idealArena()
	w.Agents[2].Runtime = Runtime{Provider: "antigravity", Model: "gemini-test"}
	profiles["reviewer"] = scoreProfile("reviewer", "review", "read", "review", "run_commands")
	refused := findingIDs(scored(t, w, profiles))
	if !Has(refused, "C6") || !Has(refused, "V5") || len(WorkflowWarnings(w, profiles)) != 1 || blanketApprovalNote(w, profiles) != "" {
		t.Fatalf("without the option: %v %v", refused, WorkflowWarnings(w, profiles))
	}
	w.Agents[2].Runtime = Runtime{Provider: "antigravity", Model: "gemini-test", Options: map[string]any{"commandApproval": antigravityBlanketApproval}}
	opted := findingIDs(scored(t, w, profiles))
	if Has(opted, "C6") || Has(opted, "V5") || len(WorkflowWarnings(w, profiles)) != 0 {
		t.Fatalf("with the option run_commands counts: %v %v", opted, WorkflowWarnings(w, profiles))
	}
	if note := blanketApprovalNote(w, profiles); !strings.Contains(note, "reviewer") || !strings.Contains(note, "network") {
		t.Fatalf("consent note: %q", note)
	}

	if !writeFenceAvailable() {
		t.Skip("no write fence on this host")
	}
	fakeCLI(t, "agy", `case "$1" in
--version) echo 1.3.6 ;;
--help) echo "--input-format --output-format --json-schema --disable-slash-commands --effort (low|high)" ;;
models) printf 'gemini-test\tGemini test\n' ;;
esac`)
	plain := Probe(context.Background(), t.TempDir(), Runtime{Provider: "antigravity", Model: "gemini-test"})
	if !plain.CanStart || !strings.Contains(plain.Detail, "headless runs cannot execute commands") {
		t.Fatalf("plain status: %+v", plain)
	}
	blanket := Probe(context.Background(), t.TempDir(), Runtime{Provider: "antigravity", Model: "gemini-test", Options: map[string]any{"commandApproval": antigravityBlanketApproval}})
	if commandFenceAvailable() != nil {
		if blanket.CanStart {
			t.Fatalf("blanket approval without its fence: %+v", blanket)
		}
		return
	}
	if !blanket.CanStart || !strings.Contains(blanket.Detail, "commands: sandboxed blanket approval (opt-in)") || strings.Contains(blanket.Detail, "cannot execute") {
		t.Fatalf("blanket status: %+v", blanket)
	}
}

func TestConsentSurfacesFlagBlanketApproval(t *testing.T) {
	readyProbe(t)
	p := testProject(t)
	var out bytes.Buffer
	cli := NewCLI(strings.NewReader(""), &out, &out)
	args := []string{"workflow", "create", "agy", "--head", "head", "--provider", "antigravity", "--model", "gemini-test", "--command-approval", antigravityBlanketApproval, "--capabilities", "read,run_commands", "--yes", "--project", p.Root}
	if err := cli.Run(context.Background(), args); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if text := out.String(); !strings.Contains(text, "Blanket command approval (opt-in): head") || strings.Contains(text, "warning:") {
		t.Fatalf("create preview:\n%s", text)
	}
	code, err := p.ExportArena("agy")
	if err != nil {
		t.Fatal(err)
	}
	b, err := DecodeShare(code)
	if err != nil {
		t.Fatal(err)
	}
	d, err := p.PrepareImport(context.Background(), b, "", shareProbe)
	if err != nil {
		t.Fatal(err)
	}
	if notes := strings.Join(d.Notes, "\n"); !strings.Contains(notes, "Blanket command approval (opt-in): head") || !strings.Contains(notes, "The author chose this") {
		t.Fatalf("import notes:\n%s", notes)
	}
	if !strings.Contains(interviewInstructions(), `commandApproval "sandboxed-blanket"`) {
		t.Fatal("interview must tell the orchestrator the mode is the user's choice")
	}
}

func TestCoworkerRequestsCannotSelectBlanketApproval(t *testing.T) {
	p := testProject(t)
	w := NewWorkflow("cw", "Coworker fixture")
	w.HeadAgent = "head"
	w.Agents = []Agent{{Id: "head", Level: Int(0), Profile: ".agents/cw/head.agent.json", Runtime: Runtime{Provider: "codex", Model: "gpt-test", ReasoningEffort: "high"}}}
	if _, err := p.Save(w, map[string]Profile{"head": NewProfile("head", "research", "Inspect evidence", "Use Arena for coworkers", []string{"read"})}, ""); err != nil {
		t.Fatal(err)
	}
	q := CoworkerRequest{WorkflowID: "cw", From: "head", AgentID: "runner", Role: "test-runner", Objective: "Run the tests", Reason: "needs commands", Runtime: blanketRuntime, Capabilities: []string{"read", "run_commands"}, WorkspaceMode: "read-only"}
	if _, err := p.RequestCoworker(context.Background(), q, true, nil); err == nil || !strings.Contains(err.Error(), "only the user sets commandApproval") {
		t.Fatalf("agent-chosen blanket approval: %v", err)
	}
}

func TestNestedCommandArgsIsolatesTheNetworkAndBus(t *testing.T) {
	f := commandFence{Home: "/home/u", RuntimeDir: "/run/user/7", State: "/home/u/.gemini/antigravity-cli", Dir: "/w/tree"}
	ro := f.nestedCommandArgs()
	for _, flag := range []string{"--unshare-net", "--unshare-pid", "--unshare-ipc", "--new-session", "--die-with-parent"} {
		if !Has(ro, flag) {
			t.Errorf("nested fence missing %s", flag)
		}
	}
	if argRun(ro, "--ro-bind", "/", "/") < 0 {
		t.Error("nested fence must inherit the read-only root")
	}
	if argRun(ro, "--tmpfs", "/run/user/7") < 0 {
		t.Error("nested fence must hide the keyring relay socket")
	}
	if info, err := os.Stat("/run/user"); err == nil && info.IsDir() {
		// Every user's runtime directory, whatever XDG_RUNTIME_DIR says.
		if argRun(ro, "--tmpfs", "/run/user") < 0 || argRun(f.bwrapArgs("bwrap"), "--tmpfs", "/run/user") < 0 {
			t.Error("fences must empty /run/user: a read-only socket is still connectable")
		}
	}
	if argRun(ro, "--tmpfs", "/home/u/.gemini/antigravity-cli") < 0 {
		t.Error("nested fence must hide agy's sign-in-bearing state")
	}
	if argRun(ro, "--ro-bind", "/w/tree", "/w/tree") < 0 || argRun(ro, "--chdir", "/w/tree") < 0 {
		t.Error("a reader's worktree is read-only in the nested fence")
	}
	if argRun(ro, "--bind", "/w/tree", "/w/tree") >= 0 {
		t.Error("a reader must not get a writable worktree")
	}
	// A writer's worktree is the one writable path.
	f.Write = true
	rw := f.nestedCommandArgs()
	if argRun(rw, "--bind", "/w/tree", "/w/tree") < 0 {
		t.Error("a writer's worktree must be writable in the nested fence")
	}
}

func TestOuterFenceEmptiesHomeAndBindsBackAllowlist(t *testing.T) {
	home := t.TempDir()
	gemini := filepath.Join(home, ".gemini")
	if err := os.MkdirAll(gemini, 0o755); err != nil {
		t.Fatal(err)
	}
	f := commandFence{Home: home, BindBack: []string{gemini}, State: antigravityStateDir(home), Dir: t.TempDir(), Executable: "/usr/bin/agy", CmdWrapDir: t.TempDir(), RuntimeDir: "/run/user/9", BusSocket: "/tmp/relay/bus"}
	argv := f.bwrapArgs("/usr/bin/bwrap")
	homeAt := argRun(argv, "--tmpfs", home)
	if homeAt < 0 {
		t.Fatal("the home directory must be emptied with a tmpfs")
	}
	if at := argRun(argv, "--ro-bind", gemini, gemini); at < 0 || at < homeAt {
		t.Error("agy's config must be bound back read-only, after the home tmpfs")
	}
	if argRun(argv, "--ro-bind", f.CmdWrapDir, cmdWrapMount) < 0 {
		t.Error("the command wrapper directory must be mounted read-only")
	}
	// Other users' homes are emptied too.
	if homes := otherHomes(home); Has(homes, home) {
		t.Error("the fence must not empty its own home")
	}
}

func TestHomeBindBacksStayUnderHome(t *testing.T) {
	home := t.TempDir()
	inside := filepath.Join(home, ".gemini")
	outside := t.TempDir()
	if err := os.MkdirAll(inside, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", inside+string(os.PathListSeparator)+outside+string(os.PathListSeparator)+"/usr/bin")
	t.Setenv("ARENA_ANTIGRAVITY_EXTRA_READS", outside)
	got := homeBindBacks(home)
	home, inside, outside = resolveForTest(home), resolveForTest(inside), resolveForTest(outside)
	if !Has(got, inside) {
		t.Errorf("a PATH dir under home should be bound back: %v", got)
	}
	for _, p := range got {
		if !within(p, home) || p == home {
			t.Errorf("bind-back %q is not strictly under home", p)
		}
	}
	if Has(got, outside) {
		t.Error("paths outside home must not be bound back (they are visible via the read-only root)")
	}
}

func TestWriteCommandWrappersReExecNestedFence(t *testing.T) {
	f := commandFence{Dir: "/w/tree", RealBash: "/usr/bin/bash", RealSh: "/bin/dash", RuntimeDir: "/run/user/3"}
	dir, err := writeCommandWrappers(f, "/usr/bin/bwrap")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	for name, real := range map[string]string{"bash": "/usr/bin/bash", "sh": "/bin/dash"} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		script := string(b)
		if !strings.Contains(script, "--unshare-net") || !strings.Contains(script, "'"+real+"'") || !strings.HasSuffix(strings.TrimSpace(script), `"$@"`) {
			t.Errorf("%s wrapper does not re-exec %s in the nested fence:\n%s", name, real, script)
		}
		if info, _ := os.Stat(filepath.Join(dir, name)); info == nil || info.Mode()&0o111 == 0 {
			t.Errorf("%s wrapper is not executable", name)
		}
	}
}

func TestShellJoinAndPrependPath(t *testing.T) {
	if got := shellJoin([]string{"a", "b c", "it's"}); got != `'a' 'b c' 'it'\''s'` {
		t.Fatalf("shellJoin: %s", got)
	}
	sep := string(os.PathListSeparator)
	if got := prependPath([]string{"HOME=/h", "PATH=/usr/bin" + sep + "/bin"}, "/w"); got[1] != "PATH=/w"+sep+"/usr/bin"+sep+"/bin" {
		t.Fatalf("prependPath existing: %v", got)
	}
	if got := prependPath([]string{"HOME=/h"}, "/w"); got[len(got)-1] != "PATH=/w" {
		t.Fatalf("prependPath missing: %v", got)
	}
}

// TestCommandWrapperNestedFenceIsolatesCommandsLive runs the real two-layer
// fence (no agy): a command invoked as `bash` lands in a network-less namespace
// that can write only the worktree, while agy's layer keeps the host network.
func TestCommandWrapperNestedFenceIsolatesCommandsLive(t *testing.T) {
	if err := commandFenceAvailable(); err != nil {
		t.Skip(err)
	}
	home := t.TempDir()
	secret := filepath.Join(home, "secret")
	if err := os.WriteFile(secret, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	tree := t.TempDir()
	bwrap, _ := exec.LookPath("bwrap")
	realBash, _ := exec.LookPath("bash")
	f := commandFence{Home: home, Dir: tree, Write: true, Executable: realBash, RealBash: realBash, RuntimeDir: resolveForTest(os.Getenv("XDG_RUNTIME_DIR"))}
	f.RealSh = f.RealBash
	wrapDir, err := writeCommandWrappers(f, bwrap)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(wrapDir)
	f.CmdWrapDir = wrapDir
	hostNet, _ := os.Readlink("/proc/self/ns/net")
	probe := `{
		readlink /proc/self/ns/net
		(echo w > ` + tree + `/w.txt && echo worktree=ok) || echo worktree=fail
		(echo h > ` + home + `/h.txt 2>/dev/null && echo home=WROTE) || echo home=readonly
		(test -r ` + secret + ` && echo secret=READABLE) || echo secret=hidden
	} > ` + tree + `/r.txt 2>&1`
	// Outer fence runs bash, which runs the probe, which invokes `bash` (PATH) -> wrapper -> nested fence.
	argv := f.bwrapArgs(bwrap)
	argv = append(argv, "-c", "bash -c "+shellJoin([]string{probe}))
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Env = prependPath([]string{"PATH=/usr/bin:/bin:" + wrapDir}, cmdWrapMount)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("fence run: %v\n%s", err, out)
	}
	got, err := os.ReadFile(filepath.Join(tree, "r.txt"))
	if err != nil {
		t.Fatal(err)
	}
	report := string(got)
	nestedNet := strings.SplitN(report, "\n", 2)[0]
	if nestedNet == hostNet {
		t.Errorf("command shares the host network namespace %q", hostNet)
	}
	for _, want := range []string{"worktree=ok", "home=readonly", "secret=hidden"} {
		if !strings.Contains(report, want) {
			t.Errorf("want %q in:\n%s", want, report)
		}
	}
}

func resolveForTest(p string) string {
	if r, err := filepath.EvalSymlinks(p); err == nil {
		return r
	}
	return p
}

func TestBlanketApprovalRefusesAnUnknownRuntimeDir(t *testing.T) {
	if err := commandFenceAvailable(); err != nil {
		t.Skip(err)
	}
	t.Setenv("XDG_RUNTIME_DIR", "")
	cmd := exec.Command("true")
	if _, cleanup, err := commandFenceCommand(cmd, "", t.TempDir(), false); err == nil || !strings.Contains(err.Error(), "XDG_RUNTIME_DIR") {
		cleanup()
		t.Fatalf("fence ran without a runtime dir: %v", err)
	}
}
