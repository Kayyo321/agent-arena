package arena

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	c "github.com/Kayyo321/agent-arena/internal/contract"
)

const (
	longObjective    = "Deliver the requested change with focused, tested and reviewable commits for the team."
	longInstructions = "Read the task packet and the relevant files first, plan the smallest coherent change, implement it in your worktree, run the project's tests, and report the files you touched, the commands you ran, their results and any open risks you noticed."
)

func scoreProfile(id, role string, caps ...string) Profile {
	p := NewProfile(id, role, longObjective+" ("+id+" "+role+")", longInstructions+" Focus: "+id+" as "+role+" "+strings.Repeat(id+" ", 3), caps)
	if Has(caps, "write") {
		p.Constraints = []string{"Only change files the task names."}
	}
	return p
}

func scoreAgent(w *Workflow, id string, level int, provider, model string) {
	a := Agent{Id: id, Profile: ".agents/" + w.Id + "/" + id + ".agent.json", Activation: "path", Level: Int(level), Runtime: Runtime{Provider: provider, Model: model, ReasoningEffort: "high"}}
	if level < 0 {
		a.Activation, a.Level = "on-call", nil
	}
	w.Agents = append(w.Agents, a)
}

// idealArena is the S+ example from docs/ARENA-SCORING.md.
func idealArena() (Workflow, map[string]Profile) {
	w := NewWorkflow("ideal", "Ideal arena")
	w.Description = "A lead plans, an implementer changes code, an independent reviewer checks it and can send it back."
	w.HeadAgent = "lead"
	w.Settings.MaxConcurrentAgents = 2
	scoreAgent(&w, "lead", 0, "codex", "gpt-test")
	scoreAgent(&w, "implementer", 1, "claude-code", "sonnet")
	scoreAgent(&w, "reviewer", 2, "codex", "gpt-test")
	scoreAgent(&w, "maintainer", -1, "codex", "gpt-test")
	w.Relationships = []Relationship{{Type: "delegates", From: "lead", To: "implementer"}, {Type: "delegates", From: "implementer", To: "reviewer"}, {Type: "delegates", From: "reviewer", To: "gate"}}
	w.DecisionRouters = []Router{{Id: "gate", Name: "Review gate", InputKind: "review-result", PayloadPointer: "/outcome", Routes: []c.WorkflowDecisionRoute{{Id: "revise", Equals: "ChangesRequested", To: "implementer", Loop: boolPtr(true), MaxIterations: Int(2)}}, Fallback: c.WorkflowDecisionRouterFallback{Action: "stop"}}}
	w.OnCall = []c.WorkflowOnCallRule{{AgentId: "maintainer", MaxInvocationsPerRun: 1, Triggers: []Trigger{{Type: "event", Event: "validation.failed"}}}}
	profiles := map[string]Profile{
		"lead":        scoreProfile("lead", "coordinator", "read", "research"),
		"implementer": scoreProfile("implementer", "implementation", "read", "write", "run_commands"),
		"reviewer":    scoreProfile("reviewer", "review", "read", "review", "run_commands"),
		"maintainer":  scoreProfile("maintainer", "escalation", "read", "review"),
	}
	return w, profiles
}

func boolPtr(b bool) *bool { return &b }

func scored(t *testing.T, w Workflow, profiles map[string]Profile) Scorecard {
	t.Helper()
	s := DefaultSettings(Runtime{Provider: "codex", Model: "gpt-test", ReasoningEffort: "high"})
	s.ApprovalPolicies.ChangeIntegration = "auto-if-clean"
	if err := ValidateWorkflow(w, profiles, s); err != nil {
		t.Fatalf("fixture does not validate: %v", err)
	}
	return ScoreArena(w, profiles)
}

func findingIDs(s Scorecard) []string {
	ids := []string{}
	for _, f := range s.Findings {
		ids = append(ids, f.ID)
	}
	return ids
}

func TestIdealArenaScoresSPlus(t *testing.T) {
	w, profiles := idealArena()
	s := scored(t, w, profiles)
	if s.Grade != "S+" || s.Points != 100 {
		t.Fatalf("ideal arena: %s %d %v\n%s", s.Grade, s.Points, findingIDs(s), strings.Join(s.Report(), "\n"))
	}
}

