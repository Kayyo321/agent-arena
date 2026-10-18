package arena

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// ResolutionItem is one proposed change in the end-of-run resolution plan.
// Status is ready, conflict, blocked or skipped.
type ResolutionItem struct {
	ChangeID string   `json:"changeId"`
	AgentID  string   `json:"agentId"`
	Order    int      `json:"order"`
	Paths    []string `json:"paths"`
	Status   string   `json:"status"`
	Reason   string   `json:"reason,omitempty"`
}

// Resolution is the combined plan for a run's proposed changes against the
// checkout. Ready is true only when every item is ready and the checkout is clean.
type Resolution struct {
	RunID  string           `json:"runId"`
	Head   string           `json:"head"`
	Ready  bool             `json:"ready"`
	Reason string           `json:"reason,omitempty"`
	Items  []ResolutionItem `json:"items"`
}

// PrepareResolution dry-runs a run's proposed changes in dependency order
// against the checkout. It writes no refs, branches, worktrees or checkout
// files, and no journal events: the TUI calls it while rendering.
//
// Skipped changes are left out of Items. A change that depends on a skipped,
// superseded or conflicted change is reported blocked, never applied silently.
func (p *Project) PrepareResolution(run string, skip []string) (Resolution, error) {
	res := Resolution{RunID: run, Items: []ResolutionItem{}}
	if _, err := p.ReadRun(run); err != nil {
		return res, err
	}
	changes, err := p.Changes(run)
	if err != nil {
		return res, err
	}
	byID := map[string]Change{}
	for _, c := range changes {
		byID[c.ID] = c
	}
	skipped := map[string]bool{}
	for _, id := range skip {
		c, ok := byID[id]
		if !ok {
			return res, fmt.Errorf("skip: unknown change %s", id)
		}
		if c.State != "proposed" {
			return res, fmt.Errorf("skip: change %s is %s, not proposed", id, c.State)
		}
		skipped[id] = true
	}
	state, err := p.State(run)
	if err != nil {
		return res, err
	}
	head, headErr := Git(p.Root, "rev-parse", "HEAD")
	if headErr == nil {
		res.Head = head
	}
	switch state.Status {
	case "new", "created", "running", "paused", "interrupted":
		res.Reason = fmt.Sprintf("run is %s; resolve its changes after it ends", state.Status)
		return res, nil
	case "cancelled":
		// A cancelled run can still be draining its agents, whose last
		// proposals have not landed yet.
		if RunAlive(p, run) {
			res.Reason = "run is still stopping; wait for it to finish, then run /integrate again"
			return res, nil
		}
	}
	if headErr != nil {
		return res, headErr
	}

	candidates := []Change{}
	isCandidate := map[string]bool{}
	for _, c := range changes {
		if c.State == "proposed" && !skipped[c.ID] {
			candidates = append(candidates, c)
			isCandidate[c.ID] = true
		}
	}
	ordered, err := orderResolution(candidates, isCandidate)
	if err != nil {
		return res, err
	}
	if len(ordered) == 0 {
		res.Reason = "no proposed changes to apply"
		return res, nil
	}

	status, err := Git(p.Root, "status", "--porcelain=v1")
	if err != nil {
		return res, err
	}
	dirty := status != ""

	index := p.Path(".state", "indices", ID("resolution-"))
	if err := os.MkdirAll(filepath.Dir(index), 0700); err != nil {
		return res, err
	}
	defer os.Remove(index)
	env := []string{"GIT_INDEX_FILE=" + index}
	if _, err := gitEnv(p.Root, env, "read-tree", head); err != nil {
		return res, err
	}

	outcome := map[string]string{}
	allReady := true
	for i, c := range ordered {
		item := ResolutionItem{ChangeID: c.ID, AgentID: c.AgentID, Order: i + 1, Paths: append([]string{}, c.Paths...), Status: "ready"}
		if reason := resolutionBlock(byID, outcome, isCandidate, skipped, c); reason != "" {
			item.Status, item.Reason = "blocked", reason
		} else if branch := integrateBranch(c.ID); gitRefExists(p.Root, "refs/heads/"+branch) {
			item.Status = "blocked"
			item.Reason = fmt.Sprintf("stale integration branch %s from an earlier attempt; inspect it and delete it before retrying", branch)
		} else {
			patch := filepath.Join(p.RunDir(run), "changes", c.ID+".patch")
			if _, err := gitEnv(p.Root, env, "apply", "--cached", "--check", patch); err != nil {
				item.Status, item.Reason = "conflict", err.Error()
			} else if _, err := gitEnv(p.Root, env, "apply", "--cached", patch); err != nil {
				item.Status, item.Reason = "conflict", err.Error()
			}
		}
		outcome[c.ID] = item.Status
		if item.Status != "ready" {
			allReady = false
		}
		res.Items = append(res.Items, item)
	}
	switch {
	case dirty:
		res.Reason = "checkout has uncommitted changes; commit or stash them before integrating"
	case !allReady:
		res.Reason = "some changes conflict or are blocked; resolve them or skip them"
	default:
		res.Ready = true
	}
	return res, nil
}

// integrateBranch is the branch integrate creates for a change.
func integrateBranch(changeID string) string { return "codex/arena/integrate/" + changeID }

// gitRefExists reports whether ref exists. It is read-only.
func gitRefExists(dir, ref string) bool {
	_, err := Git(dir, "show-ref", "--verify", "--quiet", ref)
	return err == nil
}

