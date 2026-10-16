package arena

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// FanOutChild is one worker instance a fan-out node started: a copy of the
// worker template under the synthetic ID <fanOutId>.<n>.
type FanOutChild struct {
	FanOutID string  `json:"fanOutId"`
	Agent    Agent   `json:"agent"`
	Profile  Profile `json:"profile"`
}

// agent resolves a workflow agent or a registered fan-out child; the MCP
// bridge and the packet router use it to identify an actor.
func (r Run) agent(id string) (Agent, bool) {
	if a, ok := agentByID(r.Workflow, id); ok {
		return a, true
	}
	for _, child := range r.FanOutChildren {
		if child.Agent.Id == id {
			return child.Agent, true
		}
	}
	return Agent{}, false
}

func (e *Engine) agentByID(id string) (Agent, bool) {
	if a, ok := agentByID(e.Run.Workflow, id); ok {
		return a, true
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	child, ok := e.children[id]
	return child.Agent, ok
}

// childIDs lists the children registered for a fan-out, in ID order.
func (e *Engine) childIDs(fanOut string) []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	ids := []string{}
	for _, id := range sortedKeys(e.children) {
		if e.children[id].FanOutID == fanOut {
			ids = append(ids, id)
		}
	}
	return ids
}

func (e *Engine) profileOf(id string) Profile {
	e.mu.Lock()
	child, ok := e.children[id]
	e.mu.Unlock()
	if ok {
		return child.Profile
	}
	return e.Run.Profiles[id]
}

// fanOutChildID names the n-th (1-based) child of a fan-out.
func fanOutChildID(f FanOut, n int) string { return fmt.Sprintf("%s.%d", f.Id, n) }

// registerChildren records children 1..n of f and persists them before any
// of them starts, because each child's MCP bridge resolves its actor from
// the run's current configuration. Registration is idempotent, so a resumed
// or looped fan-out registers the same IDs again.
func (e *Engine) registerChildren(f FanOut, n int) ([]Agent, error) {
	worker, ok := agentByID(e.Run.Workflow, f.Worker)
	if !ok {
		return nil, fmt.Errorf("fan-out %s worker %s is missing", f.Id, f.Worker)
	}
	agents := []Agent{}
	e.mu.Lock()
	for i := 1; i <= n; i++ {
		a := convert[Agent](worker)
		a.Id = fanOutChildID(f, i)
		profile := convert[Profile](e.Run.Profiles[f.Worker])
		profile.Id = a.Id
		e.children[a.Id] = FanOutChild{FanOutID: f.Id, Agent: a, Profile: profile}
		agents = append(agents, a)
	}
	e.mu.Unlock()
	return agents, e.writeConfig(e.Run)
}

type fanOutItem struct {
	Title        string `json:"title"`
	Instructions string `json:"instructions"`
}

// fanOutProgress is what the journal shows of one pass of a fan-out, so a
// resumed node never replans, reasks or reruns finished work.
type fanOutProgress struct {
	plan     string // planner message ID, from fanout.planned
	planned  int    // sequence of fanout.planned, to scope its decision
	approved bool
	count    int
	dropped  int
	// conflicted is set once the pass journaled its fanout.conflict event.
	conflicted bool
	// done holds each child's terminal fanout.child.completed/failed event.
	done map[string]Event
}

func readFanOutProgress(events []Event, id string, iteration int) fanOutProgress {
	progress := fanOutProgress{done: map[string]Event{}}
	for _, v := range events {
		if !strings.HasPrefix(v.Type, "fanout.") || str(v.Data["fanOutId"]) != id || intValue(v.Data["iteration"]) != iteration {
			continue
		}
		switch v.Type {
		case "fanout.planned":
			progress.plan, progress.planned = str(v.Data["planMessageId"]), v.Sequence
		case "fanout.approved":
			progress.approved, progress.count, progress.dropped = true, intValue(v.Data["count"]), intValue(v.Data["dropped"])
		case "fanout.conflict":
			progress.conflicted = true
		case "fanout.child.completed", "fanout.child.failed":
			progress.done[str(v.Data["childId"])] = v
		}
	}
	return progress
}

