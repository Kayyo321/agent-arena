package arena

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
)

// RubricVersion identifies the score sheet in docs/ARENA-SCORING.md. A score
// is a pure function of the workflow and its profiles, so the sender and the
// receiver of a share code see the same grade.
const RubricVersion = 3

// Finding is one deduction with a stable check ID from the score sheet.
type Finding struct {
	ID       string   `json:"id"`
	Category string   `json:"category"`
	Severity string   `json:"severity"`
	Agents   []string `json:"agents,omitempty"`
	Message  string   `json:"message"`
	Fix      string   `json:"fix"`
	Points   int      `json:"points"`
}

type CategoryScore struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Weight int    `json:"weight"`
	Points int    `json:"points"`
}

type Scorecard struct {
	Rubric     int             `json:"rubric"`
	Points     int             `json:"points"`
	Grade      string          `json:"grade"`
	Band       string          `json:"band"`
	Categories []CategoryScore `json:"categories"`
	Findings   []Finding       `json:"findings"`
	// Ceilings name the findings that capped the grade below its band, and
	// Blockers say what the next tier up still needs.
	Ceilings []string `json:"ceilings,omitempty"`
	Blockers []string `json:"blockers,omitempty"`
}

var scoreCategories = []CategoryScore{
	{ID: "T", Name: "Trust distribution", Weight: 30},
	{ID: "P", Name: "Proportionality", Weight: 25},
	{ID: "V", Name: "Verification & feedback", Weight: 20},
	{ID: "C", Name: "Role clarity", Weight: 15},
	{ID: "R", Name: "Resilience & cost", Weight: 10},
}

// scoreChecks is the registry of check IDs; docs/ARENA-SCORING.md documents
// every entry and a test keeps the two in step.
var scoreChecks = map[string]string{
	"T1": "Add a reviewer or review team downstream of the writer so it receives the change, or drop write if the agent only needs to read.",
	"T2": "Let the coordinator delegate: move write and run_commands to a worker it hands off to.",
	"T3": "Remove write from reviewers; a reviewer should judge changes it cannot make.",
	"T4": "Run at least one reviewer of this writer on a different provider or model.",
	"T5": "Use integration policy human-review, or review every writer before auto-if-clean integration.",
	"T6": "Grant only the capabilities the role needs.",
	"T7": "Put team members on different providers or models so the team does not share blind spots.",
	"P1": "Merge the two agents, or give each a distinct objective and capability set.",
	"P2": "Remove the relay and delegate straight to the agent that does the work.",
	"P3": "Flatten the graph; most work needs at most a coordinator, workers and reviewers.",
	"P4": "Match maxConcurrentAgents to the widest fan-out and the number of path agents.",
	"P5": "Keep teams to 2-3 members and at most 3 deliberation rounds.",
	"P6": "Replace the router with a plain delegates edge, or give it real branches.",
	"P7": "Give on-call agents a distinct escalation role and a cap of at most 3 invocations.",
	"P8": "Lower loop caps, team rounds or on-call caps; the worst case runs far more invocations than the graph has agents.",
	"P9": "Use one agent for read-only fan-out, or add the review or write step that justifies several.",
	"V1": "Add a review agent or team for the writers.",
	"V2": "Add a decision router after the review with a capped loop route back to the writer for ChangesRequested.",
	"V3": "Use 2-3 loop iterations and branch loops on review-result or team-result packets.",
	"V4": "Route the fallback to a reviewer, ask-user or stop instead of an unreviewed writer.",
	"V5": "Give one reviewer run_commands (without write) so it can run the tests itself.",
	"V6": "Add an on-call agent triggered by team.no-consensus, agent.failed or validation.failed.",
	"V7": "Give reviewers at least the writer's reasoning effort.",
	"C1": "Write an objective of at least 8 words and instructions of at least 25.",
	"C2": "Add constraints that bound what the writer may change.",
	"C3": "Name agents after what they do.",
	"C4": "Make the head a coordinator or worker; reviewers belong downstream of the work.",
	"C5": "Describe the arena's purpose in its description.",
	"C6": "Remove the capability, or run the agent on a provider whose adapter can exercise it.",
	"R1": "Spread agents across at least two providers so one outage does not stop the arena.",
	"R2": "Add an event trigger to the on-call rule so escalation happens without the user.",
	"R3": "Raise maxConcurrentAgents so parallel branches can run in parallel.",
	"R4": "Cut loop caps, team rounds or on-call caps; the worst case exceeds 60 invocations.",
}

