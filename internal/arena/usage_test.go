package arena

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func usageEvent(agent string, usage map[string]any) Event {
	return Event{AgentID: agent, Type: "usage.reported", Data: map[string]any{"provider": "p", "usage": usage}}
}

func TestSumUsageKeepsOpaqueMapsAndTakesOnlyReportedTokens(t *testing.T) {
	events := []Event{
		{AgentID: "a", Type: "agent.started"},
		// Running totals within one invocation count once, at their highest.
		usageEvent("a", map[string]any{"tokenUsage": map[string]any{"total": map[string]any{"inputTokens": json.Number("100"), "outputTokens": json.Number("10"), "cachedInputTokens": json.Number("5")}}}),
		usageEvent("a", map[string]any{"tokenUsage": map[string]any{"total": map[string]any{"inputTokens": json.Number("300"), "outputTokens": json.Number("40")}}}),
		// A retry opens a new window and adds to the first.
		{AgentID: "a", Type: "agent.started"},
		usageEvent("a", map[string]any{"input_tokens": float64(50), "output_tokens": float64(5), "service_tier": "standard"}),
		{AgentID: "b", Type: "agent.started"},
		usageEvent("b", map[string]any{"promptTokenCount": 7, "candidatesTokenCount": 3}),
		{AgentID: "c", Type: "agent.started"},
		usageEvent("c", map[string]any{"mystery": 12}),
	}
	u := SumUsage(events)
	if !u.Reported || u.InputTokens != 357 || u.OutputTokens != 48 {
		t.Fatalf("run total %+v", u.UsageTotals)
	}
	if a := u.Agents["a"]; a.InputTokens != 350 || a.OutputTokens != 45 {
		t.Fatalf("agent a %+v", a)
	}
	if _, ok := u.Agents["c"]; ok {
		t.Fatal("an agent whose provider reported no tokens must stay unreported")
	}
	if got := u.Report([]string{"c", "b"}); got[len(got)-2] != "b | 7 in · 3 out" || got[len(got)-1] != "c | unreported" {
		t.Fatalf("report %q", got)
	}
}

func TestSumUsageUnreportedWithoutEvents(t *testing.T) {
	u := SumUsage([]Event{{AgentID: "a", Type: "agent.started"}})
	if u.Reported || u.String() != "unreported" {
		t.Fatalf("%+v", u)
	}
}

func TestUsageSurfacesInBandPanelAndHistory(t *testing.T) {
	w, profiles, s := fixture(t)
	events := []Event{{AgentID: "orchestrator", Type: "agent.started"}, usageEvent("orchestrator", map[string]any{"inputTokens": 1200, "outputTokens": 340})}
	// Wide bands get a TOKENS column; the compact one-row band appends a total.
	for _, size := range [][2]int{{140, 40}, {100, 10}, {8, 4}} {
		m := LiveModel{Run: Run{ID: "run-1", Workflow: w, Profiles: profiles, Settings: s, BaseCommit: "12345678"}, Width: size[0], Height: size[1], State: RunState{Status: "running", Agents: map[string]string{}}, Events: events}
		view := m.View().Content
		want := map[int]string{140: "1200 in", 100: "1540 tok"}[size[0]]
		if !strings.Contains(ansi.Strip(view), want) {
			t.Fatalf("%v: band lacks %q", size, want)
		}
	}
	m := LiveModel{Run: Run{ID: "run-1", Workflow: w, Profiles: profiles, Settings: s, BaseCommit: "12345678"}, Width: 100, Height: 24, State: RunState{Agents: map[string]string{}}, Events: events}
	next, _ := m.submitInput("/usage")
	panel := strings.Join(next.(LiveModel).PanelText, "\n")
	if !strings.Contains(panel, "Run total: 1200 in · 340 out") || !strings.Contains(panel, "orchestrator | 1200 in") {
		t.Fatalf("panel:\n%s", panel)
	}
	idle := LiveModel{Run: Run{Workflow: w, Profiles: profiles, Settings: s, BaseCommit: "12345678"}, Width: 140, Height: 40, State: RunState{Agents: map[string]string{}}}
	if !strings.Contains(idle.View().Content, "unreported") {
		t.Fatal("band does not say unreported")
	}
}

func TestHistoryJSONCarriesUsage(t *testing.T) {
	p := &Project{Root: t.TempDir()}
	h, err := p.History("run-x", []Event{{AgentID: "a", Type: "agent.started"}, usageEvent("a", map[string]any{"output_tokens": 9})})
	if err != nil {
		t.Fatal(err)
	}
	var out struct{ Usage RunUsage }
	if err := json.Unmarshal(JSON(h), &out); err != nil || !out.Usage.Reported || out.Usage.OutputTokens != 9 || out.Usage.Agents["a"].OutputTokens != 9 {
		t.Fatalf("%v %+v", err, out.Usage)
	}
}
