package arena

import (
	"fmt"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

type graphCard struct {
	id, first, second, status string
	x, y                      int
}
type graphEdge struct {
	from, to, label string
	peer, both      bool
	// loop marks a capped return edge from a router back to an earlier node.
	loop bool
}

// Graph positions are a projection of the validated graph; layout never schedules.
// spin is the current loading glyph shown on cards that are doing work.
func wideGraph(w Workflow, state RunState, events []Event, width int, color bool, spin string, frames ...int) []string {
	frame := -1
	if len(frames) > 0 {
		frame = frames[0]
	}
	cards := map[string]*graphCard{}
	members := map[string]bool{}
	ranks := map[string]int{}
	edges := []graphEdge{}
	for _, t := range w.Teams {
		for _, id := range t.Members {
			members[id] = true
		}
		level := 0
		for _, a := range w.Agents {
			if a.Id == t.Members[0] && a.Level != nil {
				level = *a.Level
			}
		}
		ranks[t.Id] = level * 100
		status := "waiting"
		for _, v := range events {
			if v.AgentID == t.Id && strings.HasPrefix(v.Type, "team.") {
				status = v.Type
			}
		}
		if Has([]string{"team.started", "team.round", "team.affirmations"}, status) {
			status = withSpinner(spin, "deliberating · "+strings.TrimPrefix(status, "team."))
		}
		cards[t.Id] = &graphCard{id: t.Id, first: "TEAM · " + t.Name, second: fmt.Sprintf("unanimous · max %d rounds", t.Consensus.MaxRounds), status: status}
	}
	for _, a := range w.Agents {
		if a.Activation == "on-call" || members[a.Id] {
			continue
		}
		ranks[a.Id] = *a.Level * 100
		first := a.Id + " · " + a.Runtime.Provider
		if a.Id == w.HeadAgent {
			first = "HEAD · " + a.Runtime.Provider + " · " + a.Id
		}
		second := a.Runtime.Model
		if a.Runtime.ReasoningEffort != "" {
			second += " · " + a.Runtime.ReasoningEffort
		}
		status := state.Agents[a.Id]
		if status == "" {
			status = "waiting"
		}
		if status == "running" || status == "queued" {
			status = withSpinner(spin, status)
		}
		if n := state.Iterations[a.Id]; n > 1 {
			status += fmt.Sprintf(" · pass %d", n)
		}
		cards[a.Id] = &graphCard{id: a.Id, first: first, second: second, status: status}
	}
	for _, r := range w.DecisionRouters {
		status := "waiting"
		for _, v := range events {
			switch {
			case v.AgentID == r.Id && v.Type == "router.selected":
				status = str(v.Data["branch"]) + " -> " + str(v.Data["target"])
			case v.AgentID == r.Id && v.Type == "decision.requested":
				status = "⚑ waiting for you · see prompt"
			}
		}
		cards[r.Id] = &graphCard{id: r.Id, first: "ROUTER · " + r.Id, second: r.InputKind + " " + r.PayloadPointer, status: status}
		ranks[r.Id] = -1
		for _, branch := range r.Routes {
			if LoopRoute(branch) && branch.MaxIterations != nil {
				used := 0
				for _, v := range events {
					if v.Type == "loop.iteration" && str(v.Data["routerId"]) == r.Id && str(v.Data["target"]) == branch.To {
						used++
					}
				}
				edges = append(edges, graphEdge{from: r.Id, to: branch.To, label: fmt.Sprintf("%d/%d\x00=%s", used, *branch.MaxIterations, strings.TrimSpace(string(JSON(branch.Equals)))), loop: true})
				continue
			}
			edges = append(edges, graphEdge{from: r.Id, to: branch.To, label: "=" + strings.TrimSpace(string(JSON(branch.Equals)))})
		}
		if r.Fallback.Action == "route" {
			edges = append(edges, graphEdge{from: r.Id, to: r.Fallback.To, label: "fallback"})
		}
	}
	for _, edge := range w.Relationships {
		edges = append(edges, graphEdge{from: edge.From, to: edge.To, label: edge.Type, peer: edge.Type == "communicates", both: edge.Direction == "two-way"})
	}
	for n := 0; n < len(cards); n++ {
		changed := false
		for _, edge := range edges {
			if !edge.peer && !edge.loop && ranks[edge.from] >= 0 && ranks[edge.to] < 0 {
				ranks[edge.to] = ranks[edge.from] + 1
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	groups := map[int][]string{}
	for id := range cards {
		groups[ranks[id]] = append(groups[ranks[id]], id)
	}
	levels := []int{}
	for rank := range groups {
		levels = append(levels, rank)
	}
	sort.Ints(levels)
	cardWidth := min(36, (width-8)/2)
	cols := max(1, width/(cardWidth+5))
	y := 0
	for _, rank := range levels {
		ids := groups[rank]
		sort.Strings(ids)
		for start := 0; start < len(ids); start += cols {
			end := min(len(ids), start+cols)
			count := end - start
			left := (width - (count*cardWidth + (count-1)*5)) / 2
			for i, id := range ids[start:end] {
				cards[id].x = left + i*(cardWidth+5)
				cards[id].y = y
			}
			y += 8
		}
	}
	height := max(5, y-3)
	canvas := make([][]string, height)
	for i := range canvas {
		canvas[i] = make([]string, width)
		for x := range canvas[i] {
			canvas[i][x] = " "
		}
	}
	put := func(x, y int, s string) {
		if y < 0 || y >= len(canvas) {
			return
		}
		graphemes := uniseg.NewGraphemes(s)
		for graphemes.Next() {
			text := graphemes.Str()
			cells := ansi.StringWidth(text)
			if cells == 0 {
				continue
			}
			if x >= 0 && x+cells <= width {
				canvas[y][x] = text
				for next := 1; next < cells; next++ {
					canvas[y][x+next] = ""
				}
			}
			x += cells
		}
	}
	hline := func(y, x1, x2 int, peer bool) {
		if x1 > x2 {
			x1, x2 = x2, x1
		}
		line := borders().horizontal
		if peer {
			line = "."
		}
		for x := x1; x <= x2; x++ {
			put(x, y, line)
		}
	}
	// Edge lines are collected as per-cell connection masks and drawn once,
	// so a branch leaving a shared stem renders as a junction (├) instead of
	// a corner that hides the stem.
	type cell struct{ x, y int }
	const (
		up = 1 << iota
		down
		left
		right
	)
	masks := map[cell]int{}
	connect := func(a, b cell) {
		for a != b {
			next := a
			switch {
			case next.x < b.x:
				next.x++
				masks[a] |= right
				masks[next] |= left
			case next.x > b.x:
				next.x--
				masks[a] |= left
				masks[next] |= right
			case next.y < b.y:
				next.y++
				masks[a] |= down
				masks[next] |= up
			default:
				next.y--
				masks[a] |= up
				masks[next] |= down
			}
			a = next
		}
	}
	type label struct {
		x, y int
		text string
		dots bool
		x2   int
	}
	labels := []label{}
	type point struct{ x, y int }
	paths := map[string][]point{}
	recordPath := func(from, to string, corners ...point) {
		path := []point{}
		for i, p := range corners {
			if i == 0 {
				path = append(path, p)
				continue
			}
			last := path[len(path)-1]
			for last != p {
				if last.x != p.x {
					if last.x < p.x {
						last.x++
					} else {
						last.x--
					}
				} else if last.y < p.y {
					last.y++
				} else {
					last.y--
				}
				path = append(path, last)
			}
		}
		paths[from+"/"+to] = path
		reverse := make([]point, len(path))
		for i := range path {
			reverse[len(path)-1-i] = path[i]
		}
		paths[to+"/"+from] = reverse
	}
	type joint struct {
		x, y           int
		unicode, ascii string
	}
	joints := []joint{}
	gutters := 0
	for i, edge := range edges {
		from, to := cards[edge.from], cards[edge.to]
		if from == nil || to == nil {
			continue
		}
		if edge.loop {
			// A return edge leaves the router's side, climbs a gutter beside
			// the cards and re-enters its target from the side, so it never
			// shares the downward lanes that carry ordinary delegation. It
			// takes the side where both ends are outermost in their rows and
			// climbs past every card between them.
			top, bottom := min(from.y, to.y), max(from.y, to.y)
			right, left := 0, width
			rightClear, leftClear := true, true
			for _, card := range cards {
				if card.y < top || card.y > bottom {
					continue
				}
				right, left = max(right, card.x+cardWidth+1+gutters), min(left, card.x-2-gutters)
				for _, end := range []*graphCard{from, to} {
					if card.y == end.y && card.x > end.x {
						rightClear = false
					}
					if card.y == end.y && card.x < end.x {
						leftClear = false
					}
				}
			}
			gutters++
			gutter, x1, x2, tail, head, tailUnicode, headUnicode, headASCII := right, from.x+cardWidth, to.x+cardWidth, from.x+cardWidth-1, to.x+cardWidth, "├", "◀", "<"
			if right >= width || !rightClear && leftClear && left >= 0 {
				gutter, x1, x2, tail, head, tailUnicode, headUnicode, headASCII = left, from.x-1, to.x-1, from.x, to.x-1, "┤", "▶", ">"
			}
			if gutter < 0 || gutter >= width {
				continue
			}
			y1, y2 := from.y+2, to.y+2
			connect(cell{x1, y1}, cell{gutter, y1})
			connect(cell{gutter, y1}, cell{gutter, y2})
			connect(cell{gutter, y2}, cell{x2, y2})
			joints = append(joints, joint{tail, y1, tailUnicode, "+"}, joint{head, y2, headUnicode, headASCII})
			recordPath(edge.from, edge.to, point{x1, y1}, point{gutter, y1}, point{gutter, y2}, point{x2, y2})
			// The count always shows; the matched value follows when it fits.
			count, match, _ := strings.Cut(edge.label, "\x00")
			text := "↺ " + count
			if !unicodeLettering() {
				text = "loop " + count
			}
			ly := (y1 + y2) / 2
			if gutter > max(from.x, to.x) {
				if room := width - gutter - 2; ansi.StringWidth(text+" "+match) <= room {
					text += " " + match
				}
				labels = append(labels, label{x: min(gutter+2, max(0, width-ansi.StringWidth(text))), y: ly, text: text})
			} else {
				if ansi.StringWidth(text+" "+match) <= gutter-1 {
					text += " " + match
				}
				labels = append(labels, label{x: max(0, gutter-1-ansi.StringWidth(text)), y: ly, text: text})
			}
			continue
		}
		x1, x2 := from.x+cardWidth/2, to.x+cardWidth/2
		y1, y2 := from.y+5, to.y-1
		if edge.peer && from.y == to.y {
			y1 = from.y + 2
			x1 = from.x + cardWidth
			x2 = to.x - 1
			if x1 > x2 {
				x1 = from.x - 1
				x2 = to.x + cardWidth
			}
			hline(y1, x1, x2, true)
			arrow := ">"
			if x1 > x2 {
				arrow = "<"
			}
			put(x2, y1, arrow)
			if edge.both {
				if arrow == ">" {
					put(x1, y1, "<")
				} else {
					put(x1, y1, ">")
				}
			}
			recordPath(edge.from, edge.to, point{x1, y1}, point{x2, y1})
			continue
		}
		// Lanes stay above the arrowhead row so turns and labels never collide.
		lane := y1 + (i % 2)
		stub := up
		if y2 < y1 {
			y1 = from.y - 1
			y2 = to.y + 5
			lane = max(0, y1-1)
			stub = down
		}
		masks[cell{x1, y1}] |= stub
		connect(cell{x1, y1}, cell{x1, lane})
		connect(cell{x1, lane}, cell{x2, lane})
		connect(cell{x2, lane}, cell{x2, y2})
		// Heads and tails are drawn after the cards so they join the borders.
		if to.y < from.y {
			joints = append(joints, joint{x1, from.y, "┴", "+"}, joint{x2, y2, "▲", "^"})
		} else {
			joints = append(joints, joint{x1, from.y + 4, "┬", "+"}, joint{x2, y2, "▼", "v"})
		}
		recordPath(edge.from, edge.to, point{x1, y1}, point{x1, lane}, point{x2, lane}, point{x2, y2})
		if x1 != x2 {
			labels = append(labels, label{x: min(x1, x2) + 1, y: lane, text: short(edge.label, max(0, abs(x2-x1)-2)), dots: edge.peer, x2: max(x1, x2) - 1})
		} else {
			// A straight edge is labelled beside its own arrowhead, away from
			// the lanes other branches use.
			labels = append(labels, label{x: x2 + 2, y: y2, text: short(edge.label, max(0, cardWidth/2-3))})
		}
	}
	glyphs := map[int]string{up | down: "│", left | right: "─", down | right: "╭", down | left: "╮", up | right: "╰", up | left: "╯", up | down | right: "├", up | down | left: "┤", left | right | down: "┬", left | right | up: "┴", up | down | left | right: "┼", up: "│", down: "│", left: "─", right: "─"}
	for c, mask := range masks {
		glyph := glyphs[mask]
		if !unicodeLettering() {
			switch mask {
			case up | down, up, down:
				glyph = "|"
			case left | right, left, right:
				glyph = "-"
			default:
				glyph = "+"
			}
		}
		put(c.x, c.y, glyph)
	}
	for _, l := range labels {
		if l.dots {
			for x := l.x; x <= l.x2; x++ {
				put(x, l.y, ".")
			}
		}
		put(l.x, l.y, l.text)
	}
	// Only unacknowledged packets move. The route comes from the graph projection;
	// packet bodies never enter the viewport.
	aliases := map[string]string{}
	for _, team := range w.Teams {
		for _, id := range team.Members {
			aliases[id] = team.Id
		}
	}
	for _, packet := range packetStates(events) {
		if packet.status != "pending" {
			continue
		}
		from, to := packet.from, packet.to
		if aliases[from] != "" {
			from = aliases[from]
		}
		if aliases[to] != "" {
			to = aliases[to]
		}
		path := paths[from+"/"+to]
		if len(path) == 0 {
			continue
		}
		index := len(path) / 2
		if frame >= 0 {
			index = frame % len(path)
		}
		p := path[index]
		put(max(0, min(width-3, p.x-1)), p.y, packet.marker())
	}
	for _, card := range cards {
		b := borders()
		put(card.x, card.y, b.top(cardWidth, ""))
		put(card.x, card.y+1, b.row(card.first, cardWidth))
		put(card.x, card.y+2, b.row(card.second, cardWidth))
		put(card.x, card.y+3, b.row(card.status, cardWidth))
		put(card.x, card.y+4, b.bottom(cardWidth))
	}
	for _, j := range joints {
		if unicodeLettering() {
			put(j.x, j.y, j.unicode)
		} else {
			put(j.x, j.y, j.ascii)
		}
	}
	lines := []string{}
	for y, line := range canvas {
		text := strings.Join(line, "")
		if color {
			owned := []*graphCard{}
			for _, card := range cards {
				if y == card.y+1 {
					owned = append(owned, card)
				}
			}
			sort.Slice(owned, func(i, j int) bool { return owned[i].x > owned[j].x })
			for _, card := range owned {
				start, end := card.x+2, card.x+cardWidth-2
				text = ansi.Cut(text, 0, start) + agentColor(card.id, ansi.Cut(text, start, end), true) + ansi.Cut(text, end, width)
			}
		}
		lines = append(lines, strings.TrimRight(text, " "))
	}
	legend := "▼ delegates · dotted peer links · = structured branch · Ctrl+E events"
	if !unicodeLettering() {
		legend = "v delegates · dotted peer links · = structured branch · Ctrl+E events"
	}
	for _, edge := range edges {
		if edge.loop {
			loop := " · ↺ capped loop · Ctrl+E"
			if !unicodeLettering() {
				loop = " · loop: capped return · Ctrl+E"
			}
			if longer := strings.Replace(legend, " · Ctrl+E", loop, 1); ansi.StringWidth(longer) <= width {
				legend = longer
			}
			break
		}
	}
	lines = append(lines, legend)
	return lines
}

type packetState struct{ id, from, to, kind, status string }

func (p packetState) marker() string {
	if p.kind == "error" || p.status == "failed" {
		return "<!>"
	}
	switch p.kind {
	case "task":
		return "[T]"
	case "result", "review-result", "team-result":
		return "[R]"
	case "question":
		return "[?]"
	case "context-proposal", "context-result":
		return "[C]"
	}
	return "[M]"
}
func packetStates(events []Event) []packetState {
	packets := []packetState{}
	index := map[string]int{}
	for _, event := range events {
		id := str(event.Data["messageId"])
		if event.Type == "message.sent" {
			from, _ := event.Data["from"].(map[string]any)
			to, _ := event.Data["to"].(map[string]any)
			index[id] = len(packets)
			packets = append(packets, packetState{id: id, from: str(from["agentId"]), to: str(to["agentId"]), kind: str(event.Data["kind"]), status: "pending"})
		} else if i, ok := index[id]; ok {
			switch event.Type {
			case "message.delivered":
				packets[i].status = "delivered"
			case "message.failed":
				packets[i].status = "failed"
			case "message.delayed":
				packets[i].status = "delayed"
			}
		}
	}
	return packets
}
func packetActivity(events []Event, frame int) []string {
	packets := packetStates(events)
	lines := []string{}
	for _, p := range packets[max(0, len(packets)-5):] {
		phase := 4
		if frame >= 0 && p.status == "pending" {
			phase = frame % 9
		}
		lines = append(lines, p.from+" "+strings.Repeat("-", phase)+p.marker()+strings.Repeat("-", 8-phase)+"> "+p.to+" · "+p.kind+" · "+p.status)
	}
	return lines
}
func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

func withSpinner(spin, status string) string {
	if spin == "" {
		return status
	}
	return spin + " " + status
}
