package arena

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"

	c "github.com/Kayyo321/agent-arena/internal/contract"
)

// ideaDraft is a valid arena idea: a head alone, or a head that delegates to
// a reviewer on another provider.
func ideaDraft(id string, reviewer bool, runtime Runtime) string {
	w := NewWorkflow(id, id)
	w.HeadAgent = "head"
	w.Agents = []Agent{{Id: "head", Level: Int(0), Profile: ".agents/" + id + "/head.agent.json", Runtime: runtime}}
	profiles := map[string]Profile{"head": NewProfile("head", "builder", "Build it", "Use evidence", []string{"read"})}
	if reviewer {
		w.Agents = append(w.Agents, Agent{Id: "reviewer", Level: Int(1), Profile: ".agents/" + id + "/reviewer.agent.json", Runtime: Runtime{Provider: "claude-code", Model: "sonnet-5-5", ReasoningEffort: "medium"}})
		w.Relationships = []Relationship{{Type: "delegates", From: "head", To: "reviewer"}}
		profiles["reviewer"] = NewProfile("reviewer", "review", "Review it", "Use evidence", []string{"read"})
	}
	return string(JSON(WorkflowDraft{Workflow: w, Profiles: profiles}))
}

func TestWalkthroughProposesIdeasBeforeDrafting(t *testing.T) {
	m := interviewModel(t)
	calls := 0
	m.Interview.invoker = testInvoker(func(ctx context.Context, in Invocation) (map[string]any, error) {
		calls++
		if err := in.Delivered(); err != nil {
			return nil, err
		}
		_, ideasField := in.Schema["properties"].(map[string]any)["ideasJson"]
		switch calls {
		case 1:
			if !strings.Contains(in.Prompt, "Walkthrough: the user has no design yet") || !ideasField || !strings.Contains(in.Prompt, "Walk me through a few arena ideas") {
				return nil, fmt.Errorf("walkthrough not requested: ideas=%v", ideasField)
			}
			return map[string]any{"question": "What is the project, what should it achieve, and what hurts today?", "complete": false, "builderRequestJson": "", "ideasJson": ""}, nil
		case 2:
			// Drafting before the user picks is sent back for repair.
			return map[string]any{"question": "Draft.", "complete": true, "builderRequestJson": ideaDraft("early", false, in.Runtime), "ideasJson": ""}, nil
		case 3:
			if !strings.Contains(in.Prompt, "has not picked a walkthrough idea") {
				return nil, fmt.Errorf("premature draft not repaired")
			}
			ideas := []map[string]any{
				{"title": "Solo builder", "pitch": "Cheap and fast; nobody checks the work.", "builderRequestJson": ideaDraft("solo-builder", false, Runtime{Provider: in.Runtime.Provider, Model: in.Runtime.Model, ReasoningEffort: "low"})},
				{"title": "Builder and reviewer", "pitch": "A second provider reviews every change.", "builderRequestJson": ideaDraft("reviewed-builder", true, in.Runtime)},
			}
			return map[string]any{"question": "Which idea fits, or should I combine them?", "complete": false, "builderRequestJson": "", "ideasJson": string(JSON(ideas))}, nil
		default:
			if ideasField || !strings.Contains(in.Prompt, "you proposed the arena ideas") || !strings.Contains(in.Prompt, "walkthrough ideas shown to the user") {
				return nil, fmt.Errorf("pick did not return to the drafting interview")
			}
			return map[string]any{"question": "Drafted idea 2 with your choices.", "complete": true, "builderRequestJson": ideaDraft("reviewed-builder", true, in.Runtime)}, nil
		}
	})
	next, cmd := m.submit("/walkthrough")
	m = pumpInterview(t, next.(LiveModel), cmd, "")
	if m.Error != "" || m.Walkthrough != setupWalkthrough || !strings.Contains(m.Chat[len(m.Chat)-1].Text, "what hurts today") {
		t.Fatalf("walkthrough did not start by asking about the project: %s %+v", m.Error, m.Chat)
	}
	next, cmd = m.submit("A parser library; correctness matters, reviews are slow")
	m = pumpInterview(t, next.(LiveModel), cmd, "")
	if m.Error != "" || m.Draft != nil || calls != 3 || m.Walkthrough != setupWalkthroughPicked {
		t.Fatalf("ideas not shown: calls=%d err=%s", calls, m.Error)
	}
	ideas := m.Chat[len(m.Chat)-1].Text
	for _, want := range []string{"Idea 1 · Solo builder", "Idea 2 · Builder and reviewer", "roles: head (builder), reviewer (review)", "providers (suggested): codex, claude-code", "rough cost: low · 1 agent turn per run", "grade: ", "Pick an idea"} {
		if !strings.Contains(ideas, want) {
			t.Fatalf("ideas lack %q:\n%s", want, ideas)
		}
	}
	if !strings.Contains(strings.Join(m.bodyLines(120, false), "\n"), "walkthrough · pick or combine") {
		t.Fatal("chat title does not show the walkthrough")
	}
	next, cmd = m.submit("2, and I pick the efforts: high everywhere")
	m = pumpInterview(t, next.(LiveModel), cmd, "")
	if m.Error != "" || m.Draft == nil || m.Draft.Workflow.Id != "reviewed-builder" {
		t.Fatalf("pick not drafted: %s", m.Error)
	}
	next, cmd = m.submit("/save no-commit")
	m = next.(LiveModel)
	next, _ = m.Update(cmd())
	if m = next.(LiveModel); m.Error != "" || m.Walkthrough != setupDesign || m.Run.Workflow.Id != "reviewed-builder" {
		t.Fatalf("walkthrough draft not saved: %s", m.Error)
	}
}