var gradeBands = []struct {
	min   int
	grade string
}{{97, "S+"}, {93, "S"}, {90, "S-"}, {87, "A+"}, {83, "A"}, {80, "A-"}, {77, "B+"}, {73, "B"}, {70, "B-"}, {67, "C+"}, {63, "C"}, {60, "C-"}, {57, "D+"}, {53, "D"}, {50, "D-"}, {0, "F"}}

func gradeFor(points int) string {
	for _, b := range gradeBands {
		if points >= b.min {
			return b.grade
		}
	}
	return "F"
}

// gradeRank orders grades from S+ (0) to F (15).
func gradeRank(g string) int {
	for i, b := range gradeBands {
		if b.grade == g {
			return i
		}
	}
	return len(gradeBands) - 1
}

var effortRank = map[string]int{"none": 0, "minimal": 1, "low": 2, "medium": 3, "high": 4, "xhigh": 5, "max": 6}

var (
	genericID     = regexp.MustCompile(`^(agent|node|worker|bot)[-_.]?\d*$`)
	verifierRole  = regexp.MustCompile(`review|test|qa|verif|valid|audit`)
	readOnlyRole  = regexp.MustCompile(`research|plan|architect|analy`)
	wordPattern   = regexp.MustCompile(`[a-z0-9]+`)
	scoreStopword = map[string]bool{"the": true, "and": true, "for": true, "with": true, "that": true, "this": true, "you": true, "your": true, "are": true, "from": true, "into": true, "each": true, "any": true, "all": true, "not": true, "use": true, "its": true, "their": true, "them": true, "when": true, "then": true, "than": true, "only": true, "will": true, "must": true, "should": true, "can": true, "have": true, "has": true, "but": true, "who": true, "what": true, "which": true, "about": true, "agent": true}
)

type arenaGraph struct {
	w        Workflow
	profiles map[string]Profile
	agents   map[string]Agent
	members  map[string]string // team member, fan-out planner or worker -> its node
	teams    map[string]Team
	fanOuts  map[string]FanOut
	edges    map[string][]string
	parents  map[string][]string
	path     []string // standalone path agents in workflow order
	writers  []string // path agents with write, teams with a writing member, fan-outs with a writing worker
	gates    []string // review agents, review teams and fan-outs of reviewers
}

func has(p Profile, capability string) bool { return Has(p.Capabilities, capability) }

// adapterLimits are capabilities a provider adapter cannot exercise, with the
// reason. They are fixed per adapter and runtime options, unlike readiness, so
// scores stay a pure function of the arena. Antigravity's headless mode has no
// per-command approval, so invokeAntigravity refuses run_commands unless the
// runtime opts into sandboxed blanket approval (antigravityBlanket).
var adapterLimits = map[string]map[string]string{
	"antigravity": {"run_commands": "headless runs cannot execute commands without commandApproval \"" + antigravityBlanketApproval + "\""},
}

// adapterLimit is the reason r's adapter cannot exercise capability, if any.
func adapterLimit(r Runtime, capability string) (string, bool) {
	reason, limited := adapterLimits[r.Provider][capability]
	if limited && capability == "run_commands" && antigravityBlanket(r) {
		return "", false
	}
	return reason, limited
}

// unusableCapabilities lists the profile capabilities a's adapter cannot
// exercise.
func unusableCapabilities(a Agent, p Profile) []string {
	out := []string{}
	for _, capability := range p.Capabilities {
		if _, limited := adapterLimit(a.Runtime, capability); limited {
			out = append(out, capability)
		}
	}
	return out
}

// WorkflowWarnings reports configuration that validates but cannot work as
// written, such as a capability the agent's adapter cannot exercise.
func WorkflowWarnings(w Workflow, profiles map[string]Profile) []string {
	warnings := []string{}
	for _, a := range w.Agents {
		for _, capability := range unusableCapabilities(a, profiles[a.Id]) {
			reason, _ := adapterLimit(a.Runtime, capability)
			warnings = append(warnings, fmt.Sprintf("warning: %s has %s, but %s %s; the agent fails when it starts. Remove %s or choose another provider.", a.Id, capability, a.Runtime.Provider, reason, capability))
		}
	}
	return warnings
}

// forwardEdges are the scheduling edges: delegations plus non-loop router
// routes and route fallbacks. Loop routes are back edges and stay out.
func forwardEdges(w Workflow) map[string][]string {
	edges := map[string][]string{}
	add := func(from, to string) {
		if !Has(edges[from], to) {
			edges[from] = append(edges[from], to)
		}
	}
	for _, r := range w.Relationships {
		if r.Type == "delegates" {
			add(r.From, r.To)
		}
	}
	for _, r := range w.DecisionRouters {
		for _, route := range r.Routes {
			if !LoopRoute(route) {
				add(r.Id, route.To)
			}
		}
		if r.Fallback.Action == "route" {
			add(r.Id, r.Fallback.To)
		}
	}
	return edges
}

