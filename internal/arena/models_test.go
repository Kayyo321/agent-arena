package arena

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

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

func TestWaitInboxBlocksUntilPeerReplyAndSkipsSeen(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	go func() {
		time.Sleep(300 * time.Millisecond)
		packet := NewPacket(r.ID, "message", "peer", "codex", "head", "codex", "", map[string]any{"text": "API agreed"}, Runtime{Provider: "codex"})
		_, _ = p.Deliver(r, packet)
	}()
	returned := map[string]bool{}
	start := time.Now()
	packets, err := p.WaitInbox(context.Background(), r, "head", 5*time.Second, returned, true)
	if err != nil || len(packets) != 1 || time.Since(start) < 250*time.Millisecond {
		t.Fatalf("wait returned %d packets after %s: %v", len(packets), time.Since(start), err)
	}
	returned[str(packets[0]["messageId"])] = true
	packets, err = p.WaitInbox(context.Background(), r, "head", 600*time.Millisecond, returned, true)
	if err != nil || len(packets) != 0 {
		t.Fatalf("seen message returned again: %d %v", len(packets), err)
	}
}

func TestContextSearchMatchesWordsNotWholePhrases(t *testing.T) {
	entries := []ContextEntry{
		{Id: "old", Title: "Ledger module API and money convention", Content: "Amounts are Decimal; expenses positive.", Tags: []string{"ledger", "api"}, CreatedAt: "2026-10-15T23:00:00Z"},
		{Id: "new", Title: "Ledger v1 API", Content: "Use Decimal for money.", Tags: []string{"ledger"}, CreatedAt: "2026-10-15T23:30:00Z"},
		{Id: "other", Title: "Deployment notes", Content: "Unrelated.", CreatedAt: "2026-10-15T23:40:00Z"},
	}
	ids := func(found []ContextEntry) []string {
		out := []string{}
		for _, e := range found {
			out = append(out, e.Id)
		}
		return out
	}
	if got := ids(ContextSearch(entries, "ledger API money convention")); len(got) != 2 || got[0] != "old" {
		t.Fatalf("natural query: %v", got)
	}
	objective := "Implement ledger v1: python3 -m ledger summary FILE.csv. Record the module API and the money-handling convention in shared context."
	if got := ids(ContextSearch(entries, objective)); !Has(got, "old") || !Has(got, "new") || Has(got, "other") {
		t.Fatalf("task objective did not surface relevant context: %v", got)
	}
	if got := ids(ContextSearch(entries, "")); len(got) != 3 || got[0] != "other" {
		t.Fatalf("empty query should list all, newest first: %v", got)
	}
	if got := ContextSearch(entries, "kubernetes"); len(got) != 0 {
		t.Fatalf("unrelated query matched: %v", ids(got))
	}
}

func TestStallBannerClearsWhenRunEnds(t *testing.T) {
	m := LiveModel{Run: Run{ID: "run-x", Settings: DefaultSettings(Runtime{})}, State: RunState{Status: "succeeded"}, Stall: &stallDiagnosis{RunID: "run-x", Summary: "stalled", Applied: true, DecisionID: "d", Answer: "builder"}}
	if lines := m.attentionLines(100, false); len(lines) != 0 {
		t.Fatalf("finished run still shows the stall note: %v", lines)
	}
	m.State.Status = "running"
	if lines := m.attentionLines(100, false); len(lines) == 0 {
		t.Fatal("running run hides the stall note")
	}
}

// DiscoverModels reads OpenCode's catalog from `opencode models --verbose`: exact
// provider/model IDs with the variants each model accepts as efforts. Contract
// coverage on a fake CLI that prints the recorded fixture.
func TestDiscoverModelsListsTheOpenCodeCatalog(t *testing.T) {
	fixture, err := filepath.Abs(openCodeFixture)
	if err != nil {
		t.Fatal(err)
	}
	record, _ := openCodeFake(t, "cat '"+fixture+"'\n")
	models, err := DiscoverModels(context.Background(), t.TempDir(), Runtime{Provider: "opencode"})
	if err != nil || len(models) != 6 {
		t.Fatalf("catalog: %d models, %v", len(models), err)
	}
	if models[0].ID != "opencode/big-pickle" || len(models[0].Efforts) != 0 || strings.Join(models[1].Efforts, ",") != "low,medium,high,xhigh,max" {
		t.Fatalf("catalog entries: %+v", models[:2])
	}
	if args := recorded(t, record, "args"); args != "models\n--verbose\n" {
		t.Fatalf("argv: %q", args)
	}
	openCodeFake(t, "exit 1\n")
	if _, err := DiscoverModels(context.Background(), t.TempDir(), Runtime{Provider: "opencode"}); err == nil || !strings.Contains(err.Error(), "could not list models") {
		t.Fatalf("failed listing: %v", err)
	}
	if _, err := DiscoverModels(context.Background(), t.TempDir(), Runtime{Provider: "opencode-cloud"}); err == nil || !strings.Contains(err.Error(), "unsupported provider") {
		t.Fatalf("unknown providers must stay refused: %v", err)
	}
}

// The provider menu lists OpenCode after the providers that already had numbers,
// so a script that answers "4" or "5" still gets Gemini or a local model. OpenCode
// asks for no effort: it is optional, and the catalog check at readiness covers it.
func TestRuntimeChoiceOffersOpenCodeWithoutRenumberingOthers(t *testing.T) {
	fixture, err := filepath.Abs(openCodeFixture)
	if err != nil {
		t.Fatal(err)
	}
	openCodeFake(t, "cat '"+fixture+"'\n")
	var out bytes.Buffer
	// "6" picks the provider; the catalog then lists models with their efforts,
	// and typing an exact ID finishes the choice. Nothing else is read.
	cli := NewCLI(strings.NewReader("6\nopencode/cat-jam-free\n"), &out, &out)
	r, err := cli.runtimeChoice(context.Background(), &Project{t.TempDir()}, Runtime{}, Options{})
	if err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if r.Provider != "opencode" || r.Model != "opencode/cat-jam-free" || r.ReasoningEffort != "" || len(r.Options) != 0 {
		t.Fatalf("runtime: %+v", r)
	}
	for _, want := range []string{" 4) gemini", " 5) local", " 6) opencode", "efforts: low, medium, high, xhigh, max"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("menu lacks %q:\n%s", want, out.String())
		}
	}
	// Flags skip the menu, and an effort that is not given stays unset.
	cli = NewCLI(strings.NewReader(""), &out, &out)
	r, err = cli.runtimeChoice(context.Background(), &Project{t.TempDir()}, Runtime{}, Options{"provider": {"opencode"}, "model": {"opencode/big-pickle"}})
	if err != nil || r.Provider != "opencode" || r.ReasoningEffort != "" {
		t.Fatalf("flags: %+v %v", r, err)
	}
	r, err = cli.runtimeChoice(context.Background(), &Project{t.TempDir()}, Runtime{}, Options{"provider": {"opencode"}, "model": {"opencode/cat-jam-free"}, "effort": {"high"}})
	if err != nil || r.ReasoningEffort != "high" {
		t.Fatalf("flags with effort: %+v %v", r, err)
	}
	if _, err = cli.runtimeChoice(context.Background(), &Project{t.TempDir()}, Runtime{}, Options{"provider": {"opencode"}, "model": {"big-pickle"}}); err == nil {
		t.Fatal("a model without a provider prefix was accepted")
	}
}
