package arena

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// controlClient drives a control server in-process over pipes, as an MCP
// client would over stdio.
type controlClient struct {
	t      *testing.T
	in     *io.PipeWriter
	out    *bufio.Reader
	done   chan error
	nextID int
}

func startControl(t *testing.T, srv *controlServer) *controlClient {
	t.Helper()
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &controlClient{t: t, in: inW, out: bufio.NewReader(outR), done: make(chan error, 1)}
	go func() {
		err := srv.serve(context.Background(), inR, outW)
		outW.Close()
		c.done <- err
	}()
	t.Cleanup(func() { inW.Close(); outR.Close() })
	c.request("initialize", map[string]any{"protocolVersion": "2025-06-18"})
	return c
}

func (c *controlClient) request(method string, params map[string]any) map[string]any {
	c.t.Helper()
	c.nextID++
	b, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": c.nextID, "method": method, "params": params})
	if _, err := c.in.Write(append(b, '\n')); err != nil {
		c.t.Fatal(err)
	}
	line, err := c.out.ReadBytes('\n')
	if err != nil {
		c.t.Fatalf("%s: %v", method, err)
	}
	var response map[string]any
	if err := json.Unmarshal(line, &response); err != nil {
		c.t.Fatalf("%s: %v: %s", method, err, line)
	}
	return response
}

// call returns a tool's text and whether it reported an error; a JSON-RPC
// error is returned as an error text too.
func (c *controlClient) call(tool string, args map[string]any) (string, bool) {
	c.t.Helper()
	response := c.request("tools/call", map[string]any{"name": tool, "arguments": args})
	if e, ok := response["error"].(map[string]any); ok {
		return str(e["message"]), true
	}
	result := response["result"].(map[string]any)
	content := result["content"].([]any)[0].(map[string]any)
	return str(content["text"]), result["isError"] == true
}

func (c *controlClient) mustCall(tool string, args map[string]any) string {
	c.t.Helper()
	text, isError := c.call(tool, args)
	if isError {
		c.t.Fatalf("%s: %s", tool, text)
	}
	return text
}

func (c *controlClient) close() error {
	c.in.Close()
	select {
	case err := <-c.done:
		return err
	case <-time.After(10 * time.Second):
		c.t.Fatal("control server did not stop after the client disconnected")
		return nil
	}
}

func cliOutput(t *testing.T, p *Project, args ...string) string {
	t.Helper()
	var out bytes.Buffer
	if err := NewCLI(strings.NewReader(""), &out, io.Discard).Run(context.Background(), append(args, "--project", p.Root)); err != nil {
		t.Fatalf("%v: %v", args, err)
	}
	return out.String()
}

// stubStart replaces the readiness probe and engine so a run starts without a
// provider; the run is the fixture run, as in the TUI start tests.
func stubStart(srv *controlServer, r Run, invoker Invoker) {
	srv.prepare = func(context.Context, *Project, string, string) (Run, []Readiness, error) {
		return r, []Readiness{{Provider: "codex", Model: "explicit-test-model", State: "good", CanStart: true}}, nil
	}
	srv.execute = func(ctx context.Context, p *Project, r Run) error {
		return NewEngine(p, r, invoker).Execute(ctx, false, false)
	}
}

func TestControlToolsListIsTheV1SetPlusTheTwoRequestTools(t *testing.T) {
	p := committedProject(t)
	c := startControl(t, newControlServer(p))
	response := c.request("tools/list", nil)
	names := []string{}
	for _, tool := range response["result"].(map[string]any)["tools"].([]any) {
		tool := tool.(map[string]any)
		names = append(names, str(tool["name"]))
		schema := tool["inputSchema"].(map[string]any)
		if schema["additionalProperties"] != false {
			t.Errorf("%s accepts unknown arguments", tool["name"])
		}
		for key := range schema["properties"].(map[string]any) {
			for _, forbidden := range []string{"provider", "model", "effort", "runtime", "token", "key", "credential", "answer"} {
				if strings.Contains(strings.ToLower(key), forbidden) {
					t.Errorf("%s exposes %q", tool["name"], key)
				}
			}
		}
	}
	want := []string{"arena_status", "arena_history", "arena_changes", "arena_score", "arena_workflow_show", "arena_run_start", "arena_integrate", "arena_rollback"}
	if strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("tools %v, want %v", names, want)
	}
	if text, isError := c.call("arena_ask_user", map[string]any{"question": "?"}); !isError || !strings.Contains(text, "unknown Arena tool") {
		t.Fatalf("worker tool reachable from the control server: %q", text)
	}
	if err := c.close(); err != nil {
		t.Fatal(err)
	}
}

