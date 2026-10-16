package arena

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"sync"
	"time"

	c "github.com/Kayyo321/agent-arena/internal/contract"
)

type Engine struct {
	teamConsent  map[string]bool
	Project      *Project
	Run          Run
	Invoker      Invoker
	slots        chan struct{}
	mu           sync.Mutex
	onCallCounts map[string]int
	onCallSeen   map[string]bool
	Observe      func(Event)
	// Now overrides the clock the minutes budget reads; nil means time.Now.
	Now func() time.Time
	// iterations counts how often a node has been restarted by a loop route
	// (absent means its first pass); loops counts revisions per router/target.
	iterations map[string]int
	loops      map[string]int
	// children are the fan-out worker instances registered so far; guarded by mu.
	children map[string]FanOutChild
}

func NewEngine(p *Project, r Run, invoker Invoker) *Engine {
	e := &Engine{Project: p, Run: r, Invoker: invoker, slots: make(chan struct{}, r.Workflow.Settings.MaxConcurrentAgents), onCallCounts: map[string]int{}, onCallSeen: map[string]bool{}, teamConsent: map[string]bool{}, iterations: map[string]int{}, loops: map[string]int{}, children: map[string]FanOutChild{}}
	for _, child := range r.FanOutChildren {
		e.children[child.Agent.Id] = child
	}
	return e
}

// iteration is the 1-based pass a node is on; a loop route starts the next one.
func (e *Engine) iteration(node string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.iterations[node] + 1
}
func (e *Engine) loopCount(router, target string) int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.loops[router+"/"+target]
}

