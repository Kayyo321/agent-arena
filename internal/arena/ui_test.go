package arena

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
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

func TestPanMovesGraphCameraAndSpaceRecenters(t *testing.T) {
	w, profiles, s := fixture(t)
	m := LiveModel{Run: Run{Workflow: w, Profiles: profiles, Settings: s}, Width: 140, Height: 40, State: RunState{Agents: map[string]string{}}}
	press := func(m LiveModel, code rune) LiveModel {
		next, _ := m.Update(tea.KeyPressMsg{Code: code})
		return next.(LiveModel)
	}
	m.setPrompt("/pan", 4)
	m = press(m, tea.KeyEnter)
	if !m.Panning || m.Error != "" {
		t.Fatalf("/pan did not start panning: %q", m.Error)
	}
	centered := m.bodyLines(140, false)
	m = press(press(m, tea.KeyRight), tea.KeyDown)
	if m.PanX != panStepX || m.PanY != panStepY || m.Prompt != "" {
		t.Fatalf("arrows did not pan: %d,%d prompt %q", m.PanX, m.PanY, m.Prompt)
	}
	moved := m.bodyLines(140, false)
	if len(moved) != len(centered) {
		t.Fatalf("panning changed body height: %d != %d", len(moved), len(centered))
	}
	if strings.Join(moved, "\n") == strings.Join(centered, "\n") {
		t.Fatal("panned graph did not move")
	}
	legend := func(lines []string) string {
		for _, line := range lines {
			if strings.Contains(line, "dotted peer links") {
				return line
			}
		}
		return ""
	}
	if legend(centered) == "" || legend(moved) != legend(centered) {
		t.Fatalf("legend moved with the graph: %q", legend(moved))
	}
	m = press(m, tea.KeySpace)
	if m.PanX != 0 || m.PanY != 0 || !m.Panning {
		t.Fatalf("space did not recenter: %d,%d", m.PanX, m.PanY)
	}
	if strings.Join(m.bodyLines(140, false), "\n") != strings.Join(centered, "\n") {
		t.Fatal("recentered graph differs from the centered layout")
	}
	m = press(press(m, tea.KeyLeft), tea.KeyEscape)
	if m.Panning || m.PanX != -panStepX {
		t.Fatalf("esc should leave pan mode and keep the view: %v %d", m.Panning, m.PanX)
	}
	// Typing after leaving pan mode reaches the prompt; arrows recall history again.
	m.Panning = true
	next, _ := m.Update(tea.KeyPressMsg{Code: 'h', Text: "h"})
	if m = next.(LiveModel); m.Panning || m.Prompt != "h" {
		t.Fatalf("typing did not leave pan mode: %v %q", m.Panning, m.Prompt)
	}
}

func TestPanLinesClampsAndKeepsHeight(t *testing.T) {
	lines := []string{"abcd", "efgh", "ijkl"}
	if got := panLines(lines, 1, 1, 4); strings.Join(got, "|") != "fgh|jkl|" {
		t.Fatalf("pan right/down: %q", got)
	}
	if got := panLines(lines, -2, -1, 4); strings.Join(got, "|") != "|  abcd|  efgh" {
		t.Fatalf("pan left/up: %q", got)
	}
	for _, got := range [][]string{panLines(lines, 99, 99, 4), panLines(lines, -99, -99, 4)} {
		if len(got) != len(lines) || strings.TrimSpace(strings.Join(got, "")) == "" {
			t.Fatalf("clamped pan lost the graph: %q", got)
		}
	}
}

func TestSlashMenuSuggestsAndFillsWithoutRunning(t *testing.T) {
	w, profiles, s := fixture(t)
	m := LiveModel{Run: Run{Workflow: w, Profiles: profiles, Settings: s}, Width: 120, Height: 30, State: RunState{Agents: map[string]string{}}}
	press := func(m LiveModel, key tea.KeyPressMsg) LiveModel {
		next, _ := m.Update(key)
		return next.(LiveModel)
	}
	for _, char := range "/re" {
		m = press(m, tea.KeyPressMsg{Code: char, Text: string(char)})
	}
	options := m.slashSuggestions()
	if len(options) != 2 || options[0].text != "/resume" || options[1].text != "/refresh" {
		t.Fatalf("unexpected matches for /re: %+v", options)
	}
	if view := ansi.Strip(m.View().Content); !strings.Contains(view, "› /resume") || !strings.Contains(view, "/refresh  Required-provider readiness") {
		t.Fatalf("menu missing above prompt:\n%s", view)
	}
	m = press(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.Prompt != "/re" || m.suggestion(2) != 1 {
		t.Fatalf("down did not move the highlight: %q %d", m.Prompt, m.suggestion(2))
	}
	m = press(m, tea.KeyPressMsg{Code: tea.KeyDown})
	if m.suggestion(2) != 0 {
		t.Fatal("highlight did not wrap")
	}
	m = press(m, tea.KeyPressMsg{Code: tea.KeyUp})
	m = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.Prompt != "/refresh " || m.Error != "" {
		t.Fatalf("enter did not fill the highlight: %q %q", m.Prompt, m.Error)
	}
	// A partial command never runs, even when it uniquely names one.
	m.setPrompt("/canc", 5)
	m = press(m, tea.KeyPressMsg{Code: tea.KeyEnter})
	if m.Prompt != "/cancel " {
		t.Fatalf("partial command ran or was lost: %q", m.Prompt)
	}
	if usage := slashUsage["/detach"][0]; usage != "/detach N|all" || slashUsage["/attachments"][0] != "/attachments" {
		t.Fatalf("grouped usage misassigned arguments: %q", usage)
	}
	m.setPrompt("/view ", 6)
	if options := m.slashSuggestions(); len(options) != len(w.Agents)+1 || options[0].usage != "arena-orchestrator" {
		t.Fatalf("agent suggestions after /view: %+v", options)
	}
	m = press(m, tea.KeyPressMsg{Code: tea.KeyTab})
	if m.Prompt != "/view arena-orchestrator " {
		t.Fatalf("tab did not complete the agent: %q", m.Prompt)
	}
	m.setPrompt("hello /re", 9)
	if len(m.slashSuggestions()) != 0 {
		t.Fatal("plain text opened the menu")
	}
}
