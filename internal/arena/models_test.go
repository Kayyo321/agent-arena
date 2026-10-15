package arena

import (
	"bytes"
	"strings"
	"testing"

	c "github.com/Kayyo321/agent-arena/internal/contract"
)

func TestChooseAcceptsNumberOrExactValueAndRejectsUnlisted(t *testing.T) {
	var out bytes.Buffer
	cli := NewCLI(strings.NewReader("2\nnope\nhigh\n"), &out, &out)
	options := []string{"low", "high"}
	if v, e := cli.choose("Effort", options, nil, true); e != nil || v != "high" {
		t.Fatalf("number: %q %v", v, e)
	}
	if v, e := cli.choose("Effort", options, nil, true); e != nil || v != "high" {
		t.Fatalf("strict retry: %q %v", v, e)
	}
	if !strings.Contains(out.String(), `"nope" is not in the list`) {
		t.Fatal(out.String())
	}
	cli = NewCLI(strings.NewReader("custom-alias\n"), &out, &out)
	if v, e := cli.choose("Model", []string{"a"}, nil, false); e != nil || v != "custom-alias" {
		t.Fatalf("free entry: %q %v", v, e)
	}
	cli = NewCLI(strings.NewReader(""), &out, &out)
	if _, e := cli.choose("Model", nil, nil, false); e == nil {
		t.Fatal("empty input must not select a model")
	}
}

func TestTeamRouterGraphNeedsIncomingEdgeAndThenValidates(t *testing.T) {
	p := testProject(t)
	s, err := p.Settings()
	if err != nil {
		t.Fatal(err)
	}
	w := NewWorkflow("ledger-demo", "Ledger demo")
	w.HeadAgent = "lead"
	profiles := map[string]Profile{}
	for _, a := range []struct {
		id    string
		level int
	}{{"lead", 0}, {"builder", 1}, {"tester", 1}, {"reviewer-a", 2}, {"reviewer-b", 2}, {"fixer", 3}, {"docs", 3}} {
		w.Agents = append(w.Agents, Agent{Id: a.id, Level: Int(a.level), Profile: ".agents/ledger-demo/" + a.id + ".agent.json", Runtime: s.DefaultModel})
		profiles[a.id] = NewProfile(a.id, a.id, "Do the "+a.id+" work", "Use evidence", []string{"read"})
	}
	w.Teams = []Team{{Id: "review-board", Name: "Review board", Members: []string{"reviewer-a", "reviewer-b"}, Facilitator: "reviewer-a", Consensus: c.WorkflowTeamConsensus{Mode: "unanimous-deliberation", MaxRounds: 2}, OutputContract: c.WorkflowTeamOutputContract{Format: "arena.team-verdict.v1", Verdicts: []string{"Approved", "ChangesRequested", "Deny", "NeedsMoreInformation"}}}}
	w.DecisionRouters = []Router{{Id: "review-gate", Name: "Review gate", InputKind: "team-result", PayloadPointer: "/verdict", Routes: []c.WorkflowDecisionRoute{{Id: "approved", Equals: "Approved", To: "docs"}, {Id: "changes", Equals: "ChangesRequested", To: "fixer"}}, Fallback: c.WorkflowDecisionRouterFallback{Action: "stop"}}}
	w.Relationships = []Relationship{{Type: "delegates", From: "lead", To: "builder"}, {Type: "delegates", From: "lead", To: "tester"}, {Type: "delegates", From: "builder", To: "review-board"}, {Type: "delegates", From: "tester", To: "review-board"}, {Type: "communicates", From: "builder", To: "tester", Direction: "two-way"}}
	if err := ValidateWorkflow(w, profiles, s); err == nil || !strings.Contains(err.Error(), "router review-gate has no incoming delegates edge") {
		t.Fatalf("missing router edge not explained: %v", err)
	}
	w.Relationships = append(w.Relationships, Relationship{Type: "delegates", From: "review-board", To: "review-gate"})
	if err := ValidateWorkflow(w, profiles, s); err != nil {
		t.Fatal(err)
	}
}
