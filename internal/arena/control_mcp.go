package arena

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
)

// The control server is a project-scoped MCP server (`arena mcp --control`)
// that lets another agent or an IDE read Arena state and start runs of saved
// workflows. It is separate from the per-agent worker bridge in mcp.go and
// exposes no credential, decision-answering or workflow-edit tool. Its
// integrate and rollback tools only open a pending decision for a human.
const (
	// maxControlTask bounds the task string of arena_run_start, in bytes.
	maxControlTask = 8 << 10
	// maxControlRuns bounds the runs one control session may start.
	maxControlRuns = 3
	// maxControlOutput bounds one tool result, in bytes.
	maxControlOutput = 1 << 20
	// maxControlPatch bounds the patch text arena_changes returns for a change.
	maxControlPatch = 256 << 10
	// controlRunsShown is how many of the newest runs arena_status lists.
	controlRunsShown = 20
)

type controlServer struct {
	p       *Project
	prepare func(context.Context, *Project, string, string) (Run, []Readiness, error)
	execute func(context.Context, *Project, Run) error

	ctx     context.Context // lives as long as the MCP session; runs derive from it
	workers sync.WaitGroup
	started int
}

func newControlServer(p *Project) *controlServer {
	return &controlServer{p: p, prepare: prepareNewExecution, execute: func(ctx context.Context, p *Project, r Run) error {
		return NewEngine(p, r, Providers{}).Execute(ctx, false, false)
	}}
}

// ServeControlMCP serves the control tools on in/out until the client
// disconnects or ctx ends. Runs it started are supervised by this process: when
// the session ends they are cancelled, journaled workflow.interrupted, and
// stay resumable with `arena resume`. ServeControlMCP returns only after they
// have stopped.
func ServeControlMCP(ctx context.Context, p *Project, in io.Reader, out io.Writer) error {
	return newControlServer(p).serve(ctx, in, out)
}

func (c *controlServer) serve(ctx context.Context, in io.Reader, out io.Writer) error {
	ctx, cancel := context.WithCancel(ctx)
	c.ctx = ctx
	// Reading goes through a pipe so cancelling ctx (a signal) ends the session
	// even while the client is silent.
	pr, pw := io.Pipe()
	defer pr.Close()
	go func() { _, err := io.Copy(pw, in); pw.CloseWithError(err) }()
	go func() { <-ctx.Done(); pw.Close() }()
	err := serveMCPLoop(ctx, pr, out, c.tools, c.call)
	cancel()
	c.workers.Wait()
	return err
}

func controlTool(name, description string, readOnly bool, required []string, properties map[string]any) map[string]any {
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return map[string]any{"name": name, "description": description, "inputSchema": schema, "annotations": map[string]any{"readOnlyHint": readOnly, "destructiveHint": false}}
}

func (c *controlServer) tools() []any {
	id := func(what string) map[string]any {
		return map[string]any{"type": "string", "pattern": idPattern.String(), "description": what}
	}
	return []any{
		controlTool("arena_status", "Project readiness and run states: the workflows in this project, the provider readiness `arena status --json` reports for them (or for one workflow), and the state of the newest runs. Read-only.", true, nil, map[string]any{"workflowId": id("Limit readiness to this workflow.")}),
		controlTool("arena_history", "Run history as `arena history RUN --json` reports it: status, pending decisions, usage totals and journaled events. Without runId, lists run IDs. Read-only.", true, nil, map[string]any{"runId": id("Run to read."), "maxEvents": map[string]any{"type": "integer", "minimum": 1, "description": "Return only the newest N events."}}),
		controlTool("arena_changes", "Change proposals recorded by a run, read-only. Without changeId, lists them as `arena changes RUN` does; with it, returns that change and its redacted patch. Integrating or rolling back is only requested, with arena_integrate and arena_rollback.", true, []string{"runId"}, map[string]any{"runId": id("Run that produced the changes."), "changeId": id("Show one change and its patch.")}),
		controlTool("arena_score", "The scorecard `arena score WORKFLOW --json` reports for a saved workflow. Read-only.", true, []string{"workflowId"}, map[string]any{"workflowId": id("Saved workflow to score.")}),
		controlTool("arena_workflow_show", "The saved workflow `arena workflow show WORKFLOW --json` reports. Read-only.", true, []string{"workflowId"}, map[string]any{"workflowId": id("Saved workflow to show.")}),
		controlTool("arena_run_start", fmt.Sprintf("Start a run of an existing saved, validated workflow with a task and return its run ID at once. Provider, model, effort and credentials cannot be chosen: the workflow's own runtime is used, and the run is refused if a provider is not ready or a required sandbox is missing. Arena supervises the run in this server process for as long as this MCP session stays open; if the client disconnects the run is interrupted and a human can continue it with `arena resume RUN`. Approvals and budget questions stay pending for the human (TUI or `arena decision answer`). At most %d runs per session.", maxControlRuns), false, []string{"workflowId", "task"}, map[string]any{"workflowId": id("Saved workflow to run."), "task": map[string]any{"type": "string", "minLength": 1, "maxLength": maxControlTask, "description": "What the workflow should do."}}),
		controlTool("arena_integrate", "Ask a human to integrate a proposed change. Nothing is integrated: Arena runs the checks `arena integrate` makes (change is proposed, prerequisites integrated, checkout clean, patch applies, run is not retired or imported), then opens a pending decision with the preview and choices approve and reject, and returns its ID at once. Only a human answering it (TUI, or `arena decision answer RUN ID approve` in a terminal) integrates the change, once; a decision for the same change that is still open is returned instead of a new one. If HEAD moves before the answer, approval is refused and the request must be made again. This server cannot answer decisions.", false, []string{"runId", "changeId"}, map[string]any{"runId": id("Run that produced the change."), "changeId": id("Change to integrate.")}),
		controlTool("arena_rollback", "Ask a human to roll back a change. Nothing is rolled back: Arena runs the checks `arena rollback` makes (including that the change is not already rolled back), then opens a pending decision carrying the rollback preview with choices approve and reject, and returns its ID at once. Only a human answering it (TUI, or `arena decision answer RUN ID approve` in a terminal) performs the rollback, once, as a reversible revert commit or isolated recovery worktree that keeps later work; a decision for the same change that is still open is returned instead of a new one. If HEAD moves before the answer, approval is refused and the request must be made again. This server cannot answer decisions.", false, []string{"runId", "changeId"}, map[string]any{"runId": id("Run that produced the change."), "changeId": id("Change to roll back.")}),
	}
}

