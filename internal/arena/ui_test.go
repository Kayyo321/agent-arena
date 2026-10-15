package arena

import (
	"strings"
	"testing"

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
