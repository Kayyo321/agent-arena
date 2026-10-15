package arena

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestJournalRecoveryAndStateSurviveReopen(t *testing.T) {
	p := testProject(t)
	w, profiles, s := fixture(t)
	r, e := p.CreateRun(w, profiles, s, "simulated state-machine test", "fixture-base")
	if e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"workflow.started", "workflow.paused", "workflow.resumed", "workflow.interrupted"} {
		if _, e := p.Append(r.ID, w.Id, "", kind, map[string]any{"message": "api_key=very-secret-value"}); e != nil {
			t.Fatal(e)
		}
	}
	reopened := &Project{p.Root}
	state, e := reopened.State(r.ID)
	if e != nil || state.Status != "interrupted" {
		t.Fatalf("state %v %v", state, e)
	}
	b, e := os.ReadFile(filepath.Join(p.RunDir(r.ID), "events.ndjson"))
	if e != nil {
		t.Fatal(e)
	}
	if strings.Contains(string(b), "very-secret-value") {
		t.Fatal("secret persisted")
	}
	if e := os.WriteFile(filepath.Join(p.RunDir(r.ID), "events.ndjson"), append(b, []byte(`{"partial":`)...), 0600); e != nil {
		t.Fatal(e)
	}
	if _, e := p.Events(r.ID); e == nil {
		t.Fatal("corrupt mirror accepted")
	}
	if e := p.RepairJournal(r.ID); e != nil {
		t.Fatal(e)
	}
	events, e := p.Events(r.ID)
	if e != nil || len(events) != 5 {
		t.Fatalf("recovered %d events: %v", len(events), e)
	}
	preserved, _ := filepath.Glob(filepath.Join(p.RunDir(r.ID), "events.ndjson.preserved-*"))
	if len(preserved) != 1 {
		t.Fatal("corrupt mirror not preserved")
	}
}
func TestContextRevisionArchiveRestoreAndIsolation(t *testing.T) {
	p := testProject(t)
	w := NewWorkflow("context-test", "Context test")
	w.HeadAgent = "head"
	w.Agents = []Agent{{Id: "head", Profile: ".agents/context-test/head.agent.json", Level: Int(0), Runtime: Runtime{Provider: "codex", Model: "explicit-test-model", ReasoningEffort: "high"}}}
	profiles := map[string]Profile{"head": NewProfile("head", "research", "Research", "Use evidence", []string{"read"})}
	if _, e := p.Save(w, profiles, ""); e != nil {
		t.Fatal(e)
	}
	q := ContextProposal{WorkflowID: w.Id, Operation: "add", Title: "Fact", Content: "A bounded fact", Confidence: "inferred", Tags: []string{}}
	first, e := p.ProposeContext(q, "user", "", "", "", false)
	if e != nil {
		t.Fatal(e)
	}
	q.Operation = "revise"
	q.EntryID = first.EntryID
	q.Content = "A corrected fact"
	second, e := p.ProposeContext(q, "user", "", "", "", false)
	if e != nil {
		t.Fatal(e)
	}
	if *first.EntryID == *second.EntryID {
		t.Fatal("revision overwrote identity")
	}
	entries, e := p.ContextEntries(w.Id, false)
	if e != nil || len(entries) != 1 || entries[0].Supersedes == nil || *entries[0].Supersedes != *first.EntryID {
		t.Fatalf("bad version chain %v %v", entries, e)
	}
	q = ContextProposal{WorkflowID: w.Id, Operation: "archive", EntryID: second.EntryID, Reason: "superseded"}
	if _, e := p.ProposeContext(q, "user", "", "", "", false); e != nil {
		t.Fatal(e)
	}
	q.Operation = "restore"
	if _, e := p.ProposeContext(q, "user", "", "", "", false); e != nil {
		t.Fatal(e)
	}
	q.EntryID = first.EntryID
	if _, e := p.ProposeContext(q, "user", "", "", "", false); e != nil {
		t.Fatal(e)
	}
	entries, e = p.ContextEntries(w.Id, false)
	if e != nil || len(entries) != 1 || entries[0].Id != *first.EntryID {
		t.Fatalf("restoration retained conflicting active versions: %v %v", entries, e)
	}
	if _, e := p.ContextEntries("different-workflow", false); e == nil {
		t.Fatal("accepted foreign workflow read")
	}
}

func TestReducedRetentionCompactsConsumedDecision(t *testing.T) {
	p := testProject(t)
	w, profiles, s := fixture(t)
	s.Audit.Retention = "metadata-only"
	r, err := p.CreateRun(w, profiles, s, "private task", "fixture-base")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	response := make(chan string, 1)
	failure := make(chan error, 1)
	go func() {
		answer, err := p.Decide(ctx, r, "head", "question", "private decision prompt", nil, map[string]any{"private": "private context"})
		response <- answer
		failure <- err
	}()
	var decisions []Decision
	for len(decisions) == 0 {
		decisions, err = p.Decisions(r.ID)
		if err != nil {
			t.Fatal(err)
		}
		select {
		case <-ctx.Done():
			t.Fatal("decision was not created")
		case <-time.After(10 * time.Millisecond):
		}
	}
	if err := p.Answer(r.ID, decisions[0].ID, "private answer"); err != nil {
		t.Fatal(err)
	}
	if answer := <-response; answer != "private answer" {
		t.Fatal("decision lost its answer")
	}
	if err := <-failure; err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(filepath.Join(p.RunDir(r.ID), "decisions", decisions[0].ID+".json"))
	if err != nil || strings.Contains(string(b), "private") {
		t.Fatalf("consumed decision retained private content: %s %v", b, err)
	}
	if pending, err := p.Decisions(r.ID); err != nil || len(pending) != 0 {
		t.Fatal("compacted decision appears pending")
	}
}
