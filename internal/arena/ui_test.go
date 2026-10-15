package arena

import (
	"strings"
	"testing"

	c "github.com/Kayyo321/agent-arena/internal/contract"
	"github.com/charmbracelet/x/ansi"
)

func TestLiveLayoutFitsTerminalAndKeepsPrompt(t *testing.T) {
	w, profiles, s := fixture(t)
	for _, size := range [][2]int{{140, 40}, {100, 24}, {60, 18}, {30, 8}, {8, 4}} {
		m := LiveModel{Run: Run{Workflow: w, Profiles: profiles, Settings: s, BaseCommit: "12345678"}, Width: size[0], Height: size[1], State: RunState{Status: "running", Agents: map[string]string{}}, Readiness: []Readiness{{Provider: "codex", State: "unknown"}, {Provider: "claude-code", State: "not configured"}}}
		content := m.View().Content
		lines := strings.Split(content, "\n")
		if len(lines) > size[1] {
			t.Fatalf("%v: %d rows", size, len(lines))
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > size[0] {
				t.Fatalf("%v: row width %d", size, ansi.StringWidth(line))
			}
		}
		if !strings.HasPrefix(lines[len(lines)-1], ">") {
			t.Fatalf("%v: missing full-width prompt row", size)
		}
		if size[0] >= 30 && !strings.Contains(content, "memento mori") {
			t.Fatal("idle prompt changed")
		}
	}
}
func TestGraphShowsTeamsRoutersAuthorityAndProviderIdentity(t *testing.T) {
	w, _, _ := fixture(t)
	lines := wideGraph(w, RunState{Agents: map[string]string{}}, nil, 140, false, "")
	view := strings.Join(lines, "\n")
	for _, part := range []string{"HEAD", "TEAM", "codex", "claude-code", "unanimous", "delegates"} {
		if !strings.Contains(view, part) {
			t.Fatalf("missing %s", part)
		}
	}
}

func TestPacketAnimationStopsAtRecordedDeliveryOrFailure(t *testing.T) {
	events := []Event{{Type: "message.sent", Data: map[string]any{"messageId": "one", "kind": "task", "from": map[string]any{"agentId": "head"}, "to": map[string]any{"agentId": "worker"}}}}
	first := strings.Join(packetActivity(events, 0), "\n")
	second := strings.Join(packetActivity(events, 1), "\n")
	if first == second || !strings.Contains(first, "pending") {
		t.Fatal("pending packet does not move")
	}
	for _, outcome := range []string{"delivered", "failed"} {
		finished := append(append([]Event{}, events...), Event{Type: "message." + outcome, Data: map[string]any{"messageId": "one"}})
		first, second = strings.Join(packetActivity(finished, 0), "\n"), strings.Join(packetActivity(finished, 1), "\n")
		if first != second || !strings.Contains(first, outcome) {
			t.Fatalf("%s packet keeps animating", outcome)
		}
	}
}

func TestRouterBranchesSplitFromStemWithOwnLabels(t *testing.T) {
	w := NewWorkflow("branches", "Branches")
	w.HeadAgent = "lead"
	w.Agents = []Agent{{Id: "lead", Level: Int(0), Runtime: Runtime{Provider: "codex", Model: "m"}}}
	for _, id := range []string{"left", "middle", "right"} {
		w.Agents = append(w.Agents, Agent{Id: id, Level: Int(2), Runtime: Runtime{Provider: "codex", Model: "m"}})
	}
	w.DecisionRouters = []Router{{Id: "gate", InputKind: "result", PayloadPointer: "/v", Routes: []c.WorkflowDecisionRoute{{Id: "a", Equals: "A", To: "left"}, {Id: "b", Equals: "B", To: "middle"}, {Id: "c", Equals: "C", To: "right"}}, Fallback: c.WorkflowDecisionRouterFallback{Action: "stop"}}}
	w.Relationships = []Relationship{{Type: "delegates", From: "lead", To: "gate"}}
	lines := wideGraph(w, RunState{Agents: map[string]string{}}, nil, 140, false, "")
	text := strings.Join(lines, "\n")
	if !strings.ContainsAny(text, "├┤┼") {
		t.Fatalf("branches leaving the stem hide it:\n%s", text)
	}
	for _, line := range lines {
		if strings.Contains(line, `="B"`) && !strings.Contains(line, "▼") {
			t.Fatalf("straight branch label is not beside its arrowhead: %q\n%s", line, text)
		}
	}
}

func TestGraphNodeKindsHaveDistinctFrames(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("LC_ALL", "en_US.UTF-8")
	w, _ := loopWorkflow(DefaultSettings(Runtime{Provider: "codex", Model: "m"}), 2)
	state := RunState{Agents: map[string]string{"builder": "running"}}
	plain := strings.Join(wideGraph(w, state, nil, 140, false, ""), "\n")
	for kind, frame := range map[string]string{"head": "┏━ ◆ HEAD", "worker": "╭─ builder", "team": "╭┄ ◈ TEAM", "router": "╱─ ◇ ROUTER"} {
		if !strings.Contains(plain, frame) {
			t.Fatalf("%s frame %q missing:\n%s", kind, frame, plain)
		}
	}
	colored := strings.Join(wideGraph(w, state, nil, 140, true, ""), "\n")
	if ansi.Strip(colored) != plain {
		t.Fatal("color changed graph geometry")
	}
	// A working card lights its frame; a waiting one stays dim.
	if !strings.Contains(colored, "\x1b[1;38;5;252m╭") || !strings.Contains(colored, "\x1b[38;5;240m╭") {
		t.Fatal("card frames do not reflect running and waiting states")
	}
	t.Setenv("LC_ALL", "C")
	for _, line := range wideGraph(w, state, nil, 140, false, "") {
		if strings.ContainsFunc(line, func(r rune) bool { return r >= 0x2500 && r <= 0x25ff }) {
			t.Fatalf("non-Unicode locale drew Unicode frames: %q", line)
		}
	}
}
