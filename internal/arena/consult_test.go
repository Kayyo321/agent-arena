package arena

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
)

// consultModel is the live view of a saved one-agent arena with no run.
func consultModel(t *testing.T) LiveModel {
	t.Helper()
	m := interviewModel(t)
	launchWorkflow(t, m.Project, "solo")
	r, err := loadWorkflowView(m.Project, "solo")
	if err != nil {
		t.Fatal(err)
	}
	m.Run, m.State, m.ChatMode = r, RunState{Status: "idle", Agents: map[string]string{}}, false
	return m
}

func TestConsultAdvisesWithoutChangingTheArena(t *testing.T) {
	m := consultModel(t)
	before, err := os.ReadFile(m.Project.Path("solo.arena.json"))
	if err != nil {
		t.Fatal(err)
	}
	calls := 0
	m.Interview.invoker = testInvoker(func(ctx context.Context, in Invocation) (map[string]any, error) {
		calls++
		if in.Write || in.Commands || in.Runtime.Model != "explicit-test-model" {
			return nil, fmt.Errorf("consult must run read-only on the project default runtime")
		}
		for _, want := range []string{"You only advise", "Saved arena solo", "Arena score", "No run is loaded", "why is the grade low", "Arena envelope"} {
			if !strings.Contains(in.Prompt, want) {
				return nil, fmt.Errorf("consult prompt lacks %q", want)
			}
		}
		if _, ok := in.Schema["properties"].(map[string]any)["answer"]; !ok {
			return nil, fmt.Errorf("consult schema has no answer")
		}
		if err := in.Delivered(); err != nil {
			return nil, err
		}
		return map[string]any{"answer": "The head both coordinates and works; add a reviewer on another provider.", "builderRequestJson": ""}, nil
	})
	next, cmd := m.submit("/consult why is the grade low?")
	m = pumpInterview(t, next.(LiveModel), cmd, "")
	if m.Error != "" || calls != 1 || m.Draft != nil || !m.ChatMode || !m.Consulting {
		t.Fatalf("consult did not answer in the conversation: calls=%d err=%s", calls, m.Error)
	}
	if !strings.Contains(m.Chat[len(m.Chat)-1].Text, "add a reviewer") || !strings.Contains(m.View().Content, "Consulting arena-orchestrator about solo") {
		t.Fatalf("answer not shown: %+v", m.Chat)
	}
	after, _ := os.ReadFile(m.Project.Path("solo.arena.json"))
	if string(before) != string(after) {
		t.Fatal("consulting changed the arena")
	}
	assertNoLaunchRun(t, m.Project)
	types := []string{}
	for _, event := range m.ControlEvents {
		types = append(types, event.Type)
	}
	if !Has(types, "consult.started") || !Has(types, "consult.replied") {
		t.Fatalf("consultation not journaled: %v", types)
	}
	// /graph leaves the consultation; /consult returns to it.
	next, _ = m.submit("/graph")
	if m = next.(LiveModel); m.ChatMode {
		t.Fatal("/graph did not return to the graph")
	}
	next, _ = m.submit("/consult")
	if m = next.(LiveModel); !m.ChatMode || !m.Consulting || calls != 1 {
		t.Fatal("/consult without a question should only open the consultation")
	}
	// /chat switches the same conversation back to designing a new arena.
	next, _ = m.submit("/chat")
	if m = next.(LiveModel); m.Consulting {
		t.Fatal("/chat kept consulting")
	}
}