func (c *controlServer) call(name string, args map[string]any) (any, error) {
	var value any
	var err error
	switch name {
	case "arena_status":
		value, err = c.status(args)
	case "arena_history":
		value, err = c.history(args)
	case "arena_changes":
		value, err = c.changes(args)
	case "arena_score":
		var id string
		if id, err = controlID(args, "workflowId", true, "workflowId"); err == nil {
			var w Workflow
			var profiles map[string]Profile
			if w, profiles, err = c.p.Load(id); err == nil {
				value = ScoreArena(w, profiles)
			}
		}
	case "arena_workflow_show":
		var id string
		if id, err = controlID(args, "workflowId", true, "workflowId"); err == nil {
			value, _, err = c.p.Load(id)
		}
	case "arena_run_start":
		value, err = c.runStart(args)
	case "arena_integrate":
		value, err = c.requestChange("integrate", args)
	case "arena_rollback":
		value, err = c.requestChange("rollback", args)
	default:
		err = fmt.Errorf("unknown Arena tool")
	}
	if err == nil && len(SafeJSON(value)) > maxControlOutput {
		return nil, fmt.Errorf("result exceeds %d bytes; narrow the request (for example maxEvents)", maxControlOutput)
	}
	return value, err
}

// controlID reads an optional or required ID argument and rejects any
// argument outside allowed, so a client cannot smuggle runtime choices.
func controlID(args map[string]any, key string, required bool, allowed ...string) (string, error) {
	for k := range args {
		if !Has(allowed, k) {
			return "", fmt.Errorf("unknown argument %q", k)
		}
	}
	return controlIDValue(args, key, required)
}

func controlIDValue(args map[string]any, key string, required bool) (string, error) {
	v, present := args[key]
	if !present || v == nil {
		if required {
			return "", fmt.Errorf("%s is required", key)
		}
		return "", nil
	}
	s, ok := v.(string)
	if !ok {
		return "", fmt.Errorf("%s must be a string", key)
	}
	return s, validID(s)
}

func controlOnly(args map[string]any, allowed ...string) error {
	for k := range args {
		if !Has(allowed, k) {
			return fmt.Errorf("unknown argument %q", k)
		}
	}
	return nil
}

func (c *controlServer) status(args map[string]any) (any, error) {
	workflowID, err := controlID(args, "workflowId", false, "workflowId")
	if err != nil {
		return nil, err
	}
	readiness, err := c.p.readiness(c.ctx, workflowID)
	if err != nil {
		return nil, err
	}
	workflows, err := c.p.List()
	if err != nil {
		return nil, err
	}
	ids, err := c.p.Runs()
	if err != nil {
		return nil, err
	}
	shown := ids[max(len(ids)-controlRunsShown, 0):]
	runs := []map[string]any{}
	for _, id := range shown {
		entry := map[string]any{"runId": id}
		if r, err := c.p.ReadRun(id); err == nil {
			entry["workflowId"] = r.Workflow.Id
		}
		if s, err := c.p.State(id); err == nil {
			entry["status"] = s.Status
		} else {
			entry["status"] = "invalid"
			entry["error"] = Redact(err.Error())
		}
		runs = append(runs, entry)
	}
	return map[string]any{"workflows": workflows, "readiness": readiness, "runCount": len(ids), "runs": runs}, nil
}