func TestControlReadToolsMatchTheCLI(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	for _, e := range []Event{{AgentID: "head", Type: "agent.started"}, usageEvent("head", map[string]any{"input_tokens": float64(11), "output_tokens": float64(4)})} {
		if _, err := p.Append(r.ID, r.Workflow.Id, e.AgentID, e.Type, e.Data); err != nil {
			t.Fatal(err)
		}
	}
	c := startControl(t, newControlServer(p))
	defer c.close()

	if got, want := c.mustCall("arena_workflow_show", map[string]any{"workflowId": "test"}), cliOutput(t, p, "workflow", "show", "test", "--json"); got != want {
		t.Fatalf("workflow show differs:\n%s\n%s", got, want)
	}
	if got, want := c.mustCall("arena_score", map[string]any{"workflowId": "test"}), cliOutput(t, p, "score", "test", "--json"); got != want {
		t.Fatalf("score differs:\n%s\n%s", got, want)
	}
	history := c.mustCall("arena_history", map[string]any{"runId": r.ID})
	if want := cliOutput(t, p, "history", r.ID, "--json"); history != want {
		t.Fatalf("history differs:\n%s\n%s", history, want)
	}
	var h RunHistory
	if err := json.Unmarshal([]byte(history), &h); err != nil || !h.Usage.Reported || h.Usage.InputTokens != 11 {
		t.Fatalf("history lacks usage totals: %+v %v", h.Usage, err)
	}
	var recent RunHistory
	if err := json.Unmarshal([]byte(c.mustCall("arena_history", map[string]any{"runId": r.ID, "maxEvents": 1})), &recent); err != nil || len(recent.Events) != 1 || recent.Events[0].Type != "usage.reported" {
		t.Fatalf("maxEvents: %+v %v", recent.Events, err)
	}
	if got, want := c.mustCall("arena_history", nil), cliOutput(t, p, "history"); got != want {
		t.Fatalf("run list differs: %s %s", got, want)
	}
	if got, want := c.mustCall("arena_changes", map[string]any{"runId": r.ID}), cliOutput(t, p, "changes", r.ID); got != want {
		t.Fatalf("changes differ: %s %s", got, want)
	}

	var status struct {
		Workflows []string
		Readiness json.RawMessage
		RunCount  int
		Runs      []map[string]any
	}
	if err := json.Unmarshal([]byte(c.mustCall("arena_status", nil)), &status); err != nil {
		t.Fatal(err)
	}
	var wantReadiness, gotReadiness any
	if err := json.Unmarshal([]byte(cliOutput(t, p, "status", "--json")), &wantReadiness); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(status.Readiness, &gotReadiness); err != nil || fmt.Sprint(gotReadiness) != fmt.Sprint(wantReadiness) {
		t.Fatalf("readiness differs: %v %v %v", gotReadiness, wantReadiness, err)
	}
	if len(status.Workflows) != 1 || status.RunCount != 1 || status.Runs[0]["runId"] != r.ID || status.Runs[0]["status"] != "created" || status.Runs[0]["workflowId"] != "test" {
		t.Fatalf("status: %+v", status)
	}
}

func TestControlChangesShowRedactsPatchAndReadsNothingElse(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	w, err := p.Workspace(r, "head", "edit", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.Path, "new.txt"), []byte("hello\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := p.FinishWorkspace(r, w); err != nil {
		t.Fatal(err)
	}
	changes, err := p.Changes(r.ID)
	if err != nil || len(changes) != 1 {
		t.Fatalf("%v %v", changes, err)
	}
	patchPath := filepath.Join(p.RunDir(r.ID), "changes", changes[0].ID+".patch")
	patch, err := os.ReadFile(patchPath)
	if err != nil {
		t.Fatal(err)
	}
	secret := "sk-abcdefghijklmnopqrstuvwxyz0123456789"
	if err := os.WriteFile(patchPath, append(patch, []byte("+token "+secret+"\n")...), 0600); err != nil {
		t.Fatal(err)
	}
	before := p.Path()
	snapshot := dirSnapshot(t, before)
	c := startControl(t, newControlServer(p))
	defer c.close()
	text := c.mustCall("arena_changes", map[string]any{"runId": r.ID, "changeId": changes[0].ID})
	if strings.Contains(text, secret) || !strings.Contains(text, "[REDACTED]") || !strings.Contains(text, "new.txt") {
		t.Fatalf("patch not redacted: %s", text)
	}
	for _, args := range []map[string]any{{"runId": "../etc"}, {"runId": r.ID, "changeId": "../x"}, {"runId": r.ID, "extra": 1}, {}, {"runId": 7}} {
		if text, isError := c.call("arena_changes", args); !isError {
			t.Fatalf("%v accepted: %s", args, text)
		}
	}
	if after := dirSnapshot(t, before); after != snapshot {
		t.Fatalf("read tools changed the project:\n%s\n%s", snapshot, after)
	}
}