func analyzeArena(w Workflow, profiles map[string]Profile) arenaGraph {
	g := arenaGraph{w: w, profiles: profiles, agents: map[string]Agent{}, members: map[string]string{}, teams: map[string]Team{}, fanOuts: map[string]FanOut{}, edges: forwardEdges(w), parents: map[string][]string{}}
	for _, a := range w.Agents {
		g.agents[a.Id] = a
	}
	for _, t := range w.Teams {
		g.teams[t.Id] = t
		for _, id := range t.Members {
			g.members[id] = t.Id
		}
	}
	for _, f := range w.FanOuts {
		g.fanOuts[f.Id] = f
		g.members[f.Planner], g.members[f.Worker] = f.Id, f.Id
	}
	for from, targets := range g.edges {
		for _, to := range targets {
			g.parents[to] = append(g.parents[to], from)
		}
	}
	for _, a := range w.Agents {
		if a.Activation == "on-call" || g.members[a.Id] != "" {
			continue
		}
		g.path = append(g.path, a.Id)
		if has(profiles[a.Id], "write") {
			g.writers = append(g.writers, a.Id)
		}
		if profiles[a.Id].Role == "review" {
			g.gates = append(g.gates, a.Id)
		}
	}
	for _, t := range w.Teams {
		for _, id := range t.Members {
			if p := profiles[id]; p.Role == "review" || has(p, "review") {
				g.gates = append(g.gates, t.Id)
				break
			}
		}
		// A team is one graph node: when a member writes, the team's result
		// carries that change and needs review like any writer's.
		for _, id := range t.Members {
			if has(profiles[id], "write") {
				g.writers = append(g.writers, t.Id)
				break
			}
		}
	}
	// A fan-out is one graph node standing for its worker's children; the
	// planner runs read-only.
	for _, f := range w.FanOuts {
		if p := profiles[f.Worker]; p.Role == "review" || has(p, "review") {
			g.gates = append(g.gates, f.Id)
		}
		if has(profiles[f.Worker], "write") {
			g.writers = append(g.writers, f.Id)
		}
	}
	return g
}

// writerRuntimes are the provider/model pairs that produce a node's changes.
func (g arenaGraph) writerRuntimes(id string) map[string]bool {
	keys := map[string]bool{}
	if t, ok := g.teams[id]; ok {
		for _, m := range t.Members {
			if has(g.profiles[m], "write") {
				keys[runtimeKey(g.agents[m].Runtime)] = true
			}
		}
		return keys
	}
	if f, ok := g.fanOuts[id]; ok {
		id = f.Worker
	}
	keys[runtimeKey(g.agents[id].Runtime)] = true
	return keys
}

// reviewers are the agents that judge on behalf of a gate.
func (g arenaGraph) reviewers(gate string) []string {
	if t, ok := g.teams[gate]; ok {
		return t.Members
	}
	if f, ok := g.fanOuts[gate]; ok {
		return []string{f.Worker}
	}
	return []string{gate}
}

func (g arenaGraph) isVerifier(id string) bool {
	p := g.profiles[id]
	return p.Role == "review" || has(p, "review") || verifierRole.MatchString(strings.ToLower(p.Role))
}

// coordinates reports an agent that hands work to another worker, rather
// than only passing its own result on to review.
func (g arenaGraph) coordinates(id string) bool {
	for _, child := range g.edges[id] {
		if _, ok := g.agents[child]; ok && !Has(g.gates, child) {
			return true
		}
	}
	return false
}

func (g arenaGraph) delegatesWriting(id string) bool {
	for _, child := range g.edges[id] {
		if Has(g.writers, child) {
			return true
		}
	}
	return false
}

func runtimeKey(r Runtime) string { return r.Provider + "/" + r.Model }

// gatesOf lists the review gates downstream of id, which receive its result.
func (g arenaGraph) gatesOf(id string) []string {
	out := []string{}
	for _, node := range descendants(g.edges, id)[1:] {
		if Has(g.gates, node) {
			out = append(out, node)
		}
	}
	return out
}

// independent reports whether one of the gate's reviewers runs on a different
// provider or model than id.
func (g arenaGraph) independent(id, gate string) bool {
	produced := g.writerRuntimes(id)
	for _, r := range g.reviewers(gate) {
		if !produced[runtimeKey(g.agents[r].Runtime)] {
			return true
		}
	}
	return false
}

