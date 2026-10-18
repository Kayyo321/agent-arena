package arena

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (cli *CLI) runtimeCommand(ctx context.Context, p *Project, args []string, o Options) error {
	switch args[0] {
	case "adapters":
		cli.print(Manifest())
		return nil
	case "checkpoint":
		if len(args) == 1 || args[1] == "list" {
			paths, err := filepath.Glob(p.Path(".state", "checkpoints", "*.json"))
			if err != nil {
				return err
			}
			for _, path := range paths {
				fmt.Fprintln(cli.Out, strings.TrimSuffix(filepath.Base(path), ".json"))
			}
			return nil
		}
		if len(args) != 3 {
			return fmt.Errorf("checkpoint show|restore ID")
		}
		if args[1] == "show" {
			var c Checkpoint
			if err := ReadJSON(p.Path(".state", "checkpoints", args[2]+".json"), &c); err != nil {
				return err
			}
			cli.print(c)
			return nil
		}
		if args[1] == "restore" {
			if err := cli.confirm("Restore into an isolated recovery worktree", o); err != nil {
				return err
			}
			path, err := p.RestoreCheckpoint(args[2])
			fmt.Fprintln(cli.Out, path)
			return err
		}
		return fmt.Errorf("unknown checkpoint command")
	case "mcp":
		r, err := p.ReadRun(o.Get("run"))
		if err != nil {
			return err
		}
		return ServeMCP(ctx, p, r, o.Get("actor"), cli.In, cli.Out)
	case "run", "resume":
		return cli.execute(ctx, p, args, o)
	case "watch":
		if len(args) != 2 {
			return fmt.Errorf("watch requires run ID")
		}
		r, err := p.ReadRun(args[1])
		if err != nil {
			return err
		}
		if o.Bool("plain") {
			events, err := p.Events(r.ID)
			if err != nil {
				return err
			}
			cli.print(events)
			return nil
		}
		return Watch(ctx, p, r, nil)
	case "changes":
		if len(args) < 2 {
			return fmt.Errorf("changes RUN [CHANGE]")
		}
		if len(args) == 2 {
			changes, err := p.Changes(args[1])
			if err != nil {
				return err
			}
			cli.print(changes)
			return nil
		}
		c, err := p.Change(args[1], args[2])
		if err != nil {
			return err
		}
		patch, err := os.ReadFile(filepath.Join(p.RunDir(args[1]), "changes", c.ID+".patch"))
		if err != nil {
			return err
		}
		fmt.Fprint(cli.Out, Redact(string(patch)))
		return nil
	case "integrate":
		if o.Bool("all") {
			if len(args) != 2 {
				return fmt.Errorf("integrate RUN --all takes no change IDs; leave one out with --skip CHANGE, repeating the flag for each change")
			}
			resolution, err := p.PrepareResolution(args[1], o["skip"])
			if err != nil {
				return err
			}
			cli.print(resolution)
			if o.Bool("dry-run") {
				return nil
			}
			if !resolution.Ready {
				return fmt.Errorf("resolution not ready: %s", blockingResolutionItems(resolution))
			}
			ready := 0
			for _, item := range resolution.Items {
				if item.Status == "ready" {
					ready++
				}
			}
			if err := cli.confirm(fmt.Sprintf("Apply %d changes to the checkout", ready), o); err != nil {
				return err
			}
			applied, err := p.ApplyResolution(args[1], "user", o["skip"])
			cli.print(applied)
			return err
		}
		if len(o["skip"]) > 0 || o.Bool("dry-run") {
			return fmt.Errorf("--skip and --dry-run apply only to integrate RUN --all")
		}
		if len(args) != 3 {
			return fmt.Errorf("integrate RUN CHANGE [--yes]")
		}
		change, err := p.Change(args[1], args[2])
		if err != nil {
			return err
		}
		cli.print(change)
		if err := cli.confirm("Integrate this reviewed change", o); err != nil {
			return err
		}
		change, err = p.Integrate(args[1], args[2], true)
		cli.print(change)
		return err
	case "rollback":
		if len(args) != 3 {
			return fmt.Errorf("rollback RUN CHANGE [--yes]")
		}
		change, err := p.Change(args[1], args[2])
		if err != nil {
			return err
		}
		preview, err := p.RollbackPreview(args[1], args[2])
		if err != nil {
			return err
		}
		cli.print(preview)
		if !o.Bool("yes") {
			fmt.Fprintln(cli.Out, "Preview only. Use --yes after reviewing affected paths and later commits.")
			return nil
		}
		change, err = p.Rollback(args[1], args[2], true)
		cli.print(change)
		return err
	case "on-call":
		if len(args) != 4 || args[1] != "invoke" {
			return fmt.Errorf("on-call invoke RUN AGENT")
		}
		r, err := p.ReadRun(args[2])
		if err != nil {
			return err
		}
		engine := NewEngine(p, r, Providers{})
		event, err := p.Append(r.ID, r.Workflow.Id, "user", "on-call.manual", map[string]any{"agentId": args[3]})
		if err != nil {
			return err
		}
		return engine.onCall(ctx, args[3], event, true)

	case "history":
		if len(args) == 1 {
			ids, e := p.Runs()
			if e != nil {
				return e
			}
			cli.print(ids)
			return nil
		}
		if args[1] == "repair" {
			if len(args) != 3 {
				return fmt.Errorf("history repair requires run ID")
			}
			return p.RepairJournal(args[2])
		}
		id := args[1]
		if len(args) > 2 && args[2] == "retire" {
			if e := cli.confirm("Retire run history and checkpoints into recoverable local storage", o); e != nil {
				return e
			}
			return p.PruneRun(id, o.Bool("including-checkpoints"))
		}
		events, e := p.Events(id)
		if e != nil {
			return e
		}
		if o.Bool("json") {
			history, e := p.History(id, events)
			if e != nil {
				return e
			}
			cli.print(history)
		} else {
			for _, event := range events {
				fmt.Fprintf(cli.Out, "%d %s %s %s\n", event.Sequence, event.At, event.AgentID, event.Type)
			}
		}
		return nil
	case "pause", "cancel":
		if len(args) != 2 {
			return fmt.Errorf("run ID required")
		}
		state := "paused"
		if args[0] == "cancel" {
			state = "cancelled"
		}
		return p.Transition(args[1], state)
	case "decision":
		if len(args) < 3 {
			return fmt.Errorf("decision list RUN | decision answer RUN ID ANSWER")
		}
		if args[1] == "list" {
			d, e := p.Decisions(args[2])
			if e != nil {
				return e
			}
			cli.print(d)
			return nil
		}
		if args[1] == "answer" && len(args) == 5 {
			return p.Answer(args[2], args[3], args[4])
		}
		return fmt.Errorf("invalid decision command")
	case "agent":
		if len(args) < 3 {
			return fmt.Errorf("agent request WORKFLOW [flags] | agent resolve REQUEST --approve")
		}
		if args[1] == "resolve" {
			if e := validID(args[2]); e != nil {
				return e
			}
			var q CoworkerRequest
			if e := ReadJSON(p.Path(".state", "coworker-requests", args[2]+".json"), &q); e != nil {
				return e
			}
			if !o.Bool("approve") {
				return fmt.Errorf("explicit --approve is required to resolve a pending request")
			}
			q, e := p.RequestCoworker(ctx, q, true, Providers{})
			cli.print(q)
			return e
		}
		if args[1] != "request" {
			return fmt.Errorf("unknown agent command")
		}
		s, e := p.Settings()
		if e != nil {
			return e
		}
		r := Runtime{}
		if s.ApprovalPolicies.ArenaSubagentModificationInquiry != "auto-refuse" {
			r, e = cli.runtimeChoice(ctx, p, r, o)
			if e != nil {
				return e
			}
		}
		q := CoworkerRequest{WorkflowID: args[2], From: o.Get("from"), AgentID: o.Get("id"), Role: o.Get("role"), Objective: o.Get("objective"), Reason: o.Get("reason"), Runtime: r, Capabilities: split(o.Get("capabilities")), WorkspaceMode: o.Get("workspace-mode"), Files: split(o.Get("files")), RunID: o.Get("run")}
		q, e = p.RequestCoworker(ctx, q, false, Providers{})
		cli.print(q)
		return e
	case "context-request":
		if len(args) != 2 {
			return fmt.Errorf("context-request REQUEST --approve")
		}
		if !o.Bool("approve") {
			return fmt.Errorf("explicit --approve required")
		}
		if e := validID(args[1]); e != nil {
			return e
		}
		path := p.Path(".state", "context-requests", args[1]+".json")
		var pending struct {
			Proposal ContextProposal `json:"proposal"`
			Actor    string          `json:"actor"`
			Provider string          `json:"provider"`
			Run      string          `json:"run"`
			Task     string          `json:"task"`
		}
		if e := ReadJSON(path, &pending); e != nil {
			return e
		}
		result, e := p.ProposeContext(pending.Proposal, pending.Actor, pending.Provider, pending.Run, pending.Task, true)
		if e != nil {
			return e
		}
		if e := os.Rename(path, filepath.Join(filepath.Dir(path), args[1]+".resolved")); e != nil {
			return e
		}
		cli.print(result)
		return nil
	}
	return fmt.Errorf("unknown or not yet implemented command %q; see arena --help", args[0])
}

// blockingResolutionItems names the items that keep a resolution from being
// applied as a set, for the error returned by integrate RUN --all.
func blockingResolutionItems(r Resolution) string {
	parts := []string{}
	for _, item := range r.Items {
		if item.Status == "ready" || item.Status == "skipped" {
			continue
		}
		part := item.ChangeID + " " + item.Status
		if item.Reason != "" {
			part += " (" + item.Reason + ")"
		}
		parts = append(parts, part)
	}
	if len(parts) == 0 {
		if r.Reason != "" {
			return r.Reason
		}
		return "no ready changes"
	}
	return strings.Join(parts, "; ")
}