// startedFanOutChildren counts the distinct children the run has started,
// across every fan-out and loop pass, for limits.maxFanOutChildrenPerRun.
func startedFanOutChildren(events []Event) int {
	seen := map[string]bool{}
	for _, v := range events {
		if v.Type == "fanout.child.started" {
			seen[fmt.Sprintf("%s/%d", str(v.Data["childId"]), intValue(v.Data["iteration"]))] = true
		}
	}
	return len(seen)
}

// intValue reads a decoded journal number, 0 when it is not one.
func intValue(v any) int {
	n, _ := usageCount(v)
	return int(n)
}

func fanOutPlanSchema() map[string]any {
	item := objectSchema(map[string]any{"title": map[string]any{"type": "string"}, "instructions": map[string]any{"type": "string"}})
	return objectSchema(map[string]any{"items": arraySchema(item), "rationale": map[string]any{"type": "string"}})
}

// fanOut runs a fan-out node: the planner splits the task into items, the
// user (or auto-within-cap) approves them, and one worker child runs per
// item in its own worktree, never more than maxChildren or the run's
// remaining maxFanOutChildrenPerRun. The downstream node receives one
// fanout-result packet carrying every child's outcome and change.
func (e *Engine) fanOut(ctx context.Context, f FanOut, parents []Packet) (Packet, error) {
	iteration := e.iteration(f.Id)
	events, err := e.Project.Events(e.Run.ID)
	if err != nil {
		return nil, err
	}
	progress := readFanOutProgress(events, f.Id, iteration)
	planner, _ := agentByID(e.Run.Workflow, f.Planner)
	worker, _ := agentByID(e.Run.Workflow, f.Worker)
	var plan Packet
	if progress.plan != "" {
		if plan, err = e.Project.ReadPacket(e.Run.ID, progress.plan); err != nil {
			return nil, fmt.Errorf("resume requires retained fan-out plan %s; selected retention omitted it", progress.plan)
		}
	} else {
		if _, err := e.event(f.Id, "fanout.started", map[string]any{"fanOutId": f.Id, "iteration": iteration}); err != nil {
			return nil, err
		}
		prompt := fmt.Sprintf("Fan-out %s: split the task into independent work items for worker %s. Return at most %d items; Arena never starts more. Give each item a short title and complete instructions, including the files it owns, so it can be done without the other items. Return an empty list if the task needs no work.", f.Id, f.Worker, f.MaxChildren)
		contextParents := append(append([]Packet{}, parents...), NewPacket(e.Run.ID, "message", f.Id, "arena", f.Planner, planner.Runtime.Provider, "", map[string]any{"text": prompt}, Runtime{Provider: "arena"}))
		if plan, err = e.agent(ctx, planner, contextParents, map[string]any{"mode": "fanout-planner", "fanOutId": f.Id}, fanOutPlanSchema()); err != nil {
			return nil, err
		}
	}
	payload, _ := plan["payload"].(map[string]any)
	items := convert[[]fanOutItem](anySliceOrEmpty(convert[map[string]any](payload["plan"])["items"]))
	for i, item := range items {
		if strings.TrimSpace(item.Title) == "" || strings.TrimSpace(item.Instructions) == "" {
			return nil, fmt.Errorf("fan-out %s planner returned item %d without a title or instructions", f.Id, i+1)
		}
	}
	if progress.plan == "" {
		event, err := e.event(f.Id, "fanout.planned", map[string]any{"fanOutId": f.Id, "iteration": iteration, "planMessageId": plan["messageId"], "items": len(items), "maxChildren": f.MaxChildren})
		if err != nil {
			return nil, err
		}
		progress.plan, progress.planned = str(plan["messageId"]), event.Sequence
	}
	if !progress.approved {
		if progress.count, progress.dropped, err = e.approveFanOut(ctx, f, iteration, items, progress.planned); err != nil {
			return nil, err
		}
	}
	children, err := e.registerChildren(f, progress.count)
	if err != nil {
		return nil, err
	}
	type outcome struct {
		packet Packet
		err    error
	}
	outcomes := make([]outcome, len(children))
	var wg sync.WaitGroup
	for i, child := range children {
		if v, ok := progress.done[child.Id]; ok {
			if v.Type == "fanout.child.completed" {
				packet, err := e.Project.ReadPacket(e.Run.ID, str(v.Data["messageId"]))
				if err != nil {
					return nil, fmt.Errorf("resume requires retained child result %s; selected retention omitted it", str(v.Data["messageId"]))
				}
				outcomes[i] = outcome{packet: packet}
			} else {
				outcomes[i] = outcome{err: fmt.Errorf("%s", str(v.Data["error"]))}
			}
			continue
		}
		wg.Add(1)
		go func(i int, child Agent) {
			defer wg.Done()
			outcomes[i].packet, outcomes[i].err = e.fanOutChild(ctx, f, iteration, i, len(children), child, items[i], planner, parents)
		}(i, child)
	}
	wg.Wait()
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	results := []any{}
	changeIDs := []string{}
	for _, parent := range parents {
		changeIDs = append(changeIDs, stringSlice(parent["arenaChangeIds"])...)
	}
	providers := []string{planner.Runtime.Provider, worker.Runtime.Provider}
	for _, parent := range parents {
		source, _ := parent["from"].(map[string]any)
		if provider := str(source["provider"]); provider != "arena" {
			providers = append(providers, provider)
		}
		providers = append(providers, stringSlice(parent["arenaSourceProviders"])...)
	}
	childChanges := map[string][]string{}
	order := []string{}
	for i, child := range children {
		if outcomes[i].err == nil {
			order = append(order, child.Id)
			childChanges[child.Id] = stringSlice(outcomes[i].packet["arenaChangeIds"])
		}
	}
	conflicts, err := e.Project.fanOutConflictsWith(e.Run, order, childChanges, e.patchApply)
	if err != nil {
		return nil, err
	}
	if len(conflicts) > 0 && !progress.conflicted {
		held := []string{}
		pairs := [][]string{}
		heldChanges := []string{}
		for _, id := range order {
			if len(conflicts[id]) > 0 {
				held = append(held, id)
				heldChanges = append(heldChanges, childChanges[id]...)
				for _, other := range conflicts[id] {
					// A pair is listed once; a child held only against the
					// combination of the others has no mirrored entry.
					if id < other || !Has(conflicts[other], id) {
						pairs = append(pairs, []string{id, other})
					}
				}
			}
		}
		if _, err := e.event(f.Id, "fanout.conflict", map[string]any{"fanOutId": f.Id, "iteration": iteration, "children": held, "pairs": pairs, "changeIds": heldChanges}); err != nil {
			return nil, err
		}
	}
	succeeded := 0
	for i, child := range children {
		result := map[string]any{"childId": child.Id, "item": items[i], "status": "failed", "messageId": nil, "changeIds": []string{}, "summary": ""}
		if o := outcomes[i]; o.err != nil {
			result["summary"] = Redact(o.err.Error())
		} else {
			succeeded++
			body, _ := o.packet["payload"].(map[string]any)
			ids := stringSlice(o.packet["arenaChangeIds"])
			result["status"], result["messageId"], result["changeIds"], result["summary"] = "succeeded", o.packet["messageId"], ids, str(body["summary"])
			if with := conflicts[child.Id]; len(with) > 0 {
				// The proposal stays reviewable but is held out of the base
				// the next node builds on, so the run goes on.
				result["conflict"], result["conflictsWith"] = true, with
			} else {
				changeIDs = append(changeIDs, ids...)
			}
		}
		results = append(results, result)
	}
	if len(children) > 0 && succeeded == 0 {
		return nil, fmt.Errorf("every child of fan-out %s failed", f.Id)
	}
	summary := fmt.Sprintf("Fan-out %s: %d of %d children succeeded.", f.Id, succeeded, len(children))
	if len(items) == 0 {
		summary = fmt.Sprintf("Fan-out %s: the planner found no work items.", f.Id)
	}
	if len(conflicts) > 0 {
		summary += fmt.Sprintf(" %d children conflict with each other: their changes stay proposed for review and are left out of the next node's workspace.", len(conflicts))
	}
	if progress.dropped > 0 {
		summary += fmt.Sprintf(" %d planned items were dropped at the cap.", progress.dropped)
	}
	caller, callerProvider := "arena", "arena"
	if len(parents) > 0 {
		actor, _ := parents[0]["from"].(map[string]any)
		if str(actor["agentId"]) != "" {
			caller, callerProvider = str(actor["agentId"]), str(actor["provider"])
		}
	}
	dropped := items[len(children):]
	packet := NewPacket(e.Run.ID, "fanout-result", f.Id, "arena", caller, callerProvider, "", map[string]any{"format": "arena.fanout-result.v1", "fanOutId": f.Id, "summary": summary, "planned": len(items), "results": results, "dropped": dropped}, Runtime{Provider: "arena"})
	packet["arenaChangeIds"] = uniqueStrings(changeIDs)
	packet["arenaSourceProviders"] = uniqueStrings(providers)
	if _, err := e.Project.Deliver(e.Run, packet); err != nil {
		return nil, err
	}
	if _, err := e.event(f.Id, "fanout.completed", map[string]any{"fanOutId": f.Id, "iteration": iteration, "messageId": packet["messageId"], "succeeded": succeeded, "failed": len(children) - succeeded, "dropped": len(dropped), "conflicting": len(conflicts)}); err != nil {
		return nil, err
	}
	return packet, nil
}