// resolutionBlock explains why c cannot apply because of a prerequisite, or
// returns "" when every prerequisite is integrated or ready earlier in the plan.
func resolutionBlock(byID map[string]Change, outcome map[string]string, isCandidate, skipped map[string]bool, c Change) string {
	for _, dep := range c.DependsOn {
		if isCandidate[dep] {
			if got := outcome[dep]; got != "ready" {
				return fmt.Sprintf("prerequisite %s is %s", dep, got)
			}
			continue
		}
		switch {
		case skipped[dep]:
			return fmt.Sprintf("prerequisite %s is skipped", dep)
		case byID[dep].ID == "":
			return fmt.Sprintf("prerequisite %s is missing", dep)
		case byID[dep].State != "integrated":
			return fmt.Sprintf("prerequisite %s is %s", dep, byID[dep].State)
		}
	}
	return ""
}

// orderResolution sorts candidates so every candidate follows its candidate
// prerequisites, breaking ties by CreatedAt and then ID.
func orderResolution(candidates []Change, isCandidate map[string]bool) ([]Change, error) {
	sort.Slice(candidates, func(i, j int) bool {
		if candidates[i].CreatedAt != candidates[j].CreatedAt {
			return candidates[i].CreatedAt < candidates[j].CreatedAt
		}
		return candidates[i].ID < candidates[j].ID
	})
	placed := map[string]bool{}
	out := make([]Change, 0, len(candidates))
	for len(out) < len(candidates) {
		progressed := false
		for _, c := range candidates {
			if placed[c.ID] {
				continue
			}
			waiting := false
			for _, dep := range c.DependsOn {
				if isCandidate[dep] && !placed[dep] {
					waiting = true
					break
				}
			}
			if waiting {
				continue
			}
			placed[c.ID] = true
			out = append(out, c)
			progressed = true
			break
		}
		if !progressed {
			return nil, fmt.Errorf("change dependency cycle")
		}
	}
	return out, nil
}

// ApplyResolution integrates every ready item in order through integrate. The
// gate is all-or-nothing: it refuses, and applies nothing, unless
// PrepareResolution reports Ready. If an integrate fails partway, the changes
// already committed stay in place (each reversible with rollback).
func (p *Project) ApplyResolution(run, actor string, by Approval, skip []string) (Resolution, error) {
	res, err := p.PrepareResolution(run, skip)
	if err != nil {
		return res, err
	}
	if !res.Ready {
		return res, fmt.Errorf("resolution is not ready, nothing applied: %s", res.blockers())
	}
	r, err := p.ReadRun(run)
	if err != nil {
		return res, err
	}
	applied := []string{}
	for i, item := range res.Items {
		if _, err := p.IntegrateBy(run, item.ChangeID, by); err != nil {
			res.Items[i].Reason = Redact(err.Error())
			_, _ = p.Append(run, r.Workflow.Id, actor, "resolution.stopped", map[string]any{"applied": applied, "failedChange": item.ChangeID, "reason": Redact(err.Error()), "approval": by.Record()})
			return res, err
		}
		applied = append(applied, item.ChangeID)
	}
	head, err := Git(p.Root, "rev-parse", "HEAD")
	if err != nil {
		return res, err
	}
	res.Head = head
	_, err = p.Append(run, r.Workflow.Id, actor, "resolution.applied", map[string]any{"changeIds": applied, "head": head, "approval": by.Record()})
	return res, err
}

// blockers lists what keeps a resolution from applying, for error messages.
func (r Resolution) blockers() string {
	parts := []string{}
	if r.Reason != "" {
		parts = append(parts, r.Reason)
	}
	for _, item := range r.Items {
		if item.Status != "ready" {
			parts = append(parts, fmt.Sprintf("%s %s: %s", item.ChangeID, item.Status, firstLine(item.Reason)))
		}
	}
	return strings.Join(parts, "; ")
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// readyData is the journal payload of a resolution.ready event.
func (r Resolution) readyData() map[string]any {
	ready, blocked, conflict := 0, 0, 0
	ids := []string{}
	for _, item := range r.Items {
		switch item.Status {
		case "ready":
			ready++
			ids = append(ids, item.ChangeID)
		case "blocked":
			blocked++
		case "conflict":
			conflict++
		}
	}
	return map[string]any{"ready": ready, "blocked": blocked, "conflict": conflict, "changeIds": ids, "applicable": r.Ready, "reason": r.Reason}
}

// journalResolution is the end-of-run hook: when the run left proposed changes
// it prepares the combined resolution and journals resolution.ready, or
// resolution.error when preparation fails. It never fails the run.
func (e *Engine) journalResolution() {
	changes, err := e.Project.Changes(e.Run.ID)
	if err != nil {
		_, _ = e.event("", "resolution.error", map[string]any{"reason": Redact(err.Error())})
		return
	}
	proposed := false
	for _, c := range changes {
		if c.State == "proposed" {
			proposed = true
			break
		}
	}
	if !proposed {
		return
	}
	res, err := e.Project.PrepareResolution(e.Run.ID, nil)
	if err != nil {
		_, _ = e.event("", "resolution.error", map[string]any{"reason": Redact(err.Error())})
		return
	}
	_, _ = e.event("", "resolution.ready", res.readyData())
}