func TestWalkthroughIdeasAreValidated(t *testing.T) {
	p := testProject(t)
	s, err := p.Settings()
	if err != nil {
		t.Fatal(err)
	}
	runtime := s.DefaultModel
	one := `[{"title":"a","pitch":"","builderRequestJson":` + string(JSON(ideaDraft("a", false, runtime))) + `}]`
	if _, err := decodeIdeas(p, s, one); err == nil || !strings.Contains(err.Error(), "two or three ideas") {
		t.Fatalf("one idea accepted: %v", err)
	}
	same := `[{"title":"a","builderRequestJson":` + string(JSON(ideaDraft("a", false, runtime))) + `},{"title":"b","builderRequestJson":` + string(JSON(ideaDraft("a", true, runtime))) + `}]`
	if _, err := decodeIdeas(p, s, same); err == nil || !strings.Contains(err.Error(), "reuses workflow ID") {
		t.Fatalf("duplicate IDs accepted: %v", err)
	}
	broken := `[{"title":"a","builderRequestJson":` + string(JSON(ideaDraft("a", false, runtime))) + `},{"title":"b","builderRequestJson":"{}"}]`
	if _, err := decodeIdeas(p, s, broken); err == nil || !strings.Contains(err.Error(), "idea 2") {
		t.Fatalf("invalid idea accepted: %v", err)
	}
}

func TestRoughCostCountsTurnsEffortAndLoops(t *testing.T) {
	runtime := func(effort string) Runtime { return Runtime{Provider: "codex", Model: "m", ReasoningEffort: effort} }
	w := NewWorkflow("cost", "Cost")
	w.HeadAgent = "lead"
	w.Agents = []Agent{{Id: "lead", Runtime: runtime("low")}}
	if got := roughCost(w); got != "low · 1 agent turn per run" {
		t.Fatalf("one low agent: %q", got)
	}
	w.Agents = append(w.Agents, Agent{Id: "builder", Runtime: runtime("high")}, Agent{Id: "reviewer", Runtime: runtime("high")}, Agent{Id: "pager", Activation: "on-call", Runtime: runtime("max")})
	w.Relationships = []Relationship{{Type: "delegates", From: "lead", To: "builder"}, {Type: "delegates", From: "builder", To: "reviewer"}, {Type: "delegates", From: "reviewer", To: "gate"}}
	loop := true
	w.DecisionRouters = []Router{{Id: "gate", Routes: []c.WorkflowDecisionRoute{{Id: "again", To: "builder", Loop: &loop, MaxIterations: Int(2)}}}}
	if got := roughCost(w); got != "high · 3 agent turns per run, up to 7 with revisions" {
		t.Fatalf("loop arena: %q", got)
	}
}

