package arena

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

func TestCompactPurpleWordmarkSitsAtTopRight(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("LC_ALL", "en_US.UTF-8")
	t.Setenv("NO_COLOR", "")
	w, profiles, s := fixture(t)
	m := LiveModel{Run: Run{Workflow: w, Profiles: profiles, Settings: s, BaseCommit: "12345678"}, Width: 140, Height: 40}
	view := m.View().Content
	lines := strings.Split(view, "\n")
	if !strings.HasPrefix(ansi.Strip(lines[0]), "╭") || !strings.Contains(view, "git trees") {
		t.Fatal("Git tree not retained on the left")
	}
	for _, line := range lines[:3] {
		plain := []rune(ansi.Strip(line))
		if len(plain) != 140 || !strings.ContainsAny(string(plain[113:]), "⣿⠛⣤") {
			t.Fatalf("wordmark not confined to the top-right 27 columns: %q", ansi.Strip(line))
		}
	}
	for _, ink := range []string{"38;5;183", "38;5;141", "38;5;99"} {
		if !strings.Contains(view, ink) {
			t.Fatalf("missing purple gradient %s", ink)
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
	// Remaining graph/command space survives the three-row logo.
	if !strings.Contains(view, "HEAD") || !strings.Contains(ansi.Strip(view), "> memento mori") {
		t.Fatal("branding displaced the graph or prompt")
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
		if len(lines) != 1 || !strings.HasSuffix(lines[0], "A2rena") || ansi.StringWidth(lines[0]) != size[0] {
			t.Fatalf("compact fallback not at right edge: %v %q", size, lines)
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