// iterationTask keeps each revision in its own worktree: the first pass uses
// the plain task ID, later passes append -iterN within the 64-character limit.
func iterationTask(task string, n int) string {
	if n <= 1 {
		return task
	}
	suffix := fmt.Sprintf("-iter%d", n)
	return task[:min(len(task), 64-len(suffix))] + suffix
}
func (e *Engine) event(agent, kind string, data map[string]any) (Event, error) {
	v, err := e.Project.Append(e.Run.ID, e.Run.Workflow.Id, agent, kind, data)
	if err == nil && e.Observe != nil {
		e.Observe(v)
	}
	return v, err
}
func (e *Engine) Execute(ctx context.Context, resume, retry bool) error {
	l, err := e.Project.Lock("run-" + e.Run.ID)
	if err != nil {
		return err
	}
	defer l.Close()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	state, err := e.Project.State(e.Run.ID)
	if err != nil {
		return err
	}
	events, err := e.Project.Events(e.Run.ID)
	if err != nil {
		return err
	}
	for _, v := range events {
		if v.Type == "on-call.started" {
			e.onCallCounts[v.AgentID]++
			e.onCallSeen[v.AgentID+"/"+str(v.Data["triggerEventId"])] = true
		}
	}
	if resume {
		if state.Status == "running" || state.Status == "paused" {
			if _, err := e.event("", "workflow.interrupted", map[string]any{"reason": "attached supervisor restarted"}); err != nil {
				return err
			}
			state.Status = "interrupted"
		}
		if state.Status != "interrupted" && state.Status != "paused" && !(state.Status == "failed" && retry) {
			return fmt.Errorf("cannot resume %s", state.Status)
		}
		if !retry {
			for _, status := range state.Agents {
				if status == "running" || status == "waiting" {
					return fmt.Errorf("interrupted provider request requires explicit --retry-interrupted; output is not replayed automatically")
				}
			}
		}
		if _, err := e.event("", "workflow.resumed", map[string]any{"retryInterrupted": retry}); err != nil {
			return err
		}
	} else {
		if _, err := e.event("", "workflow.started", map[string]any{}); err != nil {
			return err
		}
	}
	monitorDone := make(chan struct{})
	runID := e.Run.ID
	defer func() { cancel(); <-monitorDone }()
	go func() {
		defer close(monitorDone)
		ticker := time.NewTicker(150 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				s, err := e.Project.State(runID)
				if err != nil || s.Status == "cancelled" {
					cancel()
					return
				}
			}
		}
	}()
	nodes := map[string]string{}
	incoming := map[string][]string{}
	outgoing := map[string][]string{}
	for _, a := range e.Run.Workflow.Agents {
		if a.Activation != "on-call" {
			nodes[a.Id] = "pending"
		}
	}
	for _, t := range e.Run.Workflow.Teams {
		nodes[t.Id] = "pending"
		for _, id := range t.Members {
			delete(nodes, id)
		}
	}
	for _, r := range e.Run.Workflow.DecisionRouters {
		nodes[r.Id] = "pending"
	}
	add := func(from, to string) {
		if !Has(outgoing[from], to) {
			outgoing[from] = append(outgoing[from], to)
			incoming[to] = append(incoming[to], from)
		}
	}
	for _, r := range e.Run.Workflow.Relationships {
		if r.Type == "delegates" {
			add(r.From, r.To)
		}
	}
	// Loop routes are back edges: they never gate scheduling, they restart
	// the subgraph below their target once the router has chosen them.
	loopTargets := map[string]map[string]c.WorkflowDecisionRoute{}
	for _, r := range e.Run.Workflow.DecisionRouters {
		for _, branch := range r.Routes {
			if LoopRoute(branch) {
				if loopTargets[r.Id] == nil {
					loopTargets[r.Id] = map[string]c.WorkflowDecisionRoute{}
				}
				if _, ok := loopTargets[r.Id][branch.To]; !ok {
					loopTargets[r.Id][branch.To] = branch
				}
				continue
			}
			add(r.Id, branch.To)
		}
		if r.Fallback.Action == "route" {
			add(r.Id, r.Fallback.To)
		}
	}
	packets := map[string]Packet{}
	active := map[string]bool{e.Run.Workflow.HeadAgent: true}
	selected := map[string][]string{}
	feedback := map[string][]Packet{}
	type pendingLoop struct {
		router, target string
		inputs         []Packet
	}
	loops := []pendingLoop{}
	closure := func(target string) []string { return descendants(outgoing, target) }
	// queueLoops records the loop targets a completed router chose; the
	// restart waits until nothing in the loop body is still running.
	queueLoops := func(id string, packet Packet, targets []string) []string {
		forward := []string{}
		for _, target := range targets {
			if _, ok := loopTargets[id][target]; !ok {
				forward = append(forward, target)
				continue
			}
			inputs, _ := packet["arenaRoutingInputs"].(map[string]any)
			loops = append(loops, pendingLoop{id, target, convert[[]Packet](inputs[target])})
		}
		return forward
	}
	restart := func(l pendingLoop) map[string]any {
		body := closure(l.target)
		agents := []string{}
		for _, id := range body {
			nodes[id] = "pending"
			delete(packets, id)
			delete(selected, id)
			agents = append(agents, id)
			for _, t := range e.Run.Workflow.Teams {
				if t.Id == id {
					agents = append(agents, t.Members...)
				}
			}
		}
		for _, id := range body {
			active[id] = id == l.target
			for _, parent := range incoming[id] {
				if !Has(body, parent) && Has(selected[parent], id) {
					active[id] = true
				}
			}
		}
		route := loopTargets[l.router][l.target]
		e.mu.Lock()
		for _, id := range body {
			e.iterations[id]++
		}
		e.loops[l.router+"/"+l.target]++
		used := e.loops[l.router+"/"+l.target]
		iteration := e.iterations[l.target] + 1
		e.mu.Unlock()
		target, _ := agentByID(e.Run.Workflow, l.target)
		note := NewPacket(e.Run.ID, "message", l.router, "arena", l.target, target.Runtime.Provider, "", map[string]any{"text": fmt.Sprintf("Revision iteration %d (loop route %s, %d of %d allowed). The verdict packet that follows sent this work back to you: address its summary, member positions and dissent. Your workspace already contains the change it reviewed; build on it rather than starting over.", iteration, route.Id, used, *route.MaxIterations)}, Runtime{Provider: "arena"})
		feedback[l.target] = append([]Packet{note}, l.inputs...)
		ids := []string{}
		for _, packet := range l.inputs {
			ids = append(ids, str(packet["messageId"]))
		}
		return map[string]any{"routerId": l.router, "routeId": route.Id, "target": l.target, "iteration": used, "maxIterations": *route.MaxIterations, "nodes": body, "agents": agents, "feedback": ids}
	}
	for _, v := range events {
		if v.Type == "node.completed" {
			id := str(v.Data["nodeId"])
			messageID := str(v.Data["messageId"])
			packet, err := e.Project.ReadPacket(e.Run.ID, messageID)
			if err != nil {
				return fmt.Errorf("resume requires retained packet %s; selected retention omitted it", messageID)
			}
			nodes[id] = "completed"
			packets[id] = packet
			targets := stringSlice(v.Data["targets"])
			selected[id] = append(selected[id], targets...)
			for _, target := range queueLoops(id, packet, targets) {
				active[target] = true
			}
		}
		if v.Type == "loop.iteration" {
			for i, l := range loops {
				if l.router == str(v.Data["routerId"]) && l.target == str(v.Data["target"]) {
					loops = append(loops[:i], loops[i+1:]...)
					restart(l)
					break
				}
			}
		}
		if v.Type == "workflow.config-updated" {
			parent, id := str(v.Data["parent"]), str(v.Data["agentId"])
			active[id] = true
			selected[parent] = append(selected[parent], id)
		}
		if v.Type == "node.skipped" {
			nodes[str(v.Data["nodeId"])] = "skipped"
		}
	}
	type completion struct {
		id      string
		packet  Packet
		targets []string
		err     error
	}
	completed := make(chan completion, len(nodes))
	running := 0
	failed := false
