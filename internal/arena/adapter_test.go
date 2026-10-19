package arena

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalHTTPContractFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/tags":
			fmt.Fprint(w, `{"models":[{"name":"fixture-model"}]}`)
		case "/api/chat":
			w.Header().Set("Content-Type", "application/x-ndjson")
			fmt.Fprintln(w, `{"message":{"content":"{\"action\":\"complete\",\"result\":{\"summary\":\"fixture\"}}"},"done":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	runtime := Runtime{Provider: "local", Model: "fixture-model", Options: map[string]any{"backend": "ollama", "endpoint": server.URL}}
	status := Probe(context.Background(), ".", runtime)
	if !status.CanStart {
		t.Fatal(status)
	}
	out, e := invokeLocal(context.Background(), Invocation{Runtime: runtime, Prompt: "Return fixture", Dir: t.TempDir(), Schema: objectSchema(map[string]any{"summary": map[string]any{"type": "string"}})})
	if e != nil || out["summary"] != "fixture" {
		t.Fatalf("HTTP contract: %v %v", out, e)
	}
}
func TestMediatedFileToolsEnforceWorkspaceAndProfile(t *testing.T) {
	root := t.TempDir()
	in := Invocation{Dir: root}
	if _, e := performTool(context.Background(), in, "write_file", map[string]any{"path": "file.txt", "content": "x"}); e == nil {
		t.Fatal("read-only writer accepted")
	}
	in.Write = true
	for _, path := range []string{"../outside", ".git/config", ".arena/settings.arena.json", ".env"} {
		if _, e := performTool(context.Background(), in, "write_file", map[string]any{"path": path, "content": "x"}); e == nil {
			t.Fatalf("accepted protected path %s", path)
		}
	}
	if _, e := performTool(context.Background(), in, "write_file", map[string]any{"path": "safe.txt", "content": "safe"}); e != nil {
		t.Fatal(e)
	}
	b, e := os.ReadFile(filepath.Join(root, "safe.txt"))
	if e != nil || string(b) != "safe" {
		t.Fatal("valid write failed")
	}
}

func TestCodexConfirmsOnlyItsArenaBridgeAndPreservesExplicitReview(t *testing.T) {
	base := map[string]any{"serverName": "arena", "threadId": "thread-test", "mode": "form", "requestedSchema": map[string]any{"type": "object", "properties": map[string]any{}}, "_meta": map[string]any{"codex_approval_kind": "mcp_tool_call"}}
	if !arenaToolConfirmation(base, "thread-test") {
		t.Fatal("known bridge confirmation refused")
	}
	for _, change := range []map[string]any{{"serverName": "external"}, {"threadId": "another-thread"}, {"mode": "url"}, {"requestedSchema": map[string]any{"type": "object", "properties": map[string]any{"credential": map[string]any{"type": "string"}}}}, {"_meta": map[string]any{"codex_approval_kind": "browser_auth"}}} {
		params := convert[map[string]any](base)
		for key, value := range change {
			params[key] = value
		}
		if arenaToolConfirmation(params, "thread-test") {
			t.Fatalf("accepted unrelated or input-requiring confirmation: %v", change)
		}
	}
	for _, key := range []string{"codex_requires_user_input", "codex_sensitive_action", "codex_strict_auto_review"} {
		params := convert[map[string]any](base)
		params["_meta"].(map[string]any)[key] = true
		if arenaToolConfirmation(params, "thread-test") {
			t.Fatalf("bypassed %s", key)
		}
	}
}
func TestSkillUpdatesPreserveUserEdits(t *testing.T) {
	p := testProject(t)
	path, e := p.InstallSkill("codex", false, false)
	if e != nil {
		t.Fatal(e)
	}
	original, e := os.ReadFile(path)
	if e != nil {
		t.Fatal(e)
	}
	changed := append(original, []byte("\nUser change\n")...)
	if e := os.WriteFile(path, changed, 0644); e != nil {
		t.Fatal(e)
	}
	if _, e := p.InstallSkill("codex", false, false); e == nil {
		t.Fatal("overwrote modified skill without replace")
	}
	if _, e := p.InstallSkill("codex", false, true); e != nil {
		t.Fatal(e)
	}
	backups, e := filepath.Glob(p.Path(".state", "skill-backups", "*.md"))
	if e != nil || len(backups) != 1 {
		t.Fatal("missing skill backup")
	}
	b, e := os.ReadFile(backups[0])
	if e != nil || !strings.Contains(string(b), "User change") {
		t.Fatal("user skill edit lost")
	}
	for _, target := range []string{"claude", "gemini", "gemini-native", "copilot", "opencode"} {
		installed, e := p.InstallSkill(target, false, false)
		if e != nil {
			t.Fatal(e)
		}
		b, e := os.ReadFile(installed)
		if e != nil || Hash(b) != Hash(original) {
			t.Fatal("installed skills diverge from canonical source")
		}
	}
}

// Contract coverage through the fake CLIs of opencode_cli_test.go; none of this
// runs OpenCode or any inference.

// Probe sends opencode to its own readiness check. A provider with a binary
// but no branch of its own would be answered "adapter not installed", or, if
// mapped to a binary, by the Codex app-server probe, so a Codex that fails
// the test when it is started sits on PATH beside the fake opencode.
func TestProbeRoutesOpenCodeToItsOwnProbe(t *testing.T) {
	codexRan := filepath.Join(t.TempDir(), "codex-ran")
	_, capture := openCodeProbeCLI{}.install(t)
	fakeCLI(t, "codex", "touch '"+codexRan+"'\nexit 1\n")
	runtime := Runtime{Provider: "opencode", Model: "opencode/cat-jam-free", ReasoningEffort: "xhigh"}
	got := Probe(context.Background(), t.TempDir(), runtime)
	if !got.CanStart || got.State != "good" || got.Provider != "opencode" || got.Version != openCodeVersion || !strings.Contains(got.Compatibility, "matches") || strings.Join(got.Efforts, ",") != "low,medium,high,xhigh,max" {
		t.Fatalf("readiness: %+v", got)
	}
	if !strings.Contains(got.Detail, "live inference is unverified") {
		t.Fatalf("readiness must not claim verified inference: %q", got.Detail)
	}
	if _, err := os.Stat(codexRan); err == nil {
		t.Fatal("the Codex probe ran for an OpenCode runtime")
	}
	if calls := recorded(t, capture, "calls"); calls != openCodeProbeThroughCatalog {
		t.Fatalf("a probe may only make the read-only calls: %q", calls)
	}

	// The OpenCode probe's own refusals reach the caller unchanged.
	openCodeProbeCLI{auth: openCodeAuthSignedOut}.install(t)
	if got := Probe(context.Background(), t.TempDir(), runtime); got.CanStart || got.State != "signed out" {
		t.Fatalf("signed out: %+v", got)
	}
	// So does a user configuration that gives workers more tools than Arena's map.
	openCodeProbeCLI{agentReader: openCodeCat(t, openCodeAgentReaderWidened)}.install(t)
	if got := Probe(context.Background(), t.TempDir(), runtime); got.CanStart || got.State != "unsupported" || !strings.Contains(got.Detail, "codesearch, mymcp_*") {
		t.Fatalf("widened configuration: %+v", got)
	}
	t.Setenv("PATH", t.TempDir())
	if got := Probe(context.Background(), t.TempDir(), runtime); got.CanStart || got.State != "missing" || !strings.Contains(got.Detail, "opencode is not installed") {
		t.Fatalf("missing binary: %+v", got)
	}
}

// OpenCode has an opencode skill install target, as Gemini has a gemini one, but a
// worker does not read a host copy, so its readiness reports the embedded skill
// that Gemini and the local provider report, not "unknown host", whether or not a
// host copy exists.
func TestProviderSkillStateOfOpenCodeMatchesGeminiAndLocal(t *testing.T) {
	p := testProject(t)
	want := providerSkillState(p.Root, "gemini")
	if want != "canonical skill embedded for Arena control calls" || providerSkillState(p.Root, "local") != want {
		t.Fatalf("gemini and local: %q", want)
	}
	if got := providerSkillState(p.Root, "opencode"); got != want {
		t.Fatalf("opencode without a host copy: %q, want %q", got, want)
	}
	if _, err := p.InstallSkill("opencode", false, false); err != nil {
		t.Fatal(err)
	}
	if got := providerSkillState(p.Root, "opencode"); got != want {
		t.Fatalf("opencode with a host copy: %q, want %q", got, want)
	}
	if got := providerSkillState(p.Root, "not-a-provider"); got != "unknown host" {
		t.Fatalf("unknown providers must still read as unknown: %q", got)
	}
}

// Providers.Invoke hands an OpenCode agent to invokeOpenCode.
func TestProvidersInvokeRunsOpenCode(t *testing.T) {
	record, capture := openCodeFake(t, openCodeFixtureStream)
	in := openCodeInvocation(t)
	out, err := Providers{}.Invoke(context.Background(), in)
	if err != nil || out["summary"] != "fixture" {
		t.Fatalf("invoke: %v %v", out, err)
	}
	if args := recorded(t, record, "args"); !strings.HasPrefix(args, "run\n--pure\n--format\njson\n") || !strings.Contains(args, "--model\nopencode/cat-jam-free\n") {
		t.Fatalf("argv: %q", args)
	}
	if recorded(t, capture, "projectconfig") != "1" {
		t.Fatal("a worker must ignore the worktree's own opencode configuration")
	}
	// The adapter's refusals come through the same entry point.
	in.Commands = true
	callsBefore := recorded(t, capture, "calls")
	if _, err := (Providers{}).Invoke(context.Background(), in); err == nil || !strings.Contains(err.Error(), "no command sandbox") {
		t.Fatalf("run_commands: %v", err)
	}
	if recorded(t, capture, "calls") != callsBefore {
		t.Fatal("a refused invocation must not start opencode")
	}
}
