package arena

import (
	"context"
	"fmt"
)

type position struct {
	AgentID  string   `json:"agentId"`
	Verdict  *string  `json:"verdict"`
	Status   string   `json:"status"`
	Summary  string   `json:"summary"`
	Evidence []string `json:"evidence"`
	Affirm   bool     `json:"affirm"`
}

func (e *Engine) team(ctx context.Context, t Team, parents []Packet) (Packet, error) {
	providers := []string{}
	for _, id := range t.Members {
		a, _ := agentByID(e.Run.Workflow, id)
		if !Has(providers, a.Runtime.Provider) {
			providers = append(providers, a.Runtime.Provider)
		}
	}
	for _, packet := range parents {
		source, _ := packet["from"].(map[string]any)
		provider := str(source["provider"])
		if provider != "arena" && !Has(providers, provider) {
			providers = append(providers, provider)
		}
		providers = uniqueStrings(append(providers, stringSlice(packet["arenaSourceProviders"])...))
	}
	if len(providers) > 1 {
		policy := e.Run.Settings.ApprovalPolicies.CrossProviderContextSharing
		if policy == "deny" {
			return nil, fmt.Errorf("cross-provider team deliberation is denied by project policy")
		}
		if policy == "ask-user" {
			answer, err := e.Project.Decide(ctx, e.Run, t.Id, "cross-provider-context-sharing", "Allow task-scoped deliberation and final affirmations across these team providers?", []string{"yes", "no"}, map[string]any{"providers": providers, "teamId": t.Id})
			if err != nil {
				return nil, err
			}
			if answer != "yes" {
				return nil, fmt.Errorf("cross-provider team deliberation declined")
			}
		}
		e.mu.Lock()
		e.teamConsent[t.Id] = true
		e.mu.Unlock()
	}
	if _, err := e.event(t.Id, "team.started", map[string]any{"teamId": t.Id, "members": t.Members}); err != nil {
		return nil, err
	}
	positions := []position{}
	consensus := false
	verdict := ""
	rounds := 0
	memberChanges := map[string][]string{}
	schema := objectSchema(map[string]any{"verdict": map[string]any{"type": "string", "enum": t.OutputContract.Verdicts}, "affirm": map[string]any{"type": "boolean"}, "summary": map[string]any{"type": "string"}, "evidence": arraySchema(map[string]any{"type": "string"})})
	collect := func(prompt string) ([]position, error) {
		type reply struct {
			p       position
			changes []string
			err     error
		}
		replies := make(chan reply, len(t.Members))
		for _, id := range t.Members {
			go func(id string) {
				a, _ := agentByID(e.Run.Workflow, id)
				contextParents := append([]Packet{}, parents...)
				contextParents = append(contextParents, NewPacket(e.Run.ID, "message", "team-coordinator", "arena", id, a.Runtime.Provider, "", map[string]any{"text": prompt}, Runtime{Provider: "arena"}))
				packet, err := e.agent(ctx, a, contextParents, map[string]any{"mode": "team-member", "teamId": t.Id}, schema)
				if err != nil {
					replies <- reply{p: position{AgentID: id, Status: "unavailable", Summary: Redact(err.Error()), Evidence: []string{}}, err: nil}
					return
				}
				payload, _ := packet["payload"].(map[string]any)
				result, _ := payload["position"].(map[string]any)
				v := str(result["verdict"])
				p := position{AgentID: id, Verdict: &v, Status: "disagreed", Summary: str(result["summary"]), Evidence: stringSlice(result["evidence"]), Affirm: result["affirm"] == true}
				if !Has(t.OutputContract.Verdicts, v) {
					p.Status = "unavailable"
					p.Verdict = nil
					p.Summary = "member returned a verdict outside the configured contract"
				}
				replies <- reply{p: p, changes: stringSlice(packet["arenaChangeIds"])}
			}(id)
		}
		byID := map[string]position{}
		for range t.Members {
			select {
			case reply := <-replies:
				if reply.err != nil {
					return nil, reply.err
				}
				byID[reply.p.AgentID] = reply.p
				if len(reply.changes) > 0 {
					memberChanges[reply.p.AgentID] = reply.changes
				}
			}
		}
		out := []position{}
		for _, id := range t.Members {
			out = append(out, byID[id])
		}
		return out, nil
	}
	for round := 1; round <= t.Consensus.MaxRounds; round++ {
		rounds = round
		var err error
		positions, err = collect(fmt.Sprintf("Team %s, deliberation round %d. Return your independent position using exactly one allowed verdict. Prior positions: %s", t.Id, round, string(JSON(positions))))
		if err != nil {
			return nil, err
		}
		if _, err := e.event(t.Id, "team.round", map[string]any{"teamId": t.Id, "round": round, "positions": positions}); err != nil {
			return nil, err
		}
		candidate := ""
		agree := true
		for _, p := range positions {
			if p.Verdict == nil || p.Status == "unavailable" {
				agree = false
				break
			}
			if candidate == "" {
				candidate = *p.Verdict
			}
			if *p.Verdict != candidate {
				agree = false
			}
		}
		if !agree {
			continue
		}
		affirmed, err := collect(fmt.Sprintf("Final affirmation for team %s in round %d. Candidate verdict is %q. Explicitly set affirm=true only if you affirm this exact verdict after considering every member's position. Otherwise set affirm=false and preserve your position. Positions: %s", t.Id, round, candidate, string(JSON(positions))))
		if err != nil {
			return nil, err
		}
		positions = affirmed
		consensus = true
		for i := range positions {
			p := &positions[i]
			if !p.Affirm || p.Verdict == nil || *p.Verdict != candidate || p.Status == "unavailable" {
				consensus = false
			} else {
				p.Status = "affirmed"
			}
		}
		if _, err := e.event(t.Id, "team.affirmations", map[string]any{"teamId": t.Id, "round": round, "positions": positions, "unanimous": consensus}); err != nil {
			return nil, err
		}
		if consensus {
			verdict = candidate
			break
		}
	}
	memberPositions := []any{}
	dissent := []any{}
	for _, p := range positions {
		memberPositions = append(memberPositions, map[string]any{"agentId": p.AgentID, "verdict": p.Verdict, "status": p.Status, "summary": p.Summary, "evidence": p.Evidence})
		if !consensus && p.Status != "affirmed" {
			reason := p.Summary
			if reason == "" {
				reason = "member did not affirm a unanimous verdict"
			}
			dissent = append(dissent, map[string]any{"agentId": p.AgentID, "position": p.Verdict, "reason": reason})
		}
	}
	if !consensus && len(dissent) == 0 {
		dissent = append(dissent, map[string]any{"agentId": t.Facilitator, "position": nil, "reason": "no unanimous final affirmation"})
	}
	outcome := "no-consensus"
	var chosen any
	if consensus {
		outcome = "consensus"
		chosen = verdict
	}
	summary := fmt.Sprintf("Team %s returned %s after %d round(s).", t.Id, outcome, rounds)
	rationale := "Member positions and explicit final affirmations are retained in the local journal."
	next := "Inspect dissent and decide whether to revise the task or escalate."
	if consensus {
		next = "Follow the configured route for the affirmed verdict."
	}
	// The facilitator supplies prose, while Arena owns and validates the verdict.
	facilitator, _ := agentByID(e.Run.Workflow, t.Facilitator)
	schema = objectSchema(map[string]any{"summary": map[string]any{"type": "string"}, "rationale": map[string]any{"type": "string"}, "nextAction": map[string]any{"type": "string"}, "verdict": map[string]any{"type": "string", "enum": t.OutputContract.Verdicts}, "affirm": map[string]any{"type": "boolean"}, "evidence": arraySchema(map[string]any{"type": "string"})})
	finalContext := NewPacket(e.Run.ID, "message", "team-coordinator", "arena", t.Facilitator, facilitator.Runtime.Provider, "", map[string]any{"text": fmt.Sprintf("Compose the team's final prose. Arena has fixed outcome=%s, verdict=%v. Do not change it or hide disagreement. Positions: %s", outcome, chosen, string(JSON(memberPositions)))}, Runtime{Provider: "arena"})
	changeIDs := []string{}
	for _, id := range t.Members {
		changeIDs = append(changeIDs, memberChanges[id]...)
	}
	finalContext["arenaChangeIds"] = uniqueStrings(changeIDs)
	final, err := e.agent(ctx, facilitator, []Packet{finalContext}, map[string]any{"mode": "team-composition", "teamId": t.Id}, schema)
	if err == nil {
		payload, _ := final["payload"].(map[string]any)
		p, _ := payload["position"].(map[string]any)
		if str(p["summary"]) != "" {
			summary = str(p["summary"])
		}
		if str(p["rationale"]) != "" {
			rationale = str(p["rationale"])
		}
		if str(p["nextAction"]) != "" {
			next = str(p["nextAction"])
		}
	} else {
		outcome = "no-consensus"
		chosen = nil
		dissent = append(dissent, map[string]any{"agentId": t.Facilitator, "position": nil, "reason": "facilitator unavailable for final composition"})
	}
	payload := map[string]any{"format": "arena.team-verdict.v1", "teamId": t.Id, "outcome": outcome, "verdict": chosen, "summary": summary, "rationale": rationale, "evidence": []string{}, "memberPositions": memberPositions, "dissent": dissent, "rounds": rounds, "nextAction": next}
	caller, callerProvider := "arena", "arena"
	if len(parents) > 0 {
		actor, _ := parents[0]["from"].(map[string]any)
		if str(actor["agentId"]) != "" {
			caller, callerProvider = str(actor["agentId"]), str(actor["provider"])
		}
	}
	packet := NewPacket(e.Run.ID, "team-result", t.Id, "arena", caller, callerProvider, "", payload, Runtime{Provider: "arena"})
	packet["arenaChangeIds"] = uniqueStrings(changeIDs)
	packet["arenaSourceProviders"] = providers
	if _, err := e.Project.Deliver(e.Run, packet); err != nil {
		return nil, err
	}
	event, err := e.event(t.Id, "team."+outcome, map[string]any{"teamId": t.Id, "messageId": packet["messageId"], "outcome": outcome, "verdict": chosen})
	if err != nil {
		return nil, err
	}
	if outcome == "no-consensus" {
		if err := e.fire(ctx, event); err != nil {
			return nil, err
		}
	}
	return packet, nil
}
