package arena

import (
	"fmt"
	"os"
	"path/filepath"
)

// Integrating or rolling back a change is a human decision. The control server
// may only open a pending decision of one of these kinds; answering it (TUI or
// `arena decision answer`) is what executes the action, once, in AnswerDecision.
const (
	decisionIntegrate = "integrate-change"
	decisionRollback  = "rollback-change"

	// controlActor is the requesting actor journaled for control-server requests.
	controlActor = "control-mcp"
)

func isChangeRequest(d Decision) bool {
	return d.Kind == decisionIntegrate || d.Kind == decisionRollback
}

// executesOnApproval reports whether approving the decision runs an integrate
// or rollback; an unreadable or unknown decision is left to AnswerDecision.
func (p *Project) executesOnApproval(run, id string) bool {
	if validID(run) != nil || validID(id) != nil {
		return false
	}
	var d Decision
	return ReadJSON(filepath.Join(p.RunDir(run), "decisions", id+".json"), &d) == nil && isChangeRequest(d)
}

// requireLiveRun returns a run whose changes belong to this checkout: not
// retired (its directory is gone) and not imported from an audit bundle.
func (p *Project) requireLiveRun(run string) (Run, error) {
	if e := validID(run); e != nil {
		return Run{}, e
	}
	// An imported run lives only under .state/imported/RUN (audit import), never
	// as a live run directory.
	if _, e := os.Stat(p.Path(".state", "imported", run)); e == nil {
		return Run{}, fmt.Errorf("run %s was imported from an audit bundle; its changes were made elsewhere and cannot be integrated or rolled back here", run)
	}
	if _, e := os.Stat(p.RunDir(run)); e != nil {
		return Run{}, fmt.Errorf("run %s has no history here; it may have been retired", run)
	}
	return p.ReadRun(run)
}

// IntegratePreview runs the checks `arena integrate` makes before it changes
// anything, without changing anything: the change is proposed, its
// dependencies are integrated, the checkout is clean, and the patch applies to
// the current HEAD.
func (p *Project) IntegratePreview(run, id string) (map[string]any, error) {
	if _, e := p.requireLiveRun(run); e != nil {
		return nil, e
	}
	change, e := p.Change(run, id)
	if e != nil {
		return nil, e
	}
	if change.State != "proposed" {
		return nil, fmt.Errorf("change is %s", change.State)
	}
	for _, dependency := range change.DependsOn {
		required, e := p.Change(run, dependency)
		if e != nil {
			return nil, e
		}
		if required.State != "integrated" {
			return nil, fmt.Errorf("integrate prerequisite %s before %s", dependency, id)
		}
	}
	lock, e := p.WaitLock("git")
	if e != nil {
		return nil, e
	}
	defer lock.Close()
	status, e := Git(p.Root, "status", "--porcelain=v1")
	if e != nil {
		return nil, e
	}
	if status != "" {
		return nil, fmt.Errorf("integration target has user changes; commit them or review in the preserved isolated integration workspace")
	}
	head, e := Git(p.Root, "rev-parse", "HEAD")
	if e != nil {
		return nil, e
	}
	patch := filepath.Join(p.RunDir(run), "changes", id+".patch")
	// The check uses a throwaway index, so neither the user's index nor their
	// working tree is touched.
	index := p.Path(".state", "indices", ID("integrate-check-"))
	if e := os.MkdirAll(filepath.Dir(index), 0700); e != nil {
		return nil, e
	}
	defer os.Remove(index)
	env := []string{"GIT_INDEX_FILE=" + index}
	if _, e := gitEnv(p.Root, env, "read-tree", head); e != nil {
		return nil, e
	}
	if _, e := gitEnv(p.Root, env, "apply", "--cached", "--check", patch); e != nil {
		return nil, fmt.Errorf("change conflicts with the current checkout: %w", e)
	}
	stat, e := Git(p.Root, "apply", "--stat", patch)
	if e != nil {
		return nil, e
	}
	body, e := os.ReadFile(patch)
	if e != nil {
		return nil, e
	}
	return map[string]any{"change": change, "currentHead": head, "diffstat": Redact(stat), "patchSha256": Hash(body), "behavior": "applies the patch in an isolated integration worktree and fast-forwards the checkout; a conflict is journaled and the checkout is left untouched"}, nil
}

// rollbackBlocker explains why a change cannot be rolled back now.
func (p *Project) rollbackBlocker(run string, change Change) error {
	if change.State == "integrated" {
		if _, e := Git(p.Root, "cat-file", "-e", change.IntegrationCommit+"^{commit}"); e != nil {
			return fmt.Errorf("integration commit of %s is not in this repository", change.ID)
		}
	}
	changes, e := p.Changes(run)
	if e != nil {
		return e
	}
	if by := revertedBy(changes, change.ID, map[string]bool{}); by != "" {
		return fmt.Errorf("change %s is already rolled back by %s", change.ID, by)
	}
	return nil
}