func TestConsultEditIsADraftUntilSave(t *testing.T) {
	m := consultModel(t)
	calls := 0
	m.Interview.invoker = testInvoker(func(ctx context.Context, in Invocation) (map[string]any, error) {
		calls++
		if err := in.Delivered(); err != nil {
			return nil, err
		}
		w, profiles, err := m.Project.Load("solo")
		if err != nil {
			return nil, err
		}
		if calls == 1 {
			// A revision must keep the arena's ID; Arena sends it back.
			w.Id = "solo-two"
		} else if !strings.Contains(in.Prompt, "a revision keeps workflow ID solo") {
			return nil, fmt.Errorf("repair prompt lacks the ID error")
		}
		head := profiles["head"]
		head.Objective = "Plan the work and hand it to reviewers"
		profiles["head"] = head
		return map[string]any{"answer": "Here is a revision with a clearer objective.", "builderRequestJson": string(JSON(WorkflowDraft{Workflow: w, Profiles: profiles}))}, nil
	})
	next, cmd := m.submit("/consult")
	m = next.(LiveModel)
	next, cmd = m.submit("Make the head's objective clearer")
	m = pumpInterview(t, next.(LiveModel), cmd, "")
	if m.Error != "" || calls != 2 || m.Draft == nil || m.Draft.Expected == "" || m.Draft.Workflow.Id != "solo" {
		t.Fatalf("revision not offered as a draft: calls=%d err=%s", calls, m.Error)
	}
	if _, profiles, _ := m.Project.Load("solo"); strings.Contains(profiles["head"].Objective, "reviewers") {
		t.Fatal("revision applied before /save")
	}
	if view := m.View().Content; !strings.Contains(view, "Proposed revision of solo") || !strings.Contains(view, "/save") {
		t.Fatal("revision preview or /save hint missing")
	}
	next, cmd = m.submit("/save no-commit")
	m = next.(LiveModel)
	next, _ = m.Update(cmd())
	m = next.(LiveModel)
	if m.Error != "" || m.ChatMode || !strings.Contains(m.Chat[len(m.Chat)-1].Text, "Applied the revision to solo") {
		t.Fatalf("save did not apply the revision: %s", m.Error)
	}
	if _, profiles, _ := m.Project.Load("solo"); profiles["head"].Objective != "Plan the work and hand it to reviewers" {
		t.Fatal("saved arena lacks the revision")
	}
	events, _ := m.Project.Events("project")
	audited := false
	for _, event := range events {
		audited = audited || event.Type == "workflow.saved" && event.Data["source"] == "consult"
	}
	if !audited {
		t.Fatal("applied revision not audited as a consult edit")
	}
	assertNoLaunchRun(t, m.Project)
}

func TestConsultRevisionRefusesAChangedArena(t *testing.T) {
	m := consultModel(t)
	m.Interview.invoker = testInvoker(func(ctx context.Context, in Invocation) (map[string]any, error) {
		w, profiles, err := m.Project.Load("solo")
		if err != nil {
			return nil, err
		}
		w.Description = "revised"
		return map[string]any{"answer": "Revised.", "builderRequestJson": string(JSON(WorkflowDraft{Workflow: w, Profiles: profiles}))}, nil
	})
	next, cmd := m.submit("/consult describe it")
	m = pumpInterview(t, next.(LiveModel), cmd, "")
	if m.Draft == nil {
		t.Fatalf("no draft: %s", m.Error)
	}
	// Someone edits the arena after the orchestrator proposed its revision.
	w, profiles, version, err := m.Project.LoadVersion("solo")
	if err != nil {
		t.Fatal(err)
	}
	w.Name = "Edited elsewhere"
	if _, err := m.Project.Save(w, profiles, version); err != nil {
		t.Fatal(err)
	}
	next, cmd = m.submit("/save no-commit")
	m = next.(LiveModel)
	next, _ = m.Update(cmd())
	if m = next.(LiveModel); !strings.Contains(m.Error, "changed during editing") {
		t.Fatalf("stale revision overwrote a newer arena: %q", m.Error)
	}
}

func TestConsultNeedsAnOpenArena(t *testing.T) {
	m := interviewModel(t)
	next, cmd := m.submit("/consult why?")
	if m = next.(LiveModel); cmd != nil || !strings.Contains(m.Error, "Open or /save an arena first") {
		t.Fatalf("consult without an arena: %q", m.Error)
	}
}

func TestConsultEvidenceCarriesTheRun(t *testing.T) {
	m := consultModel(t)
	m.Run.ID, m.Run.Objective = "run-1", "Ship the parser"
	m.State = RunState{Status: "running", Agents: map[string]string{"head": "waiting"}}
	m.Decisions = []Decision{{ID: "decision-1", AgentID: "head", Kind: "agent.question", Prompt: "Which parser?", Choices: []string{"peg", "lr"}}}
	m.Events = []Event{{Sequence: 1, AgentID: "head", Type: "agent.started"}, {Sequence: 2, AgentID: "head", Type: "usage.reported"}, {Sequence: 3, AgentID: "head", Type: "agent.waiting"}}
	evidence := m.consultEvidence()
	for _, want := range []string{"Run run-1 · status running", "Ship the parser", "decision-1 from head", "1 head agent.started", "3 head agent.waiting"} {
		if !strings.Contains(evidence, want) {
			t.Fatalf("evidence lacks %q:\n%s", want, evidence)
		}
	}
	if strings.Contains(evidence, "usage.reported") || strings.Index(evidence, "agent.started") > strings.Index(evidence, "agent.waiting") {
		t.Fatalf("events are noisy or out of order:\n%s", evidence)
	}
}