func TestOrchestratorCannotTurnOnBlanketApproval(t *testing.T) {
	p := testProject(t)
	s, err := p.Settings()
	if err != nil {
		t.Fatal(err)
	}
	ideas := `[{"title":"a","builderRequestJson":` + string(JSON(ideaDraft("a", false, s.DefaultModel))) + `},{"title":"b","builderRequestJson":` + string(JSON(ideaDraft("b", false, blanketRuntime))) + `}]`
	if _, err := decodeIdeas(p, s, ideas); err == nil || !strings.Contains(err.Error(), "only the user chooses it") {
		t.Fatalf("idea with blanket approval accepted: %v", err)
	}
	var plain, blanket WorkflowDraft
	if err := DecodeJSON([]byte(ideaDraft("solo", false, Runtime{Provider: "antigravity", Model: "fixture"})), &plain); err != nil {
		t.Fatal(err)
	}
	if err := DecodeJSON([]byte(ideaDraft("solo", false, blanketRuntime)), &blanket); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeRevision(s, string(JSON(blanket)), plain.Workflow, "v1"); err == nil || !strings.Contains(err.Error(), "arena workflow edit") {
		t.Fatalf("revision turned on blanket approval: %v", err)
	}
	// A revision may keep the user's existing choice.
	if _, err := decodeRevision(s, string(JSON(blanket)), blanket.Workflow, "v1"); err != nil {
		t.Fatalf("revision that keeps the user's choice: %v", err)
	}
}

func TestSaveNeedsTheUsersOwnBlanketApproval(t *testing.T) {
	m := interviewModel(t)
	m.Interview.invoker = testInvoker(func(ctx context.Context, in Invocation) (map[string]any, error) {
		if err := in.Delivered(); err != nil {
			return nil, err
		}
		// The orchestrator sets the mode even though the user declined it.
		return map[string]any{"question": "Drafted.", "complete": true, "builderRequestJson": ideaDraft("agy-arena", false, blanketRuntime)}, nil
	})
	next, cmd := m.submit("An Antigravity builder, but no blanket approval")
	m = pumpInterview(t, next.(LiveModel), cmd, "")
	if m.Draft == nil || !strings.Contains(strings.Join(m.bodyLines(120, false), "\n"), "/save allow-blanket") {
		t.Fatalf("draft or consent hint missing: %s", m.Error)
	}
	next, cmd = m.submit("/save no-commit")
	if m = next.(LiveModel); cmd != nil || !strings.Contains(m.Error, "Only you can choose it") {
		t.Fatalf("/save kept the orchestrator's blanket approval: %q", m.Error)
	}
	if _, _, err := m.Project.Load("agy-arena"); err == nil {
		t.Fatal("arena saved without the user's choice")
	}
	next, cmd = m.submit("/save no-commit allow-blanket")
	m = next.(LiveModel)
	next, _ = m.Update(cmd())
	if m = next.(LiveModel); m.Error != "" || m.Run.Workflow.Id != "agy-arena" {
		t.Fatalf("/save allow-blanket: %s", m.Error)
	}
	events, _ := m.Project.Events("project")
	chosen := false
	for _, event := range events {
		chosen = chosen || event.Type == "workflow.saved" && event.Data["blanketApprovalChosenBy"] == "user"
	}
	if !chosen {
		t.Fatal("the user's blanket choice was not audited")
	}
	if usage := slashUsage["/save"][0]; usage != "/save [no-commit] [allow-blanket]" {
		t.Fatalf("/help usage: %q", usage)
	}
}

func TestWalkthroughReportsASetupErrorInsteadOfPanicking(t *testing.T) {
	m := interviewModel(t)
	// Without the control profile, setupInvocation fails before any schema exists.
	if err := os.Remove(m.Project.Path(m.Run.Settings.PermanentOnCallAgent.Profile)); err != nil {
		t.Fatal(err)
	}
	next, cmd := m.submit("/walkthrough")
	m = pumpInterview(t, next.(LiveModel), cmd, "")
	if m.Error == "" || !strings.Contains(m.Chat[len(m.Chat)-1].Text, "Request failed") {
		t.Fatalf("setup error not reported: %q", m.Error)
	}
}