func TestScoreChecksDeductAndGate(t *testing.T) {
	cases := []struct {
		name   string
		edit   func(*Workflow, map[string]Profile)
		want   string
		grade  string // exact grade, or "" to skip
		atMost string // grade must be this or lower
	}{
		{"same model review", func(w *Workflow, p map[string]Profile) { w.Agents[2].Runtime = w.Agents[1].Runtime }, "T4", "A+", ""},
		{"reviewer writes", func(w *Workflow, p map[string]Profile) {
			r := p["reviewer"]
			r.Capabilities = append(r.Capabilities, "write")
			r.Constraints = []string{"Only fix typos."}
			p["reviewer"] = r
		}, "T3", "", "A-"},
		{"no loop", func(w *Workflow, p map[string]Profile) {
			w.DecisionRouters = nil
			w.Relationships = w.Relationships[:2]
		}, "V2", "A+", ""},
		{"auto integration", func(w *Workflow, p map[string]Profile) { w.Settings.Integration.Policy = "auto-if-clean" }, "T5", "S", ""},
		{"no description", func(w *Workflow, p map[string]Profile) { w.Description = "" }, "C5", "S+", ""},
		{"two minor findings", func(w *Workflow, p map[string]Profile) {
			w.Description = ""
			w.Agents[2].Runtime.ReasoningEffort = "medium"
		}, "V7", "S", ""},
		{"thin profile", func(w *Workflow, p map[string]Profile) {
			l := p["lead"]
			l.Instructions = "Plan it."
			p["lead"] = l
		}, "C1", "S", ""},
		{"no escalation", func(w *Workflow, p map[string]Profile) {
			w.OnCall[0].Triggers = []Trigger{{Type: "manual"}}
		}, "V6", "S", ""},
		{"loop at cap five", func(w *Workflow, p map[string]Profile) { w.DecisionRouters[0].Routes[0].MaxIterations = Int(5) }, "V3", "S+", ""},
		{"researcher runs commands", func(w *Workflow, p map[string]Profile) {
			l := p["lead"]
			l.Capabilities = append(l.Capabilities, "run_commands")
			p["lead"] = l
		}, "T6", "A+", ""},
		{"generic name", func(w *Workflow, p map[string]Profile) {
			m := p["maintainer"]
			m.Name = "bot"
			p["maintainer"] = m
		}, "C3", "S+", ""},
		{"head reviews", func(w *Workflow, p map[string]Profile) { p["lead"] = scoreProfile("lead", "review", "read", "review") }, "C4", "", ""},
		{"coordinator does the work", func(w *Workflow, p map[string]Profile) {
			l := p["lead"]
			l.Capabilities = []string{"read", "write", "run_commands"}
			l.Constraints = []string{"Only change files the task names."}
			p["lead"] = l
		}, "T2", "", "A-"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			w, profiles := idealArena()
			tc.edit(&w, profiles)
			s := scored(t, w, profiles)
			if !Has(findingIDs(s), tc.want) {
				t.Fatalf("want %s in %v", tc.want, findingIDs(s))
			}
			if tc.grade != "" && s.Grade != tc.grade {
				t.Fatalf("grade %s, want %s (%d points, %v)", s.Grade, tc.grade, s.Points, findingIDs(s))
			}
			if tc.atMost != "" && gradeRank(s.Grade) < gradeRank(tc.atMost) {
				t.Fatalf("grade %s, want at most %s", s.Grade, tc.atMost)
			}
		})
	}
}

func TestUnreviewedWriterCapsAtB(t *testing.T) {
	w := NewWorkflow("solo", "Solo")
	w.Description = "One agent does everything."
	w.HeadAgent = "builder"
	w.Settings.MaxConcurrentAgents = 1
	scoreAgent(&w, "builder", 0, "codex", "gpt-test")
	profiles := map[string]Profile{"builder": scoreProfile("builder", "implementation", "read", "write", "run_commands")}
	s := scored(t, w, profiles)
	if !Has(findingIDs(s), "T1") || !Has(findingIDs(s), "V1") || gradeRank(s.Grade) < gradeRank("B") {
		t.Fatalf("solo writer: %s %v", s.Grade, findingIDs(s))
	}
	w.Settings.Integration.Policy = "auto-if-clean"
	if s = scored(t, w, profiles); gradeRank(s.Grade) < gradeRank("C") {
		t.Fatalf("auto-integrated solo writer: %s", s.Grade)
	}
}