// revisable reports whether a review-consuming router downstream of id can
// loop back to id or one of its ancestors.
func (g arenaGraph) revisable(id string) bool {
	below := descendants(g.edges, id)
	for _, r := range g.w.DecisionRouters {
		if !Has(below, r.Id) || (r.InputKind != "review-result" && r.InputKind != "team-result") {
			continue
		}
		for _, route := range r.Routes {
			if LoopRoute(route) && Has(descendants(g.edges, route.To), id) {
				return true
			}
		}
	}
	return false
}

// agentCount counts invocations a node stands for: team members, a
// fan-out's planner and up to maxChildren workers, or one agent.
func (g arenaGraph) agentCount(nodes []string) int {
	n := 0
	for _, id := range nodes {
		if t, ok := g.teams[id]; ok {
			n += len(t.Members)
		} else if f, ok := g.fanOuts[id]; ok {
			n += 1 + f.MaxChildren
		} else if _, ok := g.agents[id]; ok {
			n++
		}
	}
	return n
}

// pathAgents counts the agent instances one pass can start: standalone path
// agents, team members, and each fan-out's planner plus maxChildren workers.
func (g arenaGraph) pathAgents() int {
	n := len(g.path) + len(g.members)
	for _, f := range g.w.FanOuts {
		n += f.MaxChildren - 1
	}
	return n
}

// worstCase bounds the agent invocations one run can make.
func (g arenaGraph) worstCase() int {
	n := g.pathAgents()
	for _, r := range g.w.DecisionRouters {
		for _, route := range r.Routes {
			if LoopRoute(route) && route.MaxIterations != nil {
				n += *route.MaxIterations * g.agentCount(descendants(g.edges, route.To))
			}
		}
	}
	for _, t := range g.w.Teams {
		n += len(t.Members) * t.Consensus.MaxRounds
	}
	for _, rule := range g.w.OnCall {
		n += rule.MaxInvocationsPerRun
	}
	return n
}

func scoreWords(s string) map[string]bool {
	out := map[string]bool{}
	for _, w := range wordPattern.FindAllString(strings.ToLower(s), -1) {
		if len(w) > 2 && !scoreStopword[w] {
			out[w] = true
		}
	}
	return out
}

func jaccard(a, b map[string]bool) float64 {
	if len(a) == 0 && len(b) == 0 {
		return 1
	}
	shared := 0
	for w := range a {
		if b[w] {
			shared++
		}
	}
	return float64(shared) / float64(len(a)+len(b)-shared)
}

func capabilitySet(p Profile) string {
	c := append([]string{}, p.Capabilities...)
	sort.Strings(c)
	return strings.Join(c, ",")
}

// redundant reports two profiles that do the same job: same role, same
// capabilities and mostly the same objective and instructions.
func redundant(a, b Profile) bool {
	return a.Role == b.Role && capabilitySet(a) == capabilitySet(b) && jaccard(scoreWords(a.Objective+" "+a.Instructions), scoreWords(b.Objective+" "+b.Instructions)) >= 0.6
}

