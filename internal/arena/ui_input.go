package arena

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// Byte positions always sit on grapheme boundaries, so accents and emoji edit
// as one character. History is session-local and never written to a new log.
type promptEditor struct {
	value       string
	cursor      int
	history     []string
	recalled    int
	draft       string
	draftCursor int
}

func displayWidth(s string) int { return ansi.StringWidth(s) }

func (m *LiveModel) syncPrompt() {
	if m.input.value != m.Prompt {
		m.input.value, m.input.cursor = m.Prompt, len(m.Prompt)
	}
}

func (m *LiveModel) setPrompt(text string, cursor int) {
	cursor = max(0, min(cursor, len(text)))
	if cursor > 0 {
		cursor = nextCharacter(text, previousCharacter(text, cursor))
	}
	m.Prompt, m.input.value, m.input.cursor = text, text, cursor
}

func previousCharacter(s string, pos int) int {
	previous := 0
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		start, end := g.Positions()
		if end >= pos {
			return start
		}
		previous = end
	}
	return previous
}

func nextCharacter(s string, pos int) int {
	g := uniseg.NewGraphemes(s)
	for g.Next() {
		_, end := g.Positions()
		if end > pos {
			return end
		}
	}
	return len(s)
}

func (m *LiveModel) wordBoundary(direction int) int {
	p := m.input.cursor
	if direction < 0 {
		for p > 0 && strings.TrimSpace(m.Prompt[previousCharacter(m.Prompt, p):p]) == "" {
			p = previousCharacter(m.Prompt, p)
		}
		for p > 0 && strings.TrimSpace(m.Prompt[previousCharacter(m.Prompt, p):p]) != "" {
			p = previousCharacter(m.Prompt, p)
		}
	} else {
		for p < len(m.Prompt) && strings.TrimSpace(m.Prompt[p:nextCharacter(m.Prompt, p)]) == "" {
			p = nextCharacter(m.Prompt, p)
		}
		for p < len(m.Prompt) && strings.TrimSpace(m.Prompt[p:nextCharacter(m.Prompt, p)]) != "" {
			p = nextCharacter(m.Prompt, p)
		}
	}
	return p
}

func (m *LiveModel) insertPrompt(text string) {
	m.syncPrompt()
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	text = strings.ReplaceAll(ansi.Strip(text), "\t", "    ")
	text = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) && r != '\n' {
			return -1
		}
		return r
	}, text)
	p := m.input.cursor
	m.setPrompt(m.Prompt[:p]+text+m.Prompt[p:], p+len(text))

}

func (m *LiveModel) deletePrompt(start, end int) {
	m.setPrompt(m.Prompt[:start]+m.Prompt[end:], start)
}

func (m *LiveModel) recallPrompt(direction int) {
	m.syncPrompt()
	if len(m.input.history) == 0 {
		return
	}
	if m.input.recalled == 0 {
		m.input.draft, m.input.draftCursor = m.Prompt, m.input.cursor
	}
	m.input.recalled = max(0, min(len(m.input.history), m.input.recalled+direction))
	if m.input.recalled == 0 {
		m.setPrompt(m.input.draft, m.input.draftCursor)
	} else {
		text := m.input.history[len(m.input.history)-m.input.recalled]
		m.setPrompt(text, len(text))
	}
}

func (m *LiveModel) rememberPrompt(text string) {
	if strings.TrimSpace(text) != "" && (len(m.input.history) == 0 || m.input.history[len(m.input.history)-1] != text) {
		m.input.history = append(m.input.history, text)
		if len(m.input.history) > 100 {
			m.input.history = m.input.history[1:]
		}
	}
	m.input.recalled, m.input.draft, m.input.draftCursor = 0, "", 0
}

// Wrap the composer independently from the body and follow its caret. A spare
// cell at the right keeps a trailing cursor inside the terminal after resize.
func (m LiveModel) promptTextView(width, height int, color bool) ([]string, int, int) {
	m.syncPrompt()
	if m.Prompt == "" {
		return []string{"> " + muted(short("memento mori", width-2), color)}, 2, 0
	}
	available := max(1, width-3)
	rows := []string{""}
	x, y, cursorX, cursorY := 0, 0, 0, 0
	g := uniseg.NewGraphemes(m.Prompt)
	for g.Next() {
		start, end := g.Positions()
		text := g.Str()
		w := displayWidth(text)
		if text != "\n" && x+w > available {
			rows = append(rows, "")
			x, y = 0, y+1
		}
		if start == m.input.cursor {
			cursorX, cursorY = x, y
		}
		if text == "\n" {
			rows = append(rows, "")
			x, y = 0, y+1
		} else {
			rows[y] += text
			x += w
		}
		if end == m.input.cursor {
			cursorX, cursorY = x, y
		}
	}
	limit := max(1, min(5, height/4))
	start := max(0, cursorY-limit+1)
	end := min(len(rows), start+limit)
	visible := []string{}
	for i := start; i < end; i++ {
		prefix := "  "
		if i == 0 {
			prefix = "> "
		}
		if i == start && start > 0 {
			prefix = ": "
		}
		visible = append(visible, prefix+rows[i])
	}
	return visible, min(width-1, cursorX+2), cursorY - start
}

func (m LiveModel) promptView(width, height int, color bool) ([]string, int, int) {
	rows, x, y := m.promptTextView(width, height, color)
	if len(m.Attachments) > 0 && height > 5 {
		rows = append([]string{muted(short(m.attachmentSummary(), width), color)}, rows...)
		y++
	}
	return rows, x, y
}

// Move through visual lines at the current terminal width, preserving graphemes.
func (m *LiveModel) movePromptVertical(direction int) {
	type caret struct{ pos, x, y int }
	points := []caret{{}}
	x, y, currentX, currentY := 0, 0, 0, 0
	width := max(1, max(8, m.Width)-3)
	g := uniseg.NewGraphemes(m.Prompt)
	for g.Next() {
		start, end := g.Positions()
		text := g.Str()
		w := displayWidth(text)
		if text != "\n" && x+w > width {
			x, y = 0, y+1
			points[len(points)-1] = caret{start, x, y}
		}
		if text == "\n" {
			x, y = 0, y+1
		} else {
			x += w
		}
		points = append(points, caret{end, x, y})
	}
	for _, p := range points {
		if p.pos == m.input.cursor {
			currentX, currentY = p.x, p.y
			break
		}
	}
	target, best, distance := currentY+direction, m.input.cursor, 1<<30
	for _, p := range points {
		if p.y != target {
			continue
		}
		d := p.x - currentX
		if d < 0 {
			d = -d
		}
		if d < distance {
			best, distance = p.pos, d
		}
	}
	m.input.cursor = best
}
