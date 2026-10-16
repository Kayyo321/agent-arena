package arena

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// diffStat is line churn for one worktree or a sum of several.
type diffStat struct{ Add, Del, Files int }

func (d diffStat) plus(o diffStat) diffStat {
	return diffStat{d.Add + o.Add, d.Del + o.Del, d.Files + o.Files}
}

type diffStatsResult struct {
	RunID string
	Stats map[string]diffStat // keyed by workspace task ID
}

// diffInterval paces worktree scans; they run off the UI goroutine.
const diffInterval = 2 * time.Second

// refreshDiffStats measures each writing worktree against its own base, so
// loop passes built on earlier changes are not counted twice. A removed
// worktree falls back to the patch recorded for its last change.
func (m LiveModel) refreshDiffStats() tea.Cmd {
	trees := append([]Workspace(nil), m.Trees...)
	dir := m.Project.RunDir(m.Run.ID)
	run := m.Run.ID
	return func() tea.Msg {
		out := map[string]diffStat{}
		for _, tree := range trees {
			if !tree.Write {
				continue
			}
			if stat, ok := worktreeDiff(tree); ok {
				out[tree.TaskID] = stat
			} else if tree.LastChange != "" {
				if b, err := os.ReadFile(filepath.Join(dir, "changes", tree.LastChange+".patch")); err == nil {
					out[tree.TaskID] = patchDiff(b)
				}
			}
		}
		return diffStatsResult{run, out}
	}
}

func worktreeDiff(tree Workspace) (diffStat, bool) {
	if tree.Base == "" {
		return diffStat{}, false
	}
	if _, err := os.Stat(tree.Path); err != nil {
		return diffStat{}, false
	}
	numstat, err := Git(tree.Path, "diff", "--numstat", tree.Base)
	if err != nil {
		return diffStat{}, false
	}
	var d diffStat
	for _, line := range strings.Split(strings.TrimSpace(numstat), "\n") {
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) < 3 {
			continue
		}
		add, _ := strconv.Atoi(fields[0]) // Binary files report "-".
		del, _ := strconv.Atoi(fields[1])
		d = d.plus(diffStat{add, del, 1})
	}
	// New files count once they exist, before the agent stages anything.
	untracked, err := Git(tree.Path, "ls-files", "--others", "--exclude-standard")
	if err != nil {
		return d, true
	}
	for _, name := range strings.Split(strings.TrimSpace(untracked), "\n") {
		if name == "" {
			continue
		}
		d.Files++
		if b, err := os.ReadFile(filepath.Join(tree.Path, name)); err == nil && len(b) < 1<<20 && !bytes.Contains(b, []byte{0}) {
			d.Add += bytes.Count(b, []byte{'\n'})
			if len(b) > 0 && b[len(b)-1] != '\n' {
				d.Add++
			}
		}
	}
	return d, true
}

func patchDiff(patch []byte) diffStat {
	var d diffStat
	scan := bufio.NewScanner(bytes.NewReader(patch))
	scan.Buffer(make([]byte, 64*1024), 4<<20)
	for scan.Scan() {
		line := scan.Text()
		switch {
		case strings.HasPrefix(line, "diff --git "):
			d.Files++
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
		case strings.HasPrefix(line, "+"):
			d.Add++
		case strings.HasPrefix(line, "-"):
			d.Del++
		}
	}
	return d
}

// agentDiffs sums worktree churn per agent across its loop passes.
func (m LiveModel) agentDiffs() map[string]diffStat {
	if len(m.DiffStats) == 0 {
		return nil
	}
	out := map[string]diffStat{}
	for _, tree := range m.Trees {
		if stat, ok := m.DiffStats[tree.TaskID]; ok {
			out[tree.AgentID] = out[tree.AgentID].plus(stat)
		}
	}
	return out
}

// bandSummary is everything the status band reports, derived from audited
// run state, worktree records and the journal.
type bandSummary struct {
	name, status, elapsed string
	grade                 string // arena grade, or the draft's during setup
	running, queued       []string
	done, failed, total   int
	decisions, inflight   int
	diff                  diffStat
	diffKnown             bool
	trees                 int
	agents                []string // agents with changes, most churn first
	perAgent              map[string]diffStat
	segments              []string // node states in graph order
	base                  string
}

