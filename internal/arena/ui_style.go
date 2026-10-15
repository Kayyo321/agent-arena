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

// Graph and status-band palette (xterm-256). Each node kind owns a hue with a
// dim, normal and bright step: dim while waiting, bright while working.
const (
	sgrBrand     = "38;5;141"
	sgrHairline  = "38;5;237"
	sgrEdge      = "38;5;240"
	sgrEdgeLabel = "38;5;245"
	sgrLoop      = "38;5;179"
	sgrPacket    = "1;38;5;117"
	sgrDim       = "38;5;244"
	sgrLabel     = "38;5;242"
	sgrSoft      = "38;5;250"
	sgrStrong    = "1;38;5;255"
	sgrAdd       = "38;5;114"
	sgrDel       = "38;5;174"
	sgrRunning   = "38;5;117"
	sgrQueued    = "38;5;179"
	sgrSucceeded = "38;5;114"
	sgrFailed    = "38;5;203"
	sgrAttention = "38;5;214"
)

type nodeKind struct {
	// Frame glyphs: corners, horizontal, vertical, and the junctions an edge
	// makes where it leaves through the bottom or top border.
	tl, tr, bl, br, h, v, down, up string
	dim, normal, bright            string
	badge                          string
}

// Head agents are heavy, workers are rounded, teams are dashed groups and
// routers are chamfered decision blocks, so shape alone tells them apart.
func nodeKinds(kind string) nodeKind {
	ascii := !unicodeLettering()
	switch kind {
	case "head":
		if ascii {
			return nodeKind{"#", "#", "#", "#", "=", "#", "+", "+", "38;5;97", sgrBrand, "38;5;183", "* HEAD"}
		}
		return nodeKind{"┏", "┓", "┗", "┛", "━", "┃", "┯", "┷", "38;5;97", sgrBrand, "38;5;183", "◆ HEAD"}
	case "team":
		if ascii {
			return nodeKind{"+", "+", "+", "+", ".", ":", "+", "+", "38;5;136", "38;5;179", "38;5;222", "TEAM"}
		}
		return nodeKind{"╭", "╮", "╰", "╯", "┄", "┆", "┬", "┴", "38;5;136", "38;5;179", "38;5;222", "◈ TEAM"}
	case "router":
		if ascii {
			return nodeKind{"/", "\\", "\\", "/", "-", "|", "+", "+", "38;5;30", "38;5;73", "38;5;116", "<> ROUTER"}
		}
		return nodeKind{"╱", "╲", "╲", "╱", "─", "│", "┬", "┴", "38;5;30", "38;5;73", "38;5;116", "◇ ROUTER"}
	}
	if ascii {
		return nodeKind{"+", "+", "+", "+", "-", "|", "+", "+", "38;5;240", "38;5;246", "38;5;252", ""}
	}
	return nodeKind{"╭", "╮", "╰", "╯", "─", "│", "┬", "┴", "38;5;240", "38;5;246", "38;5;252", ""}
}

// statusLook maps a node state to its glyph and color. Running work shows the
// shared loading animation instead of a fixed glyph.
func statusLook(status, spin string) (string, string) {
	ascii := !unicodeLettering()
	pick := func(u, a string) string {
		if ascii {
			return a
		}
		return u
	}
	switch status {
	case "running", "deliberating":
		if spin == "" {
			spin = pick("●", "*")
		}
		return spin, sgrRunning
	case "queued":
		return pick("◌", "~"), sgrQueued
	case "succeeded", "consensus", "selected":
		return pick("✓", "+"), sgrSucceeded
	case "failed", "no consensus":
		return pick("✗", "x"), sgrFailed
	case "attention":
		return pick("⚑", "!"), sgrAttention
	case "cancelled":
		return pick("⊘", "-"), sgrDim
	case "interrupted", "paused":
		return pick("‖", "|"), sgrQueued
	}
	return pick("○", "o"), sgrDim
}

// Agent identity colors are soft so they read as labels, not alarms.
var agentPalette = []int{117, 150, 223, 183, 116, 216, 152, 181}

// kindCard draws a single-column card in its kind's frame for narrow layouts.
func kindCard(kind, id, title string, rows []string, width int, color bool) []string {
	k := nodeKinds(kind)
	frame := func(text string) string { return styled(text, k.normal, color) }
	if kind == "agent" {
		frame = func(text string) string { return styled(text, k.dim, color) }
	}
	inside := max(0, width-2)
	label := " " + short(title, max(0, inside-4)) + " "
	top := frame(k.tl+k.h) + styled(label, "1;"+agentSGR(id), color) + frame(strings.Repeat(k.h, max(0, inside-1-displayWidth(label)))+k.tr)
	lines := []string{top}
	for _, row := range rows {
		lines = append(lines, frame(k.v)+" "+pad(row, max(0, width-4))+" "+frame(k.v))
	}
	return append(lines, frame(k.bl+strings.Repeat(k.h, inside)+k.br))
}
