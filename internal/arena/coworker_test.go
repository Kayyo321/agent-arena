package arena

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Local HTTP discovery and inference are fixtures. This checks Arena's control
// policy/scheduling contract and does not establish real backend readiness.
func TestCoworkerControlSlotAndApprovedRunAddition(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/tags" {
			http.NotFound(w, r)
			return
		}
		fmt.Fprint(w, `{"models":[{"name":"fixture-model"}]}`)
	}))
	defer server.Close()
	p := committedProject(t)
	runtime := Runtime{Provider: "local", Model: "fixture-model", Options: map[string]any{"backend": "ollama", "endpoint": server.URL}}
	s := DefaultSettings(runtime)
	s.Limits.MaxConcurrentAgents = 1
	s.ApprovalPolicies.ArenaSubagentModificationInquiry = "auto-accept"
	s.ApprovalPolicies.AgentWorkflowEdit = "auto-accept"
	if _, err := p.Commit("fixture control settings", map[string][]byte{"settings.arena.json": JSON(s)}); err != nil {
		t.Fatal(err)
	}
	w := NewWorkflow("coworker-test", "Coworker fixture")
	w.HeadAgent = "head"
	w.Settings.MaxConcurrentAgents = 1
	w.Agents = []Agent{{Id: "head", Level: Int(0), Profile: ".agents/coworker-test/head.agent.json", Runtime: runtime}}
	profiles := map[string]Profile{"head": NewProfile("head", "research", "Inspect evidence", "Use Arena for coworkers", []string{"read"})}
	if _, err := p.Save(w, profiles, ""); err != nil {
		t.Fatal(err)
	}
	head, err := Git(p.Root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	r, err := p.CreateRun(w, profiles, s, "Fixture coworker task", head)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	worker, err := p.ProviderSlot(ctx, 1)
	if err != nil {
		t.Fatal(err)
	}
	defer worker.Close()
	controlCalls := 0
	control := testInvoker(func(ctx context.Context, in Invocation) (map[string]any, error) {
		controlCalls++
		if string(JSON(in.Runtime)) != string(JSON(runtime)) || !strings.Contains(in.Prompt, "# Manage Agent Arena workflows") || in.Write {
			t.Fatal("control runtime/skill/permissions differ from project selection")
		}
		return map[string]any{"accept": true, "rationale": "complete fixture request", "instructions": "Inspect requested evidence"}, nil
	})
	q, err := p.RequestCoworker(ctx, CoworkerRequest{WorkflowID: w.Id, From: "head", AgentID: "helper", Role: "research", Objective: "Inspect evidence", Runtime: runtime, Capabilities: []string{"read"}, WorkspaceMode: "read-only", Files: []string{}, Reason: "Independent check", RunID: r.ID}, false, control)
	if err != nil {
		t.Fatal(err)
	}
	if q.State != "accepted" || controlCalls != 1 {
		t.Fatal("complete authorized request was not accepted")
	}
	if err := worker.Close(); err != nil {
		t.Fatal(err)
	}
	engine := NewEngine(p, r, testInvoker(func(ctx context.Context, in Invocation) (map[string]any, error) { return ordinaryResult(), nil }))
	if err := engine.Execute(ctx, false, false); err != nil {
		t.Fatal(err)
	}
	updated, err := p.ReadRun(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := agentByID(updated.Workflow, "helper"); !ok {
		t.Fatal("approved coworker did not join the run")
	}
	state, err := p.State(r.ID)
	if err != nil || state.Status != "succeeded" || state.Agents["helper"] != "succeeded" {
		t.Fatalf("coworker did not complete: %v %v", state, err)
	}
}

// A config revision is written when a coworker is approved mid-run. Like
// run.json and current-config.json, it keeps no objective under redacted
// retention.
func TestConfigRevisionKeepsNoObjectiveUnderRedactedRetention(t *testing.T) {
	for _, retention := range []string{"full-local", "redacted-local"} {
		p := committedProject(t)
		s, _ := p.Settings()
		w, profiles := fanOutWorkflow(s, 2, "required")
		r := splitRun(t, p, w, profiles)
		r.Retention, r.Objective = retention, "Ship the parser rewrite"
		helper := Agent{Id: "helper", Level: Int(2), Profile: ".agents/parallel/helper.agent.json", Runtime: s.DefaultModel}
		if err := p.QueueCoworker(r.ID, ConfigUpdate{RequestID: "request-1", Parent: "split", Agent: helper, Profile: NewProfile("helper", "research", "Help", "Help", []string{"read"}), Transaction: "tx"}); err != nil {
			t.Fatal(err)
		}
		e := NewEngine(p, r, nil)
		if _, err := e.applyCoworkers(); err != nil {
			t.Fatal(err)
		}
		revision, err := os.ReadFile(filepath.Join(p.RunDir(r.ID), "config-revisions", "request-1.json"))
		if err != nil {
			t.Fatal(err)
		}
		if kept := strings.Contains(string(revision), "Ship the parser rewrite"); kept != (retention == "full-local") {
			t.Fatalf("%s: objective in the revision = %v", retention, kept)
		}
		if e.Run.Objective != "Ship the parser rewrite" {
			t.Fatal("the running engine lost its objective")
		}
	}
}
