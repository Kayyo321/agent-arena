package arena

import (
	"context"
	"fmt"
	"strings"

	c "github.com/Kayyo321/agent-arena/internal/contract"
)

func (cli *CLI) buildWorkflow(ctx context.Context, p *Project, w Workflow, profiles map[string]Profile) (Workflow, map[string]Profile, error) {
	fmt.Fprintln(cli.Out, "Guided builder. Changes stay in memory until the graph validates and you confirm its preview.")
	for {
		fmt.Fprintln(cli.Out, GraphSummary(w))
		action, e := cli.ask("Action (agent, remove-agent, head, team, fanout, router, link, remove-link, on-call, policy, finish)", "", true)
		if e != nil {
			return w, profiles, e
		}
		switch action {
		case "finish":
			return w, profiles, nil
		case "agent":
			id, e := cli.ask("Agent ID (existing ID edits it)", "", true)
			if e != nil {
				return w, profiles, e
			}
			if e := validID(id); e != nil {
				return w, profiles, e
			}
			a, _ := agentByID(w, id)
			a.Id = id
			a.Profile = ".agents/" + w.Id + "/" + id + ".agent.json"
			role := profiles[id]
			activation, e := cli.ask("Activation (path, on-call)", a.Activation, true)
			if e != nil {
				return w, profiles, e
			}
			a.Activation = activation
			if activation == "path" {
				level := 0
				if a.Level != nil {
					level = *a.Level
				}
				n, e := cli.integer("Graph level", level, 0, 1000)
				if e != nil {
					return w, profiles, e
				}
				a.Level = &n
			} else {
				a.Level = nil
			}
			a.Runtime, e = cli.runtimeChoice(ctx, p, a.Runtime, Options{})
			if e != nil {
				return w, profiles, e
			}
			role.Id = id
			role.SchemaVersion = 1
			role.Name, e = cli.ask("Display name", role.Name, false)
			if e != nil {
				return w, profiles, e
			}
			if role.Name == "" {
				role.Name = id
			}
			role.Role, e = cli.ask("Role (e.g. coordinator, implementation, research, review)", role.Role, true)
			if e != nil {
				return w, profiles, e
			}
			role.Objective, e = cli.ask("Role objective", role.Objective, true)
			if e != nil {
				return w, profiles, e
			}
			role.Instructions, e = cli.ask("Role instructions", role.Instructions, true)
			if e != nil {
				return w, profiles, e
			}
			caps, e := cli.ask("Capabilities, comma-separated (read, write, run_commands, research, review)", strings.Join(role.Capabilities, ","), true)
			if e != nil {
				return w, profiles, e
			}
			role.Capabilities = split(caps)
			constraints, e := cli.ask("Constraints, separated with semicolons", strings.Join(role.Constraints, ";"), false)
			if e != nil {
				return w, profiles, e
			}
			role.Constraints = []string{}
			if constraints != "" {
				role.Constraints = strings.Split(constraints, ";")
			}
			if role.Role == "review" {
				role.ReviewContract = NewProfile(id, "review", role.Objective, role.Instructions, role.Capabilities).ReviewContract
				fmt.Fprintln(cli.Out, "Review outcomes: Approved; ChangesRequested; Deny; NeedsMoreInformation")
			} else {
				role.ReviewContract = nil
			}
			profiles[id] = role
			found := false
			for i := range w.Agents {
				if w.Agents[i].Id == id {
					w.Agents[i] = a
					found = true
				}
			}
			if !found {
				w.Agents = append(w.Agents, a)
			}
		case "remove-agent":
			id, e := cli.ask("Agent ID to remove from draft (saved profile remains recoverable)", "", true)
			if e != nil {
				return w, profiles, e
			}
			a := []Agent{}
			for _, v := range w.Agents {
				if v.Id != id {
					a = append(a, v)
				}
			}
			w.Agents = a
		case "head":
			w.HeadAgent, e = cli.ask("Head path-agent ID", w.HeadAgent, true)
			if e != nil {
				return w, profiles, e
			}
		case "team":
			id, e := cli.ask("Team ID (existing ID replaces)", "", true)
			if e != nil {
				return w, profiles, e
			}
			t := Team{Id: id}
			for _, v := range w.Teams {
				if v.Id == id {
					t = v
				}
			}
			t.Name, e = cli.ask("Team name", t.Name, true)
			if e != nil {
				return w, profiles, e
			}
			members, e := cli.ask("Member IDs, comma-separated", strings.Join(t.Members, ","), true)
			if e != nil {
				return w, profiles, e
			}
			t.Members = split(members)
			t.Facilitator, e = cli.ask("Facilitator member ID", t.Facilitator, true)
			if e != nil {
				return w, profiles, e
			}
			verdicts, e := cli.ask("Allowed verdict labels, comma-separated", strings.Join(t.OutputContract.Verdicts, ","), true)
			if e != nil {
				return w, profiles, e
			}
			t.OutputContract = c.WorkflowTeamOutputContract{Format: "arena.team-verdict.v1", Verdicts: split(verdicts)}
			rounds := t.Consensus.MaxRounds
			if rounds == 0 {
				rounds = 2
			}
			t.Consensus.MaxRounds, e = cli.integer("Maximum deliberation rounds", rounds, 1, 5)
			if e != nil {
				return w, profiles, e
			}
			t.Consensus.Mode = "unanimous-deliberation"
			found := false
			for i := range w.Teams {
				if w.Teams[i].Id == id {
					w.Teams[i] = t
					found = true
				}
			}
			if !found {
				w.Teams = append(w.Teams, t)
			}
		case "fanout":
			id, e := cli.ask("Fan-out ID (existing ID edits it)", "", true)
			if e != nil {
				return w, profiles, e
			}
			s, e := p.Settings()
			if e != nil {
				return w, profiles, e
			}
			f := FanOut{Id: id, MaxChildren: min(4, fanOutLimit(s)), Approval: "required"}
			for _, v := range w.FanOuts {
				if v.Id == id {
					f = v
				}
			}
			f.Name, e = cli.ask("Fan-out name", f.Name, true)
			if e != nil {
				return w, profiles, e
			}
			f.Planner, e = cli.ask("Planner agent ID (splits the task into items; runs read-only)", f.Planner, true)
			if e != nil {
				return w, profiles, e
			}
			f.Worker, e = cli.ask("Worker agent ID (one copy runs per approved item)", f.Worker, true)
			if e != nil {
				return w, profiles, e
			}
			f.MaxChildren, e = cli.integer("Maximum children per pass", f.MaxChildren, 1, min(32, fanOutLimit(s)))
			if e != nil {
				return w, profiles, e
			}
			if f.Approval == "" {
				f.Approval = "required"
			}
			f.Approval, e = cli.ask("Approval (required: you approve each plan; auto-within-cap: plans within the cap start unasked)", f.Approval, true)
			if e != nil {
				return w, profiles, e
			}
			found := false
			for i := range w.FanOuts {
				if w.FanOuts[i].Id == id {
					w.FanOuts[i] = f
					found = true
				}
			}
			if !found {
				w.FanOuts = append(w.FanOuts, f)
			}
			fmt.Fprintf(cli.Out, "Delegate work to %s and from %s with link; %s and %s take no links of their own.\n", id, id, f.Planner, f.Worker)
		case "router":
			id, e := cli.ask("Router ID (existing ID replaces)", "", true)
			if e != nil {
				return w, profiles, e
			}
			r := Router{Id: id}
			r.Name, e = cli.ask("Router name", "", true)
			if e != nil {
				return w, profiles, e
			}
			r.InputKind, e = cli.ask("Input packet kind (review-result, team-result, fanout-result, result, error, context-result)", "", true)
			if e != nil {
				return w, profiles, e
			}
			r.PayloadPointer, e = cli.ask("Payload JSON pointer (e.g. /outcome)", "", true)
			if e != nil {
				return w, profiles, e
			}
			count, e := cli.integer("Number of exact-match branches", 1, 1, 100)
			if e != nil {
				return w, profiles, e
			}
			for i := 0; i < count; i++ {
				branch := c.WorkflowDecisionRoute{}
				branch.Id, e = cli.ask("Branch ID", "", true)
				if e != nil {
					return w, profiles, e
				}
				raw, e := cli.ask("Exact scalar value as JSON (e.g. \"Approved\", true, 3, null)", "", true)
				if e != nil {
					return w, profiles, e
				}
				if e = DecodeJSON([]byte(raw), &branch.Equals); e != nil {
					return w, profiles, e
				}
				branch.To, e = cli.ask("Branch target node ID", "", true)
				if e != nil {
					return w, profiles, e
				}
				r.Routes = append(r.Routes, branch)
			}
			r.Fallback.Action, e = cli.ask("Fallback (ask-user, stop, route)", "", true)
			if e != nil {
				return w, profiles, e
			}
			if r.Fallback.Action == "route" {
				r.Fallback.To, e = cli.ask("Fallback target ID", "", true)
				if e != nil {
					return w, profiles, e
				}
			}
			found := false
			for i := range w.DecisionRouters {
				if w.DecisionRouters[i].Id == id {
					w.DecisionRouters[i] = r
					found = true
				}
			}
			if !found {
				w.DecisionRouters = append(w.DecisionRouters, r)
			}
		case "link":
			r := Relationship{}
			r.Type, e = cli.ask("Link type (delegates, communicates)", "", true)
			if e != nil {
				return w, profiles, e
			}
			r.From, e = cli.ask("From node ID", "", true)
			if e != nil {
				return w, profiles, e
			}
			r.To, e = cli.ask("To node ID", "", true)
			if e != nil {
				return w, profiles, e
			}
			if r.Type == "communicates" {
				r.Direction, e = cli.ask("Direction (one-way, two-way)", "", true)
				if e != nil {
					return w, profiles, e
				}
			}
			w.Relationships = append(w.Relationships, r)
		case "remove-link":
			from, e := cli.ask("From ID", "", true)
			if e != nil {
				return w, profiles, e
			}
			to, e := cli.ask("To ID", "", true)
			if e != nil {
				return w, profiles, e
			}
			links := []Relationship{}
			for _, r := range w.Relationships {
				if r.From != from || r.To != to {
					links = append(links, r)
				}
			}
			w.Relationships = links
		case "on-call":
			rule := c.WorkflowOnCallRule{}
			rule.AgentId, e = cli.ask("On-call agent ID", "", true)
			if e != nil {
				return w, profiles, e
			}
			rule.MaxInvocationsPerRun, e = cli.integer("Maximum invocations per run", 1, 1, 100)
			if e != nil {
				return w, profiles, e
			}
			count, e := cli.integer("Number of triggers", 1, 1, 100)
			if e != nil {
				return w, profiles, e
			}
			for i := 0; i < count; i++ {
				t := Trigger{}
				t.Type, e = cli.ask("Trigger type (manual, event)", "", true)
				if e != nil {
					return w, profiles, e
				}
				if t.Type == "event" {
					t.Event, e = cli.ask("Event (team.no-consensus, agent.failed, validation.failed, change.touches-paths)", "", true)
					if e != nil {
						return w, profiles, e
					}
					switch t.Event {
					case "team.no-consensus":
						t.TeamId, e = cli.ask("Team ID", "", true)
					case "agent.failed":
						t.AgentId, e = cli.ask("Source agent ID", "", true)
					case "change.touches-paths":
						var paths string
						paths, e = cli.ask("Path globs, comma-separated", "", true)
						t.Paths = split(paths)
					}
					if e != nil {
						return w, profiles, e
					}
				}
				rule.Triggers = append(rule.Triggers, t)
			}
			found := false
			for i := range w.OnCall {
				if w.OnCall[i].AgentId == rule.AgentId {
					w.OnCall[i] = rule
					found = true
				}
			}
			if !found {
				w.OnCall = append(w.OnCall, rule)
			}
		case "policy":
			w.Settings.MaxConcurrentAgents, e = cli.integer("Maximum concurrent agents", w.Settings.MaxConcurrentAgents, 1, 64)
			if e != nil {
				return w, profiles, e
			}
			w.Settings.Integration.Policy, e = cli.ask("Integration policy (human-review, auto-if-clean)", w.Settings.Integration.Policy, true)
			if e != nil {
				return w, profiles, e
			}
			w.Settings.Audit.Retention, e = cli.ask("Run retention (full-local, redacted-local)", w.Settings.Audit.Retention, true)
			if e != nil {
				return w, profiles, e
			}
		default:
			fmt.Fprintln(cli.Out, "Unknown action; choose one of the listed actions.")
		}
	}
}
func GraphSummary(w Workflow) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Arena %s (%s), head %s\n", w.Name, w.Id, w.HeadAgent)
	for _, a := range w.Agents {
		rail := "path"
		if a.Activation == "on-call" {
			rail = "on-call"
		}
		level := "-"
		if a.Level != nil {
			level = fmt.Sprint(*a.Level)
		}
		fmt.Fprintf(&b, "  [%s] %s | %s / %s / %s | level %s\n", a.Id, rail, a.Runtime.Provider, a.Runtime.Model, a.Runtime.ReasoningEffort, level)
	}
	for _, t := range w.Teams {
		fmt.Fprintf(&b, "  [team %s] members %s; unanimous, max %d rounds; verdicts %s\n", t.Id, strings.Join(t.Members, ", "), t.Consensus.MaxRounds, strings.Join(t.OutputContract.Verdicts, ", "))
	}
	for _, f := range w.FanOuts {
		approval := f.Approval
		if approval == "" {
			approval = "required"
		}
		fmt.Fprintf(&b, "  [fan-out %s] planner %s, worker %s, max %d children; approval %s\n", f.Id, f.Planner, f.Worker, f.MaxChildren, approval)
	}
	for _, r := range w.Relationships {
		arrow := "--delegates-->"
		if r.Type == "communicates" {
			arrow = "..peer..>"
			if r.Direction == "two-way" {
				arrow = "<..peer..>"
			}
		}
		fmt.Fprintf(&b, "  %s %s %s\n", r.From, arrow, r.To)
	}
	for _, r := range w.DecisionRouters {
		fmt.Fprintf(&b, "  [router %s] %s %s\n", r.Id, r.InputKind, r.PayloadPointer)
		for _, branch := range r.Routes {
			fmt.Fprintf(&b, "    = %s --> %s\n", strings.TrimSpace(string(JSON(branch.Equals))), branch.To)
		}
		fmt.Fprintf(&b, "    fallback %s %s\n", r.Fallback.Action, r.Fallback.To)
	}
	for _, rule := range w.OnCall {
		fmt.Fprintf(&b, "  on-call %s, cap %d: %s\n", rule.AgentId, rule.MaxInvocationsPerRun, strings.TrimSpace(string(JSON(rule.Triggers))))
	}
	fmt.Fprintf(&b, "  Policy: %s; git-worktree-per-task; concurrency %d; retention %s\n", w.Settings.Integration.Policy, w.Settings.MaxConcurrentAgents, w.Settings.Audit.Retention)
	return b.String()
}
