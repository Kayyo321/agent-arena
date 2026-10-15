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