// fanOutConflicts finds the children whose changes cannot be applied together
// to the base they share, applying patches the way composeBase builds the next
// node's workspace (apply --cached --3way), so a child flagged here is one the
// next node could not have been built with. Each child's result is built once in
// a scratch index; a pair then costs one apply on a copy of one side.
//
// A clashing pair holds out both children: nothing is chosen between them.
// Children that pass every pair are then applied one after another in order, and
// one that fails on top of the others (a conflict only the combination shows) is
// held out alone, mapped to the children it failed against. Nothing is dropped.
func (p *Project) fanOutConflicts(r Run, order []string, changes map[string][]string) (map[string][]string, error) {
	return p.fanOutConflictsWith(r, order, changes, nil)
}

// patchApplier applies one patch to the index file named by index, the way the
// next node's base is built (apply --cached --3way). Real conflicts come from
// git; the type exists so a test can stand in for it where a conflict that only
// the combination of three or more children shows cannot be built from real
// patches. A nil applier means git.
type patchApplier func(root, index, patch string) error

func gitApplyCached(root, index, patch string) error {
	_, err := gitEnv(root, []string{"GIT_INDEX_FILE=" + index}, "apply", "--cached", "--3way", patch)
	return err
}

// fanOutConflictsWith is fanOutConflicts with the patch application supplied.
func (p *Project) fanOutConflictsWith(r Run, order []string, changes map[string][]string, applyPatch patchApplier) (map[string][]string, error) {
	if applyPatch == nil {
		applyPatch = gitApplyCached
	}
	conflicts := map[string][]string{}
	candidates := []string{}
	for _, id := range order {
		if len(changes[id]) > 0 {
			candidates = append(candidates, id)
		}
	}
	if len(candidates) < 2 {
		return conflicts, nil
	}
	indices := p.Path(".state", "indices")
	if err := os.MkdirAll(indices, 0700); err != nil {
		return nil, err
	}
	scratch, err := os.MkdirTemp(indices, "overlap-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(scratch)
	n := 0
	next := func() string {
		n++
		return filepath.Join(scratch, fmt.Sprintf("index-%d", n))
	}
	copyOf := func(from string) (string, error) {
		b, err := os.ReadFile(from)
		if err != nil {
			return "", err
		}
		to := next()
		return to, os.WriteFile(to, b, 0600)
	}
	// apply adds ids' patches to index; false means a patch did not apply.
	apply := func(index string, ids []string) bool {
		for _, id := range ids {
			if err := applyPatch(p.Root, index, filepath.Join(p.RunDir(r.ID), "changes", id+".patch")); err != nil {
				return false
			}
		}
		return true
	}
	own := map[string]string{}
	for _, id := range candidates {
		first, err := p.Change(r.ID, changes[id][0])
		if err != nil {
			return nil, err
		}
		own[id] = next()
		if _, err := gitEnv(p.Root, []string{"GIT_INDEX_FILE=" + own[id]}, "read-tree", first.Base); err != nil {
			return nil, err
		}
		if !apply(own[id], changes[id]) {
			return nil, fmt.Errorf("changes of %s do not apply to their own base", id)
		}
	}
	for i, a := range candidates {
		for _, b := range candidates[i+1:] {
			trial, err := copyOf(own[a])
			if err != nil {
				return nil, err
			}
			if !apply(trial, changes[b]) {
				conflicts[a], conflicts[b] = append(conflicts[a], b), append(conflicts[b], a)
			}
		}
	}
	accepted, together := []string{}, ""
	for _, id := range candidates {
		if len(conflicts[id]) > 0 {
			continue
		}
		if together == "" {
			together, accepted = own[id], []string{id}
			continue
		}
		trial, err := copyOf(together)
		if err != nil {
			return nil, err
		}
		if apply(trial, changes[id]) {
			together, accepted = trial, append(accepted, id)
		} else {
			conflicts[id] = append([]string{}, accepted...)
		}
	}
	return conflicts, nil
}

// approveFanOut settles how many planned items run: within the cap an
// auto-within-cap node approves itself; otherwise the user decides, and a
// plan over the cap can only be truncated to it or stopped, never run whole.
func (e *Engine) approveFanOut(ctx context.Context, f FanOut, iteration int, items []fanOutItem, planned int) (count, dropped int, err error) {
	events, err := e.Project.Events(e.Run.ID)
	if err != nil {
		return 0, 0, err
	}
	limit := min(f.MaxChildren, max(0, fanOutLimit(e.Run.Settings)-startedFanOutChildren(events)))
	mode, decision := "empty", ""
	count = len(items)
	if len(items) > 0 {
		over := len(items) > limit
		mode = "auto"
		if over || f.Approval != "auto-within-cap" {
			lines := []string{}
			for i, item := range items {
				lines = append(lines, fmt.Sprintf("%d. %s", i+1, item.Title))
			}
			prompt := fmt.Sprintf("Fan-out %s planned %d work items for %s:\n%s\nApprove starts one %s child per item in its own worktree; stop fails the node.", f.Id, len(items), f.Worker, strings.Join(lines, "\n"), f.Worker)
			choices := []string{"approve", "stop"}
			if over {
				prompt = fmt.Sprintf("Fan-out %s planned %d work items, but only %d may start (maxChildren %d, project maxFanOutChildrenPerRun %d):\n%s\nTruncate runs the first %d and records the rest as dropped; stop fails the node.", f.Id, len(items), limit, f.MaxChildren, fanOutLimit(e.Run.Settings), strings.Join(lines, "\n"), limit)
				choices = []string{"truncate", "stop"}
			}
			var answer string
			answer, decision, err = e.fanOutDecision(ctx, f, planned, prompt, choices, map[string]any{"fanOutId": f.Id, "iteration": iteration, "items": items, "cap": limit})
			if err != nil {
				return 0, 0, err
			}
			if answer != "approve" && answer != "truncate" {
				if _, err := e.event(f.Id, "fanout.stopped", map[string]any{"fanOutId": f.Id, "iteration": iteration, "decisionId": decision}); err != nil {
					return 0, 0, err
				}
				return 0, 0, fmt.Errorf("fan-out %s plan stopped by the user", f.Id)
			}
			mode = "user"
		}
		count = min(len(items), limit)
	}
	dropped = len(items) - count
	_, err = e.event(f.Id, "fanout.approved", map[string]any{"fanOutId": f.Id, "iteration": iteration, "mode": mode, "count": count, "dropped": dropped, "decisionId": decision})
	return count, dropped, err
}

// fanOutDecision asks for approval, or picks up the question an
// interrupted supervisor left behind for this plan, so resuming never asks
// twice: an answered question is used as is and an open one is awaited.
func (e *Engine) fanOutDecision(ctx context.Context, f FanOut, planned int, prompt string, choices []string, data map[string]any) (string, string, error) {
	events, err := e.Project.Events(e.Run.ID)
	if err != nil {
		return "", "", err
	}
	previous := ""
	for _, v := range events {
		decision, _ := v.Data["decision"].(map[string]any)
		if v.Type == "decision.requested" && v.AgentID == f.Id && v.Sequence > planned && str(decision["kind"]) == "fanout" {
			previous = str(decision["id"])
		}
	}
	if previous != "" {
		var d Decision
		if err := ReadJSON(filepath.Join(e.Project.RunDir(e.Run.ID), "decisions", previous+".json"), &d); err != nil {
			return "", "", err
		}
		if d.Answer != "" {
			return d.Answer, d.ID, nil
		}
		if d.ClosedAt == "" {
			answer, err := e.Project.awaitOpenDecision(ctx, e.Run, d.ID)
			return answer, d.ID, err
		}
	}
	answer, err := e.Project.Decide(ctx, e.Run, f.Id, "fanout", prompt, choices, data)
	return answer, "", err
}

// fanOutChild runs one child on its item and journals the outcome. A failed
// child does not stop its siblings; an interrupted one is rerun on resume.
func (e *Engine) fanOutChild(ctx context.Context, f FanOut, iteration, index, of int, child Agent, item fanOutItem, planner Agent, parents []Packet) (Packet, error) {
	if _, err := e.event(f.Id, "fanout.child.started", map[string]any{"fanOutId": f.Id, "iteration": iteration, "childId": child.Id, "index": index + 1}); err != nil {
		return nil, err
	}
	text := fmt.Sprintf("Fan-out %s, item %d of %d: %s\n%s\nDo only this item. Other children of %s handle the other items in their own worktrees.", f.Id, index+1, of, item.Title, item.Instructions, f.Id)
	note := NewPacket(e.Run.ID, "message", f.Id, "arena", child.Id, child.Runtime.Provider, "", map[string]any{"text": text, "item": item}, Runtime{Provider: "arena"})
	// The item text comes from the planner's provider; sharing it is gated.
	note["arenaSourceProviders"] = []string{planner.Runtime.Provider}
	contextParents := append(append([]Packet{}, parents...), note)
	packet, err := e.agent(ctx, child, contextParents, map[string]any{"mode": "fanout-child", "fanOutId": f.Id, "index": index + 1, "of": of}, nil)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		_, eventErr := e.event(f.Id, "fanout.child.failed", map[string]any{"fanOutId": f.Id, "iteration": iteration, "childId": child.Id, "error": Redact(err.Error())})
		if eventErr != nil {
			return nil, eventErr
		}
		return nil, err
	}
	_, err = e.event(f.Id, "fanout.child.completed", map[string]any{"fanOutId": f.Id, "iteration": iteration, "childId": child.Id, "messageId": packet["messageId"], "changeIds": stringSlice(packet["arenaChangeIds"])})
	return packet, err
}

func anySliceOrEmpty(v any) []any {
	if s := anySlice(v); s != nil {
		return s
	}
	return []any{}
}