func (c *controlServer) history(args map[string]any) (any, error) {
	runID, err := controlID(args, "runId", false, "runId", "maxEvents")
	if err != nil {
		return nil, err
	}
	if runID == "" {
		if _, ok := args["maxEvents"]; ok {
			return nil, fmt.Errorf("maxEvents needs runId")
		}
		return c.p.Runs()
	}
	events, err := c.p.Events(runID)
	if err != nil {
		return nil, err
	}
	h, err := c.p.History(runID, events)
	if err != nil {
		return nil, err
	}
	if v, ok := args["maxEvents"]; ok {
		n, isNumber := v.(float64)
		if !isNumber || n < 1 || n != float64(int(n)) {
			return nil, fmt.Errorf("maxEvents must be a positive integer")
		}
		if int(n) < len(h.Events) {
			h.Events = h.Events[len(h.Events)-int(n):]
		}
	}
	return h, nil
}

func (c *controlServer) changes(args map[string]any) (any, error) {
	if err := controlOnly(args, "runId", "changeId"); err != nil {
		return nil, err
	}
	runID, err := controlIDValue(args, "runId", true)
	if err != nil {
		return nil, err
	}
	changeID, err := controlIDValue(args, "changeId", false)
	if err != nil {
		return nil, err
	}
	if changeID == "" {
		return c.p.Changes(runID)
	}
	change, err := c.p.Change(runID, changeID)
	if err != nil {
		return nil, err
	}
	patch, err := os.ReadFile(filepath.Join(c.p.RunDir(runID), "changes", change.ID+".patch"))
	if err != nil {
		return nil, err
	}
	truncated := len(patch) > maxControlPatch
	if truncated {
		patch = patch[:maxControlPatch]
	}
	return map[string]any{"change": change, "patch": Redact(string(patch)), "patchTruncated": truncated}, nil
}

// runStart applies the same readiness, policy and isolation checks as
// `arena run` (prepareNewExecution), then lets this server's engine goroutine
// supervise the run. It never takes a runtime from the client.
func (c *controlServer) runStart(args map[string]any) (any, error) {
	workflowID, err := controlID(args, "workflowId", true, "workflowId", "task")
	if err != nil {
		return nil, err
	}
	task, ok := args["task"].(string)
	if !ok || task == "" {
		return nil, fmt.Errorf("task is required")
	}
	if len(task) > maxControlTask {
		return nil, fmt.Errorf("task is longer than %d bytes", maxControlTask)
	}
	if c.started >= maxControlRuns {
		return nil, fmt.Errorf("this control session already started %d runs; open a new session", maxControlRuns)
	}
	r, readiness, err := c.prepare(c.ctx, c.p, workflowID, task)
	if err != nil {
		return nil, err
	}
	c.started++
	c.workers.Add(1)
	go func() {
		defer c.workers.Done()
		if err := c.execute(c.ctx, c.p, r); err != nil {
			// A run that never began would otherwise stay "created" forever.
			if s, e := c.p.State(r.ID); e == nil && s.Status == "created" {
				_, _ = c.p.Append(r.ID, r.Workflow.Id, "", "workflow.failed", map[string]any{"reason": err.Error()})
			}
		}
	}()
	return map[string]any{"runId": r.ID, "workflowId": r.Workflow.Id, "state": "started", "supervision": "attached: this MCP session supervises the run; if it ends the run is interrupted and `arena resume " + r.ID + "` continues it", "readiness": readiness}, nil
}

// requestChange opens a pending decision for a human to integrate or roll back
// a change. It never executes the action and cannot answer the decision.
func (c *controlServer) requestChange(action string, args map[string]any) (any, error) {
	if err := controlOnly(args, "runId", "changeId"); err != nil {
		return nil, err
	}
	runID, err := controlIDValue(args, "runId", true)
	if err != nil {
		return nil, err
	}
	changeID, err := controlIDValue(args, "changeId", true)
	if err != nil {
		return nil, err
	}
	d, reused, err := c.p.RequestChangeAction(action, runID, changeID, controlActor)
	if err != nil {
		return nil, err
	}
	return map[string]any{"decisionId": d.ID, "runId": runID, "changeId": changeID, "action": action, "state": "pending", "reused": reused, "requestedBy": controlActor, "preview": d.Context["preview"], "next": "Nothing has been executed. A human reviews the preview and answers: `arena decision answer " + runID + " " + d.ID + " approve|reject`, or in the TUI."}, nil
}
