package arena

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func uniqueStrings(values []string) []string {
	out := []string{}
	for _, value := range values {
		if value != "" && !Has(out, value) {
			out = append(out, value)
		}
	}
	return out
}

// shareAckWait bounds how long an agent-originated peer message waits for a
// pending sharing decision before Arena acknowledges it as queued. Provider
// MCP clients time out slow tool calls (Codex after 60s by default), and an
// agent whose call timed out resends, raising yet another prompt.
var shareAckWait = 20 * time.Second

// errShareDeclined is returned when the user declines a sharing route.
var errShareDeclined = errors.New("cross-provider context sharing declined")

// A sharing route is content from one provider reaching one recipient agent
// on another provider. Under ask-user, one answer covers the route for the rest
// of the run: a peer message, an agent's retries of it and the later handoff to
// the same recipient raise a single prompt.
func shareRoute(from, recipient, to string) string {
	return from + " -> " + recipient + " (" + to + ")"
}

// shareGrant records the user's answer for a route in one run.
type shareGrant struct {
	Route      string `json:"route"`
	DecisionID string `json:"decisionId"`
	Answer     string `json:"answer"`
	At         string `json:"at"`
}

// deferredPacket is a validated peer message held until its route's pending
// decision is answered. Answer delivers or rejects it, so delivery does not
// depend on the sender's MCP process still running.
type deferredPacket struct {
	DecisionID string `json:"decisionId"`
	Key        string `json:"key"`
	Packet     Packet `json:"packet"`
}

func (p *Project) grantPath(run, route string) string {
	return filepath.Join(p.RunDir(run), "sharing", Hash([]byte(route))+".json")
}

func (p *Project) readGrant(run, route string) (string, error) {
	var grant shareGrant
	err := ReadJSON(p.grantPath(run, route), &grant)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	return grant.Answer, err
}

// ShareContext blocks until sharing policy allows content from provider from
// to reach recipient on provider to.
func (p *Project) ShareContext(ctx context.Context, r Run, actor, from, recipient, to, scope string, data map[string]any) error {
	answer, _, err := p.shareGate(r, actor, from, recipient, to, scope, data, nil)
	if err == nil && answer == "" {
		answer, err = p.awaitRoute(ctx, r.ID, shareRoute(from, recipient, to), 0)
	}
	if err != nil {
		return err
	}
	if answer != "yes" {
		return errShareDeclined
	}
	return nil
}

// shareGate applies sharing policy without blocking. It returns the route's
// answer when one is known; otherwise it returns the route's open decision,
// raising one only if none is pending, and runs queue under the sharing lock so
// an answer cannot land between the check and the queued packet.
func (p *Project) shareGate(r Run, actor, from, recipient, to, scope string, data map[string]any, queue func(decision string) error) (answer, decision string, err error) {
	if from == to || from == "arena" || to == "arena" {
		return "yes", "", nil
	}
	switch r.Settings.ApprovalPolicies.CrossProviderContextSharing {
	case "deny":
		return "", "", fmt.Errorf("cross-provider context sharing is denied")
	case "ask-user":
	default:
		return "yes", "", nil
	}
	route := shareRoute(from, recipient, to)
	l, err := p.WaitLock("sharing-" + r.ID)
	if err != nil {
		return "", "", err
	}
	defer l.Close()
	if answer, err := p.readGrant(r.ID, route); err != nil || answer != "" {
		return answer, "", err
	}
	open, err := p.Decisions(r.ID)
	if err != nil {
		return "", "", err
	}
	for _, d := range open {
		if d.Route == route {
			decision = d.ID
			break
		}
	}
	if decision == "" {
		d := Decision{ID: ID("decision-"), RunID: r.ID, AgentID: actor, Kind: "cross-provider-context-sharing", Prompt: fmt.Sprintf("Share %s context with %s (%s) for the rest of this run? First item: task-scoped %s.", from, recipient, to, scope), Choices: []string{"yes", "no"}, Context: data, Route: route, CreatedAt: Now()}
		if err := Atomic(filepath.Join(p.RunDir(r.ID), "decisions", d.ID+".json"), SafeJSON(d), 0600); err != nil {
			return "", "", err
		}
		if _, err := p.Append(r.ID, r.Workflow.Id, actor, "decision.requested", map[string]any{"decision": d}); err != nil {
			return "", "", err
		}
		decision = d.ID
	}
	if queue != nil {
		if err := queue(decision); err != nil {
			return "", "", err
		}
	}
	return "", decision, nil
}

// awaitRoute polls for the route's answer. A positive wait returns "" when it
// elapses first; zero waits until the answer or ctx ends.
func (p *Project) awaitRoute(ctx context.Context, run, route string, wait time.Duration) (string, error) {
	var deadline <-chan time.Time
	if wait > 0 {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		deadline = timer.C
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		if answer, err := p.readGrant(run, route); err != nil || answer != "" {
			return answer, err
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-deadline:
			return "", nil
		case <-ticker.C:
		}
	}
}

// deferPacket queues packet under decision. An identical packet already queued
// for that decision (an agent's retry) is not queued twice; its message ID is
// returned instead.
func (p *Project) deferPacket(run, decision string, packet Packet) (string, error) {
	key := Hash(JSON(map[string]any{"to": packet["to"], "kind": packet["kind"], "payload": packet["payload"]}))
	queued, err := p.deferredPackets(run, decision)
	if err != nil {
		return "", err
	}
	for _, q := range queued {
		if q.Key == key {
			return str(q.Packet["messageId"]), nil
		}
	}
	id := str(packet["messageId"])
	if err := validID(id); err != nil {
		return "", err
	}
	return id, Atomic(filepath.Join(p.RunDir(run), "deferred", id+".json"), SafeJSON(deferredPacket{DecisionID: decision, Key: key, Packet: packet}), 0600)
}

func (p *Project) deferredPackets(run, decision string) ([]deferredPacket, error) {
	paths, err := filepath.Glob(filepath.Join(p.RunDir(run), "deferred", "*.json"))
	if err != nil {
		return nil, err
	}
	out := []deferredPacket{}
	for _, path := range paths {
		var q deferredPacket
		if err := ReadJSON(path, &q); err != nil {
			return nil, err
		}
		if q.DecisionID == decision {
			out = append(out, q)
		}
	}
	return out, nil
}

// settleRoute delivers or rejects the packets queued under an answered route
// decision, then records the route's grant so waiters and later shares see it.
func (p *Project) settleRoute(r Run, d Decision, answer string) error {
	l, err := p.WaitLock("sharing-" + r.ID)
	if err != nil {
		return err
	}
	defer l.Close()
	queued, err := p.deferredPackets(r.ID, d.ID)
	if err != nil {
		return err
	}
	for _, q := range queued {
		if answer == "yes" {
			if _, err := p.Deliver(r, q.Packet); err != nil {
				return err
			}
		} else {
			from, _ := q.Packet["from"].(map[string]any)
			to, _ := q.Packet["to"].(map[string]any)
			if _, err := p.Append(r.ID, r.Workflow.Id, str(from["agentId"]), "packet.rejected", map[string]any{"to": str(to["agentId"]), "kind": str(q.Packet["kind"]), "error": errShareDeclined.Error()}); err != nil {
				return err
			}
		}
		if err := os.Remove(filepath.Join(p.RunDir(r.ID), "deferred", str(q.Packet["messageId"])+".json")); err != nil {
			return err
		}
	}
	return Atomic(p.grantPath(r.ID, d.Route), SafeJSON(shareGrant{Route: d.Route, DecisionID: d.ID, Answer: answer, At: Now()}), 0600)
}