func (m LiveModel) bandSummary() bandSummary {
	s := bandSummary{name: m.Run.Workflow.Id, status: m.State.Status, trees: len(m.Trees), decisions: len(m.Decisions), base: m.Run.BaseCommit[:min(7, len(m.Run.BaseCommit))]}
	if s.name == "" {
		s.name = "arena"
	}
	switch {
	case m.Draft != nil && m.DraftScore != nil:
		s.name, s.grade = m.Draft.Workflow.Id+" (draft)", m.DraftScore.Grade
	case m.Run.Workflow.Id != "":
		s.grade = m.Run.scorecard().Grade
	}
	if s.status == "" || m.Run.ID == "" {
		s.status = "idle"
	}
	agents := []Agent{}
	for _, a := range m.Run.Workflow.Agents {
		if a.Activation != "on-call" {
			agents = append(agents, a)
		}
	}
	sort.SliceStable(agents, func(i, j int) bool {
		li, lj := 0, 0
		if agents[i].Level != nil {
			li = *agents[i].Level
		}
		if agents[j].Level != nil {
			lj = *agents[j].Level
		}
		return li < lj
	})
	for _, a := range agents {
		state := m.State.Agents[a.Id]
		switch state {
		case "running":
			s.running = append(s.running, a.Id)
		case "queued":
			s.queued = append(s.queued, a.Id)
		case "succeeded":
			s.done++
		case "failed":
			s.failed++
		}
		s.segments = append(s.segments, state)
	}
	s.total = len(agents)
	for _, p := range packetStates(m.Events) {
		if p.status == "pending" || p.status == "delayed" {
			s.inflight++
		}
	}
	s.perAgent = m.agentDiffs()
	s.diffKnown = s.perAgent != nil
	for id, d := range s.perAgent {
		s.diff = s.diff.plus(d)
		if d.Add+d.Del > 0 {
			s.agents = append(s.agents, id)
		}
	}
	sort.Slice(s.agents, func(i, j int) bool {
		a, b := s.perAgent[s.agents[i]], s.perAgent[s.agents[j]]
		if a.Add+a.Del != b.Add+b.Del {
			return a.Add+a.Del > b.Add+b.Del
		}
		return s.agents[i] < s.agents[j]
	})
	// Elapsed time runs from the first start to now while running, else to
	// the last journaled event.
	var first, last time.Time
	for _, e := range m.Events {
		at, err := time.Parse(time.RFC3339Nano, e.At)
		if err != nil {
			continue
		}
		if first.IsZero() && e.Type == "workflow.started" {
			first = at
		}
		last = at
	}
	if !first.IsZero() {
		if s.status == "running" {
			last = shaderClock()
		}
		s.elapsed = clockDuration(last.Sub(first))
	}
	return s
}

func clockDuration(d time.Duration) string {
	d = d.Round(time.Second)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	}
	return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
}

// band composes styled runs and measures them in terminal cells.
type band struct {
	color bool
	b     strings.Builder
	w     int
}

func (b *band) add(text, sgr string) *band {
	b.b.WriteString(styled(text, sgr, b.color))
	b.w += ansi.StringWidth(text)
	return b
}
func (b *band) String() string { return b.b.String() }

