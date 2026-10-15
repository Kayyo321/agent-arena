package arena

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestStatusBandLeadsWithWordmarkAndRunVitals(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("LC_ALL", "en_US.UTF-8")
	t.Setenv("NO_COLOR", "")
	w, profiles, s := fixture(t)
	head := w.HeadAgent
	m := LiveModel{Run: Run{ID: "run-1", Workflow: w, Profiles: profiles, Settings: s, BaseCommit: "12345678"}, Width: 160, Height: 40}
	m.State = RunState{Status: "running", Agents: map[string]string{head: "running"}}
	m.Trees = []Workspace{{AgentID: head, TaskID: head, Write: true}, {AgentID: head, TaskID: head + "-iter2", Write: true}}
	m.DiffStats = map[string]diffStat{head: {12, 3, 2}, head + "-iter2": {5, 1, 1}}
	view := m.View().Content
	lines := strings.Split(view, "\n")
	for _, line := range lines[:3] {
		plain := []rune(ansi.Strip(line))
		if len(plain) > 160 || strings.TrimSpace(string(plain[:2])) != "" || !strings.ContainsAny(string(plain[2:29]), "⣿⠛⣤") || strings.ContainsAny(string(plain[29:]), "⣿⠛⣤") {
			t.Fatalf("wordmark does not lead the band from the left margin: %q", ansi.Strip(line))
		}
	}
	band := ansi.Strip(strings.Join(lines[:3], "\n"))
	for _, part := range []string{w.Id, "running", "ACTIVE", "● " + head, "NODES", "CHANGES", "+17 −4", "3 files", "GIT", "1234567 · 2 worktrees"} {
		if !strings.Contains(band, part) {
			t.Fatalf("status band missing %q:\n%s", part, band)
		}
	}
	if !strings.HasPrefix(ansi.Strip(lines[3]), strings.Repeat("─", 160)) || strings.Contains(view, "git trees") {
		t.Fatal("band is not closed by a hairline or still draws the old Git box")
	}
	for _, ink := range []string{"38;5;183", "38;5;141", "38;5;99"} {
		if !strings.Contains(view, ink) {
			t.Fatalf("missing purple gradient %s", ink)
		}
	}
	// The head card reports its own churn summed across loop passes.
	if !strings.Contains(ansi.Strip(view), "+17 −4 · 3 files") || !strings.Contains(view, "HEAD") || !strings.Contains(ansi.Strip(view), "> memento mori") {
		t.Fatal("band displaced the graph, prompt or per-agent changes")
	}
	// Narrower bands drop the Git column first and never drop the change size.
	for _, width := range []int{100, 70} {
		m.Width = width
		if width == 100 && !strings.Contains(ansi.Strip(m.View().Content), "2 trees") {
			t.Fatal("worktree count lost without the Git column")
		}
		m.Width = width
		band := ansi.Strip(strings.Join(strings.Split(m.View().Content, "\n")[:3], "\n"))
		if strings.Contains(band, "GIT") || !strings.Contains(band, "+17 −4") {
			t.Fatalf("width %d band:\n%s", width, band)
		}
	}
	logo := strings.Join(wordmark(140, 40, "show", true), "\n")
	rows := wordmark(140, 40, "show", false)
	if ansi.StringWidth(rows[0]) != 27 || strings.TrimSpace(string([]rune(rows[0])[11:])) != "" || strings.TrimSpace(string([]rune(rows[1])[12:])) == "" {
		t.Fatal("lowercase rena must be shorter and smaller than A2, aligned to its baseline")
	}
	if strings.ContainsAny(logo, "█▀▄") || strings.Contains(logo, "48;") || strings.Contains(logo, "38;5;54") || strings.Contains(logo, "38;5;61") {
		t.Fatal("dotted title retained a solid fill or shadow backdrop")
	}
	if ansi.Strip(logo) != strings.Join(wordmark(140, 40, "show", false), "\n") {
		t.Fatal("color added extra geometry behind the lettering")
	}
}

func TestPatchDiffCountsLinesAndFiles(t *testing.T) {
	patch := "diff --git a/x b/x\n--- a/x\n+++ b/x\n@@ -1,2 +1,2 @@\n-old\n+new\n+more\n context\ndiff --git a/y b/y\n--- /dev/null\n+++ b/y\n+y\n"
	if got := patchDiff([]byte(patch)); got != (diffStat{3, 1, 2}) {
		t.Fatalf("patch stat %+v", got)
	}
}

func TestWordmarkHonorsColorSizeAndTextFallbackPreferences(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("LC_ALL", "en_US.UTF-8")
	t.Setenv("NO_COLOR", "1")
	view := strings.Join(brandedHeader(nil, 80, 24, "show", terminalColor("always")), "\n")
	if strings.Contains(view, "\x1b") || !strings.ContainsAny(view, "⣿⠛⣤") {
		t.Fatal("NO_COLOR lost legibility or leaked color")
	}
	for _, size := range [][2]int{{140, 40}, {60, 18}, {30, 8}, {8, 4}} {
		lines := brandedHeader(nil, size[0], size[1], "compact", false)
		if len(lines) != 1 || !strings.HasPrefix(strings.TrimLeft(lines[0], " "), "A2rena") || ansi.StringWidth(lines[0]) > size[0] {
			t.Fatalf("compact fallback does not lead the header: %v %q", size, lines)
		}
	}
	if lines := brandedHeader(nil, 80, 24, "hide", false); len(lines) != 0 {
		t.Fatal("hidden title still rendered")
	}
	t.Setenv("LC_ALL", "C")
	if lines := wordmark(140, 40, "show", false); len(lines) != 1 || lines[0] != "A2rena" {
		t.Fatal("non-Unicode locale lost plain-text title")
	}
}
