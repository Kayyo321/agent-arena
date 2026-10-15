package arena

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

const sampleMarkdown = "# Plan\n## Agents\nUse **bold**, *italic*, ~~old~~ and `inline code` with [docs](https://example.com).\n- builder writes the `ledger` package and keeps going on a long line that must wrap neatly under the bullet\n  - nested item\n1. first step\n> quoted reviewer note\n---\n```python\nprint(\"hi\")\n```\n| agent | effort |\n|---|---|\n| lead | low |\nMath like 2 * 3 * 4 stays literal."

func TestMarkdownRendersStylesWithinWidth(t *testing.T) {
	for _, width := range []int{20, 44, 100} {
		lines := renderMarkdown(sampleMarkdown, width, true)
		for _, line := range lines {
			if w := ansi.StringWidth(line); w > width {
				t.Fatalf("width %d exceeded (%d): %q", width, w, line)
			}
		}
		if testing.Verbose() && width == 44 {
			t.Log("\n" + strings.Join(lines, "\n"))
		}
	}
	text := strings.Join(renderMarkdown(sampleMarkdown, 100, true), "\n")
	for _, want := range []string{"\x1b[" + sgrHeading1 + "mPlan", "\x1b[" + sgrInlineCode + "m\u00a0inline code\u00a0", "\x1b[" + sgrBold + "mbold", "\x1b[" + sgrItalic + "mitalic", "\x1b[" + sgrBullet + "m• ", "\x1b[" + joinSGR(sgrLink) + "mdocs"} {
		if !strings.Contains(text, want) {
			t.Fatalf("missing %q in\n%s", want, text)
		}
	}
	plain := strings.Join(renderMarkdown(sampleMarkdown, 100, false), "\n")
	if strings.Contains(plain, "\x1b[") || !strings.Contains(plain, "2 * 3 * 4") || strings.Contains(plain, "**") {
		t.Fatalf("plain rendering:\n%s", plain)
	}
}

func TestTranscriptStripsProviderEscapesAndSeparatesTurns(t *testing.T) {
	m := LiveModel{Chat: []chatLine{{"you", "build it"}, {"arena-orchestrator", "\x1b[31mred\x1b[0m **ok**"}, {"Arena", "Request failed: boom"}}}
	lines := m.chatTranscript(40, true)
	text := strings.Join(lines, "\n")
	if strings.Contains(text, "\x1b[31m") {
		t.Fatal("provider escape sequence reached the terminal")
	}
	rules := 0
	for _, line := range lines {
		if strings.Contains(ansi.Strip(line), "━━ You ") || strings.Contains(ansi.Strip(line), "── arena-orchestrator ") || strings.Contains(ansi.Strip(line), "── Arena ") {
			rules++
		}
		if ansi.StringWidth(line) > 40 {
			t.Fatalf("rule too wide: %q", ansi.Strip(line))
		}
	}
	if rules != 3 {
		t.Fatalf("expected a rule per message, got %d:\n%s", rules, ansi.Strip(text))
	}
	if !strings.Contains(text, "\x1b[38;5;203mRequest failed") {
		t.Fatal("failure notice not highlighted")
	}
}

func TestSpinnerRotatesSetsEveryPeriod(t *testing.T) {
	seen := map[int]bool{}
	for epoch := 0; epoch < 40; epoch++ {
		set := spinnerSet(epoch)
		if epoch > 0 && set == spinnerSet(epoch-1) {
			t.Fatalf("set repeated at epoch %d", epoch)
		}
		seen[set] = true
	}
	if len(seen) < len(spinnerSets)/2 {
		t.Fatalf("too few sets used: %d", len(seen))
	}
	m := LiveModel{Run: Run{Settings: DefaultSettings(Runtime{})}}
	m.ShaderTime = 1
	first := m.spinner(false)
	m.ShaderTime = 1 + spinnerPeriod
	if m.spinner(false) == "" || first == "" {
		t.Fatal("empty spinner frame")
	}
}
