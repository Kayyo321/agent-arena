package arena

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	c "github.com/Kayyo321/agent-arena/internal/contract"
)

// fanOutWorkflow is lead -> split (planner plans, fixer works per item) ->
// reviewer, with fixer writing in its own worktree per child.
func fanOutWorkflow(s Settings, maxChildren int, approval string) (Workflow, map[string]Profile) {
	w := NewWorkflow("parallel", "Parallel fixes")
	w.HeadAgent = "lead"
	w.Settings.MaxConcurrentAgents = 4
	profiles := map[string]Profile{}
	for _, a := range []struct {
		id    string
		level int
		caps  []string
	}{{"lead", 0, []string{"read"}}, {"planner", 1, []string{"read"}}, {"fixer", 1, []string{"read", "write"}}, {"reviewer", 2, []string{"read"}}} {
		w.Agents = append(w.Agents, Agent{Id: a.id, Level: Int(a.level), Profile: ".agents/parallel/" + a.id + ".agent.json", Runtime: s.DefaultModel})
		profiles[a.id] = NewProfile(a.id, "research", "Do the "+a.id+" work", "Use evidence", a.caps)
	}
	w.FanOuts = []FanOut{{Id: "split", Name: "Parallel fixes", Planner: "planner", Worker: "fixer", MaxChildren: maxChildren, Approval: approval}}
	w.Relationships = []Relationship{{Type: "delegates", From: "lead", To: "split"}, {Type: "delegates", From: "split", To: "reviewer"}}
	return w, profiles
}

func TestFanOutExampleFixtureValidates(t *testing.T) {
	base := "../../examples/project/.arena"
	var w Workflow
	var s Settings
	for path, v := range map[string]any{"parallel-fixes.arena.json": &w, "settings.arena.json": &s} {
		b, err := os.ReadFile(filepath.Join(base, path))
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(b, v); err != nil {
			t.Fatal(err)
		}
	}
	profiles := map[string]Profile{}
	for _, a := range w.Agents {
		b, err := os.ReadFile(filepath.Join(base, a.Profile))
		if err != nil {
			t.Fatal(err)
		}
		var p Profile
		if err := json.Unmarshal(b, &p); err != nil {
			t.Fatal(err)
		}
		profiles[a.Id] = p
	}
	if err := ValidateWorkflow(w, profiles, s); err != nil {
		t.Fatal(err)
	}
}

