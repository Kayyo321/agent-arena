package arena

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
)

type graphCard struct {
	id, kind string
	// title is drawn into the top border; tag is right-aligned there.
	title []mdSpan
	tag   string
	rows  [3]cardRow
	// tone is the status color family: active cards light their frame.
	tone string
	x, y int
}
type cardRow struct {
	left  []mdSpan
	right mdSpan
}
type graphEdge struct {
	from, to, label string
	peer, both      bool
	// loop marks a capped return edge from a router back to an earlier node.
	loop bool
	// branch marks a structured router route or fallback.
	branch bool
}

// wideGraph renders the graph without worktree statistics.
func wideGraph(w Workflow, state RunState, events []Event, width int, color bool, spin string, frames ...int) []string {
	frame := -1
	if len(frames) > 0 {
		frame = frames[0]
	}
	return graphView(w, state, events, nil, width, color, spin, frame)
}

// statusRow is a glyph, the state text and an optional right-hand note.
func statusRow(status, spin, note string) cardRow {
	glyph, sgr := statusLook(status, spin)
	return cardRow{left: []mdSpan{{glyph + " " + status, sgr}}, right: mdSpan{note, sgrDim}}
}

// diffSpans renders +added −deleted with the file count muted.
func diffSpans(d diffStat) []mdSpan {
	minus := "−"
	if !unicodeLettering() {
		minus = "-"
	}
	spans := []mdSpan{{fmt.Sprintf("+%d", d.Add), sgrAdd}, {" ", ""}, {fmt.Sprintf("%s%d", minus, d.Del), sgrDel}}
	if d.Files > 0 {
		spans = append(spans, mdSpan{fmt.Sprintf(" · %d file%s", d.Files, plural(d.Files)), sgrDim})
	}
	return spans
}

// fanOutCard shows a fan-out's roles and cap, then its latest pass: planning,
// waiting for approval, children done out of those approved, or the outcome.
// Children are never drawn as cards of their own.
func fanOutCard(f FanOut, events []Event, spin string) *graphCard {
	status, look := "waiting", "waiting"
	approved, done, failed, dropped, conflicting := 0, 0, 0, 0, 0
	for _, v := range events {
		if v.AgentID != f.Id {
			continue
		}
		switch v.Type {
		case "fanout.started":
			status, look = "planning", "running"
			approved, done, failed, dropped, conflicting = 0, 0, 0, 0, 0
		case "decision.requested":
			status, look = "waiting for you · see prompt", "attention"
		case "fanout.approved":
			approved, dropped = intValue(v.Data["count"]), intValue(v.Data["dropped"])
			status, look = "running", "running"
		case "fanout.child.completed":
			done++
		case "fanout.child.failed":
			failed++
		case "fanout.conflict":
			conflicting = len(stringSlice(v.Data["children"]))
		case "fanout.completed":
			status, look = "succeeded", "succeeded"
		case "fanout.stopped":
			status, look = "stopped", "failed"
		}
	}
	times := " ×"
	arrow := " → "
	if !unicodeLettering() {
		times, arrow = " x", " -> "
	}
	approval := "approval required"
	if f.Approval == "auto-within-cap" {
		approval = "auto within cap"
	}
	detail := fmt.Sprintf("max %d · %s", f.MaxChildren, approval)
	if approved > 0 || look == "succeeded" {
		detail = fmt.Sprintf("%d/%d done", done, approved)
		if failed > 0 {
			detail += fmt.Sprintf(" · %d failed", failed)
		}
		if dropped > 0 {
			detail += fmt.Sprintf(" · %d dropped", dropped)
		}
		if conflicting > 0 {
			detail += fmt.Sprintf(" · %d conflicting", conflicting)
		}
	}
	glyph, sgr := statusLook(look, spin)
	roles := []mdSpan{{f.Planner, sgrSoft}, {arrow, sgrDim}, {f.Worker, sgrSoft}, {fmt.Sprintf("%s%d", times, f.MaxChildren), sgrDim}}
	return &graphCard{id: f.Id, kind: "fanout", title: []mdSpan{{f.Name, "38;5;147"}}, tone: look, rows: [3]cardRow{{left: roles}, {left: []mdSpan{{detail, sgrDim}}}, {left: []mdSpan{{glyph + " " + status, sgr}}}}}
}

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