scheduling:
	for {
		progressed := false
		held := map[string]bool{}
		for i := 0; i < len(loops); i++ {
			body := closure(loops[i].target)
			busy := false
			for _, id := range body {
				busy = busy || nodes[id] == "running"
			}
			if busy {
				for _, id := range body {
					held[id] = true
				}
				continue
			}
			l := loops[i]
			loops = append(loops[:i], loops[i+1:]...)
			i--
			if _, err := e.event(l.router, "loop.iteration", restart(l)); err != nil {
				return err
			}
			progressed = true
		}
		if running == 0 {
			updates, err := e.applyCoworkers()
			if err != nil {
				return err
			}
			for id, parent := range updates {
				nodes[id] = "pending"
				active[id] = true
				incoming[id] = append(incoming[id], parent)
				outgoing[parent] = append(outgoing[parent], id)
				selected[parent] = append(selected[parent], id)
			}
		}
		if err := e.Project.AwaitScheduling(ctx, e.Run.ID); err != nil {
			cancel()
			for running > 0 {
				<-completed
				running--
			}
			s, _ := e.Project.State(e.Run.ID)
			if s.Status != "cancelled" {
				_, _ = e.event("", "workflow.interrupted", map[string]any{"reason": "supervisor stopped; worktrees preserved"})
			}
			return err
		}
		for _, id := range sortedKeys(nodes) {
			if nodes[id] != "pending" || held[id] {
				continue
			}
			ready := true
			for _, parent := range incoming[id] {
				if nodes[parent] != "completed" && nodes[parent] != "skipped" {
					ready = false
				}
			}
			if !ready {
				continue
			}
			if !active[id] {
				nodes[id] = "skipped"
				if _, err := e.event("", "node.skipped", map[string]any{"nodeId": id}); err != nil {
					return err
				}
				progressed = true
				continue
			}
			launch, err := e.enforceBudget(ctx)
			if err != nil {
				cancel()
				for running > 0 {
					<-completed
					running--
				}
				if s, _ := e.Project.State(e.Run.ID); s.Status != "cancelled" {
					_, _ = e.event("", "workflow.interrupted", map[string]any{"reason": "supervisor stopped; worktrees preserved"})
				}
				return err
			}
			if !launch {
				// Paused on a budget: wait for scheduling to resume.
				continue scheduling
			}
			nodes[id] = "running"
			running++
			parents := []Packet{}
			for _, parent := range incoming[id] {
				if Has(selected[parent], id) {
					source := packets[parent]
					if inputs, ok := source["arenaRoutingInputs"].(map[string]any); ok {
						parents = append(parents, convert[[]Packet](inputs[id])...)
					} else {
						parents = append(parents, source)
					}
				}
			}
			parents = append(parents, feedback[id]...)
			defaultTargets := append([]string{}, outgoing[id]...)
			go func(id string, parents []Packet, targets []string) {
				packet, routeTargets, err := e.executeNode(ctx, id, parents, targets)
				completed <- completion{id, packet, routeTargets, err}
			}(id, parents, defaultTargets)
			progressed = true
		}
		if running == 0 {
			pending := false
			for _, status := range nodes {
				if status == "pending" {
					pending = true
				}
			}
			if pending && !progressed {
				return fmt.Errorf("scheduler has unresolved dependencies")
			}
			if !pending {
				break
			}
			continue
		}
		select {
		case <-ctx.Done():
			continue
		case result := <-completed:
			running--
			if result.err != nil {
				if ctx.Err() != nil {
					cancel()
					for running > 0 {
						<-completed
						running--
					}
					current, _ := e.Project.State(e.Run.ID)
					if current.Status != "cancelled" {
						_, _ = e.event("", "workflow.interrupted", map[string]any{"reason": "attached supervisor stopped"})
					}
					return ctx.Err()
				}
				failed = true
				cancel()
				for running > 0 {
					<-completed
					running--
				}
				_, _ = e.event("", "workflow.failed", map[string]any{"nodeId": result.id, "error": Redact(result.err.Error())})
				return result.err
			}
			nodes[result.id] = "completed"
			packets[result.id] = result.packet
			selected[result.id] = result.targets
			for _, to := range queueLoops(result.id, result.packet, result.targets) {
				active[to] = true
			}
			messageID := str(result.packet["messageId"])
			if _, err := e.event("", "node.completed", map[string]any{"nodeId": result.id, "messageId": messageID, "targets": result.targets}); err != nil {
				return err
			}
		}
	}
	if !failed {
		if _, err = e.event("", "workflow.succeeded", map[string]any{"nodeCount": len(nodes)}); err != nil {
			return err
		}
		e.journalResolution()
		return nil
	}
	return nil
}
func (e *Engine) executeNode(ctx context.Context, id string, parents []Packet, targets []string) (Packet, []string, error) {
	for _, router := range e.Run.Workflow.DecisionRouters {
		if router.Id == id {
			return e.route(ctx, router, parents)
		}
	}
	for _, team := range e.Run.Workflow.Teams {
		if team.Id == id {
			packet, err := e.team(ctx, team, parents)
			return packet, targets, err
		}
	}
	a, ok := agentByID(e.Run.Workflow, id)
	if !ok {
		return nil, nil, fmt.Errorf("missing agent %s", id)
	}
	packet, err := e.agent(ctx, a, parents, nil, nil)
	return packet, targets, err
}
func (e *Engine) agent(ctx context.Context, a Agent, parents []Packet, invocation map[string]any, schema map[string]any) (Packet, error) {
	select {
	case e.slots <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	var slotOnce sync.Once
	release := func() { slotOnce.Do(func() { <-e.slots }) }
	defer release()
	if invocation == nil || invocation["mode"] != "on-call" {
		if err := e.Project.AwaitScheduling(ctx, e.Run.ID); err != nil {
			return nil, err
		}
	}
	profile := e.profileOf(a.Id)
	if invocation != nil && invocation["mode"] == "team-composition" {
		profile.Capabilities = []string{"read"}
	}
	write := Has(profile.Capabilities, "write")
	node := a.Id
	if invocation != nil && (invocation["mode"] == "team-member" || invocation["mode"] == "team-composition") {
		node = str(invocation["teamId"])
	}
	iteration := e.iteration(node)
	taskID := iterationTask(a.Id, iteration)
	if invocation != nil && invocation["mode"] == "team-composition" {
		taskID = iterationTask(a.Id[:min(40, len(a.Id))]+"-verdict", iteration)
	}
	if invocation != nil && invocation["mode"] == "on-call" {
		taskID = a.Id[:min(30, len(a.Id))] + "-" + Hash(JSON(invocation))[:12]
	}
	dependencies := []string{}
	for _, parent := range parents {
		for _, id := range stringSlice(parent["arenaChangeIds"]) {
			if !Has(dependencies, id) {
				dependencies = append(dependencies, id)
			}
		}
	}
	workspace, err := e.Project.Workspace(e.Run, a.Id, taskID, write, dependencies...)
	if err != nil {
		return nil, err
	}
	if _, err := e.event(a.Id, "agent.queued", map[string]any{}); err != nil {
		return nil, err
	}
	started := map[string]any{"workspace": workspace.Path, "baseCommit": workspace.Base, "runtime": a.Runtime}
	if iteration > 1 && (invocation == nil || invocation["mode"] != "on-call") {
		started["iteration"] = iteration
		started["taskId"] = taskID
	}
	if _, err := e.event(a.Id, "agent.started", started); err != nil {
		return nil, err
	}
	shared, err := e.Project.ContextEntries(e.Run.Workflow.Id, false)
	if err != nil {
		return nil, err
	}
	contextParts := []string{}
	for _, entry := range ContextSearch(shared, e.Run.Objective) {
		if entry.CreatedBy.Provider != nil {
			if err := e.Project.ShareContext(ctx, e.Run, a.Id, *entry.CreatedBy.Provider, a.Id, a.Runtime.Provider, "shared context entry", map[string]any{"entryId": entry.Id, "createdBy": entry.CreatedBy}); err != nil {
				return nil, err
			}
		}
		contextParts = append(contextParts, fmt.Sprintf("Shared context %s (%s, %s): %s", entry.Id, entry.Confidence, entry.Status, entry.Content))
		if _, err := e.event(a.Id, "context.read", map[string]any{"entryId": entry.Id, "hash": Hash(JSON(entry))}); err != nil {
			return nil, err
		}
		if len(contextParts) >= 10 {
			break
		}
	}
	teamApproved := false
	if invocation != nil && (invocation["mode"] == "team-member" || invocation["mode"] == "team-composition") {
		e.mu.Lock()
		teamApproved = e.teamConsent[str(invocation["teamId"])]
		e.mu.Unlock()
	}
	for _, parent := range parents {
		actor, _ := parent["from"].(map[string]any)
		providers := append([]string{str(actor["provider"])}, stringSlice(parent["arenaSourceProviders"])...)
		for _, provider := range uniqueStrings(providers) {
			if !teamApproved {
				if err := e.Project.ShareContext(ctx, e.Run, a.Id, provider, a.Id, a.Runtime.Provider, "handoff", map[string]any{"messageId": parent["messageId"], "from": parent["from"]}); err != nil {
					return nil, err
				}
			}
		}
		contextParts = append(contextParts, "Parent packet: "+string(SafeJSON(parent)))
	}
	inbox, err := e.Project.Inbox(e.Run, a.Id)
	if err != nil {
		return nil, err
	}
	for _, packet := range inbox {
		contextParts = append(contextParts, "Peer packet: "+string(SafeJSON(packet)))
	}
	from, fromProvider := "arena", "arena"
	if len(parents) > 0 {
		actor, _ := parents[0]["from"].(map[string]any)
		if actor["agentId"] != nil {
			from = str(actor["agentId"])
			fromProvider = str(actor["provider"])
		}
	}
	attachments, err := e.Project.loadAttachments(e.Run.ID, e.Run.Attachments)
	if err != nil {
		return nil, err
	}
	if err := attachmentPolicy(attachments, e.Run.Retention, []Runtime{a.Runtime}); err != nil {
		return nil, err
	}

	task := NewPacket(e.Run.ID, "task", from, fromProvider, a.Id, a.Runtime.Provider, "", taskPayload(e.Run.Objective, workspace.Path, workspace.Base, contextParts, write), a.Runtime)
	if len(attachments) > 0 {
		task["payload"].(map[string]any)["attachments"] = attachments
	}
	if invocation != nil {
		task["invocation"] = invocation
		if invocation["mode"] == "team-composition" {
			task["invocation"] = map[string]any{"mode": "team-member", "teamId": invocation["teamId"]}
		}
	}
	if _, err := e.Project.Send(e.Run, task); err != nil {
		return nil, err
	}
	kind := "result"
	if profile.Role == "review" {
		kind = "review-result"
	}
	if schema == nil {
		schema = resultSchema(profile.Role)
	}
	approve := func(ctx context.Context, kind string, data map[string]any) (bool, error) {
		policy := e.Run.Settings.ApprovalPolicies.ShellCommandExecution
		if policy == "auto-refuse" {
			return false, nil
		}
		if policy == "adapter-default" && kind == "shell-command-execution" {
			_, err := e.event(a.Id, "command.authorized", map[string]any{"command": data, "policy": policy})
			return err == nil, err
		}
		answer, err := e.Project.Decide(ctx, e.Run, a.Id, kind, "Allow this operation in the assigned workspace?", []string{"yes", "no"}, data)
		return answer == "yes", err
	}
	coordination := ""
	peers := []string{}
	for _, other := range e.Run.Workflow.Agents {
		if other.Id != a.Id && AllowedPeer(e.Run.Workflow, a.Id, other.Id) {
			peers = append(peers, other.Id)
		}
	}
	if len(peers) > 0 {
		// Peers run concurrently; a result returned before a reply arrives ends
		// the conversation, so agreement requires an explicit wait.
		coordination = "\nPeers running alongside you: " + strings.Join(peers, ", ") + ". Send them kind \"message\" packets. When your task depends on a peer's answer, wait for it with arena_inbox (newOnly=true, waitSeconds up to " + fmt.Sprint(maxInboxWait) + ") instead of finishing. If no reply arrives, proceed with your own proposal, state the assumption, and still complete the task."
	}
	in := Invocation{Attachments: attachments, AttachmentDir: e.Project.RunDir(e.Run.ID), Diagnostic: func(kind string, data map[string]any) error { _, err := e.event(a.Id, kind, data); return err }, Delivered: func() error { return e.Project.Acknowledge(e.Run, task) }, Usage: func(usage map[string]any) error {
		_, err := e.event(a.Id, "usage.reported", map[string]any{"provider": a.Runtime.Provider, "usage": usage})
		return err
	}, ProjectRoot: e.Project.Root, RunID: e.Run.ID, AgentID: a.Id, Runtime: a.Runtime, Dir: workspace.Path, Write: write, Commands: Has(profile.Capabilities, "run_commands"), Schema: schema, Approve: approve, Prompt: "Role profile:\n" + string(JSON(profile)) + "\nTask envelope:\n" + string(SafeJSON(task)) + "\nAssigned workspace: " + workspace.Path + "\nReturn the requested JSON result. The packets array may contain Arena messages with to, kind, and payloadJson; use an empty array when none are needed." + coordination, Output: func(text string) error {
		data := map[string]any{"bytes": len(text), "contentHash": Hash([]byte(text))}
		if e.Run.Retention == "full-local" && e.Run.Settings.Audit.ProcessOutput == "full" {
			data["output"] = text
		}
		_, err := e.event(a.Id, "process.output", data)
		return err
	}, Packet: func(ctx context.Context, data map[string]any) (any, error) {
		return e.Project.AgentPacket(ctx, e.Run, a.Id, data)
	}, Question: func(ctx context.Context, q string) (string, error) {
		packet := NewPacket(e.Run.ID, "question", a.Id, a.Runtime.Provider, "user", "arena", str(task["correlationId"]), map[string]any{"question": q, "decisionNeeded": "user answer"}, a.Runtime)
		if _, err := e.Project.Deliver(e.Run, packet); err != nil {
			return "", err
		}
		answer, err := e.Project.Decide(ctx, e.Run, a.Id, "question", q, nil, nil)
		if err != nil {
			return "", err
		}
		reply := NewPacket(e.Run.ID, "message", "user", "arena", a.Id, a.Runtime.Provider, str(packet["correlationId"]), map[string]any{"text": answer}, Runtime{Provider: "arena"})
		reply["replyTo"] = packet["messageId"]
		_, err = e.Project.Deliver(e.Run, reply)
		return answer, err
	}}
	result, err := e.invokeAttempts(ctx, a, in, task)
	release()
	if err != nil {
		failureKind := "agent.failed"
		if ctx.Err() != nil {
			failureKind = "agent.cancelled"
		}
		event, eventErr := e.event(a.Id, failureKind, map[string]any{"error": Redact(err.Error()), "timeout": errors.Is(err, context.DeadlineExceeded)})
		if eventErr != nil {
			return nil, eventErr
		}
		if checkpointErr := e.finishWorkspace(ctx, workspace); checkpointErr != nil {
			return nil, checkpointErr
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if callErr := e.fire(ctx, event); callErr != nil {
			return nil, callErr
		}
		return nil, err
	}
	// An undeliverable extra packet is the agent's mistake, not a run failure:
	// its result already returns to the delegator, so reject and continue.
	for _, packet := range anySlice(result["packets"]) {
		m, _ := packet.(map[string]any)
		payload := map[string]any{}
		err := json.Unmarshal([]byte(str(m["payloadJson"])), &payload)
		if err == nil {
			m["payload"] = payload
			_, err = e.Project.AgentPacket(ctx, e.Run, a.Id, m)
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			if _, eventErr := e.event(a.Id, "packet.rejected", map[string]any{"to": str(m["to"]), "kind": str(m["kind"]), "error": Redact(err.Error())}); eventErr != nil {
				return nil, eventErr
			}
		}
	}
	if err := e.finishWorkspace(ctx, workspace); err != nil {
		return nil, err
	}
	verifiedChanges := []string{}
	if write {
		var current Workspace
		if err := ReadJSON(filepath.Join(e.Project.RunDir(e.Run.ID), "workspaces", workspace.TaskID+".json"), &current); err != nil {
			return nil, err
		}
		if current.LastChange != "" {
			change, err := e.Project.Change(e.Run.ID, current.LastChange)
			if err != nil {
				return nil, err
			}
			verifiedChanges = append(verifiedChanges, change.ID)
			result["commit"] = change.Proposal
			result["checkpoint"] = change.Checkpoint
			result["reportedChangedFiles"] = result["changedFiles"]
			result["changedFiles"] = change.Paths
		}
	}
	packet := NewPacket(e.Run.ID, kind, a.Id, a.Runtime.Provider, from, fromProvider, str(task["correlationId"]), result, a.Runtime)
	packet["replyTo"] = task["messageId"]
	packet["arenaChangeIds"] = verifiedChanges
	if invocation != nil && (invocation["mode"] == "team-member" || invocation["mode"] == "team-composition") { // Position schemas are validated by the coordinator, not the ordinary result contract.
		packet["kind"] = "message"
		packet["payload"] = map[string]any{"text": string(JSON(result)), "position": result}
	}
	id, err := e.Project.Deliver(e.Run, packet)
	if err != nil {
		event, eventErr := e.event(a.Id, "validation.failed", map[string]any{"error": Redact(err.Error())})
		if eventErr != nil {
			return nil, eventErr
		}
		if _, eventErr := e.event(a.Id, "agent.failed", map[string]any{"error": "invalid provider result"}); eventErr != nil {
			return nil, eventErr
		}
		if eventErr := e.fire(ctx, event); eventErr != nil {
			return nil, eventErr
		}
		return nil, err
	}
	if _, err := e.event(a.Id, "agent.succeeded", map[string]any{"messageId": id}); err != nil {
		return nil, err
	}
	return packet, nil
}

func (e *Engine) route(ctx context.Context, r Router, parents []Packet) (Packet, []string, error) {
	if len(parents) == 0 {
		return nil, nil, fmt.Errorf("router %s has no input packet", r.Id)
	}
	inputs := map[string]any{}
	targets := []string{}
	for _, packet := range parents {
		target := ""
		branch := "fallback"
		if packet["kind"] == r.InputKind {
			value, exists, err := LookupPointer(packet["payload"], r.PayloadPointer)
			if err != nil {
				return nil, nil, err
			}
			for _, route := range r.Routes {
				if exists && scalarKey(value) == scalarKey(route.Equals) {
					if LoopRoute(route) {
						if used := e.loopCount(r.Id, route.To); used >= *route.MaxIterations {
							// The revision budget is spent: the router's fallback
							// decides, exactly as for an unmatched value.
							if _, err := e.event(r.Id, "loop.capped", map[string]any{"routeId": route.Id, "target": route.To, "iterations": used, "maxIterations": *route.MaxIterations, "messageId": packet["messageId"], "fallback": r.Fallback.Action}); err != nil {
								return nil, nil, err
							}
							break
						}
					}
					target = route.To
					branch = route.Id
					break
				}
			}
		}
		if target == "" {
			switch r.Fallback.Action {
			case "route":
				target = r.Fallback.To
			case "ask-user":
				choices := []string{"stop"}
				for _, route := range r.Routes {
					if LoopRoute(route) && e.loopCount(r.Id, route.To) >= *route.MaxIterations {
						continue
					}
					if !Has(choices, route.To) {
						choices = append(choices, route.To)
					}
				}
				from, _ := packet["from"].(map[string]any)
				prompt := fmt.Sprintf("%s got a %s from %s, but no route matches it (%s is ", r.Id, str(packet["kind"]), str(from["agentId"]), r.PayloadPointer)
				if packet["kind"] != r.InputKind {
					prompt = fmt.Sprintf("%s expected %s but got a %s from %s; ", r.Id, r.InputKind, str(packet["kind"]), str(from["agentId"])) + "choose where to send it"
				} else if value, exists, _ := LookupPointer(packet["payload"], r.PayloadPointer); exists {
					prompt += string(JSON(value)) + "). Where should it go?"
				} else {
					prompt += "missing). Where should it go?"
				}
				answer, err := e.Project.Decide(ctx, e.Run, r.Id, "router.fallback", prompt, choices, map[string]any{"messageId": packet["messageId"]})
				if err != nil {
					return nil, nil, err
				}
				if answer != "stop" {
					target = answer
				}
			}
		}
		if _, err := e.event(r.Id, "router.selected", map[string]any{"branch": branch, "target": target, "messageId": packet["messageId"]}); err != nil {
			return nil, nil, err
		}
		if target != "" {
			values, _ := inputs[target].([]any)
			inputs[target] = append(values, packet)
			if !Has(targets, target) {
				targets = append(targets, target)
			}
		}
	}
	bundle := NewPacket(e.Run.ID, "message", r.Id, "arena", "arena", "arena", "", map[string]any{"text": "Deterministically routed structured packets; original inputs are preserved per target."}, Runtime{Provider: "arena"})
	bundle["arenaRoutingInputs"] = inputs
	if _, err := e.Project.Deliver(e.Run, bundle); err != nil {
		return nil, nil, err
	}
	return bundle, targets, nil
}

func stringSlice(v any) []string {
	if values, ok := v.([]string); ok {
		return values
	}
	out := []string{}
	for _, x := range anySlice(v) {
		if s, ok := x.(string); ok {
			out = append(out, s)
		}
	}
	return out
}
func (e *Engine) fire(ctx context.Context, event Event) error {
	for _, rule := range e.Run.Workflow.OnCall {
		matched := false
		for _, trigger := range rule.Triggers {
			if trigger.Type != "event" || trigger.Event != event.Type {
				continue
			}
			switch trigger.Event {
			case "team.no-consensus":
				matched = trigger.TeamId == str(event.Data["teamId"])
			case "agent.failed":
				matched = trigger.AgentId == event.AgentID
			case "validation.failed":
				matched = true
			case "change.touches-paths":
				for _, pattern := range trigger.Paths {
					for _, path := range stringSlice(event.Data["paths"]) {
						ok, err := filepath.Match(pattern, path)
						if err != nil {
							return err
						}
						matched = matched || ok
					}
				}
			}
			if matched {
				break
			}
		}
		if !matched {
			continue
		}
		if err := e.onCall(ctx, rule.AgentId, event, false); err != nil {
			return err
		}
	}
	return nil
}
func (e *Engine) onCall(ctx context.Context, id string, trigger Event, manual bool) error {
	var limit int
	allowed := false
	for _, rule := range e.Run.Workflow.OnCall {
		if rule.AgentId == id {
			limit = rule.MaxInvocationsPerRun
			if !manual {
				allowed = true
			} else {
				for _, t := range rule.Triggers {
					allowed = allowed || t.Type == "manual"
				}
			}
		}
	}
	if !allowed {
		return fmt.Errorf("on-call trigger is not configured")
	}

	reservation, err := e.Project.WaitLock("oncall-" + Hash([]byte(e.Run.ID))[:20])
	if err != nil {
		return err
	}
	history, err := e.Project.Events(e.Run.ID)
	if err != nil {
		reservation.Close()
		return err
	}
	count, total := 0, 0
	duplicate := false
	for _, event := range history {
		if event.Type == "on-call.started" {
			total++
		}
		if event.Type == "on-call.started" && event.AgentID == id {
			count++
			duplicate = duplicate || event.Data["triggerEventId"] == trigger.ID
		}
	}
	if count >= limit || total >= e.Run.Settings.Limits.MaxOnCallInvocationsPerRun || duplicate {
		reservation.Close()
		_, err := e.event(id, "on-call.skipped", map[string]any{"triggerEventId": trigger.ID, "capped": count >= limit || total >= e.Run.Settings.Limits.MaxOnCallInvocationsPerRun, "duplicate": duplicate})
		return err
	}
	if _, err := e.event(id, "on-call.started", map[string]any{"triggerEventId": trigger.ID, "trigger": trigger.Type}); err != nil {
		reservation.Close()
		return err
	}
	if err := reservation.Close(); err != nil {
		return err
	}
	mode := trigger.Type
	if manual {
		mode = "manual"
	}
	agent, _ := agentByID(e.Run.Workflow, id)
	sourceProvider := "arena"
	if source, ok := e.agentByID(sourceID(trigger)); ok {
		sourceProvider = source.Runtime.Provider
	}
	contextPacket := NewPacket(e.Run.ID, "message", sourceID(trigger), sourceProvider, id, agent.Runtime.Provider, "", map[string]any{"text": "Configured on-call trigger: " + string(SafeJSON(trigger))}, Runtime{Provider: "arena"})
	parents := []Packet{contextPacket}
	if messageID := str(trigger.Data["messageId"]); messageID != "" {
		packet, readErr := e.Project.ReadPacket(e.Run.ID, messageID)
		if readErr == nil {
			parents = append(parents, packet)
		}
	}
	_, err = e.agent(ctx, agent, parents, map[string]any{"mode": "on-call", "trigger": mode, "triggerEventId": trigger.ID, "sourceId": sourceID(trigger)}, nil)
	if err != nil {
		return err
	}
	_, err = e.event(id, "on-call.completed", map[string]any{"triggerEventId": trigger.ID})
	return err
}

func sourceID(event Event) string {
	if event.AgentID != "" {
		return event.AgentID
	}
	return "user"
}

func (e *Engine) finishWorkspace(ctx context.Context, w Workspace) error {
	event, err := e.Project.FinishWorkspace(e.Run, w)
	if err != nil {
		return err
	}
	if event != nil && ctx.Err() == nil {
		if err := e.fire(ctx, *event); err != nil {
			return err
		}
		if e.Run.Settings.ApprovalPolicies.ChangeIntegration == "auto-if-clean" && e.Run.Workflow.Settings.Integration.Policy == "auto-if-clean" {
			_, err := e.Project.integrate(e.Run.ID, str(event.Data["changeId"]), "policy:auto-if-clean")
			if err != nil {
				_, eventErr := e.event(w.AgentID, "change.requires-review", map[string]any{"changeId": event.Data["changeId"], "reason": Redact(err.Error())})
				return eventErr
			}
		}
	}
	return nil
}