// ScoreArena grades an arena against the score sheet in docs/ARENA-SCORING.md.
func ScoreArena(w Workflow, profiles map[string]Profile) Scorecard {
	// Score capabilities as the adapters can exercise them: a grant the
	// adapter refuses earns no credit, and C6 deducts for it.
	unusable := map[string][]string{}
	effective := map[string]Profile{}
	for id, p := range profiles {
		effective[id] = p
	}
	for _, a := range w.Agents {
		p, ok := profiles[a.Id]
		limited := unusableCapabilities(a, p)
		if !ok || len(limited) == 0 {
			continue
		}
		unusable[a.Id] = limited
		p.Capabilities = []string{}
		for _, capability := range profiles[a.Id].Capabilities {
			if !Has(limited, capability) {
				p.Capabilities = append(p.Capabilities, capability)
			}
		}
		effective[a.Id] = p
	}
	profiles = effective
	g := analyzeArena(w, profiles)
	findings := []Finding{}
	spent := map[string]int{}
	add := func(id, severity string, points, limit int, agents []string, format string, args ...any) {
		if limit > 0 {
			points = min(points, limit-spent[id])
		}
		if points <= 0 {
			return
		}
		spent[id] += points
		if severity == "" {
			severity = "minor"
			if points >= 3 {
				severity = "moderate"
			}
		}
		findings = append(findings, Finding{ID: id, Category: id[:1], Severity: severity, Agents: agents, Message: fmt.Sprintf(format, args...), Fix: scoreChecks[id], Points: points})
	}
	solo := len(w.Agents) == 1

	// T — trust distribution.
	unreviewed := []string{}
	for _, id := range g.writers {
		if len(g.gatesOf(id)) == 0 {
			unreviewed = append(unreviewed, id)
			add("T1", "major", 12, 24, []string{id}, "%s writes changes that no reviewer receives", id)
		}
	}
	for _, id := range g.path {
		p := profiles[id]
		if !solo && g.coordinates(id) && has(p, "write") && has(p, "run_commands") {
			add("T2", "major", 8, 0, []string{id}, "%s coordinates and also writes and runs commands", id)
		}
	}
	for _, gate := range g.gates {
		for _, id := range g.reviewers(gate) {
			if has(profiles[id], "write") {
				add("T3", "major", 6, 0, []string{id}, "reviewer %s can write the changes it judges", id)
			}
		}
	}
	for _, id := range g.writers {
		gates := g.gatesOf(id)
		if len(gates) == 0 {
			continue
		}
		independent := false
		for _, gate := range gates {
			independent = independent || g.independent(id, gate)
		}
		if !independent {
			add("T4", "", 4, 0, []string{id}, "every reviewer of %s runs on its provider and model (%s)", id, strings.Join(sortedKeys(g.writerRuntimes(id)), ", "))
		}
	}
	if w.Settings.Integration.Policy == "auto-if-clean" {
		if len(unreviewed) > 0 {
			add("T5", "major", 15, 0, unreviewed, "auto-if-clean integrates unreviewed changes from %s", strings.Join(unreviewed, ", "))
		} else {
			add("T5", "", 3, 0, nil, "auto-if-clean integrates without a human look")
		}
	}
	for _, a := range w.Agents {
		p := profiles[a.Id]
		coordinatorDoer := !solo && g.coordinates(a.Id) && has(p, "write") && has(p, "run_commands")
		switch {
		case has(p, "write") && !coordinatorDoer && g.delegatesWriting(a.Id):
			add("T6", "", 2, 8, []string{a.Id}, "%s writes although it delegates writing to others", a.Id)
		case has(p, "write") && readOnlyRole.MatchString(strings.ToLower(p.Role)):
			add("T6", "", 2, 8, []string{a.Id}, "%s has write but its %s role only reads", a.Id, p.Role)
		case has(p, "run_commands") && !has(p, "write") && !g.isVerifier(a.Id):
			add("T6", "", 2, 8, []string{a.Id}, "%s runs commands but neither writes nor verifies", a.Id)
		}
	}
	for _, t := range w.Teams {
		keys := map[string]bool{}
		for _, id := range t.Members {
			keys[runtimeKey(g.agents[id].Runtime)] = true
		}
		if len(keys) == 1 {
			add("T7", "", 3, 0, t.Members, "every member of team %s runs on the same provider and model", t.Id)
		}
	}

	// P — proportionality.
	standalone := append([]string{}, g.path...)
	for id := range g.members {
		standalone = append(standalone, id)
	}
	sort.Strings(standalone)
	for i, a := range standalone {
		for _, b := range standalone[i+1:] {
			if g.members[a] != "" && g.members[a] == g.members[b] {
				continue // a team deliberately puts similar reviewers side by side
			}
			if redundant(profiles[a], profiles[b]) {
				add("P1", "", 6, 0, []string{a, b}, "%s and %s do the same job", a, b)
			}
		}
	}
	for _, id := range g.path {
		if id == w.HeadAgent || len(g.parents[id]) != 1 || len(g.edges[id]) != 1 || g.isVerifier(id) || has(profiles[id], "write") {
			continue
		}
		child := g.edges[id][0]
		if _, ok := g.agents[child]; !ok || g.members[child] != "" {
			continue
		}
		subset := true
		for _, c := range profiles[id].Capabilities {
			subset = subset && has(profiles[child], c)
		}
		if subset {
			add("P2", "", 5, 0, []string{id}, "%s only relays work from %s to %s", id, g.parents[id][0], child)
		}
	}
	depth := 0
	for _, a := range w.Agents {
		if a.Level != nil {
			depth = max(depth, *a.Level)
		}
	}
	if depth > 3 {
		add("P3", "", 3*(depth-3), 0, nil, "the graph is %d levels deep", depth+1)
	}
	fanout := 0
	for _, targets := range g.edges {
		fanout = max(fanout, len(targets))
	}
	for _, f := range w.FanOuts {
		fanout = max(fanout, f.MaxChildren)
	}
	pathAgents := g.pathAgents()
	if fanout > w.Settings.MaxConcurrentAgents && w.Settings.MaxConcurrentAgents > 1 || w.Settings.MaxConcurrentAgents > pathAgents {
		add("P4", "", 2, 0, nil, "maxConcurrentAgents %d does not fit a widest fan-out of %d over %d path agents", w.Settings.MaxConcurrentAgents, fanout, pathAgents)
	}
	for _, t := range w.Teams {
		if len(t.Members) > 3 {
			add("P5", "", 2, 0, []string{t.Id}, "team %s has %d members", t.Id, len(t.Members))
		}
		if t.Consensus.MaxRounds > 3 {
			add("P5", "", 2, 0, []string{t.Id}, "team %s allows %d deliberation rounds", t.Id, t.Consensus.MaxRounds)
		}
	}
	for _, r := range w.DecisionRouters {
		targets := map[string]bool{}
		looped := false
		for _, route := range r.Routes {
			looped = looped || LoopRoute(route)
			targets[route.To] = true
		}
		if r.Fallback.Action == "route" {
			targets[r.Fallback.To] = true
		}
		if !looped && (len(r.Routes) == 1 && r.Fallback.Action == "stop" || len(targets) == 1 && len(r.Routes) > 1) {
			add("P6", "", 3, 0, []string{r.Id}, "router %s has only one real destination", r.Id)
		}
	}
	for _, rule := range w.OnCall {
		if rule.MaxInvocationsPerRun > 3 {
			add("P7", "", 2, 0, []string{rule.AgentId}, "on-call %s may run %d times per run", rule.AgentId, rule.MaxInvocationsPerRun)
		}
		for _, id := range standalone {
			if redundant(profiles[rule.AgentId], profiles[id]) {
				add("P7", "", 2, 0, []string{rule.AgentId, id}, "on-call %s duplicates path agent %s", rule.AgentId, id)
				break
			}
		}
	}
	worst := g.worstCase()
	if worst > 6*pathAgents || worst > 40 {
		add("P8", "", 4, 0, nil, "a run can make up to %d agent invocations for %d path agents", worst, pathAgents)
	}
	if len(w.Agents) > 1 && len(g.writers) == 0 && len(g.gates) == 0 {
		add("P9", "", 5, 0, nil, "%d agents only read; one agent could do this", len(w.Agents))
	}

	// V — verification and feedback.
	if len(g.writers) > 0 && len(g.gates) == 0 {
		add("V1", "major", 10, 0, g.writers, "nothing in the arena reviews changes")
	}
	for _, id := range g.writers {
		if len(g.gatesOf(id)) > 0 && !g.revisable(id) {
			add("V2", "", 4, 0, []string{id}, "a rejected change from %s cannot come back for revision", id)
		}
	}
	for _, r := range w.DecisionRouters {
		for _, route := range r.Routes {
			if !LoopRoute(route) {
				continue
			}
			if route.MaxIterations != nil && *route.MaxIterations == 5 {
				add("V3", "", 1, 0, []string{r.Id}, "loop %s allows the maximum 5 iterations", route.Id)
			}
			if r.InputKind != "review-result" && r.InputKind != "team-result" {
				add("V3", "", 3, 0, []string{r.Id}, "loop %s branches on %s packets instead of a review verdict", route.Id, r.InputKind)
			}
		}
		if r.Fallback.Action == "route" && Has(unreviewed, r.Fallback.To) {
			add("V4", "", 3, 0, []string{r.Id}, "router %s falls back to unreviewed writer %s", r.Id, r.Fallback.To)
		}
	}
	if len(g.writers) > 0 && len(g.gates) > 0 {
		runs := false
		for _, gate := range g.gates {
			for _, id := range g.reviewers(gate) {
				runs = runs || has(profiles[id], "run_commands") && !has(profiles[id], "write")
			}
		}
		if !runs {
			add("V5", "", 3, 0, nil, "no reviewer can run the tests without also being able to write")
		}
	}
	if len(g.writers) > 0 || len(w.Teams) > 0 {
		escalates := false
		for _, rule := range w.OnCall {
			for _, t := range rule.Triggers {
				escalates = escalates || t.Event == "team.no-consensus" || t.Event == "agent.failed" || t.Event == "validation.failed"
			}
		}
		if !escalates {
			add("V6", "", 2, 0, nil, "no on-call agent wakes when a team deadlocks, an agent fails or validation fails")
		}
	}
	for _, id := range g.writers {
		wr, ok := effortRank[g.agents[id].Runtime.ReasoningEffort]
		if !ok {
			continue
		}
		for _, gate := range g.gatesOf(id) {
			for _, r := range g.reviewers(gate) {
				if rr, ok := effortRank[g.agents[r].Runtime.ReasoningEffort]; ok && rr < wr {
					add("V7", "", 1, 0, []string{r, id}, "reviewer %s thinks less (%s) than writer %s (%s)", r, g.agents[r].Runtime.ReasoningEffort, id, g.agents[id].Runtime.ReasoningEffort)
				}
			}
		}
	}

	// C — role clarity.
	for _, a := range w.Agents {
		p := profiles[a.Id]
		if len(strings.Fields(p.Objective)) < 8 || len(strings.Fields(p.Instructions)) < 25 {
			add("C1", "", 2, 8, []string{a.Id}, "%s has a thin objective or instructions", a.Id)
		}
		if has(p, "write") && len(p.Constraints) == 0 {
			add("C2", "", 2, 0, []string{a.Id}, "writer %s has no constraints", a.Id)
		}
		if genericID.MatchString(a.Id) || genericID.MatchString(strings.ToLower(p.Name)) {
			add("C3", "", 1, 0, []string{a.Id}, "%s has a generic name", a.Id)
		}
		for _, capability := range unusable[a.Id] {
			reason, _ := adapterLimit(a.Runtime, capability)
			add("C6", "", 2, 0, []string{a.Id}, "%s has %s, but %s %s", a.Id, capability, a.Runtime.Provider, reason)
		}
	}
	if profiles[w.HeadAgent].Role == "review" {
		add("C4", "", 3, 0, []string{w.HeadAgent}, "head %s is a reviewer with no work to review", w.HeadAgent)
	}
	if strings.TrimSpace(w.Description) == "" {
		add("C5", "", 1, 0, nil, "the arena has no description")
	}

	// R — resilience and cost.
	providers := map[string]bool{}
	for _, a := range w.Agents {
		providers[a.Runtime.Provider] = true
	}
	if len(w.Agents) >= 3 && len(providers) == 1 {
		add("R1", "", 2, 0, nil, "every agent runs on %s", w.Agents[0].Runtime.Provider)
	}
	if len(g.writers) > 0 {
		for _, rule := range w.OnCall {
			event := false
			for _, t := range rule.Triggers {
				event = event || t.Type == "event"
			}
			if !event {
				add("R2", "", 1, 0, []string{rule.AgentId}, "on-call %s only starts when you invoke it", rule.AgentId)
			}
		}
	}
	if fanout > 1 && w.Settings.MaxConcurrentAgents == 1 {
		add("R3", "", 1, 0, nil, "parallel branches run one at a time")
	}
	if worst > 60 {
		add("R4", "", 4, 0, nil, "a run can make up to %d agent invocations", worst)
	}

	return gradeScorecard(findings, g)
}

