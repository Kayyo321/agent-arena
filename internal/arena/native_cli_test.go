package arena

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeCLI installs an executable script named name first on PATH. It records
// its argv (one per line) and stdin under record, then runs body.
func fakeCLI(t *testing.T, name, body string) (record string) {
	return fakeCLIReading(t, name, "cat", body)
}

// fakeCLIReading is fakeCLI with a custom stdin reader, for CLIs that read
// stdin turn by turn instead of to EOF.
func fakeCLIReading(t *testing.T, name, reader, body string) (record string) {
	t.Helper()
	bin, record := t.TempDir(), t.TempDir()
	script := "#!/bin/sh\nfor a in \"$@\"; do printf '%s\\n' \"$a\"; done > " + record + "/args\n" + reader + " > " + record + "/stdin\n" + body
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	return record
}

func recorded(t *testing.T, record, file string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(record, file))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

const claudeFixtureStream = `cat <<'EOF'
{"type":"system","subtype":"init","mcp_servers":[]}
{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Read","input":{"file_path":"a.go"}}]}}
{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"x"}]}}
{"type":"assistant","message":{"content":[{"type":"text","text":"Read the file."}]}}
{"type":"result","subtype":"success","is_error":false,"result":"{}","structured_output":{"summary":"fixture"},"usage":{"output_tokens":3}}
EOF
`

func TestClaudeSignInWorkerRunsRestrictedBackgroundCLI(t *testing.T) {
	record := fakeCLI(t, "claude", claudeFixtureStream)
	events, outputs, usage := []map[string]any{}, []string{}, 0
	in := Invocation{Runtime: Runtime{Provider: "claude-code", Model: "fixture", ReasoningEffort: "low", Options: map[string]any{"authMethod": claudeSignInAuth}}, Dir: t.TempDir(), Prompt: "Return fixture", Schema: objectSchema(map[string]any{"summary": map[string]any{"type": "string"}}),
		Diagnostic: func(kind string, data map[string]any) error { events = append(events, data); return nil },
		Output:     func(text string) error { outputs = append(outputs, text); return nil },
		Usage:      func(map[string]any) error { usage++; return nil }}
	out, err := (Providers{}).Invoke(context.Background(), in)
	if err != nil || out["summary"] != "fixture" {
		t.Fatalf("result: %v %v", out, err)
	}
	args := strings.Split(recorded(t, record, "args"), "\n")
	for _, want := range []string{"-p", "--tools=Read,Glob,Grep", "dontAsk", "--json-schema", "--strict-mcp-config", "--disable-slash-commands"} {
		if !Has(args, want) {
			t.Fatalf("missing %s in %q", want, args)
		}
	}
	for _, refused := range []string{"--bare", "--mcp-config", "--settings", "Bash"} {
		if Has(args, refused) {
			t.Fatalf("read-only setup call passed %s: %q", refused, args)
		}
	}
	if !strings.Contains(recorded(t, record, "stdin"), "Return fixture") {
		t.Fatal("prompt must arrive on stdin")
	}
	if len(events) != 2 || events[0]["tool"] != "Read" || events[0]["status"] != "started" || events[1]["status"] != "completed" {
		t.Fatalf("activity: %v", events)
	}
	if len(outputs) != 1 || usage != 1 {
		t.Fatalf("output %v usage %d", outputs, usage)
	}
}

