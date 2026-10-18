package arena

import (
	"fmt"
	"strings"
)

// walkthroughInstructions guide setup for a user who arrives without a
// design: learn the project, offer distinct ideas, and draft nothing until
// the user picks or combines one.
const walkthroughInstructions = `Walkthrough: the user has no design yet and asked you to walk them through one. Do not draft an arena yet.
1. Ask about the project, its goals and its pain points, together in one message. Skip anything the conversation already answers, and inspect the project files when that answers it faster.
2. Once you understand them, return complete=false with two or three distinct arena ideas in ideasJson: a JSON array of {"title", "pitch", "builderRequestJson"}, where pitch is one or two sentences on what the idea is good and bad at and builderRequestJson is a complete workflow and profiles following the schemas. Make the ideas differ in shape (for example one agent, a builder with a reviewer on another provider, or a review team) and keep each as small as its job allows. In question, ask the user to pick one or combine parts of several. Arena validates each idea and shows the user its roles, providers, rough cost and grade, so do not repeat them in question.
3. Idea runtimes are only suggestions to compare shapes: use the model catalog for the control provider and name other providers plainly. Never set an Antigravity commandApproval in an idea.
Never return complete=true during the walkthrough; the user picks first.`

// walkthroughPickedInstructions follow the user's reply to the ideas.
const walkthroughPickedInstructions = `Walkthrough: you proposed the arena ideas recorded in the conversation, and the user's latest message picks, combines or asks about them. If they asked a question, answer it and ask them to pick. Otherwise continue the normal interview for the idea they chose: an idea's runtimes were suggestions, so ask for every provider, model and effort the user has not chosen or delegated, then return the complete draft.`

// walkthroughIdea is one arena the orchestrator offered during a walkthrough,
// validated and scored by Arena.
type walkthroughIdea struct {
	Title, Pitch string
	Draft        WorkflowDraft
	Score        Scorecard
}

func decodeIdeas(p *Project, s Settings, text string) ([]walkthroughIdea, error) {
	if len(text) > 2<<20 {
		return nil, fmt.Errorf("walkthrough ideas exceed 2 MiB")
	}
	var raw []struct {
		Title              string `json:"title"`
		Pitch              string `json:"pitch"`
		BuilderRequestJson string `json:"builderRequestJson"`
	}
	if err := DecodeJSON([]byte(text), &raw); err != nil {
		return nil, fmt.Errorf("invalid ideasJson: %w", err)
	}
	if len(raw) < 2 || len(raw) > 3 {
		return nil, fmt.Errorf("a walkthrough offers two or three ideas, got %d", len(raw))
	}
	ideas, seen := []walkthroughIdea{}, map[string]bool{}
	for i, idea := range raw {
		draft, err := decodeWorkflowDraft(p, s, idea.BuilderRequestJson)
		if err != nil {
			return nil, fmt.Errorf("idea %d: %w", i+1, err)
		}
		if agents := newBlanketApproval(draft.Workflow, Workflow{}); len(agents) > 0 {
			return nil, fmt.Errorf("idea %d sets Antigravity commandApproval %q on %s; only the user chooses it", i+1, antigravityBlanketApproval, strings.Join(agents, ", "))
		}
		if seen[draft.Workflow.Id] {
			return nil, fmt.Errorf("idea %d reuses workflow ID %s", i+1, draft.Workflow.Id)
		}
		seen[draft.Workflow.Id] = true
		title := strings.TrimSpace(idea.Title)
		if title == "" {
			title = draft.Workflow.Name
		}
		ideas = append(ideas, walkthroughIdea{Redact(title), Redact(strings.TrimSpace(idea.Pitch)), *draft, ScoreArena(draft.Workflow, draft.Profiles)})
	}
	return ideas, nil
}

// newBlanketApproval lists the agents in w that opt into Antigravity's
// sandboxed blanket approval without already having it in saved. Only the
// user turns it on; an orchestrator draft that adds it is refused.
func newBlanketApproval(w, saved Workflow) []string {
	had := map[string]bool{}
	for _, a := range saved.Agents {
		had[a.Id] = antigravityBlanket(a.Runtime)
	}
	out := []string{}
	for _, a := range w.Agents {
		if antigravityBlanket(a.Runtime) && !had[a.Id] {
			out = append(out, a.Id)
		}
	}
	return out
}