// dirSnapshot lists every file under dir with its size, ignoring the
// short-lived read locks the CLI's own readers also take.
func dirSnapshot(t *testing.T, dir string) string {
	t.Helper()
	var lines []string
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if rel, _ := filepath.Rel(dir, path); !info.IsDir() && !strings.HasPrefix(rel, "locks") {
			lines = append(lines, fmt.Sprintf("%s %d", rel, info.Size()))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return strings.Join(lines, "\n")
}

func TestControlRunStartRefusesUnreadyProviderAndUnknownWorkflow(t *testing.T) {
	p := committedProject(t)
	testRun(t, p)
	fakeCLI(t, "codex", "exit 1")
	c := startControl(t, newControlServer(p))
	defer c.close()
	text, isError := c.call("arena_run_start", map[string]any{"workflowId": "test", "task": "inspect"})
	if !isError || !strings.Contains(text, "is not ready") {
		t.Fatalf("unready provider accepted: %q %v", text, isError)
	}
	if text, isError := c.call("arena_run_start", map[string]any{"workflowId": "nope", "task": "inspect"}); !isError {
		t.Fatalf("unknown workflow accepted: %q", text)
	}
	runs, err := p.Runs()
	if err != nil || len(runs) != 1 {
		t.Fatalf("a refused start created a run: %v %v", runs, err)
	}
}

func TestControlRunStartValidatesInputsAndTakesNoRuntimeChoices(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	srv := newControlServer(p)
	prepared := 0
	stubStart(srv, r, testInvoker(func(ctx context.Context, in Invocation) (map[string]any, error) { return ordinaryResult(), nil }))
	prepare := srv.prepare
	srv.prepare = func(ctx context.Context, p *Project, w, task string) (Run, []Readiness, error) {
		prepared++
		return prepare(ctx, p, w, task)
	}
	c := startControl(t, srv)
	defer c.close()
	for name, args := range map[string]map[string]any{
		"provider":     {"workflowId": "test", "task": "x", "provider": "codex"},
		"model":        {"workflowId": "test", "task": "x", "model": "m"},
		"effort":       {"workflowId": "test", "task": "x", "effort": "high"},
		"credential":   {"workflowId": "test", "task": "x", "apiKey": "k"},
		"empty task":   {"workflowId": "test", "task": ""},
		"no task":      {"workflowId": "test"},
		"long task":    {"workflowId": "test", "task": strings.Repeat("x", maxControlTask+1)},
		"bad id":       {"workflowId": "../test", "task": "x"},
		"numeric task": {"workflowId": "test", "task": 5},
	} {
		if text, isError := c.call("arena_run_start", args); !isError {
			t.Errorf("%s accepted: %s", name, text)
		}
	}
	if prepared != 0 {
		t.Fatal("an invalid request reached run preparation")
	}
}

func TestControlRunStartRunsTheWorkflowAndReportsItsID(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	srv := newControlServer(p)
	var task string
	stubStart(srv, r, testInvoker(func(ctx context.Context, in Invocation) (map[string]any, error) { return ordinaryResult(), nil }))
	prepare := srv.prepare
	srv.prepare = func(ctx context.Context, p *Project, w, t string) (Run, []Readiness, error) {
		task = w + ":" + t
		return prepare(ctx, p, w, t)
	}
	c := startControl(t, srv)
	var started struct {
		RunID, WorkflowID, State, Supervision string
	}
	if err := json.Unmarshal([]byte(c.mustCall("arena_run_start", map[string]any{"workflowId": "test", "task": "Inspect  the baseline"})), &started); err != nil {
		t.Fatal(err)
	}
	if started.RunID != r.ID || started.WorkflowID != "test" || started.State != "started" || !strings.Contains(started.Supervision, "arena resume "+r.ID) || task != "test:Inspect  the baseline" {
		t.Fatalf("%+v %q", started, task)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		var h RunHistory
		if err := json.Unmarshal([]byte(c.mustCall("arena_history", map[string]any{"runId": r.ID})), &h); err != nil {
			t.Fatal(err)
		}
		if h.Terminal {
			if h.Status != "succeeded" {
				t.Fatalf("run ended %s", h.Status)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("run did not finish: %s", h.Status)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err := c.close(); err != nil {
		t.Fatal(err)
	}
}

func TestControlRunStartIsCappedPerSession(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	srv := newControlServer(p)
	stubStart(srv, r, testInvoker(func(ctx context.Context, in Invocation) (map[string]any, error) { return ordinaryResult(), nil }))
	srv.started = maxControlRuns
	c := startControl(t, srv)
	defer c.close()
	if text, isError := c.call("arena_run_start", map[string]any{"workflowId": "test", "task": "x"}); !isError || !strings.Contains(text, "already started") {
		t.Fatalf("run cap not enforced: %q", text)
	}
}

func TestControlDisconnectInterruptsRunsAndLeavesThemResumable(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	if _, err := p.PrepareBaseline(r); err != nil {
		t.Fatal(err)
	}
	srv := newControlServer(p)
	invoked := make(chan struct{})
	stubStart(srv, r, testInvoker(func(ctx context.Context, in Invocation) (map[string]any, error) {
		close(invoked)
		<-ctx.Done()
		return nil, ctx.Err()
	}))
	c := startControl(t, srv)
	c.mustCall("arena_run_start", map[string]any{"workflowId": "test", "task": "wait"})
	select {
	case <-invoked:
	case <-time.After(20 * time.Second):
		t.Fatal("engine never invoked the agent")
	}
	if err := c.close(); err != nil {
		t.Fatal(err)
	}
	state, err := p.State(r.ID)
	if err != nil || state.Status != "interrupted" || RunAlive(p, r.ID) {
		t.Fatalf("disconnect did not interrupt the run: %+v %v", state, err)
	}
	events, err := p.Events(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	interrupted := false
	for _, e := range events {
		interrupted = interrupted || e.Type == "workflow.interrupted"
	}
	if !interrupted {
		t.Fatal("workflow.interrupted was not journaled")
	}
}

func TestControlServerCapsToolCallsPerSession(t *testing.T) {
	p := committedProject(t)
	c := startControl(t, newControlServer(p))
	defer c.close()
	for i := 0; i < 1000; i++ {
		c.mustCall("arena_history", nil)
	}
	if text, isError := c.call("arena_history", nil); !isError || !strings.Contains(text, "invocation limit") {
		t.Fatalf("call cap not enforced: %q", text)
	}
}

func TestControlRedactsErrorsAndRejectsOversizedResults(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	for i := 0; i < 3; i++ {
		if _, err := p.Append(r.ID, r.Workflow.Id, "head", "message.note", map[string]any{"text": strings.Repeat("x", maxControlOutput/2)}); err != nil {
			t.Fatal(err)
		}
	}
	c := startControl(t, newControlServer(p))
	defer c.close()
	if text, isError := c.call("arena_history", map[string]any{"runId": r.ID}); !isError || !strings.Contains(text, "exceeds") {
		t.Fatalf("oversized history returned: %.80q", text)
	}
	if text := c.mustCall("arena_history", map[string]any{"runId": r.ID, "maxEvents": 1}); len(text) > maxControlOutput {
		t.Fatal("maxEvents did not bound the result")
	}
	secret := "sk-abcdefghijklmnopqrstuvwxyz0123456789"
	if text, _ := c.call("arena_score", map[string]any{"workflowId": secret}); strings.Contains(text, secret) {
		t.Fatalf("error leaked a secret: %s", text)
	}
}

func TestMCPControlFlagServesTheControlServerOnly(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	input := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}
{"jsonrpc":"2.0","id":2,"method":"tools/list"}
`
	var out bytes.Buffer
	if err := NewCLI(strings.NewReader(input), &out, io.Discard).Run(context.Background(), []string{"mcp", "--control", "--project", p.Root}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "arena_run_start") || strings.Contains(out.String(), "arena_send") {
		t.Fatalf("wrong tool set: %s", out.String())
	}
	for _, args := range [][]string{{"mcp", "--control", "--run", r.ID}, {"mcp", "--control", "--actor", "head"}} {
		if err := NewCLI(strings.NewReader(""), io.Discard, io.Discard).Run(context.Background(), append(args, "--project", p.Root)); err == nil {
			t.Fatalf("%v accepted", args)
		}
	}
	// The worker bridge is unchanged: it still serves one actor's four tools.
	out.Reset()
	if err := NewCLI(strings.NewReader(input), &out, io.Discard).Run(context.Background(), []string{"mcp", "--run", r.ID, "--actor", "head", "--project", p.Root}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"arena_send", "arena_inbox", "arena_context_search", "arena_ask_user"} {
		if !strings.Contains(out.String(), name) {
			t.Fatalf("worker bridge lost %s", name)
		}
	}
	if strings.Contains(out.String(), "arena_run_start") {
		t.Fatal("worker bridge gained a control tool")
	}
}
