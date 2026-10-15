package arena

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func pendingDecisionModel(t *testing.T) (LiveModel, chan string) {
	t.Helper()
	p := committedProject(t)
	r := testRun(t, p)
	answered := make(chan string, 1)
	go func() {
		answer, _ := p.Decide(context.Background(), r, "review-gate", "router.fallback", "review-gate got a team-result", []string{"stop", "fixer"}, map[string]any{})
		answered <- answer
	}()
	var decisions []Decision
	for i := 0; i < 100 && len(decisions) == 0; i++ {
		time.Sleep(20 * time.Millisecond)
		decisions, _ = p.Decisions(r.ID)
	}
	if len(decisions) != 1 {
		t.Fatalf("decision not pending: %d", len(decisions))
	}
	decisions[0].CreatedAt = time.Now().Add(-time.Minute).UTC().Format(time.RFC3339Nano)
	m := LiveModel{Project: p, Run: r, State: RunState{Status: "running", Agents: map[string]string{"head": "succeeded"}}, Decisions: decisions, Width: 100, Height: 30, Context: context.Background()}
	return m, answered
}

func TestStalledRunIsDiagnosedAndRecommendationAccepted(t *testing.T) {
	m, answered := pendingDecisionModel(t)
	m.Run.Settings.ApprovalPolicies.OrchestratorStallRecovery = "recommend-only"
	id := m.Decisions[0].ID
	m.Invoker = testInvoker(func(ctx context.Context, in Invocation) (map[string]any, error) {
		if !strings.Contains(in.Prompt, id) || in.Write || in.Runtime.Model != m.Run.Settings.DefaultModel.Model {
			t.Errorf("diagnosis lacks the decision or runs with write access")
		}
		return map[string]any{"summary": "The router is waiting on you.", "recommendation": "Send it to fixer.", "decisionId": id, "answer": "fixer"}, nil
	})
	if !strings.Contains(strings.Join(m.attentionLines(100, false), "\n"), "review-gate needs you") {
		t.Fatal("pending decision not pinned above the prompt")
	}
	cmd := m.watchStall()
	if cmd == nil || !m.StallBusy || m.watchStall() != nil {
		t.Fatal("stall not detected exactly once")
	}
	next, _ := m.Update(cmd())
	m = next.(LiveModel)
	if m.Stall == nil || m.Stall.Answer != "fixer" || !strings.Contains(strings.Join(m.attentionLines(100, false), "\n"), "/accept answers") {
		t.Fatalf("diagnosis not shown: %+v", m.Stall)
	}
	if colored := strings.Join(m.attentionLines(100, true), "\n"); !strings.Contains(colored, "\x1b["+sgrOrchestrator+"m  Send it to fixer.") {
		t.Fatalf("orchestrator recommendation lost its color: %q", colored)
	}
	next, cmd = m.submit("/accept")
	next.(LiveModel).Update(cmd())
	select {
	case answer := <-answered:
		if answer != "fixer" {
			t.Fatal(answer)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("recommendation not applied")
	}
	events, _ := m.Project.Events(m.Run.ID)
	audited := false
	for _, event := range events {
		audited = audited || event.Type == "orchestrator.stall-diagnosed"
	}
	if !audited {
		t.Fatal("diagnosis not journaled")
	}
}

func TestTypingAChoiceAnswersTheOnlyPendingDecision(t *testing.T) {
	m, answered := pendingDecisionModel(t)
	m.Executing = true
	next, cmd := m.submit("fixer")
	if next.(LiveModel).Error != "" || cmd == nil {
		t.Fatalf("choice not routed to decision: %s", next.(LiveModel).Error)
	}
	cmd()
	select {
	case answer := <-answered:
		if answer != "fixer" {
			t.Fatal(answer)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("decision not answered")
	}
}

func TestShellPassthroughRunsInProjectRoot(t *testing.T) {
	p := committedProject(t)
	m := LiveModel{Project: p, Run: Run{Settings: DefaultSettings(Runtime{})}, Width: 80, Height: 24, Context: context.Background()}
	next, cmd := m.submit("$ pwd; exit 3")
	if cmd == nil {
		t.Fatal("no shell command")
	}
	next, _ = next.(LiveModel).Update(cmd())
	m = next.(LiveModel)
	root, _ := filepath.EvalSymlinks(p.Root)
	if !strings.Contains(m.Panel, "exit 3") || !strings.Contains(strings.Join(m.PanelText, "\n"), root) {
		t.Fatalf("shell result: %s %v", m.Panel, m.PanelText)
	}
}

func TestCommitScaffoldCommitsAndRefusesSecrets(t *testing.T) {
	for key, value := range map[string]string{"GIT_AUTHOR_NAME": "Test", "GIT_AUTHOR_EMAIL": "test@localhost", "GIT_COMMITTER_NAME": "Test", "GIT_COMMITTER_EMAIL": "test@localhost"} {
		t.Setenv(key, value)
	}
	p := committedProject(t)
	if err := os.WriteFile(filepath.Join(p.Root, "README.md"), []byte("ledger\n"), 0644); err != nil {
		t.Fatal(err)
	}
	head, err := commitScaffold(p)
	if err != nil || head == "" {
		t.Fatalf("scaffold commit: %q %v", head, err)
	}
	if message, _ := Git(p.Root, "log", "-1", "--format=%s"); strings.TrimSpace(message) != "Arena scaffold" {
		t.Fatal(message)
	}
	if head, err := commitScaffold(p); err != nil || head != "" {
		t.Fatalf("clean tree should not commit: %q %v", head, err)
	}
	if err := os.WriteFile(filepath.Join(p.Root, ".env"), []byte("OPENAI_API_KEY=sk-proj-abcdefghijklmnopqrstuvwxyz0123456789\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := commitScaffold(p); err == nil {
		t.Fatal("secret-like file committed")
	}
}

func TestReadinessFoldsDuplicateRuntimes(t *testing.T) {
	s := DefaultSettings(Runtime{Provider: "codex", Model: "m", ReasoningEffort: "high"})
	w := NewWorkflow("w", "W")
	for _, effort := range []string{"low", "low", "high"} {
		w.Agents = append(w.Agents, Agent{Runtime: Runtime{Provider: "codex", Model: "m", ReasoningEffort: effort}})
	}
	if got := Required([]Workflow{w}, s); len(got) != 2 {
		t.Fatalf("runtimes not deduplicated: %+v", got)
	}
	lines := foldReadiness([]Readiness{{Provider: "codex", Model: "m", State: "unknown"}, {Provider: "codex", Model: "m", State: "unknown"}, {Provider: "local", Model: "x", State: "good"}})
	if len(lines) != 1 {
		t.Fatalf("readiness not folded: %v", lines)
	}
}

func TestUndeliverableResultPacketIsRejectedWithoutFailingRun(t *testing.T) {
	p := committedProject(t)
	r := teamRun(t, p)
	fake := testInvoker(func(ctx context.Context, in Invocation) (map[string]any, error) {
		if in.Schema["properties"].(map[string]any)["verdict"] != nil {
			return map[string]any{"verdict": "Approved", "acceptTeamVerdict": true, "summary": "fixture", "evidence": []any{}, "rationale": "fixture evidence", "nextAction": "inspect"}, nil
		}
		result := ordinaryResult()
		result["packets"] = []any{map[string]any{"to": "nobody", "kind": "message", "payloadJson": `{"text":"hi"}`}, map[string]any{"to": "nobody", "kind": "message", "payloadJson": "not json"}}
		return result, nil
	})
	if err := NewEngine(p, r, fake).Execute(context.Background(), false, false); err != nil {
		t.Fatal(err)
	}
	events, _ := p.Events(r.ID)
	rejected, succeeded := 0, false
	for _, event := range events {
		if event.Type == "packet.rejected" {
			rejected++
		}
		succeeded = succeeded || event.Type == "workflow.succeeded"
	}
	if rejected == 0 || !succeeded {
		t.Fatalf("rejected=%d succeeded=%v", rejected, succeeded)
	}
}

func TestOrchestratorAnswersStalledRouterFallbackItself(t *testing.T) {
	m, answered := pendingDecisionModel(t)
	if !strings.Contains(strings.Join(m.attentionLines(100, false), "\n"), "arena-orchestrator is deciding") {
		t.Fatalf("banner does not say the orchestrator will decide: %v", m.attentionLines(100, false))
	}
	m.Invoker = testInvoker(func(ctx context.Context, in Invocation) (map[string]any, error) {
		schema := in.Schema["properties"].(map[string]any)
		if !Has(stringSlice(schema["answer"].(map[string]any)["enum"]), "fixer") || !strings.Contains(in.Prompt, "applies your decisionId") {
			t.Errorf("diagnosis is not constrained to the decision's choices")
		}
		return map[string]any{"summary": "Reviewers requested changes.", "recommendation": "Routing to fixer.", "decisionId": "", "answer": "fixer"}, nil
	})
	cmd := m.watchStall()
	next, _ := m.Update(cmd())
	m = next.(LiveModel)
	select {
	case answer := <-answered:
		if answer != "fixer" {
			t.Fatal(answer)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("orchestrator did not answer the router fallback")
	}
	if m.Stall == nil || !m.Stall.Applied || !strings.Contains(strings.Join(m.attentionLines(100, false), "\n"), "orchestrator answered") {
		t.Fatalf("applied answer not shown: %+v", m.Stall)
	}
}

func TestOrchestratorNeverAnswersAgentQuestions(t *testing.T) {
	s := DefaultSettings(Runtime{})
	if orchestratorMayAnswer(s, Decision{Kind: "question"}) || orchestratorMayAnswer(s, Decision{Kind: "shell-command-execution"}) {
		t.Fatal("orchestrator may answer a user question or approval")
	}
	if !orchestratorMayAnswer(s, Decision{Kind: "router.fallback"}) {
		t.Fatal("default policy should allow routing answers")
	}
	s.ApprovalPolicies.OrchestratorStallRecovery = "recommend-only"
	if orchestratorMayAnswer(s, Decision{Kind: "router.fallback"}) {
		t.Fatal("recommend-only policy ignored")
	}
}