func TestSoloReaderTopsOutAtS(t *testing.T) {
	w := NewWorkflow("reader", "Reader")
	w.Description = "One researcher answers questions about the code."
	w.HeadAgent = "researcher"
	w.Settings.MaxConcurrentAgents = 1
	scoreAgent(&w, "researcher", 0, "codex", "gpt-test")
	s := scored(t, w, map[string]Profile{"researcher": scoreProfile("researcher", "research", "read", "research")})
	if s.Points != 100 || s.Grade != "S" || len(s.Blockers) == 0 {
		t.Fatalf("solo reader: %s %d %v", s.Grade, s.Points, s.Blockers)
	}
}

func TestRedundantAndRelayAgents(t *testing.T) {
	w, profiles := idealArena()
	// A second implementer doing the same job, and a relay in front of the reviewer.
	scoreAgent(&w, "implementer-b", 1, "claude-code", "sonnet")
	w.Relationships = append(w.Relationships, Relationship{Type: "delegates", From: "lead", To: "implementer-b"}, Relationship{Type: "delegates", From: "implementer-b", To: "reviewer"})
	twin := profiles["implementer"]
	twin.Id, twin.Name = "implementer-b", "implementer-b"
	profiles["implementer-b"] = twin
	s := scored(t, w, profiles)
	if !Has(findingIDs(s), "P1") || gradeRank(s.Grade) < gradeRank("A") {
		t.Fatalf("redundant pair: %s %v", s.Grade, findingIDs(s))
	}

	w, profiles = idealArena()
	w.Agents[2].Level = Int(3)
	scoreAgent(&w, "forwarder", 2, "codex", "gpt-test")
	w.Relationships = []Relationship{{Type: "delegates", From: "lead", To: "implementer"}, {Type: "delegates", From: "implementer", To: "forwarder"}, {Type: "delegates", From: "forwarder", To: "reviewer"}, {Type: "delegates", From: "reviewer", To: "gate"}}
	profiles["forwarder"] = scoreProfile("forwarder", "handoff", "read")
	s = scored(t, w, profiles)
	if !Has(findingIDs(s), "P2") {
		t.Fatalf("relay: %v", findingIDs(s))
	}
}

func TestExampleArenaScoresSPlusAndSiblingReviewDoesNotCount(t *testing.T) {
	w, profiles, settings := fixture(t)
	if s := ScoreArena(w, profiles); s.Grade != "S+" {
		t.Fatalf("example arena: %s %d %v", s.Grade, s.Points, findingIDs(s))
	}
	// Make the review team the implementer's sibling: it still runs after the
	// coordinator, but it never receives the implementer's change.
	w.Relationships = []Relationship{{Type: "delegates", From: "orchestrator", To: "implementer"}, {Type: "delegates", From: "orchestrator", To: "release-review"}, {Type: "delegates", From: "release-review", To: "review-gate"}}
	w.DecisionRouters[0].Routes[0].To = "orchestrator"
	if err := ValidateWorkflow(w, profiles, settings); err != nil {
		t.Fatal(err)
	}
	if s := ScoreArena(w, profiles); !Has(findingIDs(s), "T1") || s.Grade != "B" {
		t.Fatalf("sibling review: %s %d %v", s.Grade, s.Points, findingIDs(s))
	}
}

func TestGradeBands(t *testing.T) {
	for points, want := range map[int]string{100: "S+", 97: "S+", 96: "S", 90: "S-", 89: "A+", 80: "A-", 79: "B+", 60: "C-", 50: "D-", 49: "F", 0: "F"} {
		if got := gradeFor(points); got != want {
			t.Fatalf("gradeFor(%d) = %s, want %s", points, got, want)
		}
	}
}

func TestScoreSheetDocumentsEveryCheck(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join("..", "..", "docs", "ARENA-SCORING.md"))
	if err != nil {
		t.Fatal(err)
	}
	for id := range scoreChecks {
		if !strings.Contains(string(doc), "| "+id+" |") {
			t.Fatalf("docs/ARENA-SCORING.md does not document %s", id)
		}
	}
}