// header is the status band: the dotted wordmark leads, the run's vital
// signs sit beside it, and the Git tree closes the row on wide terminals.
// A hairline separates the band from the graph.
func (m LiveModel) header(width, height int, color bool) []string {
	s := m.bandSummary()
	mode := m.Run.Settings.Display.AsciiTitle
	glyph := func(u, a string) string {
		if unicodeLettering() {
			return u
		}
		return a
	}
	minus := glyph("−", "-")
	statusGlyph, statusSGR := statusLook(s.status, "")
	if s.status == "running" {
		statusGlyph = m.spinner(false)
	}
	if s.status == "idle" {
		statusSGR = sgrDim
	}
	vitals := func(b *band) {
		b.add(statusGlyph+" "+s.status, statusSGR)
		if s.elapsed != "" {
			b.add("  "+s.elapsed, sgrDim)
		}
		if m.Run.ID == "" {
			b.add("  · type a task or /run TASK", sgrDim)
		}
	}
	diff := func(b *band) {
		if !s.diffKnown {
			b.add("±0", sgrDim)
			return
		}
		b.add(fmt.Sprintf("+%d", s.diff.Add), sgrAdd).add(" ", "").add(fmt.Sprintf("%s%d", minus, s.diff.Del), sgrDel)
		if s.diff.Files > 0 {
			b.add(fmt.Sprintf(" · %d file%s", s.diff.Files, plural(s.diff.Files)), sgrDim)
		}
	}
	names := func(ids []string, room int) string {
		out := strings.Join(ids, ", ")
		for n := len(ids) - 1; n > 0 && ansi.StringWidth(out) > room; n-- {
			out = strings.Join(ids[:n], ", ") + fmt.Sprintf(" +%d", len(ids)-n)
		}
		return out
	}
	activity := func(b *band, room int) {
		if len(s.running) > 0 {
			b.add(glyph("●", "*")+" ", sgrRunning).add(names(s.running, max(8, room/3)), sgrSoft)
		} else {
			b.add(glyph("○", "o")+" nothing running", sgrDim)
		}
		if len(s.queued) > 0 {
			b.add(fmt.Sprintf("   %s %d queued", glyph("◌", "~"), len(s.queued)), sgrQueued)
		}
		doneSGR := sgrDim
		if s.done > 0 {
			doneSGR = sgrSucceeded
		}
		b.add(fmt.Sprintf("   %s %d/%d done", glyph("✓", "+"), s.done, s.total), doneSGR)
		if s.failed > 0 {
			b.add(fmt.Sprintf("   %s %d failed", glyph("✗", "x"), s.failed), sgrFailed)
		}
		if s.decisions > 0 {
			b.add(fmt.Sprintf("   %s %d waiting on you", glyph("⚑", "!"), s.decisions), "1;"+sgrAttention)
		}
		if s.inflight > 0 {
			b.add(fmt.Sprintf("   %s %d in flight", glyph("⇄", "<>"), s.inflight), sgrDim)
		}
	}
	// progress is one segment per agent in graph order, colored by state.
	progress := func(b *band, room int) {
		if s.total == 0 || room < s.total {
			return
		}
		cell := min(6, max(1, (room+1)/s.total-1))
		for i, state := range s.segments {
			if i > 0 {
				b.add(" ", "")
			}
			_, sgr := statusLook(state, "")
			if state == "" || state == "waiting" {
				sgr = sgrHairline
			}
			b.add(strings.Repeat(glyph("━", "="), cell), sgr)
		}
	}
	logo := wordmark(width, height, mode, color)
	if len(logo) < 3 {
		// Compact: one row with the title, vitals, activity and change size.
		var b band
		b.color = color
		b.add(" ", "")
		if len(logo) == 1 {
			b.b.WriteString(logo[0])
			b.w += ansi.StringWidth(logo[0])
			b.add("  ", "")
		}
		b.add(s.name, sgrStrong).add("  ", "")
		if s.grade != "" {
			b.add(s.grade, gradeSGR(s.grade)).add("  ", "")
		}
		vitals(&b)
		b.add("   ", "")
		activity(&b, width/2)
		b.add("   ", "")
		diff(&b)
		lines := []string{short(b.String(), width)}
		if height >= 12 {
			lines = append(lines, styled(strings.Repeat(glyph("─", "-"), width), sgrHairline, color))
		}
		return lines
	}
	const margin, gap = 2, 4
	logoWidth := 0
	for _, line := range logo {
		logoWidth = max(logoWidth, ansi.StringWidth(line))
	}
	// Each column is a muted label over two value rows; the identity column
	// shows the workflow, its state and per-agent progress instead.
	type column struct {
		rows  [3]band
		width int
	}
	newColumn := func(label string, least int) *column {
		c := &column{width: least}
		for i := range c.rows {
			c.rows[i].color = color
		}
		c.rows[0].add(label, sgrLabel)
		return c
	}
	identity := newColumn("", 24)
	identity.rows[0].add(s.name, sgrStrong)
	if s.grade != "" {
		identity.rows[0].add("  ", "").add(s.grade, gradeSGR(s.grade)).add(" score", sgrLabel)
	}
	vitals(&identity.rows[1])
	progress(&identity.rows[2], 26)

	active := newColumn("ACTIVE", 18)
	if len(s.running) > 0 {
		active.rows[1].add(glyph("●", "*")+" ", sgrRunning).add(names(s.running, 24), sgrSoft)
	} else {
		active.rows[1].add(glyph("○", "o")+" idle", sgrDim)
	}
	switch {
	case s.decisions > 0:
		active.rows[2].add(fmt.Sprintf("%s %d waiting on you", glyph("⚑", "!"), s.decisions), "1;"+sgrAttention)
	case len(s.queued) > 0:
		active.rows[2].add(fmt.Sprintf("%s %d queued", glyph("◌", "~"), len(s.queued)), sgrQueued)
	case s.inflight > 0:
		active.rows[2].add(fmt.Sprintf("%s %d in flight", glyph("⇄", "<>"), s.inflight), sgrDim)
	}

	nodes := newColumn("NODES", 10)
	doneSGR := sgrDim
	if s.done > 0 {
		doneSGR = sgrSucceeded
	}
	nodes.rows[1].add(fmt.Sprintf("%s %d/%d done", glyph("✓", "+"), s.done, s.total), doneSGR)
	if s.failed > 0 {
		nodes.rows[2].add(fmt.Sprintf("%s %d failed", glyph("✗", "x"), s.failed), sgrFailed)
	} else {
		nodes.rows[2].add("0 failed", sgrDim)
	}

	changes := newColumn("CHANGES", 14)
	if s.diffKnown {
		changes.rows[1].add(fmt.Sprintf("+%d", s.diff.Add), "1;"+sgrAdd).add(" ", "").add(fmt.Sprintf("%s%d", minus, s.diff.Del), "1;"+sgrDel)
		changes.rows[2].add(fmt.Sprintf("%d file%s", s.diff.Files, plural(s.diff.Files)), sgrDim)
	} else {
		changes.rows[1].add("±0", sgrDim)
	}
	// The Git tree closes the band flush right on wide terminals; elsewhere
	// the worktree count joins the change size.
	var git *column
	if width < 130 || m.Run.Settings.Display.GitTree == "hide" {
		changes.rows[2].add(fmt.Sprintf(" · %d tree%s", s.trees, plural(s.trees)), sgrDim)
	} else {
		git = newColumn("GIT  ", 0)
		git.rows[0].add(s.base, sgrSoft).add(fmt.Sprintf(" · %d worktree%s", s.trees, plural(s.trees)), sgrDim)
		rows := s.agents
		if len(rows) == 0 {
			seen := map[string]bool{}
			for _, tree := range m.Trees {
				if !seen[tree.AgentID] {
					seen[tree.AgentID] = true
					rows = append(rows, tree.AgentID)
				}
			}
		}
		rows = rows[:min(2, len(rows))]
		nameWidth := 0
		for _, id := range rows {
			nameWidth = max(nameWidth, ansi.StringWidth(id))
		}
		for i, id := range rows {
			branch := glyph("├ ", "|- ")
			if i == len(rows)-1 {
				branch = glyph("└ ", "`- ")
			}
			row := &git.rows[1+i]
			row.add(branch, sgrHairline).add(pad(id, nameWidth+2), agentSGR(id))
			if d, ok := s.perAgent[id]; ok {
				row.add(fmt.Sprintf("+%d", d.Add), sgrAdd).add(" ", "").add(fmt.Sprintf("%s%d", minus, d.Del), sgrDel)
			}
			if i == 1 && len(s.agents) > 2 {
				row.add(fmt.Sprintf("  +%d more", len(s.agents)-2), sgrDim)
			}
		}
	}
	for _, c := range []*column{identity, active, nodes, changes, git} {
		if c == nil {
			continue
		}
		for _, row := range c.rows {
			c.width = max(c.width, row.w)
		}
	}
	// Columns join by priority while they fit, then render in reading order.
	room := width - 2*margin - logoWidth - gap
	chosen := map[*column]bool{}
	used := 0
	for _, c := range []*column{identity, changes, active, nodes, git} {
		if c == nil {
			continue
		}
		need := c.width
		if used > 0 {
			need += 3
		}
		if used+need <= room || c == identity {
			chosen[c] = true
			used += need
		}
	}
	if git != nil && !chosen[git] {
		changes.rows[2].add(fmt.Sprintf(" · %d tree%s", s.trees, plural(s.trees)), sgrDim)
		changes.width = max(changes.width, changes.rows[2].w)
	}
	if !chosen[changes] {
		// Change size is never dropped: it folds in beside the workflow name.
		identity.rows[0].add("   ", "")
		diff(&identity.rows[0])
	}
	order := []*column{}
	for _, c := range []*column{identity, active, nodes, changes} {
		if chosen[c] {
			order = append(order, c)
		}
	}
	// Middle columns breathe evenly up to a cap; leftover space sits before
	// the Git tree so it stays flush with the right margin.
	slack := room - used
	spacing := 3
	if len(order) > 1 {
		spacing += min(slack/len(order), 8)
	}
	lines := []string{}
	for i := range 3 {
		var line strings.Builder
		line.WriteString(strings.Repeat(" ", margin) + pad(logo[i], logoWidth) + strings.Repeat(" ", gap))
		x := 0
		for j, c := range order {
			if j > 0 {
				line.WriteString(strings.Repeat(" ", spacing))
				x += spacing
			}
			cell := c.rows[i].String()
			if j < len(order)-1 || chosen[git] {
				cell = pad(cell, c.width)
				x += c.width
			} else {
				x += c.rows[i].w
			}
			line.WriteString(cell)
		}
		if chosen[git] {
			line.WriteString(strings.Repeat(" ", max(3, room-x-git.width)))
			line.WriteString(git.rows[i].String())
		}
		lines = append(lines, short(line.String(), width))
	}
	// The hairline starts in the brand color under the title and fades out.
	rule := glyph("─", "-")
	lead := margin + logoWidth
	lines = append(lines, styled(strings.Repeat(rule, min(width, lead)), "38;5;60", color)+styled(strings.Repeat(rule, max(0, width-lead)), sgrHairline, color))
	return lines
}