// Lines describes an idea for the chat: its roles, suggested providers,
// rough cost and grade.
func (idea walkthroughIdea) Lines(n int) []string {
	w := idea.Draft.Workflow
	lines := []string{fmt.Sprintf("Idea %d · %s", n, idea.Title)}
	if idea.Pitch != "" {
		lines = append(lines, "  "+idea.Pitch)
	}
	roles, providers := []string{}, []string{}
	for _, a := range w.Agents {
		role := idea.Draft.Profiles[a.Id].Role
		if role == "" || role == a.Id {
			roles = append(roles, a.Id)
		} else {
			roles = append(roles, a.Id+" ("+role+")")
		}
		if !Has(providers, a.Runtime.Provider) {
			providers = append(providers, a.Runtime.Provider)
		}
	}
	lines = append(lines,
		"  roles: "+strings.Join(roles, ", "),
		"  providers (suggested): "+strings.Join(providers, ", "),
		"  rough cost: "+roughCost(w),
		fmt.Sprintf("  grade: %s (%d/100)", idea.Score.Grade, idea.Score.Points))
	return lines
}

// roughCost estimates one run's spend from its agent turns weighted by
// reasoning effort. A pass runs every path agent and team member once, and
// a fan-out's worker up to maxChildren times; each loop route can rerun its
// target's subgraph up to maxIterations more times. On-call agents run only
// when triggered and are left out.
func roughCost(w Workflow) string {
	weight := func(effort string) int {
		switch effort {
		case "minimal", "low":
			return 1
		case "high":
			return 3
		case "xhigh", "max":
			return 4
		}
		return 2
	}
	agents, members := map[string]Agent{}, map[string][]string{}
	for _, a := range w.Agents {
		if a.Activation != "on-call" {
			agents[a.Id] = a
		}
	}
	for _, t := range w.Teams {
		members[t.Id] = t.Members
	}
	copies := map[string]int{}
	for _, f := range w.FanOuts {
		members[f.Id] = []string{f.Planner, f.Worker}
		copies[f.Worker] = f.MaxChildren
	}
	// runs is how many instances of an agent one pass can start.
	runs := func(id string) int { return max(1, copies[id]) }
	turns, units := 0, 0
	for id, a := range agents {
		turns += runs(id)
		units += runs(id) * weight(a.Runtime.ReasoningEffort)
	}
	// below collects the agents a rerun from id would start again.
	below := func(id string) map[string]bool {
		seen, queue := map[string]bool{}, []string{id}
		for len(queue) > 0 {
			next := queue[0]
			queue = queue[1:]
			if seen[next] {
				continue
			}
			seen[next] = true
			queue = append(queue, members[next]...)
			for _, edge := range w.Relationships {
				if edge.Type == "delegates" && edge.From == next {
					queue = append(queue, edge.To)
				}
			}
		}
		return seen
	}
	maxTurns, maxUnits := turns, units
	for _, r := range w.DecisionRouters {
		for _, route := range r.Routes {
			if !LoopRoute(route) || route.MaxIterations == nil {
				continue
			}
			for id := range below(route.To) {
				if a, ok := agents[id]; ok {
					maxTurns += *route.MaxIterations * runs(id)
					maxUnits += *route.MaxIterations * runs(id) * weight(a.Runtime.ReasoningEffort)
				}
			}
		}
	}
	label := "high"
	switch {
	case maxUnits <= 4:
		label = "low"
	case maxUnits <= 12:
		label = "moderate"
	}
	text := fmt.Sprintf("%s · %d agent turn%s per run", label, turns, plural(turns))
	if maxTurns > turns {
		text += fmt.Sprintf(", up to %d with revisions", maxTurns)
	}
	return text
}

// showIdeas lists the walkthrough's ideas in the chat and records them in the
// setup conversation; the user's next message picks or combines them.
func (m *LiveModel) showIdeas(ideas []walkthroughIdea) {
	lines, recorded := []string{}, []map[string]any{}
	for i, idea := range ideas {
		lines = append(append(lines, idea.Lines(i+1)...), "")
		recorded = append(recorded, map[string]any{"idea": i + 1, "title": idea.Title, "pitch": idea.Pitch, "grade": idea.Score.Grade, "roughCost": roughCost(idea.Draft.Workflow), "builderRequestJson": WorkflowDraft{Workflow: idea.Draft.Workflow, Profiles: idea.Draft.Profiles}})
	}
	lines = append(lines, "Pick an idea by number, or combine them (\"1, with the reviewer from 2\"). Providers above are suggestions: the orchestrator asks for every provider, model and effort you have not chosen before it drafts.")
	m.Chat = append(m.Chat, chatLine{"Arena", strings.Join(lines, "\n")})
	m.Transcript += "\nArena: walkthrough ideas shown to the user:\n" + Redact(string(JSON(recorded)))
	m.Walkthrough = setupWalkthroughPicked
}
