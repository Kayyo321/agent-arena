package arena

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"time"

	c "github.com/Kayyo321/agent-arena/internal/contract"
)

// Packet keeps extension fields and opaque provider context intact.
type Packet map[string]any

func NewPacket(run, kind, from, fromProvider, to, toProvider, correlation string, payload map[string]any, runtime Runtime) Packet {
	if correlation == "" {
		correlation = ID("correlation-")
	}
	if runtime.Options == nil {
		runtime.Options = map[string]any{}
	}
	return Packet{"protocolVersion": "1.2", "messageId": ID("message-"), "runId": run, "kind": kind, "from": map[string]any{"agentId": from, "provider": fromProvider}, "to": map[string]any{"agentId": to, "provider": toProvider}, "createdAt": Now(), "correlationId": correlation, "attemptId": ID("attempt-"), "replyTo": nil, "payload": payload, "providerContext": map[string]any{"model": runtime.Model, "reasoningEffort": runtime.ReasoningEffort, "options": runtime.Options}}
}
func ValidatePacket(packet Packet, w Workflow) error {
	if e := c.ValidateEnvelope(JSON(packet)); e != nil {
		return e
	}
	if packet["kind"] == "team-result" {
		payload, _ := packet["payload"].(map[string]any)
		if e := ValidateTeamPacket(payload, w); e != nil {
			return e
		}
	}
	return nil
}
func ValidateTeamPacket(payload map[string]any, w Workflow) error {
	payload = convert[map[string]any](payload)
	var team *Team
	for _, t := range w.Teams {
		if t.Id == payload["teamId"] {
			v := t
			team = &v
		}
	}
	if team == nil {
		return fmt.Errorf("team result references an unknown team")
	}
	positions := anySlice(payload["memberPositions"])
	if len(positions) != len(team.Members) {
		return fmt.Errorf("team result requires every member position")
	}
	seen := map[string]bool{}
	for _, p := range positions {
		m, _ := p.(map[string]any)
		id := str(m["agentId"])
		if !Has(team.Members, id) || seen[id] {
			return fmt.Errorf("team result has duplicate or foreign member")
		}
		seen[id] = true
		if payload["outcome"] == "consensus" {
			if m["status"] != "affirmed" || m["verdict"] != payload["verdict"] || !Has(team.OutputContract.Verdicts, str(m["verdict"])) {
				return fmt.Errorf("consensus requires unanimous explicit affirmation")
			}
		}
	}
	return nil
}
func (p *Project) Deliver(r Run, packet Packet) (string, error) {
	return p.storePacket(r, packet, true)
}
func (p *Project) Send(r Run, packet Packet) (string, error) { return p.storePacket(r, packet, false) }
func (p *Project) storePacket(r Run, packet Packet, ack bool) (string, error) {
	if e := ValidatePacket(packet, r.Workflow); e != nil {
		return "", e
	}
	id := str(packet["messageId"])
	if e := validID(id); e != nil {
		return "", e
	}
	data := SafeJSON(packet)
	retained := r.Retention == "full-local"
	meta := map[string]any{"messageId": id, "kind": packet["kind"], "from": packet["from"], "to": packet["to"], "correlationId": packet["correlationId"], "attemptId": packet["attemptId"], "contentHash": Hash(data), "retained": retained}
	if retained {
		if e := Atomic(filepath.Join(p.RunDir(r.ID), "messages", id+".json"), data, 0600); e != nil {
			return "", e
		}
	}
	if _, e := p.Append(r.ID, r.Workflow.Id, "", "message.sent", meta); e != nil {
		return "", e
	}
	if ack {
		if _, err := p.Append(r.ID, r.Workflow.Id, "", "message.delivered", meta); err != nil {
			return "", err
		}
	}
	return id, nil
}
func (p *Project) ReadPacket(run, id string) (Packet, error) {
	var packet Packet
	if e := validID(run); e != nil {
		return nil, e
	}
	if e := validID(id); e != nil {
		return nil, e
	}
	if e := ReadJSON(filepath.Join(p.RunDir(run), "messages", id+".json"), &packet); e != nil {
		return nil, e
	}
	return packet, nil
}
func AllowedPeer(w Workflow, from, to string) bool {
	for _, e := range w.Relationships {
		if e.Type == "communicates" && (e.From == from && e.To == to || e.Direction == "two-way" && e.To == from && e.From == to) {
			return true
		}
	}
	return false
}
func resultSchema(role string) map[string]any {
	properties := map[string]any{"summary": map[string]any{"type": "string"}, "changedFiles": arraySchema(map[string]any{"type": "string"}), "validation": arraySchema(map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}, "outcome": map[string]any{"type": "string"}}, "required": []string{"command", "outcome"}, "additionalProperties": false}), "limitations": arraySchema(map[string]any{"type": "string"}), "nextAction": map[string]any{"type": "string"}}
	if role == "review" {
		properties = map[string]any{"format": map[string]any{"type": "string", "enum": []string{"arena.review-verdict.v1"}}, "outcome": map[string]any{"type": "string", "enum": []string{"Approved", "ChangesRequested", "Deny", "NeedsMoreInformation"}}, "summary": map[string]any{"type": "string"}, "rationale": map[string]any{"type": "string"}, "findings": arraySchema(map[string]any{"type": "object", "properties": map[string]any{"severity": map[string]any{"type": "string", "enum": []string{"critical", "high", "medium", "low", "info"}}, "file": map[string]any{"type": "string"}, "line": map[string]any{"type": []string{"integer", "null"}}, "evidence": map[string]any{"type": "string"}, "recommendation": map[string]any{"type": "string"}}, "required": []string{"severity", "file", "line", "evidence", "recommendation"}, "additionalProperties": false}), "openQuestions": arraySchema(map[string]any{"type": "string"}), "nextAction": map[string]any{"type": "string"}}
	}
	properties["packets"] = arraySchema(objectSchema(map[string]any{"to": map[string]any{"type": "string"}, "kind": map[string]any{"type": "string"}, "payloadJson": map[string]any{"type": "string"}}))
	return objectSchema(properties)
}
func arraySchema(item map[string]any) map[string]any {
	return map[string]any{"type": "array", "items": item}
}
func objectSchema(properties map[string]any) map[string]any {
	return map[string]any{"type": "object", "properties": properties, "required": sortedKeys(properties), "additionalProperties": false}
}
func taskPayload(objective, dir, base string, context []string, write bool) map[string]any {
	mode := "read-only"
	if write {
		mode = "write"
	}
	return map[string]any{"objective": objective, "acceptanceCriteria": []string{"Return the requested structured result and report actual validation and limitations."}, "context": context, "constraints": []string{"Use only the assigned workspace. Do not merge branches or access provider credentials."}, "files": []string{}, "expectedOutput": []string{"structured result"}, "workspace": map[string]any{"mode": mode, "baseCommit": base}}
}
func (p *Project) AgentPacket(ctx context.Context, r Run, from string, input map[string]any) (any, error) {
	a, ok := r.agent(from)
	if !ok {
		return nil, fmt.Errorf("unknown source agent")
	}
	to := str(input["to"])
	kind := str(input["kind"])
	payload, _ := input["payload"].(map[string]any)
	switch kind {
	case "message":
		if !AllowedPeer(r.Workflow, from, to) {
			return nil, fmt.Errorf("peer route %s -> %s is not configured", from, to)
		}
		target, ok := agentByID(r.Workflow, to)
		if !ok {
			return nil, fmt.Errorf("unknown target agent")
		}
		// Check the packet before any sharing prompt: the user should never
		// approve a packet Arena would then reject.
		if text, ok := payload["text"].(string); !ok || text == "" {
			return nil, fmt.Errorf(`kind "message" needs payload.text, a non-empty string; keep structured fields alongside it, e.g. {"text":"<summary>","acceptanceCriteria":[...]}`)
		}
		packet := NewPacket(r.ID, kind, from, a.Runtime.Provider, to, target.Runtime.Provider, "", payload, a.Runtime)
		if err := ValidatePacket(packet, r.Workflow); err != nil {
			return nil, fmt.Errorf("invalid message packet: %w", err)
		}
		id := ""
		answer, decision, err := p.shareGate(r, from, a.Runtime.Provider, to, target.Runtime.Provider, "peer message", map[string]any{"payload": payload, "target": to}, func(decision string) (err error) {
			id, err = p.deferPacket(r.ID, decision, packet)
			return err
		})
		if err != nil {
			return nil, err
		}
		if answer == "" {
			// Queued: Answer delivers it, even after this call returns.
			answer, err = p.awaitRoute(ctx, r.ID, shareRoute(a.Runtime.Provider, to, target.Runtime.Provider), shareAckWait)
			if err != nil {
				return nil, err
			}
			if answer == "" {
				return map[string]any{"status": "awaiting-approval", "messageId": id, "decisionId": decision, "note": "The user must approve sharing " + a.Runtime.Provider + " context with " + to + " (" + target.Runtime.Provider + "). Arena delivers this message automatically once approved; do not resend it. Continue your task, or wait for replies with arena_inbox."}, nil
			}
			if answer == "yes" {
				return id, nil
			}
		}
		if answer != "yes" {
			return nil, errShareDeclined
		}
		return p.Deliver(r, packet)
	case "context-proposal":
		var q ContextProposal
		if err := DecodeJSON(JSON(payload), &q); err != nil {
			return nil, fmt.Errorf("invalid context-proposal payload: %w", err)
		}
		if q.WorkflowID != r.Workflow.Id {
			return nil, fmt.Errorf("context is scoped to the active workflow")
		}
		packet := NewPacket(r.ID, kind, from, a.Runtime.Provider, "context-manager", "arena", "", payload, a.Runtime)
		if _, e := p.Deliver(r, packet); e != nil {
			return nil, e
		}
		result, e := p.ProposeContext(q, from, a.Runtime.Provider, r.ID, str(packet["correlationId"]), false)
		if e != nil {
			return nil, e
		}
		reply := NewPacket(r.ID, "context-result", "context-manager", "arena", from, a.Runtime.Provider, str(packet["correlationId"]), convert[map[string]any](result), Runtime{Provider: "arena"})
		reply["replyTo"] = packet["messageId"]
		if _, e := p.Deliver(r, reply); e != nil {
			return nil, e
		}
		return result, nil
	case "coworker-request":
		if payload["workflowId"] != r.Workflow.Id {
			return nil, fmt.Errorf("coworker-request payload.workflowId must be %q; it asks the orchestrator for a new agent. To talk to an existing peer, send kind \"message\"", r.Workflow.Id)
		}
		packet := NewPacket(r.ID, kind, from, a.Runtime.Provider, "arena-orchestrator", r.Settings.DefaultModel.Provider, "", payload, a.Runtime)
		if _, e := p.Deliver(r, packet); e != nil {
			return nil, e
		}
		var q CoworkerRequest
		if err := DecodeJSON(JSON(payload), &q); err != nil {
			return nil, fmt.Errorf("invalid coworker-request payload: %w", err)
		}
		// Persistence metadata belongs to Arena, never to the requesting agent.
		q.ID, q.State, q.CreatedAt = "", "", ""
		q.From = from
		q.RunID = r.ID
		q.AgentID = str(payload["agentId"])
		return p.handleCoworkerPacket(ctx, r, q)
	}
	return nil, fmt.Errorf("unsupported agent-originated packet kind %s", kind)
}
func (p *Project) Inbox(r Run, agent string) ([]Packet, error) {
	paths, e := filepath.Glob(filepath.Join(p.RunDir(r.ID), "messages", "*.json"))
	if e != nil {
		return nil, e
	}
	packets := []Packet{}
	for _, path := range paths {
		var packet Packet
		if e := ReadJSON(path, &packet); e != nil {
			return nil, e
		}
		to, _ := packet["to"].(map[string]any)
		if to["agentId"] == agent && packet["kind"] == "message" {
			packets = append(packets, packet)
		}
	}
	return packets, nil
}

// WaitInbox returns the actor's peer messages oldest first. With newOnly it
// skips already returned IDs and polls until one arrives or wait elapses.
func (p *Project) WaitInbox(ctx context.Context, r Run, actor string, wait time.Duration, returned map[string]bool, newOnly bool) ([]Packet, error) {
	deadline := time.Now().Add(wait)
	for {
		all, err := p.Inbox(r, actor)
		if err != nil {
			return nil, err
		}
		sort.SliceStable(all, func(i, j int) bool { return str(all[i]["createdAt"]) < str(all[j]["createdAt"]) })
		packets := []Packet{}
		for _, packet := range all {
			if !newOnly || !returned[str(packet["messageId"])] {
				packets = append(packets, packet)
			}
		}
		if len(packets) > 0 || !time.Now().Before(deadline) {
			return packets, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(min(500*time.Millisecond, time.Until(deadline))):
		}
	}
}

func (p *Project) Acknowledge(r Run, packet Packet) error {
	_, err := p.Append(r.ID, r.Workflow.Id, "", "message.delivered", map[string]any{"messageId": packet["messageId"], "kind": packet["kind"], "from": packet["from"], "to": packet["to"]})
	return err
}