func TestWritingTeamNeedsReviewLikeAnyWriter(t *testing.T) {
	w := NewWorkflow("pair", "Pair builders")
	w.Description = "A read-only lead hands the change to a two-writer team with nobody reviewing it."
	w.HeadAgent = "lead"
	w.Settings.MaxConcurrentAgents = 2
	w.Settings.Integration.Policy = "auto-if-clean"
	scoreAgent(&w, "lead", 0, "codex", "gpt-test")
	scoreAgent(&w, "builder-a", 1, "codex", "gpt-test")
	scoreAgent(&w, "builder-b", 1, "claude-code", "sonnet")
	scoreAgent(&w, "helper", -1, "claude-code", "sonnet")
	w.Teams = []Team{{Id: "builders", Name: "Builders", Members: []string{"builder-a", "builder-b"}, Facilitator: "builder-a", Consensus: c.WorkflowTeamConsensus{Mode: "unanimous-deliberation", MaxRounds: 2}, OutputContract: c.WorkflowTeamOutputContract{Format: "arena.team-verdict.v1", Verdicts: []string{"Approved", "ChangesRequested"}}}}
	w.Relationships = []Relationship{{Type: "delegates", From: "lead", To: "builders"}}
	w.OnCall = []c.WorkflowOnCallRule{{AgentId: "helper", MaxInvocationsPerRun: 1, Triggers: []Trigger{{Type: "event", Event: "validation.failed"}}}}
	profiles := map[string]Profile{
		"lead":      scoreProfile("lead", "coordinator", "read", "research"),
		"builder-a": scoreProfile("builder-a", "implementation", "read", "write"),
		"builder-b": scoreProfile("builder-b", "implementation", "read", "write"),
		"helper":    scoreProfile("helper", "escalation", "read"),
	}
	s := scored(t, w, profiles)
	ids := findingIDs(s)
	if !Has(ids, "T1") || !Has(ids, "V1") || Has(ids, "P9") || gradeRank(s.Grade) < gradeRank("C") {
		t.Fatalf("writing team: %s %d %v", s.Grade, s.Points, ids)
	}
	for _, f := range s.Findings {
		if f.ID == "T5" && f.Severity != "major" {
			t.Fatal("auto-if-clean over an unreviewed writing team is not major")
		}
	}
}

// Antigravity's headless adapter refuses run_commands, so granting it to an
// Antigravity reviewer must not satisfy V5 or lift the grade.
func TestScoreIgnoresCapabilitiesTheAdapterCannotExercise(t *testing.T) {
	antigravityReviewer := func(caps ...string) (Workflow, map[string]Profile) {
		w, profiles := idealArena()
		w.Agents[2].Runtime = Runtime{Provider: "antigravity", Model: "gemini-test", ReasoningEffort: "high"}
		profiles["reviewer"] = scoreProfile("reviewer", "review", caps...)
		return w, profiles
	}
	w, profiles := antigravityReviewer("read", "review")
	without := scored(t, w, profiles)
	if !Has(findingIDs(without), "V5") || len(WorkflowWarnings(w, profiles)) != 0 {
		t.Fatalf("reviewer without run_commands: %v %v", findingIDs(without), WorkflowWarnings(w, profiles))
	}
	w, profiles = antigravityReviewer("read", "review", "run_commands")
	with := scored(t, w, profiles)
	ids := findingIDs(with)
	if !Has(ids, "V5") || !Has(ids, "C6") || with.Points >= without.Points || gradeRank(with.Grade) < gradeRank(without.Grade) {
		t.Fatalf("unusable run_commands raised the score: %s %d %v (was %s %d)\n%s", with.Grade, with.Points, ids, without.Grade, without.Points, strings.Join(with.Report(), "\n"))
	}
	warnings := WorkflowWarnings(w, profiles)
	if len(warnings) != 1 || !strings.Contains(warnings[0], "reviewer has run_commands") || !strings.Contains(warnings[0], "antigravity") {
		t.Fatalf("warnings: %v", warnings)
	}
	// Moving the same reviewer to an adapter that runs commands earns V5.
	w.Agents[2].Runtime = Runtime{Provider: "gemini", Model: "gemini-test", Options: map[string]any{"authMethod": "vertex-ai"}}
	if ids := findingIDs(scored(t, w, profiles)); Has(ids, "V5") || Has(ids, "C6") {
		t.Fatalf("capable adapter: %v", ids)
	}
}