func gradeScorecard(findings []Finding, g arenaGraph) Scorecard {
	s := Scorecard{Rubric: RubricVersion, Findings: findings}
	lost := map[string]int{}
	ids := map[string]bool{}
	for _, f := range findings {
		lost[f.Category] += f.Points
		ids[f.ID] = true
	}
	ratio := 1.0
	for _, c := range scoreCategories {
		c.Points = max(0, c.Weight-lost[c.ID])
		s.Points += c.Points
		ratio = min(ratio, float64(c.Points)/float64(c.Weight))
		s.Categories = append(s.Categories, c)
	}
	s.Band = gradeFor(s.Points)
	rank := gradeRank(s.Band)
	ceiling := func(id, grade, reason string) {
		if ids[id] && gradeRank(grade) > rank {
			rank = gradeRank(grade)
			s.Ceilings = append(s.Ceilings, reason)
		}
	}
	for _, f := range findings {
		if f.ID == "T5" && f.Severity == "major" {
			ceiling("T5", "C", "auto-if-clean with unreviewed writers caps the grade at C")
		}
	}
	ceiling("T1", "B", "an unreviewed writer caps the grade at B")
	ceiling("T2", "A-", "a coordinator that also writes and runs commands caps the grade at A-")
	ceiling("T3", "A-", "a reviewer that can write caps the grade at A-")
	ceiling("P1", "A", "a redundant agent caps the grade at A")
	ceiling("P2", "A", "a relay agent caps the grade at A")
	// The S tiers need distributed, independent, revisable review.
	sGate := []string{}
	for _, id := range []string{"T1", "T3", "T4", "T6", "P1", "P2", "V2"} {
		if ids[id] {
			sGate = append(sGate, "clear "+id+" ("+scoreCheckTitle[id]+")")
		}
	}
	if ratio < 0.85 {
		sGate = append(sGate, "every category at 85% or more")
	}
	if len(sGate) > 0 && rank <= gradeRank("S-") {
		rank = gradeRank("A+")
	}
	splus := append([]string{}, sGate...)
	if !g.verified() {
		splus = append(splus, "an independent reviewer downstream of the work")
	}
	if ratio < 0.9 {
		splus = append(splus, "every category at 90% or more")
	}
	if len(findings) > 1 || len(findings) == 1 && findings[0].Severity != "minor" {
		splus = append(splus, "at most one finding, and it must be minor")
	}
	if len(splus) > len(sGate) && rank == gradeRank("S+") {
		rank = gradeRank("S")
	}
	s.Grade = gradeBands[rank].grade
	switch {
	case rank == 0:
	case rank <= gradeRank("S-"):
		s.Blockers = splus
	default:
		s.Blockers = sGate
	}
	return s
}