// Graph positions are a projection of the validated graph; layout never schedules.
// spin is the current loading glyph shown on cards that are doing work, and
// diffs holds per-agent worktree statistics when they are known.
func graphView(w Workflow, state RunState, events []Event, diffs map[string]diffStat, width int, color bool, spin string, frame int) []string {
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
		status, round := "waiting", 0
		for _, v := range events {
			if v.AgentID != t.Id || !strings.HasPrefix(v.Type, "team.") {
				continue
			}
			switch v.Type {
			case "team.started":
				status, round = "deliberating", 0
			case "team.round", "team.affirmations":
				status = "deliberating"
				if n, ok := v.Data["round"].(float64); ok {
					round = int(n)
				}
			case "team.consensus":
				status = "consensus"
			case "team.no-consensus":
				status = "no consensus"
			default:
				status = strings.TrimPrefix(v.Type, "team.")
			}
		}
		roster := []mdSpan{}
		for i, id := range t.Members {
			if i > 0 {
				roster = append(roster, mdSpan{"  ", ""})
			}
			glyph, sgr := statusLook(state.Agents[id], "")
			if s := state.Agents[id]; s == "" || s == "waiting" {
				glyph = "○"
				if !unicodeLettering() {
					glyph = "o"
				}
			}
			roster = append(roster, mdSpan{glyph, sgr}, mdSpan{" " + id, sgrSoft})
		}
		rounds := fmt.Sprintf("unanimous · max %d rounds", t.Consensus.MaxRounds)
		if round > 0 {
			rounds = fmt.Sprintf("unanimous · round %d/%d", round, t.Consensus.MaxRounds)
		}
		cards[t.Id] = &graphCard{id: t.Id, kind: "team", title: []mdSpan{{t.Name, "38;5;222"}}, tone: status, rows: [3]cardRow{{left: roster}, {left: []mdSpan{{rounds, sgrDim}}}, statusRow(status, spin, "")}}
	}
	for _, f := range w.FanOuts {
		members[f.Planner], members[f.Worker] = true, true
		ranks[f.Id] = 0
		if planner, _ := agentByID(w, f.Planner); planner.Level != nil {
			ranks[f.Id] = *planner.Level * 100
		}
		cards[f.Id] = fanOutCard(f, events, spin)
	}
	for _, a := range w.Agents {
		if a.Activation == "on-call" || members[a.Id] {
			continue
		}
		ranks[a.Id] = *a.Level * 100
		kind := "agent"
		if a.Id == w.HeadAgent {
			kind = "head"
		}
		model := []mdSpan{{a.Runtime.Model, sgrSoft}}
		if a.Runtime.ReasoningEffort != "" {
			model = append(model, mdSpan{" · " + a.Runtime.ReasoningEffort, sgrDim})
		}
		status := state.Agents[a.Id]
		if status == "" {
			status = "waiting"
		}
		note := ""
		if n := state.Iterations[a.Id]; n > 1 {
			note = fmt.Sprintf("pass %d", n)
		}
		// Before a run the card names the agent's role profile; once worktree
		// statistics exist it shows that agent's change size instead.
		role, _, _ := strings.Cut(filepath.Base(a.Profile), ".")
		meta := []mdSpan{{"role " + role, sgrDim}}
		if d, ok := diffs[a.Id]; ok {
			meta = diffSpans(d)
		} else if diffs != nil {
			meta = []mdSpan{{"no changes", sgrDim}}
		}
		cards[a.Id] = &graphCard{id: a.Id, kind: kind, title: []mdSpan{{a.Id, ""}}, tag: a.Runtime.Provider, tone: status, rows: [3]cardRow{{left: model}, statusRow(status, spin, note), {left: meta}}}
	}
	for _, r := range w.DecisionRouters {
		status, choice := "waiting", ""
		for _, v := range events {
			switch {
			case v.AgentID == r.Id && v.Type == "router.selected":
				arrow := " → "
				if !unicodeLettering() {
					arrow = " -> "
				}
				status, choice = "selected", str(v.Data["branch"])+arrow+str(v.Data["target"])
			case v.AgentID == r.Id && v.Type == "decision.requested":
				status = "attention"
			}
		}
		result := statusRow(status, spin, "")
		switch status {
		case "selected":
			result.left[0].text = strings.SplitN(result.left[0].text, " ", 2)[0] + " " + choice
		case "attention":
			result.left[0].text = strings.SplitN(result.left[0].text, " ", 2)[0] + " waiting for you · see prompt"
		}
		routes := []string{}
		for _, branch := range r.Routes {
			arrow := " → "
			if LoopRoute(branch) {
				arrow = " ↺ "
			}
			if !unicodeLettering() {
				arrow = " -> "
			}
			routes = append(routes, strings.Trim(strings.TrimSpace(string(JSON(branch.Equals))), `"`)+arrow+branch.To)
		}
		routes = append(routes, "else "+r.Fallback.Action)
		cards[r.Id] = &graphCard{id: r.Id, kind: "router", title: []mdSpan{{r.Id, "38;5;116"}}, tone: status, rows: [3]cardRow{{left: []mdSpan{{r.InputKind + " " + r.PayloadPointer, sgrSoft}}}, {left: []mdSpan{{strings.Join(routes, " · "), sgrDim}}}, result}}
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
			edges = append(edges, graphEdge{from: r.Id, to: branch.To, label: "=" + strings.TrimSpace(string(JSON(branch.Equals))), branch: true})
		}
		if r.Fallback.Action == "route" {
			edges = append(edges, graphEdge{from: r.Id, to: r.Fallback.To, label: "fallback", branch: true})
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
	styles := make([][]string, height)
	for i := range canvas {
		canvas[i] = make([]string, width)
		styles[i] = make([]string, width)
		for x := range canvas[i] {
			canvas[i][x] = " "
		}
	}
	// put writes text in cells; an optional SGR colors exactly those cells.
	put := func(x, y int, s string, sgr ...string) {
		if y < 0 || y >= len(canvas) {
			return
		}
		style := ""
		if len(sgr) > 0 {
			style = sgr[0]
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
				styles[y][x] = style
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
			put(x, y, line, sgrEdge)
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
	// Each lane cell keeps the color of the edge drawn through it last.
	tints := map[cell]string{}
	tint := sgrEdge
	connect := func(a, b cell) {
		tints[a], tints[b] = tint, tint
		for a != b {
			tints[a] = tint
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
		sgr  string
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
		sgr            string
	}
	joints := []joint{}
	gutters := 0
	// A card's frame is dim until it starts, bright while it works and red
	// when it fails, so the active part of the graph is visible at a glance.
	frameSGR := func(card *graphCard) string {
		k := nodeKinds(card.kind)
		switch card.tone {
		case "running", "deliberating", "queued":
			return "1;" + k.bright
		case "attention":
			return "1;" + sgrAttention
		case "failed", "no consensus":
			return sgrFailed
		case "waiting", "":
			return k.dim
		}
		return k.normal
	}
	for i, edge := range edges {
		from, to := cards[edge.from], cards[edge.to]
		if from == nil || to == nil {
			continue
		}
		tint = sgrEdge
		labelSGR := sgrEdgeLabel
		switch {
		case edge.loop:
			tint, labelSGR = sgrLoop, sgrLoop
		case edge.branch:
			tint, labelSGR = nodeKinds("router").dim, nodeKinds("router").bright
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
			joints = append(joints, joint{tail, y1, tailUnicode, "+", frameSGR(from)}, joint{head, y2, headUnicode, headASCII, sgrLoop})
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
				labels = append(labels, label{x: min(gutter+2, max(0, width-ansi.StringWidth(text))), y: ly, text: text, sgr: labelSGR})
			} else {
				if ansi.StringWidth(text+" "+match) <= gutter-1 {
					text += " " + match
				}
				labels = append(labels, label{x: max(0, gutter-1-ansi.StringWidth(text)), y: ly, text: text, sgr: labelSGR})
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
			put(x2, y1, arrow, sgrEdgeLabel)
			if edge.both {
				if arrow == ">" {
					put(x1, y1, "<", sgrEdgeLabel)
				} else {
					put(x1, y1, ">", sgrEdgeLabel)
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
		kind := nodeKinds(from.kind)
		if to.y < from.y {
			joints = append(joints, joint{x1, from.y, kind.up, kind.up, frameSGR(from)}, joint{x2, y2, "▲", "^", labelSGR})
		} else {
			joints = append(joints, joint{x1, from.y + 4, kind.down, kind.down, frameSGR(from)}, joint{x2, y2, "▼", "v", labelSGR})
		}
		recordPath(edge.from, edge.to, point{x1, y1}, point{x1, lane}, point{x2, lane}, point{x2, y2})
		if x1 != x2 {
			labels = append(labels, label{x: min(x1, x2) + 1, y: lane, text: short(edge.label, max(0, abs(x2-x1)-2)), dots: edge.peer, x2: max(x1, x2) - 1, sgr: labelSGR})
		} else {
			// A straight edge is labelled beside its own arrowhead, away from
			// the lanes other branches use.
			labels = append(labels, label{x: x2 + 2, y: y2, text: short(edge.label, max(0, cardWidth/2-3)), sgr: labelSGR})
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
		put(c.x, c.y, glyph, tints[c])
	}
	for _, l := range labels {
		if l.dots {
			for x := l.x; x <= l.x2; x++ {
				put(x, l.y, ".", sgrEdge)
			}
		}
		put(l.x, l.y, l.text, l.sgr)
	}
	// Only unacknowledged packets move. The route comes from the graph projection;
	// packet bodies never enter the viewport.
	aliases := map[string]string{}
	for _, team := range w.Teams {
		for _, id := range team.Members {
			aliases[id] = team.Id
		}
	}
	for _, f := range w.FanOuts {
		aliases[f.Planner], aliases[f.Worker] = f.Id, f.Id
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
		if f := fanOutChildOf(w, from); f != "" {
			from = f
		}
		if f := fanOutChildOf(w, to); f != "" {
			to = f
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
		put(max(0, min(width-3, p.x-1)), p.y, packet.marker(), sgrPacket)
	}
	// spans writes styled runs from x, truncating with an ellipsis at room
	// cells, and returns the number of cells used.
	spans := func(x, y int, runs []mdSpan, room int) int {
		used := 0
		for _, run := range runs {
			left := room - used
			if left <= 0 {
				break
			}
			text := run.text
			if ansi.StringWidth(text) > left {
				text = short(text, left)
			}
			put(x+used, y, text, run.sgr)
			used += ansi.StringWidth(text)
		}
		return used
	}
	for _, card := range cards {
		k := nodeKinds(card.kind)
		edge := frameSGR(card)
		right := card.x + cardWidth - 1
		put(card.x, card.y, k.tl, edge)
		put(right, card.y, k.tr, edge)
		put(card.x, card.y+4, k.bl, edge)
		put(right, card.y+4, k.br, edge)
		for x := card.x + 1; x < right; x++ {
			put(x, card.y, k.h, edge)
			put(x, card.y+4, k.h, edge)
		}
		// The top border carries the node's kind badge, name and runtime tag.
		title := []mdSpan{}
		if k.badge != "" {
			title = append(title, mdSpan{k.badge, "1;" + k.normal}, mdSpan{" · ", k.dim})
		}
		for _, run := range card.title {
			if run.sgr == "" {
				run.sgr = "1;" + agentSGR(card.id)
			}
			title = append(title, run)
		}
		room := cardWidth - 6
		if tag := ansi.StringWidth(card.tag); tag > 0 && tag+3 < room/2 {
			room -= tag + 3
			put(right-2-tag, card.y, " "+card.tag+" ", sgrDim)
		}
		put(card.x+2, card.y, " ")
		put(card.x+3+spans(card.x+3, card.y, title, room), card.y, " ")
		for i, row := range card.rows {
			y := card.y + 1 + i
			put(card.x, y, k.v, edge)
			put(right, y, k.v, edge)
			for x := card.x + 1; x < right; x++ {
				put(x, y, " ")
			}
			room := cardWidth - 4
			if note := ansi.StringWidth(row.right.text); note > 0 {
				room -= note + 1
				put(right-1-note, y, row.right.text, row.right.sgr)
			}
			spans(card.x+2, y, row.left, room)
		}
	}
	for _, j := range joints {
		if unicodeLettering() {
			put(j.x, j.y, j.unicode, j.sgr)
		} else {
			put(j.x, j.y, j.ascii, j.sgr)
		}
	}
	lines := []string{}
	for y, line := range canvas {
		last := len(line) - 1
		for last >= 0 && (line[last] == " " || line[last] == "") {
			last--
		}
		var text strings.Builder
		current := ""
		for x := 0; x <= last; x++ {
			if color && line[x] != "" && styles[y][x] != current {
				if current != "" {
					text.WriteString("\x1b[0m")
				}
				if styles[y][x] != "" {
					text.WriteString("\x1b[" + styles[y][x] + "m")
				}
				current = styles[y][x]
			}
			text.WriteString(line[x])
		}
		if current != "" {
			text.WriteString("\x1b[0m")
		}
		lines = append(lines, text.String())
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
	case "result", "review-result", "team-result", "fanout-result":
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