func TestClaudeSignInWriterGetsEditsAndSandboxedBashOnly(t *testing.T) {
	record := fakeCLI(t, "claude", claudeFixtureStream)
	in := Invocation{Runtime: Runtime{Provider: "claude-code", Model: "fixture", ReasoningEffort: "low", Options: map[string]any{"authMethod": claudeSignInAuth}}, Dir: t.TempDir(), Prompt: "x", Write: true, Commands: true}
	if _, err := invokeClaudeNative(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	args := recorded(t, record, "args")
	if !strings.Contains(args, "--tools=Read,Glob,Grep,Edit,Write,NotebookEdit,Bash") || !strings.Contains(args, "acceptEdits") || !strings.Contains(args, `"allowUnsandboxedCommands":false`) {
		t.Fatalf("writer args: %s", args)
	}
	if strings.Contains(args, "--allowedTools=Bash") || strings.Contains(args, "bypassPermissions") {
		t.Fatalf("commands must not be pre-approved: %s", args)
	}
}

func TestClaudeSignInReportsProviderFailure(t *testing.T) {
	fakeCLI(t, "claude", `echo '{"type":"result","subtype":"success","is_error":true,"result":"Not logged in · Please run /login"}'; exit 1`)
	in := Invocation{Runtime: Runtime{Provider: "claude-code", Model: "fixture", ReasoningEffort: "low", Options: map[string]any{"authMethod": claudeSignInAuth}}, Dir: t.TempDir(), Prompt: "x"}
	if _, err := invokeClaudeNative(context.Background(), in); err == nil || !strings.Contains(err.Error(), "Not logged in") {
		t.Fatalf("want sign-in failure, got %v", err)
	}
}

func TestAntigravityWorkerIsFencedToItsWorktree(t *testing.T) {
	if !writeFenceAvailable() {
		t.Skip("no write fence on this host")
	}
	root := t.TempDir()
	tree := filepath.Join(root, ".arena", ".state", "runs", "r1", "trees", "task")
	if err := os.MkdirAll(tree, 0755); err != nil {
		t.Fatal(err)
	}
	probe := t.TempDir()
	record := fakeCLIReading(t, "agy", "head -n 1", `
touch "`+root+`/escaped" 2>/dev/null && echo escaped >> `+probe+`/log
touch inside 2>/dev/null && echo inside >> `+probe+`/log
cat <<'EOF'
{"event":"init","init":{"model":"fixture"}}
{"event":"step_update","step_update":{"step_index":1,"state":"ACTIVE","step_type":"tool","tool_name":"view_file","tool_info":{"name":"view_file","parameters":{}}}}
{"event":"step_update","step_update":{"step_index":1,"state":"DONE","step_type":"tool","tool_name":"view_file","tool_info":{"name":"view_file","parameters":{}}}}
{"event":"step_update","step_update":{"step_index":2,"state":"ACTIVE","step_type":"agent_response","text_delta":"Done"}}
{"event":"step_update","step_update":{"step_index":2,"state":"DONE","step_type":"agent_response","text_delta":"."}}
{"event":"result","result":{"status":"SUCCESS","response":"{}","structured_output":{"summary":"fixture"},"usage":{"total_tokens":5}}}
EOF
`)
	run := func(write bool) string {
		_ = os.Remove(filepath.Join(probe, "log"))
		outputs := []string{}
		in := Invocation{Runtime: Runtime{Provider: "antigravity", Model: "fixture"}, ProjectRoot: root, Dir: tree, Prompt: "Return fixture", Write: write, Schema: objectSchema(map[string]any{"summary": map[string]any{"type": "string"}}), Output: func(text string) error { outputs = append(outputs, text); return nil }}
		out, err := (Providers{}).Invoke(context.Background(), in)
		if err != nil || out["summary"] != "fixture" || len(outputs) != 1 || outputs[0] != "Done." {
			t.Fatalf("result: %v %v %v", out, err, outputs)
		}
		log, _ := os.ReadFile(filepath.Join(probe, "log"))
		return string(log)
	}
	if log := run(false); log != "" {
		t.Fatalf("read-only worker wrote: %q", log)
	}
	if log := run(true); log != "inside\n" {
		t.Fatalf("writer may write only its worktree: %q", log)
	}
	args := strings.Split(recorded(t, record, "args"), "\n")
	for _, want := range []string{"--input-format", "stream-json", "--json-schema", "--disable-slash-commands"} {
		if !Has(args, want) {
			t.Fatalf("missing %s in %q", want, args)
		}
	}
	if Has(args, "--dangerously-skip-permissions") || Has(args, "--effort") {
		t.Fatalf("unexpected args %q", args)
	}
	if stdin := recorded(t, record, "stdin"); !strings.Contains(stdin, `"event":"user"`) || !strings.Contains(stdin, "Return fixture") {
		t.Fatalf("stdin: %s", stdin)
	}
}

func TestAntigravityRefusesCommandsAndOptions(t *testing.T) {
	in := Invocation{Runtime: Runtime{Provider: "antigravity", Model: "fixture"}, Dir: t.TempDir(), Commands: true}
	if _, err := invokeAntigravity(context.Background(), in); err == nil {
		t.Fatal("commands must be refused")
	}
	if err := ValidateRuntime(Runtime{Provider: "antigravity", Model: "m", Options: map[string]any{"backend": "x"}}); err == nil {
		t.Fatal("antigravity options must be refused")
	}
	if err := ValidateRuntime(Runtime{Provider: "antigravity", Model: "m"}); err != nil {
		t.Fatal(err)
	}
	if err := ValidateRuntime(Runtime{Provider: "claude-code", Model: "m", ReasoningEffort: "low", Options: map[string]any{"authMethod": "oauth-token"}}); err == nil {
		t.Fatal("unknown Claude auth method must be refused")
	}
}

func TestHelpEffortsReadsBothCLIFormats(t *testing.T) {
	claude := "  --effort <level>                      Effort level for the current session\n                                        (low, medium, high, xhigh, max)\n"
	agy := "  --effort                        Reasoning effort for the current CLI session (low|medium|high|xhigh|max)\n"
	for _, help := range []string{claude, agy} {
		if got := helpEfforts(help); strings.Join(got, ",") != "low,medium,high,xhigh,max" {
			t.Fatalf("%q -> %v", help, got)
		}
	}
}

func TestAntigravityFollowsUpOnceAfterDeniedCommand(t *testing.T) {
	if !writeFenceAvailable() {
		t.Skip("no write fence on this host")
	}
	probe := t.TempDir()
	fakeCLIReading(t, "agy", "head -n 1", `
echo '{"event":"result","result":{"status":"SUCCESS","response":"","denied_actions":[{"action":"command","display_name":"RunCommand"}]}}'
read -r next; printf '%s\n' "$next" > `+probe+`/followup
echo '{"event":"result","result":{"status":"SUCCESS","response":"{}","structured_output":{"summary":"fixture"}}}'
`)
	denied := 0
	in := Invocation{Runtime: Runtime{Provider: "antigravity", Model: "fixture"}, Dir: t.TempDir(), Prompt: "x", Diagnostic: func(kind string, _ map[string]any) error {
		if kind == "provider.permission-denied" {
			denied++
		}
		return nil
	}}
	out, err := invokeAntigravity(context.Background(), in)
	if err != nil || out["summary"] != "fixture" || denied != 1 {
		t.Fatalf("follow-up: %v %v denied=%d", out, err, denied)
	}
	if followup := recorded(t, probe, "followup"); !strings.Contains(followup, "list_dir") {
		t.Fatalf("follow-up turn: %s", followup)
	}
}