var scoreCheckTitle = map[string]string{"T1": "unreviewed writer", "T3": "reviewer can write", "T4": "same-model review", "T6": "excess capabilities", "P1": "redundant agents", "P2": "relay agent", "V2": "no revision loop"}

// verified reports at least one gate that reviews another agent's work from a
// different provider or model.
func (g arenaGraph) verified() bool {
	for _, id := range append(append([]string{}, g.path...), g.writers...) {
		if Has(g.gates, id) {
			continue
		}
		for _, gate := range g.gatesOf(id) {
			if g.independent(id, gate) {
				return true
			}
		}
	}
	return false
}

// gradeSGR colours a grade: S tiers bright, A and B soft, C and D amber, F red.
func gradeSGR(grade string) string {
	switch grade[:1] {
	case "S":
		return "1;" + sgrSucceeded
	case "A", "B":
		return "1;" + sgrSoft
	case "C", "D":
		return "1;" + sgrAttention
	}
	return "1;" + sgrFailed
}

// TopFindings returns the n costliest findings.
func (s Scorecard) TopFindings(n int) []Finding {
	out := append([]Finding{}, s.Findings...)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Points > out[j].Points })
	return out[:min(n, len(out))]
}

// Summary is the one-line grade shown at creation and on import.
func (s Scorecard) Summary() string {
	line := fmt.Sprintf("Score %s (%d/100, rubric v%d)", s.Grade, s.Points, s.Rubric)
	if top := s.TopFindings(3); len(top) > 0 {
		parts := []string{}
		for _, f := range top {
			parts = append(parts, fmt.Sprintf("%s -%d %s", f.ID, f.Points, f.Message))
		}
		line += " · " + strings.Join(parts, "; ")
	}
	return line
}

