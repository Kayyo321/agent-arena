package arena

import (
	"context"
	"fmt"
	assets "github.com/Kayyo321/agent-arena"
	"path/filepath"
)

type CoworkerRequest struct {
	ID            string   `json:"id"`
	WorkflowID    string   `json:"workflowId"`
	From          string   `json:"from"`
	AgentID       string   `json:"agentId"`
	Role          string   `json:"role"`
	Objective     string   `json:"objective"`
	Runtime       Runtime  `json:"runtime"`
	Capabilities  []string `json:"capabilities"`
	WorkspaceMode string   `json:"workspaceMode"`
	Files         []string `json:"files"`
	Reason        string   `json:"reason"`
	RunID         string   `json:"runId,omitempty"`
	State         string   `json:"state"`
	CreatedAt     string   `json:"createdAt"`
}

// coworkerRequestPath confines a request file to .state/coworker-requests.
// Request IDs are Arena-generated; a malformed one never names a path.
func (p *Project) coworkerRequestPath(id string) (string, error) {
	if err := validID(id); err != nil {
		return "", fmt.Errorf("coworker request: %w", err)
	}
	return SafePath(p.Path(), filepath.ToSlash(filepath.Join(".state", "coworker-requests", id+".json")))
}

func (p *Project) RequestCoworker(ctx context.Context, q CoworkerRequest, approved bool, invoker Invoker) (CoworkerRequest, error) {
	w, profiles, version, e := p.LoadVersion(q.WorkflowID)
	if e != nil {
		return q, e
	}
	s, e := p.Settings()
	if e != nil {
		return q, e
	}
	source, ok := agentByID(w, q.From)
	if !ok {
		if f := fanOutChildOf(w, q.From); f != "" {
			return q, fmt.Errorf("fan-out children cannot request coworkers; fan-out %s caps its own agents with maxChildren", f)
		}
		return q, fmt.Errorf("requesting agent is not in the workflow")
	}
	if q.ID == "" {
		q.ID = ID("request-")
		q.CreatedAt = Now()
		q.State = "requested"
	}
	path, e := p.coworkerRequestPath(q.ID)
	if e != nil {
		return q, e
	}
	save := func() error { return Atomic(path, SafeJSON(q), 0600) }
	policy := s.ApprovalPolicies.ArenaSubagentModificationInquiry
	if policy == "auto-refuse" {
		q.State = "refused"
		if e := save(); e != nil {
			return q, e
		}
		return q, p.Audit(w.Id, "coworker.refused", map[string]any{"request": q, "policy": policy})
	}
	if e := validID(q.AgentID); e != nil {
		return q, e
	}
	if q.Role == "" || q.Objective == "" || q.Reason == "" || len(q.Capabilities) == 0 {
		return q, fmt.Errorf("coworker request needs explicit role, objective, capabilities and reason")
	}
	if e := ValidateRuntime(q.Runtime); e != nil {
		return q, e
	}
	if _, ok := q.Runtime.Options["commandApproval"]; ok {
		return q, fmt.Errorf("a coworker request cannot select blanket command approval; only the user sets commandApproval, by editing the workflow")
	}
	if q.WorkspaceMode != "read-only" && q.WorkspaceMode != "write" {
		return q, fmt.Errorf("explicit workspaceMode is required")
	}
	if Has(q.Capabilities, "write") != (q.WorkspaceMode == "write") {
		return q, fmt.Errorf("capabilities and workspaceMode disagree")
	}
	if _, ok := agentByID(w, q.AgentID); ok {
		return q, fmt.Errorf("agent ID already exists")
	}
	if source.Activation == "on-call" {
		return q, fmt.Errorf("on-call agents cannot create ordinary graph edges")
	}
	q.State = "needs-user"
	if e := save(); e != nil {
		return q, e
	}
	if e := p.Audit(w.Id, "coworker.requested", map[string]any{"request": q, "policy": policy, "controlAgent": "arena-orchestrator"}); e != nil {
		return q, e
	}
	if policy == "ask-user" && !approved {
		return q, nil
	}
	if s.ApprovalPolicies.AgentWorkflowEdit == "auto-refuse" {
		q.State = "refused"
		_ = save()
		return q, fmt.Errorf("agent-workflow-edit policy refuses graph changes")
	}
	if s.ApprovalPolicies.AgentWorkflowEdit == "ask-user" && !approved {
		return q, nil
	}
	if source.Runtime.Provider != s.DefaultModel.Provider {
		switch s.ApprovalPolicies.CrossProviderContextSharing {
		case "deny":
			q.State = "refused"
			if err := save(); err != nil {
				return q, err
			}
			return q, p.Audit(w.Id, "coworker.refused", map[string]any{"requestId": q.ID, "reason": "cross-provider control sharing denied"})
		case "ask-user":
			if !approved {
				return q, nil
			}
		}
	}
	requests, e := filepath.Glob(p.Path(".state", "coworker-requests", "*.json"))
	if e != nil {
		return q, e
	}
	count := 0
	for _, path := range requests {
		var old CoworkerRequest
		if ReadJSON(path, &old) == nil && old.RunID == q.RunID && old.WorkflowID == q.WorkflowID && old.State == "accepted" {
			count++
		}
	}
	if count >= s.Limits.MaxCoworkerAdditionsPerRun {
		return q, fmt.Errorf("coworker addition cap reached")
	}
	if string(JSON(q.Runtime)) != string(JSON(s.DefaultModel)) {
		ready := Probe(ctx, p.Root, q.Runtime)
		if !ready.CanStart {
			return q, fmt.Errorf("requested coworker runtime is not ready: %s (%s)", ready.Detail, ready.Auth)
		}
	}
	status := Probe(ctx, p.Root, s.DefaultModel)
	if !status.CanStart {
		return q, fmt.Errorf("permanent orchestrator unavailable: %s", status.Detail)
	}
	schema := map[string]any{"type": "object", "properties": map[string]any{"accept": map[string]any{"type": "boolean"}, "rationale": map[string]any{"type": "string"}, "instructions": map[string]any{"type": "string"}}, "required": []string{"accept", "rationale", "instructions"}, "additionalProperties": false}

	bounded, cancel := inferenceDeadline(ctx, s.Limits.MaxAgentRunMinutes)
	defer cancel()
	slot, e := p.WaitLock("orchestrator")
	if e != nil {
		return q, e
	}
	defer slot.Close()
	result, e := invoker.Invoke(bounded, Invocation{Runtime: s.DefaultModel, Dir: p.Root, Prompt: string(assets.Skill()) + "\nYou are the permanent arena-orchestrator. Evaluate this complete, policy-authorized coworker request. Do not change runtime choices or edit files. Return whether to accept, a concise rationale, and role instructions. Request: " + string(JSON(q)), Schema: schema})
	if e != nil {
		return q, e
	}
	if e := p.Audit(w.Id, "coworker.control-verdict", map[string]any{"requestId": q.ID, "controlRuntime": s.DefaultModel, "verdict": result}); e != nil {
		return q, e
	}
	if result["accept"] != true {
		q.State = "refused"
		return q, save()
	}
	parent := source.Id
	level := *source.Level + 1
	for _, team := range w.Teams {
		if Has(team.Members, source.Id) {
			parent = team.Id
		}
	}
	for _, f := range w.FanOuts {
		if f.Planner == source.Id || f.Worker == source.Id {
			parent = f.Id
		}
	}
	node := Agent{Id: q.AgentID, Level: &level, Profile: ".agents/" + w.Id + "/" + q.AgentID + ".agent.json", Runtime: q.Runtime}
	w.Agents = append(w.Agents, node)
	w.Relationships = append(w.Relationships, Relationship{Type: "delegates", From: parent, To: node.Id})
	profiles[node.Id] = NewProfile(node.Id, q.Role, q.Objective, str(result["instructions"]), q.Capabilities)
	tx, e := p.Save(w, profiles, version)
	if e != nil {
		return q, e
	}
	q.State = "accepted"
	if err := p.QueueCoworker(q.RunID, ConfigUpdate{RequestID: q.ID, Parent: parent, Agent: node, Profile: profiles[node.Id], Transaction: tx}); err != nil {
		return q, err
	}
	if e := save(); e != nil {
		return q, e
	}
	return q, p.Audit(w.Id, "coworker.accepted", map[string]any{"requestId": q.ID, "transaction": tx})
}

func (p *Project) handleCoworkerPacket(ctx context.Context, r Run, q CoworkerRequest) (CoworkerRequest, error) {
	result, err := p.RequestCoworker(ctx, q, false, Providers{})
	if err != nil || result.State != "needs-user" {
		return result, err
	}
	answer, err := p.Decide(ctx, r, q.From, "coworker-request", "Approve sharing this complete request with the project control runtime and adding the coworker?", []string{"yes", "no"}, map[string]any{"request": result, "controlRuntime": r.Settings.DefaultModel})
	if err != nil {
		return result, err
	}
	if answer != "yes" {
		result.State = "refused"
		path, err := p.coworkerRequestPath(result.ID)
		if err != nil {
			return result, err
		}
		if err := Atomic(path, SafeJSON(result), 0600); err != nil {
			return result, err
		}
		return result, p.Audit(r.Workflow.Id, "coworker.refused", map[string]any{"requestId": result.ID, "reason": "user declined"})
	}
	return p.RequestCoworker(ctx, result, true, Providers{})
}