// revertedBy names an integrated rollback of id that has not itself been rolled back.
func revertedBy(changes []Change, id string, seen map[string]bool) string {
	for _, c := range changes {
		if c.Reverts == id && c.State == "integrated" && !seen[c.ID] {
			seen[c.ID] = true
			if revertedBy(changes, c.ID, seen) == "" {
				return c.ID
			}
		}
	}
	return ""
}

// RequestChangeAction validates an integrate or rollback exactly as the CLI
// would and opens a pending decision for a human, without acting. A request
// for a change that already has an open decision returns that decision; one
// whose HEAD has moved since is withdrawn and replaced.
func (p *Project) RequestChangeAction(action, run, id, actor string) (d Decision, reused bool, err error) {
	var preview map[string]any
	kind, verb := decisionIntegrate, "Integrate"
	if action == "rollback" {
		kind, verb = decisionRollback, "Roll back"
		preview, err = p.RollbackPreview(run, id)
	} else {
		preview, err = p.IntegratePreview(run, id)
	}
	if err != nil {
		return d, false, err
	}
	r, err := p.ReadRun(run)
	if err != nil {
		return d, false, err
	}
	head := str(preview["currentHead"])
	lock, err := p.WaitLock("decisions-" + run)
	if err != nil {
		return d, false, err
	}
	defer lock.Close()
	open, err := p.Decisions(run)
	if err != nil {
		return d, false, err
	}
	for _, old := range open {
		if old.Kind != kind || str(old.Context["changeId"]) != id {
			continue
		}
		if str(old.Context["head"]) == head {
			return old, true, nil
		}
		compactDecision(&old)
		if err := Atomic(filepath.Join(p.RunDir(run), "decisions", old.ID+".json"), SafeJSON(old), 0600); err != nil {
			return d, false, err
		}
		if _, err := p.Append(run, r.Workflow.Id, actor, "decision.superseded", map[string]any{"decisionId": old.ID, "action": action, "changeId": id, "reason": "HEAD moved since the request"}); err != nil {
			return d, false, err
		}
	}
	prompt := fmt.Sprintf("%s change %s of run %s, requested by %s? Review the preview in this decision; approve executes it once, reject discards the request.", verb, id, run, actor)
	data := map[string]any{"action": action, "changeId": id, "requestedBy": actor, "head": head, "preview": preview}
	d, err = p.newDecision(r, actor, kind, prompt, []string{"approve", "reject"}, data)
	if err != nil {
		return d, false, err
	}
	if _, err := p.Append(run, r.Workflow.Id, actor, "decision.requested", map[string]any{"decision": d}); err != nil {
		compactDecision(&d)
		_ = Atomic(filepath.Join(p.RunDir(run), "decisions", d.ID+".json"), SafeJSON(d), 0600)
		return d, false, err
	}
	return d, false, nil
}

// executeChangeRequest performs an approved request once. It re-runs the CLI's
// checks first and refuses if HEAD is not the one the human reviewed. The
// outcome, success or refusal, is journaled with both actors.
func (p *Project) executeChangeRequest(r Run, d Decision, via Approval) (map[string]any, error) {
	action, id, head := str(d.Context["action"]), str(d.Context["changeId"]), str(d.Context["head"])
	record := map[string]any{"decisionId": d.ID, "action": action, "changeId": id, "requestedBy": str(d.Context["requestedBy"]), "approval": via.Record()}
	fail := func(reason error) (map[string]any, error) {
		record["reason"] = Redact(reason.Error())
		if _, e := p.Append(r.ID, r.Workflow.Id, "user", "decision.refused", record); e != nil {
			reason = fmt.Errorf("%w (and the journal write failed: %v)", reason, e)
		}
		return record, fmt.Errorf("approved, but %s of %s was not executed: %w", action, id, reason)
	}
	var preview map[string]any
	var e error
	if action == "rollback" {
		preview, e = p.RollbackPreview(r.ID, id)
	} else {
		preview, e = p.IntegratePreview(r.ID, id)
	}
	if e != nil {
		return fail(e)
	}
	if current := str(preview["currentHead"]); current != head {
		return fail(fmt.Errorf("stale request: HEAD moved from %.12s to %.12s since it was made; request it again", head, current))
	}
	// What runs is what was reviewed: a patch or integration commit that
	// differs from the one in the request's preview is a different operation. A
	// request without the digests (made before they were recorded) is refused too.
	reviewed, _ := d.Context["preview"].(map[string]any)
	for _, key := range []string{"patchSha256", "integrationCommit"} {
		if str(reviewed[key]) != str(preview[key]) {
			return fail(fmt.Errorf("%s changed since the request was made; request it again", key))
		}
	}
	var result Change
	if action == "rollback" {
		result, e = p.RollbackBy(r.ID, id, via)
	} else {
		result, e = p.IntegrateBy(r.ID, id, via)
	}
	record["resultChangeId"], record["resultState"], record["integrationCommit"] = result.ID, result.State, result.IntegrationCommit
	if e != nil {
		return fail(e)
	}
	if _, e := p.Append(r.ID, r.Workflow.Id, "user", "decision.executed", record); e != nil {
		return record, e
	}
	return record, nil
}