// Report is the full breakdown for /score and arena score.
func (s Scorecard) Report() []string {
	lines := []string{fmt.Sprintf("Grade %s · %d/100 · rubric v%d (band %s)", s.Grade, s.Points, s.Rubric, s.Band), ""}
	full, empty := "#", "."
	if unicodeLettering() {
		full, empty = "█", "░"
	}
	for _, c := range s.Categories {
		filled := c.Points * 20 / c.Weight
		lines = append(lines, fmt.Sprintf("%-24s %s%s %2d/%d", c.Name, strings.Repeat(full, filled), strings.Repeat(empty, 20-filled), c.Points, c.Weight))
	}
	for _, c := range s.Ceilings {
		lines = append(lines, "", "Capped: "+c)
	}
	if len(s.Blockers) > 0 {
		next := "The S tiers need"
		if gradeRank(s.Grade) <= gradeRank("S-") {
			next = "S+ needs"
		}
		lines = append(lines, "", next+": "+strings.Join(s.Blockers, "; "))
	}
	if len(s.Findings) == 0 {
		return append(lines, "", "No deductions.")
	}
	lines = append(lines, "", "Deductions")
	for _, f := range s.Findings {
		lines = append(lines, fmt.Sprintf("  %s -%d [%s] %s", f.ID, f.Points, f.Severity, f.Message), "      fix: "+f.Fix)
	}
	return lines
}

func (cli *CLI) score(p *Project, args []string, o Options) error {
	if len(args) != 1 {
		return fmt.Errorf("arena score ID [--json]")
	}
	w, profiles, err := p.Load(args[0])
	if err != nil {
		return err
	}
	s := ScoreArena(w, profiles)
	if o.Bool("json") {
		cli.print(s)
		return nil
	}
	fmt.Fprintln(cli.Out, w.Name+" ("+w.Id+")")
	fmt.Fprintln(cli.Out, strings.Join(s.Report(), "\n"))
	return nil
}
