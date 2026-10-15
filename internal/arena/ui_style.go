package arena

import "strings"

// One quiet set of borders across the graph, Git tree and detail windows.
// Geometry is identical with and without color; legacy terminals keep ASCII.
type uiBorder struct{ topLeft, topRight, bottomLeft, bottomRight, horizontal, vertical string }

func borders() uiBorder {
	if !unicodeLettering() {
		return uiBorder{"+", "+", "+", "+", "-", "|"}
	}
	return uiBorder{"╭", "╮", "╰", "╯", "─", "│"}
}

func (b uiBorder) top(width int, title string) string {
	inside := strings.Repeat(b.horizontal, max(0, width-2))
	if title != "" && width > 6 {
		label := " " + short(title, width-6) + " "
		inside = b.horizontal + label + strings.Repeat(b.horizontal, max(0, width-3-displayWidth(label)))
	}
	return b.topLeft + inside + b.topRight
}

func (b uiBorder) bottom(width int) string {
	return b.bottomLeft + strings.Repeat(b.horizontal, max(0, width-2)) + b.bottomRight
}

func (b uiBorder) row(text string, width int) string {
	return b.vertical + " " + pad(text, max(0, width-4)) + " " + b.vertical
}

func muted(text string, color bool) string {
	if color {
		return "\x1b[90m" + text + "\x1b[0m"
	}
	return text
}

func promptRule(width int, color bool) string {
	line := borders().horizontal
	label := " /help "
	if width < displayWidth(label)+1 {
		return muted(strings.Repeat(line, width), color)
	}
	return muted(line+label+strings.Repeat(line, width-displayWidth(label)-1), color)
}
