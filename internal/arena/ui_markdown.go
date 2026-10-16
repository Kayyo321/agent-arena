package arena

import (
	"hash/fnv"
	"strconv"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

// Markdown in the transcript is rendered by Arena itself. Provider text is
// stripped of escape sequences first, so only these styles reach the terminal.
const (
	sgrBold       = "1"
	sgrItalic     = "3"
	sgrUnderline  = "4"
	sgrStrike     = "9"
	sgrInlineCode = "38;5;215;48;5;236"
	sgrCodeBlock  = "38;5;252;48;5;235"
	sgrCodeLabel  = "38;5;244;48;5;235"
	sgrHeading1   = "1;4;38;5;141"
	sgrHeading2   = "1;38;5;75"
	sgrHeading3   = "1;38;5;110"
	sgrQuote      = "3;38;5;250"
	sgrQuoteBar   = "38;5;141"
	sgrBullet     = "38;5;141"
	sgrLink       = "4;38;5;75"
	sgrMuted      = "90"
	sgrTableRule  = "38;5;240"
)

// Transcript roles: a distinct color per speaker, with errors called out.
var chatRoleStyles = map[string]string{
	"You":                "1;38;5;81",
	"arena-orchestrator": "1;38;5;141",
	"Arena":              "1;38;5;214",
}

type mdSpan struct{ text, sgr string }

func styled(text, sgr string, color bool) string {
	if !color || sgr == "" || text == "" {
		return text
	}
	return "\x1b[" + sgr + "m" + text + "\x1b[0m"
}

func joinSGR(parts ...string) string {
	out := []string{}
	for _, part := range parts {
		if part != "" {
			out = append(out, part)
		}
	}
	return strings.Join(out, ";")
}

func sanitizeTranscript(text string) string {
	text = ansi.Strip(Redact(text))
	text = strings.ReplaceAll(strings.ReplaceAll(text, "\r\n", "\n"), "\r", "\n")
	return strings.ReplaceAll(text, "\t", "    ")
}

// chatRule is the bar that opens every message, so turns are easy to find.
func chatRule(role string, width int, color bool) string {
	heavy, light := "━", "─"
	if !unicodeLettering() {
		heavy, light = "=", "-"
	}
	line := light
	if role == "You" {
		line = heavy
	}
	label := " " + role + " "
	if width < displayWidth(label)+3 {
		return styled(short(role, width), chatRoleStyles[role], color)
	}
	sgr := chatRoleStyles[role]
	if sgr == "" {
		sgr = chatRoleStyles["Arena"]
	}
	return styled(line+line, sgrMuted, color) + styled(label, sgr, color) + styled(strings.Repeat(line, width-displayWidth(label)-2), sgrMuted, color)
}

// chatTranscript renders each message under its own colored rule. Agent text
// is Markdown; the user's own text and Arena notices stay literal.
func (m LiveModel) chatTranscript(width int, color bool) []string {
	lines := []string{}
	for _, line := range m.Chat {
		role := line.Role
		if role == "you" {
			role = "You"
		}
		lines = append(lines, chatRule(role, width, color))
		text := sanitizeTranscript(line.Text)
		switch {
		case role == "Arena" && strings.HasPrefix(text, "Request failed"):
			lines = append(lines, wrapSpans([]mdSpan{{text, "38;5;203"}}, width, nil, nil, color)...)
		case role == "Arena":
			lines = append(lines, wrapSpans([]mdSpan{{text, "38;5;223"}}, width, nil, nil, color)...)
		case role == "You":
			for _, row := range strings.Split(text, "\n") {
				lines = append(lines, wrapSpans([]mdSpan{{row, ""}}, width, nil, nil, color)...)
			}
		default:
			lines = append(lines, renderMarkdown(text, width, color)...)
		}
		lines = append(lines, "")
	}
	return lines
}

// renderMarkdown supports the block and inline forms agents actually use:
// headings, fenced code, quotes, lists, rules, tables and inline emphasis.
func renderMarkdown(text string, width int, color bool) []string {
	width = max(4, width)
	out := []string{}
	rows := strings.Split(text, "\n")
	for i := 0; i < len(rows); i++ {
		row := rows[i]
		trimmed := strings.TrimSpace(row)
		if fence, ok := codeFence(trimmed); ok {
			language := strings.TrimSpace(strings.TrimPrefix(trimmed, fence))
			code := []string{}
			for i++; i < len(rows) && !strings.HasPrefix(strings.TrimSpace(rows[i]), fence); i++ {
				code = append(code, rows[i])
			}
			out = append(out, codeBlock(code, language, width, color)...)
			continue
		}
		switch {
		case trimmed == "":
			out = append(out, "")
		case isRule(trimmed):
			rule := "─"
			if !unicodeLettering() {
				rule = "-"
			}
			out = append(out, styled(strings.Repeat(rule, width), sgrMuted, color))
		case strings.HasPrefix(trimmed, "#"):
			level := len(trimmed) - len(strings.TrimLeft(trimmed, "#"))
			title := strings.TrimSpace(trimmed[level:])
			if level > 6 || title == "" && level != len(trimmed) {
				out = append(out, wrapSpans(inlineSpans(row, ""), width, nil, nil, color)...)
				continue
			}
			sgr := map[int]string{1: sgrHeading1, 2: sgrHeading2}[level]
			if sgr == "" {
				sgr = sgrHeading3
			}
			if !color {
				title = strings.ToUpper(title)
			}
			out = append(out, wrapSpans(inlineSpans(title, sgr), width, nil, nil, color)...)
		case strings.HasPrefix(trimmed, ">"):
			bar := "▌ "
			if !unicodeLettering() {
				bar = "| "
			}
			prefix := []mdSpan{{bar, sgrQuoteBar}}
			out = append(out, wrapSpans(inlineSpans(strings.TrimSpace(strings.TrimPrefix(trimmed, ">")), sgrQuote), width, prefix, prefix, color)...)
		case strings.HasPrefix(trimmed, "|"):
			out = append(out, tableRow(trimmed, width, color))
		default:
			if marker, body, ok := listItem(row); ok {
				indent := strings.Repeat(" ", len(row)-len(strings.TrimLeft(row, " ")))
				first := []mdSpan{{indent, ""}, {marker, sgrBullet}}
				rest := []mdSpan{{indent + strings.Repeat(" ", displayWidth(marker)), ""}}
				out = append(out, wrapSpans(inlineSpans(body, ""), width, first, rest, color)...)
				continue
			}
			out = append(out, wrapSpans(inlineSpans(row, ""), width, nil, nil, color)...)
		}
	}
	return out
}

func codeFence(trimmed string) (string, bool) {
	for _, fence := range []string{"```", "~~~"} {
		if strings.HasPrefix(trimmed, fence) {
			return fence, true
		}
	}
	return "", false
}

func isRule(trimmed string) bool {
	if len(trimmed) < 3 {
		return false
	}
	compact := strings.ReplaceAll(trimmed, " ", "")
	for _, r := range []string{"-", "*", "_"} {
		if strings.Trim(compact, r) == "" && len(compact) >= 3 {
			return true
		}
	}
	return false
}

func listItem(row string) (string, string, bool) {
	trimmed := strings.TrimLeft(row, " ")
	for _, bullet := range []string{"- ", "* ", "+ "} {
		if strings.HasPrefix(trimmed, bullet) {
			body := trimmed[2:]
			mark := "• "
			if !unicodeLettering() {
				mark = "* "
			}
			switch {
			case strings.HasPrefix(body, "[ ] "):
				return mark + "☐ ", body[4:], true
			case strings.HasPrefix(body, "[x] "), strings.HasPrefix(body, "[X] "):
				return mark + "☑ ", body[4:], true
			}
			return mark, body, true
		}
	}
	digits := len(trimmed) - len(strings.TrimLeft(trimmed, "0123456789"))
	if digits > 0 && digits < 4 && len(trimmed) > digits+1 && (trimmed[digits] == '.' || trimmed[digits] == ')') && trimmed[digits+1] == ' ' {
		return trimmed[:digits+1] + " ", trimmed[digits+2:], true
	}
	return "", "", false
}

func codeBlock(code []string, language string, width int, color bool) []string {
	out := []string{}
	fill := func(text, sgr string) string {
		if !color {
			return "  " + text
		}
		return styled(" "+text+strings.Repeat(" ", max(0, width-1-displayWidth(text))), sgr, true)
	}
	if language != "" {
		out = append(out, fill(language, sgrCodeLabel))
	}
	for _, line := range code {
		wrapped := strings.Split(ansi.Hardwrap(line, max(1, width-2), true), "\n")
		for _, part := range wrapped {
			out = append(out, fill(part, sgrCodeBlock))
		}
	}
	if len(code) == 0 {
		out = append(out, fill("", sgrCodeBlock))
	}
	return out
}

func tableRow(trimmed string, width int, color bool) string {
	cells := strings.Split(strings.Trim(trimmed, "|"), "|")
	separator := true
	for _, cell := range cells {
		if strings.Trim(strings.TrimSpace(cell), ":-") != "" || strings.TrimSpace(cell) == "" {
			separator = false
		}
	}
	bar, rule := "│", "─"
	if !unicodeLettering() {
		bar, rule = "|", "-"
	}
	if separator {
		return styled(short(strings.Repeat(rule, displayWidth(trimmed)), width), sgrTableRule, color)
	}
	parts := []string{}
	for _, cell := range cells {
		parts = append(parts, renderSpans(inlineSpans(strings.TrimSpace(cell), ""), color))
	}
	return ansi.Truncate(strings.Join(parts, " "+styled(bar, sgrTableRule, color)+" "), width, "…")
}

// inlineSpans splits one line into styled runs. Emphasis markers toggle only
// when a closing marker exists, so arithmetic like "2 * 3" stays literal.
func inlineSpans(text, base string) []mdSpan {
	spans := []mdSpan{}
	var current strings.Builder
	bold, italic, strike := false, false, false
	style := func() string {
		s := []string{base}
		if bold {
			s = append(s, sgrBold)
		}
		if italic {
			s = append(s, sgrItalic)
		}
		if strike {
			s = append(s, sgrStrike)
		}
		return joinSGR(s...)
	}
	flush := func() {
		if current.Len() > 0 {
			spans = append(spans, mdSpan{current.String(), style()})
			current.Reset()
		}
	}
	for i := 0; i < len(text); {
		rest := text[i:]
		switch {
		case rest[0] == '`':
			if end := strings.IndexByte(rest[1:], '`'); end >= 0 {
				flush()
				// Non-breaking padding keeps the code background around the text.
				spans = append(spans, mdSpan{"\u00a0" + rest[1:1+end] + "\u00a0", joinSGR(sgrInlineCode)})
				i += end + 2
				continue
			}
		case rest[0] == '[':
			if close := strings.Index(rest, "]("); close > 0 {
				if end := strings.IndexByte(rest[close+2:], ')'); end >= 0 {
					flush()
					label, target := rest[1:close], rest[close+2:close+2+end]
					spans = append(spans, mdSpan{label, joinSGR(base, sgrLink)})
					if target != label {
						spans = append(spans, mdSpan{" (" + target + ")", sgrMuted})
					}
					i += close + 3 + end
					continue
				}
			}
		case strings.HasPrefix(rest, "**") || strings.HasPrefix(rest, "__"):
			marker := rest[:2]
			if bold || strings.Contains(rest[2:], marker) {
				flush()
				bold = !bold
				i += 2
				continue
			}
		case strings.HasPrefix(rest, "~~"):
			if strike || strings.Contains(rest[2:], "~~") {
				flush()
				strike = !strike
				i += 2
				continue
			}
		case rest[0] == '*' || rest[0] == '_':
			marker := rest[:1]
			wordStart := i == 0 || text[i-1] == ' ' || text[i-1] == '('
			opening := !italic && wordStart && len(rest) > 1 && rest[1] != ' ' && strings.Contains(rest[1:], marker)
			closing := italic && i > 0 && text[i-1] != ' '
			if opening || closing {
				flush()
				italic = !italic
				i++
				continue
			}
		}
		g := uniseg.NewGraphemes(rest)
		g.Next()
		current.WriteString(g.Str())
		i += len(g.Str())
	}
	flush()
	return spans
}

// renderSpans merges neighbouring runs that share a style before emitting.
func renderSpans(spans []mdSpan, color bool) string {
	merged := []mdSpan{}
	for _, span := range spans {
		if n := len(merged); n > 0 && merged[n-1].sgr == span.sgr {
			merged[n-1].text += span.text
		} else {
			merged = append(merged, span)
		}
	}
	var b strings.Builder
	for _, span := range merged {
		b.WriteString(styled(span.text, span.sgr, color))
	}
	return b.String()
}

// wrapSpans word-wraps styled runs. Each output line opens and closes its own
// styles, so a wrapped code span or link never bleeds into the next row.
func wrapSpans(spans []mdSpan, width int, first, rest []mdSpan, color bool) []string {
	type word struct {
		parts []mdSpan
		space bool
	}
	words := []word{}
	pending := word{}
	for _, span := range spans {
		for i, piece := range strings.Split(span.text, " ") {
			if i > 0 {
				if len(pending.parts) > 0 {
					words = append(words, pending)
				}
				pending = word{space: true}
			}
			if piece != "" {
				pending.parts = append(pending.parts, mdSpan{piece, span.sgr})
			}
		}
	}
	if len(pending.parts) > 0 {
		words = append(words, pending)
	}
	spanWidth := func(parts []mdSpan) int {
		w := 0
		for _, p := range parts {
			w += displayWidth(p.text)
		}
		return w
	}
	lines := []string{}
	prefix := first
	line := append([]mdSpan(nil), prefix...)
	used := spanWidth(prefix)
	empty := true
	emit := func() {
		lines = append(lines, renderSpans(line, color))
		prefix = rest
		line = append([]mdSpan(nil), prefix...)
		used = spanWidth(prefix)
		empty = true
	}
	for _, w := range words {
		ww := spanWidth(w.parts)
		gap := 0
		if !empty && w.space {
			gap = 1
		}
		if !empty && used+gap+ww > width {
			emit()
			gap = 0
		}
		if gap > 0 {
			// A space carries a style only inside one styled run.
			sgr := ""
			if previous := line[len(line)-1].sgr; previous == w.parts[0].sgr {
				sgr = previous
			}
			line = append(line, mdSpan{" ", sgr})
			used++
		}
		for _, part := range w.parts {
			// A word wider than the row is hard-wrapped by display cells.
			for used+displayWidth(part.text) > width && width > used {
				head := ansi.Truncate(part.text, width-used, "")
				if head == "" {
					break
				}
				line = append(line, mdSpan{head, part.sgr})
				part.text = part.text[len(head):]
				emit()
			}
			if part.text != "" {
				line = append(line, part)
				used += displayWidth(part.text)
			}
		}
		empty = false
	}
	if !empty || len(lines) == 0 {
		lines = append(lines, renderSpans(line, color))
	}
	return lines
}

// Unicode loading animations. A different set is picked every few seconds;
// the choice is a pure function of elapsed time so rendering stays stateless.
var spinnerSets = [][]string{
	{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"},
	{"◐", "◓", "◑", "◒"},
	{"▁", "▂", "▃", "▄", "▅", "▆", "▇", "█", "▇", "▆", "▅", "▄", "▃", "▂"},
	{"◢", "◣", "◤", "◥"},
	{"⣾", "⣽", "⣻", "⢿", "⡿", "⣟", "⣯", "⣷"},
	{"←", "↖", "↑", "↗", "→", "↘", "↓", "↙"},
	{"◜", "◝", "◞", "◟"},
	{"▖", "▘", "▝", "▗"},
	{"✶", "✸", "✹", "✺", "✹", "✷"},
	{"∙∙∙", "●∙∙", "∙●∙", "∙∙●"},
	{"▱▱▱▱", "▰▱▱▱", "▰▰▱▱", "▰▰▰▱", "▰▰▰▰", "▱▰▰▰", "▱▱▰▰", "▱▱▱▰"},
	{"☰", "☱", "☳", "☷", "☶", "☴"},
	{"⬒", "⬔", "⬓", "⬕"},
	{"♩", "♪", "♫", "♬"},
}

const spinnerPeriod = 4.0

func spinnerHash(epoch int) int {
	h := fnv.New32a()
	h.Write([]byte(strconv.Itoa(epoch)))
	return int(h.Sum32() % uint32(len(spinnerSets)))
}

// spinnerSet picks a pseudo-random set per period, never repeating the
// previous period's pick.
func spinnerSet(epoch int) int {
	index := spinnerHash(epoch)
	if epoch > 0 && index == spinnerHash(epoch-1) {
		index = (index + 1) % len(spinnerSets)
	}
	return index
}

// spinner returns the current loading glyph, or a static mark when animation
// is off or the terminal cannot show Unicode.
func (m LiveModel) spinner(color bool) string {
	if !m.shaderAnimated() || !unicodeLettering() {
		return "…"
	}
	epoch := int(m.ShaderTime / spinnerPeriod)
	frames := spinnerSets[spinnerSet(epoch)]
	frame := frames[int(m.ShaderTime*8)%len(frames)]
	return styled(frame, sgrBullet, color)
}