func TestFanOutValidatorRejections(t *testing.T) {
	s := DefaultSettings(Runtime{Provider: "codex", Model: "gpt-test", ReasoningEffort: "high"})
	w, profiles := fanOutWorkflow(s, 4, "required")
	if err := ValidateWorkflow(w, profiles, s); err != nil {
		t.Fatal(err)
	}
	if *s.Limits.MaxFanOutChildrenPerRun != 8 {
		t.Fatalf("default fan-out limit = %d", *s.Limits.MaxFanOutChildrenPerRun)
	}
	older := convert[Settings](s)
	older.Limits.MaxFanOutChildrenPerRun = nil
	if fanOutLimit(older) != defaultFanOutChildren {
		t.Fatal("settings without the limit must fall back to the default")
	}
	tests := map[string]struct {
		mutate func(*Workflow, map[string]Profile, *Settings)
		want   string
	}{
		"over project limit":      {func(w *Workflow, _ map[string]Profile, s *Settings) { s.Limits.MaxFanOutChildrenPerRun = Int(3) }, "exceeds project maxFanOutChildrenPerRun"},
		"same planner and worker": {func(w *Workflow, _ map[string]Profile, _ *Settings) { w.FanOuts[0].Worker = "planner" }, "distinct planner and worker"},
		"missing worker":          {func(w *Workflow, _ map[string]Profile, _ *Settings) { w.FanOuts[0].Worker = "ghost" }, "invalid planner or worker"},
		"level mismatch": {func(w *Workflow, _ map[string]Profile, _ *Settings) {
			w.Agents[2].Level = Int(2)
		}, "same level"},
		"worker as team member": {func(w *Workflow, p map[string]Profile, _ *Settings) {
			w.Agents = append(w.Agents, Agent{Id: "other", Level: Int(1), Profile: ".agents/parallel/other.agent.json", Runtime: w.Agents[0].Runtime})
			p["other"] = NewProfile("other", "research", "o", "o", []string{"read"})
			w.Teams = []Team{{Id: "pair", Name: "Pair", Members: []string{"fixer", "other"}, Facilitator: "other"}}
			w.Teams[0].Consensus.Mode, w.Teams[0].Consensus.MaxRounds = "unanimous-deliberation", 1
			w.Teams[0].OutputContract.Format, w.Teams[0].OutputContract.Verdicts = "arena.team-verdict.v1", []string{"Approved"}
		}, "cannot be a team member"},
		"worker in two fan-outs": {func(w *Workflow, p map[string]Profile, _ *Settings) {
			w.Agents = append(w.Agents, Agent{Id: "planner2", Level: Int(1), Profile: ".agents/parallel/planner2.agent.json", Runtime: w.Agents[0].Runtime})
			p["planner2"] = NewProfile("planner2", "research", "o", "o", []string{"read"})
			w.FanOuts = append(w.FanOuts, FanOut{Id: "split2", Name: "Again", Planner: "planner2", Worker: "fixer", MaxChildren: 2})
			w.Relationships = append(w.Relationships, Relationship{Type: "delegates", From: "lead", To: "split2"})
		}, "serve another fan-out"},
		"on-call worker": {func(w *Workflow, _ map[string]Profile, _ *Settings) {
			w.Agents[2].Activation, w.Agents[2].Level = "on-call", nil
		}, "invalid planner or worker"},
		"edge to worker": {func(w *Workflow, _ map[string]Profile, _ *Settings) {
			w.Relationships = append(w.Relationships, Relationship{Type: "delegates", From: "lead", To: "fixer"})
		}, "cannot reference team members"},
		"worker as head": {func(w *Workflow, _ map[string]Profile, _ *Settings) {
			w.HeadAgent = "planner"
			w.Agents[1].Level = Int(0)
			w.Agents[2].Level = Int(0)
		}, "head must be a standalone path agent"},
		"reserved child ID": {func(w *Workflow, p map[string]Profile, _ *Settings) {
			w.Agents = append(w.Agents, Agent{Id: "split.2", Level: Int(2), Profile: ".agents/parallel/split.2.agent.json", Runtime: w.Agents[0].Runtime})
			p["split.2"] = NewProfile("split.2", "research", "o", "o", []string{"read"})
			w.Relationships = append(w.Relationships, Relationship{Type: "delegates", From: "split", To: "split.2"})
		}, "reserved for a child of fan-out split"},
		"long ID": {func(w *Workflow, _ map[string]Profile, _ *Settings) {
			id := strings.Repeat("f", 56)
			w.FanOuts[0].Id = id
			w.Relationships = []Relationship{{Type: "delegates", From: "lead", To: id}, {Type: "delegates", From: id, To: "reviewer"}}
		}, "longer than 55"},
		"unreachable": {func(w *Workflow, _ map[string]Profile, _ *Settings) {
			w.Relationships = []Relationship{{Type: "delegates", From: "lead", To: "reviewer"}}
		}, "path node split is unreachable"},
		"on-call trigger on worker": {func(w *Workflow, p map[string]Profile, _ *Settings) {
			w.Agents = append(w.Agents, Agent{Id: "medic", Activation: "on-call", Profile: ".agents/parallel/medic.agent.json", Runtime: w.Agents[0].Runtime})
			p["medic"] = NewProfile("medic", "research", "o", "o", []string{"read"})
			w.OnCall = []c.WorkflowOnCallRule{{AgentId: "medic", MaxInvocationsPerRun: 1, Triggers: []Trigger{{Type: "event", Event: "agent.failed", AgentId: "fixer"}}}}
		}, "agent.failed triggers do not fire for fan-out children"},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			w, profiles := fanOutWorkflow(s, 4, "required")
			settings := convert[Settings](s)
			test.mutate(&w, profiles, &settings)
			err := ValidateWorkflow(w, profiles, settings)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("got %v, want %q", err, test.want)
			}
		})
	}
	if fanOutChildOf(w, "split.12") != "split" || fanOutChildOf(w, "split.") != "" || fanOutChildOf(w, "split.1a") != "" || fanOutChildOf(w, "splitx.1") != "" {
		t.Fatal("child ID recognition")
	}
}
