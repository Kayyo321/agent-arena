package arena

import (
	"bytes"
	"context"
	"os"
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
	mustAfterRoot("--tmpfs", filepath.Join(home, "elsewhere", "aws")) // a symlinked entry hides its target
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
	if stdin := recorded(t, tree, ".agy-stdin"); !strings.Contains(stdin, "Terminal commands run inside a sandbox") {
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
