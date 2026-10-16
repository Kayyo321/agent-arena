package arena

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// sharingRun creates a run where a codex coordinator delegates to, and may
// message, a claude-code builder under the given sharing policy.
func sharingRun(t *testing.T, policy, retention string) (*Project, Run) {
	t.Helper()
	p := committedProject(t)
	s, err := p.Settings()
	if err != nil {
		t.Fatal(err)
	}
	s.ApprovalPolicies.CrossProviderContextSharing = policy
	s.Audit.Retention = retention
	if _, err := p.Commit("sharing settings", map[string][]byte{"settings.arena.json": JSON(s)}); err != nil {
		t.Fatal(err)
	}
	w := NewWorkflow("share", "Sharing fixture")
	w.HeadAgent = "coordinator"
	w.Agents = []Agent{
		{Id: "coordinator", Level: Int(0), Profile: ".agents/share/coordinator.agent.json", Runtime: Runtime{Provider: "codex", Model: "explicit-test-model", ReasoningEffort: "high"}},
		{Id: "builder", Level: Int(1), Profile: ".agents/share/builder.agent.json", Runtime: Runtime{Provider: "claude-code", Model: "sonnet", ReasoningEffort: "high"}},
	}
	w.Relationships = []Relationship{{Type: "delegates", From: "coordinator", To: "builder"}, {Type: "communicates", From: "coordinator", To: "builder", Direction: "two-way"}}
	profiles := map[string]Profile{
		"coordinator": NewProfile("coordinator", "coordinator", "Plan the change", "Delegate implementation", []string{"read"}),
		"builder":     NewProfile("builder", "implementer", "Implement the change", "Edit files in scope", []string{"read", "write"}),
	}
	if _, err := p.Save(w, profiles, ""); err != nil {
		t.Fatal(err)
	}
	head, err := Git(p.Root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	r, err := p.CreateRun(w, profiles, s, "Fix slugify", head)
	if err != nil {
		t.Fatal(err)
	}
	return p, r
}

func shortShareWait(t *testing.T) {
	old := shareAckWait
	shareAckWait = 50 * time.Millisecond
	t.Cleanup(func() { shareAckWait = old })
}

func sendMessage(p *Project, r Run, payload map[string]any) (any, error) {
	return p.AgentPacket(context.Background(), r, "coordinator", map[string]any{"to": "builder", "kind": "message", "payload": payload})
}

func openDecisions(t *testing.T, p *Project, r Run) []Decision {
	t.Helper()
	d, err := p.Decisions(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

// The demo run raised three prompts for one coordinator -> builder handoff:
// a blocked arena_send, the agent's structured retry, and the result handoff.
func TestOneSharingPromptCoversRetriesAndHandoffOnARoute(t *testing.T) {
	shortShareWait(t)
	p, r := sharingRun(t, "ask-user", "full-local")

	if _, err := sendMessage(p, r, map[string]any{"goal": "robust slugify", "checks": []any{"pytest"}}); err == nil || !strings.Contains(err.Error(), "payload.text") {
		t.Fatalf("structured payload without text: %v", err)
	}
	if d := openDecisions(t, p, r); len(d) != 0 {
		t.Fatalf("an invalid packet raised a sharing prompt: %+v", d)
	}

	payload := map[string]any{"text": "Implement robust slugify", "acceptanceCriteria": []any{"strip punctuation"}}
	ack, err := sendMessage(p, r, payload)
	if err != nil {
		t.Fatal(err)
	}
	queued, _ := ack.(map[string]any)
	if queued["status"] != "awaiting-approval" || queued["messageId"] == "" {
		t.Fatalf("pending share did not return an ack: %#v", ack)
	}
	retry, err := sendMessage(p, r, payload)
	if err != nil || retry.(map[string]any)["messageId"] != queued["messageId"] {
		t.Fatalf("identical retry was queued again: %#v %v", retry, err)
	}
	if inbox, _ := p.Inbox(r, "builder"); len(inbox) != 0 {
		t.Fatal("message delivered before approval")
	}

	handoff := make(chan error, 1)
	go func() {
		handoff <- p.ShareContext(context.Background(), r, "builder", "codex", "builder", "claude-code", "handoff", map[string]any{"messageId": "message-1"})
	}()
	time.Sleep(300 * time.Millisecond)
	decisions := openDecisions(t, p, r)
	if len(decisions) != 1 || decisions[0].Route != shareRoute("codex", "builder", "claude-code") {
		t.Fatalf("route raised more than one prompt: %+v", decisions)
	}
	if err := p.Answer(r.ID, decisions[0].ID, "yes"); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-handoff:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("handoff still waiting after approval")
	}
	inbox, err := p.Inbox(r, "builder")
	if err != nil || len(inbox) != 1 || inbox[0]["messageId"] != queued["messageId"] {
		t.Fatalf("approved message was not delivered exactly once: %v %v", inbox, err)
	}

	id, err := sendMessage(p, r, map[string]any{"text": "One more detail"})
	if _, ok := id.(string); err != nil || !ok {
		t.Fatalf("approved route still gated a message: %#v %v", id, err)
	}
	if d := openDecisions(t, p, r); len(d) != 0 {
		t.Fatalf("approved route prompted again: %+v", d)
	}
	// The reverse route is a different boundary and still asks.
	if _, err := p.AgentPacket(context.Background(), r, "builder", map[string]any{"to": "coordinator", "kind": "message", "payload": map[string]any{"text": "done"}}); err != nil {
		t.Fatal(err)
	}
	if d := openDecisions(t, p, r); len(d) != 1 || d[0].Route != shareRoute("claude-code", "coordinator", "codex") {
		t.Fatalf("reverse route: %+v", d)
	}
}

func TestDeclinedRouteRejectsQueuedPacketsWithoutPromptingAgain(t *testing.T) {
	shortShareWait(t)
	p, r := sharingRun(t, "ask-user", "redacted-local")
	if _, err := sendMessage(p, r, map[string]any{"text": "private plan"}); err != nil {
		t.Fatal(err)
	}
	decisions := openDecisions(t, p, r)
	if len(decisions) != 1 {
		t.Fatalf("decisions: %+v", decisions)
	}
	if err := p.Answer(r.ID, decisions[0].ID, "no"); err != nil {
		t.Fatal(err)
	}
	events, err := p.Events(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	rejected := false
	for _, e := range events {
		rejected = rejected || e.Type == "packet.rejected" && e.Data["to"] == "builder"
		if e.Type == "message.sent" {
			t.Fatal("declined packet was delivered")
		}
	}
	if !rejected {
		t.Fatal("declined packet left no audit event")
	}
	if _, err := sendMessage(p, r, map[string]any{"text": "again"}); !errors.Is(err, errShareDeclined) {
		t.Fatalf("declined route: %v", err)
	}
	if err := p.ShareContext(context.Background(), r, "builder", "codex", "builder", "claude-code", "handoff", nil); !errors.Is(err, errShareDeclined) {
		t.Fatalf("declined handoff: %v", err)
	}
	if d := openDecisions(t, p, r); len(d) != 0 {
		t.Fatalf("declined route prompted again: %+v", d)
	}
	var stored Decision
	if err := ReadJSON(p.RunDir(r.ID)+"/decisions/"+decisions[0].ID+".json", &stored); err != nil || stored.ClosedAt == "" || stored.Context != nil {
		t.Fatalf("reduced-retention route decision kept its content: %+v %v", stored, err)
	}
}

func TestTaskScopedSharingDeliversWithoutPrompt(t *testing.T) {
	p, r := sharingRun(t, "task-scoped", "full-local")
	id, err := sendMessage(p, r, map[string]any{"text": "plan"})
	if _, ok := id.(string); err != nil || !ok {
		t.Fatalf("task-scoped message: %#v %v", id, err)
	}
	if d := openDecisions(t, p, r); len(d) != 0 {
		t.Fatalf("task-scoped policy prompted: %+v", d)
	}
}
