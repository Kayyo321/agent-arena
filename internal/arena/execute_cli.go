package arena

import (
	"context"
	"fmt"
	"os"
	"strings"

	"github.com/charmbracelet/x/term"
)

func (cli *CLI) execute(ctx context.Context, p *Project, args []string, o Options) error {
	var r Run
	var err error
	var readiness []Readiness
	resume := args[0] == "resume"
	if resume {
		if len(args) != 2 {
			return fmt.Errorf("resume requires run ID")
		}
		r, err = p.ReadRun(args[1])
		if err != nil {
			return err
		}
		if r.Objective == "" {
			return fmt.Errorf("retention omitted the original task; start a new run with an explicit task")
		}
		if RunAlive(p, r.ID) {
			return p.Transition(r.ID, "resumed")
		}
	} else {
		if len(args) < 3 {
			return fmt.Errorf("run WORKFLOW TASK is required")
		}
		r, readiness, err = prepareNewExecution(ctx, p, args[1], strings.Join(args[2:], " "))
		if err != nil {
			return err
		}
	}
	if resume {
		readiness, err = executionReadiness(ctx, p, r)
		if err != nil {
			return err
		}
		if _, err := p.PrepareBaseline(r); err != nil {
			return err
		}
	}
	fmt.Fprintln(cli.Out, "Run", r.ID, "retention:", r.Retention, "— worktrees and history stay local")
	engine := NewEngine(p, r, Providers{})
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	done := make(chan error, 1)
	events := make(chan Event, 32)
	plain := o.Bool("plain")
	if file, ok := cli.Out.(*os.File); !ok || !term.IsTerminal(file.Fd()) {
		plain = true
	}
	if plain {
		engine.Observe = func(e Event) { events <- e }
	}
	go func() { done <- engine.Execute(runCtx, resume, o.Bool("retry-interrupted")) }()
	if !plain {
		watchErr := Watch(runCtx, p, r, readiness)
		cancel()
		runErr := <-done
		if runErr != nil {
			return runErr
		}
		return watchErr
	}
	for {
		select {
		case event := <-events:
			fmt.Fprintf(cli.Out, "%04d %s %s\n", event.Sequence, event.AgentID, event.Type)
			if event.Type == "decision.requested" {
				fmt.Fprintln(cli.Out, "Pending decision: use arena decision list", r.ID, "then arena decision answer", r.ID, "ID ANSWER")
			}
		case err := <-done:
			for len(events) > 0 {
				event := <-events
				fmt.Fprintf(cli.Out, "%04d %s %s\n", event.Sequence, event.AgentID, event.Type)
			}
			return err
		}
	}
}

// Both CLI and TUI starts use the same readiness, policy and isolation checks.
// Opening a graph never calls this function or creates a run.
func prepareNewExecution(ctx context.Context, p *Project, workflow, objective string) (Run, []Readiness, error) {
	if strings.TrimSpace(objective) == "" {
		return Run{}, nil, fmt.Errorf("task objective is required")
	}
	w, profiles, err := p.Load(workflow)
	if err != nil {
		return Run{}, nil, err
	}
	s, err := p.Settings()
	if err != nil {
		return Run{}, nil, err
	}
	head, err := Git(p.Root, "rev-parse", "HEAD")
	if err != nil {
		return Run{}, nil, fmt.Errorf("commit the initial repository state before running workers: %w", err)
	}
	readiness, err := executionReadiness(ctx, p, Run{Workflow: w, Profiles: profiles, Settings: s})
	if err != nil {
		return Run{}, readiness, err
	}
	if err := ctx.Err(); err != nil {
		return Run{}, readiness, err
	}
	r, err := p.CreateRun(w, profiles, s, objective, head)
	if err != nil {
		return r, readiness, err
	}
	if _, err := p.PrepareBaseline(r); err != nil {
		_, _ = p.Append(r.ID, w.Id, "", "workflow.failed", map[string]any{"reason": err.Error()})
		return r, readiness, err
	}
	return r, readiness, nil
}

func executionReadiness(ctx context.Context, p *Project, r Run) ([]Readiness, error) {
	readiness := []Readiness{}
	seen := map[string]bool{}
	for _, runtime := range Required([]Workflow{r.Workflow}, r.Settings) {
		key := string(JSON(runtime))
		if seen[key] {
			continue
		}
		seen[key] = true
		status := Probe(ctx, p.Root, runtime)
		readiness = append(readiness, status)
		if !status.CanStart {
			return readiness, fmt.Errorf("%s / %s is not ready: %s (%s)", runtime.Provider, runtime.Model, status.Detail, status.Auth)
		}
	}
	for _, a := range r.Workflow.Agents {
		native := a.Runtime.Provider == "codex" || a.Runtime.Provider == "antigravity" || a.Runtime.Provider == "claude-code" && claudeSignIn(a.Runtime)
		if native && r.Settings.ApprovalPolicies.ShellCommandExecution != "adapter-default" {
			return readiness, fmt.Errorf("%s cannot enforce Arena's %s shell policy with its CLI interface; use adapter-default or a mediated adapter", a.Runtime.Provider, r.Settings.ApprovalPolicies.ShellCommandExecution)
		}
	}
	for _, profile := range r.Profiles {
		if !Has(profile.Capabilities, "run_commands") {
			continue
		}
		for _, a := range r.Workflow.Agents {
			if a.Id != profile.Id {
				continue
			}
			if a.Runtime.Provider == "antigravity" && !antigravityBlanket(a.Runtime) {
				return readiness, fmt.Errorf("%s runs on Antigravity, whose headless mode cannot run commands without blanket approval; remove run_commands, opt in with runtime.options.commandApproval %q, or choose another provider", a.Id, antigravityBlanketApproval)
			}
			if a.Runtime.Provider != "codex" && !claudeSignIn(a.Runtime) && !sandboxAvailable() {
				return readiness, fmt.Errorf("%s needs a supported OS command sandbox before launch", a.Id)
			}
		}
	}
	return readiness, nil
}
