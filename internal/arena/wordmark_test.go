package arena

import (
	"fmt"
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
	if ansi.StringWidth(rows[0]) != 27 || trimBlankBraille(string([]rune(rows[0])[11:])) != "" || trimBlankBraille(string([]rune(rows[1])[12:])) == "" {
		t.Fatal("lowercase rena must be shorter and smaller than A2, aligned to its baseline")
	}
	if strings.ContainsAny(logo, "█▀▄") || strings.Contains(logo, "48;") || strings.Contains(logo, "38;5;54") || strings.Contains(logo, "38;5;61") {
		t.Fatal("dotted title retained a solid fill or shadow backdrop")
	}
	if ansi.Strip(logo) != strings.Join(wordmark(140, 40, "show", false), "\n") {
		t.Fatal("color added extra geometry behind the lettering")
	}
}

// trimBlankBraille trims the wordmark's empty cells, which are U+2800 rather
// than ASCII spaces, along with any ordinary padding.
func trimBlankBraille(s string) string {
	return strings.Trim(s, " \u2800")
}

func TestWordmarkRowsUseBlankBrailleCells(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("LC_ALL", "en_US.UTF-8")
	t.Setenv("NO_COLOR", "")
	rows := wordmark(80, 24, "show", false)
	if len(rows) != 3 {
		t.Fatalf("static wordmark has %d rows, want 3: %q", len(rows), rows)
	}
	if ansi.StringWidth("\u2800") != 1 {
		t.Fatalf("blank Braille cell is %d columns wide, want 1", ansi.StringWidth("\u2800"))
	}
	want := ansi.StringWidth(rows[0])
	for y, row := range rows {
		// Padding after the logo is not part of it.
		row = strings.TrimRight(row, " ")
		if got := ansi.StringWidth(row); got != want {
			t.Fatalf("row %d is %d columns wide, row 0 is %d: %q", y, got, want, row)
		}
		for x, r := range row {
			if r < 0x2800 || r > 0x28ff {
				t.Fatalf("row %d column offset %d is %q, want a Braille cell: %q", y, x, r, row)
			}
		}
		if strings.Contains(row, " ") {
			t.Fatalf("row %d has an ASCII space inside the logo: %q", y, row)
		}
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

func TestWordmarkSpinsOneLetterAtATimeInPlace(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("LC_ALL", "en_US.UTF-8")
	static := wordmark(140, 40, "show", false)
	// Each letter's columns within the 27-column title, in order A2rena.
	spans := [][2]int{{0, 5}, {6, 11}, {12, 15}, {16, 19}, {20, 23}, {24, 27}}
	changed := func(rows []string) []int {
		letters := []int{}
		for i, span := range spans {
			for y, row := range rows {
				if string([]rune(row)[span[0]:span[1]]) != string([]rune(static[y])[span[0]:span[1]]) {
					letters = append(letters, i)
					break
				}
			}
		}
		return letters
	}
	order := []int{}
	for step := range wordmarkTurn * 7 {
		rows := spinningWordmark(140, 40, "show", false, (float64(step)+.5)*wordmarkStep)
		for y, row := range rows {
			if ansi.StringWidth(row) != ansi.StringWidth(static[y]) {
				t.Fatalf("step %d row %d changed width: %q", step, y, row)
			}
			for _, gap := range spans[1:] {
				if []rune(row)[gap[0]-1] != blankBraille {
					t.Fatalf("step %d spilled into the gap before column %d: %q", step, gap[0], row)
				}
			}
		}
		letters := changed(rows)
		if len(letters) > 1 {
			t.Fatalf("step %d turns several letters at once: %v", step, letters)
		}
		if len(letters) == 1 && (len(order) == 0 || order[len(order)-1] != letters[0]) {
			order = append(order, letters[0])
		}
		if step%wordmarkTurn == 0 && len(letters) != 0 {
			t.Fatalf("step %d does not start the turn facing front", step)
		}
	}
	if fmt.Sprint(order) != "[0 1 2 3 4 5 0]" {
		t.Fatalf("letters turned in order %v, want A2rena then A again", order)
	}
	if strings.Join(spinningWordmark(140, 40, "show", false, -1), "\n") != strings.Join(static, "\n") {
		t.Fatal("a negative clock is not the static wordmark")
	}
	if lines := spinningWordmark(140, 40, "compact", true, 1); len(lines) != 1 || ansi.Strip(lines[0]) != "A2rena" {
		t.Fatal("the text fallback spun")
	}
	if wordmarkClock(3, false, true) >= 0 || wordmarkClock(3, true, false) >= 0 || wordmarkClock(3, true, true) != 3 {
		t.Fatal("no-color or reduced motion did not keep the static wordmark")
	}
}

func TestLiveBandSpinsOnlyWithAnimation(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("LC_ALL", "en_US.UTF-8")
	t.Setenv("NO_COLOR", "")
	w, profiles, s := fixture(t)
	m := LiveModel{Run: Run{Workflow: w, Profiles: profiles, Settings: s}, Width: 160, Height: 40, ShaderTime: 2.5 * wordmarkStep}
	m.State = RunState{Status: "idle", Agents: map[string]string{}}
	head := func() string { return ansi.Strip(strings.Join(strings.Split(m.View().Content, "\n")[:3], "\n")) }
	static := ansi.Strip(strings.Join(wordmark(160, 40, "show", false), "\n"))
	if !strings.Contains(head(), "⣿") || strings.Contains(head(), strings.Split(static, "\n")[0]) {
		t.Fatal("animated band kept the static wordmark")
	}
	m.Run.Settings.Display.Animation = "off"
	if band := head(); !strings.Contains(band, strings.Split(static, "\n")[0]) {
		t.Fatalf("reduced motion still spins the wordmark:\n%s", band)
	}
}
