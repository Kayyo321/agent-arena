package arena

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// tmuxCommands lays a run out in the user's own tmux. Window 0, pane 0 is the
// supervisor, the process that executes the run; ending it interrupts the run,
// as for any attached supervisor. Every other pane is an observer
// (`arena watch --json --follow`, `arena view --follow`) that only reads the
// journal, so closing one never stops the run.
//
// Each command is an argv for tmux. tmux runs a multi-word shell-command
// directly, without a shell, so no user text is ever parsed as shell syntax.
// The run is reserved before the layout so every pane can name it.
func tmuxCommands(exe, root, session string, supervisor []string, runID string, agents []string) [][]string {
	pane := func(args ...string) []string { return append([]string{"--", exe}, args...) }
	target := session + ":"
	cmds := [][]string{
		append([]string{"new-session", "-d", "-s", session, "-n", "run", "-c", root}, pane(append([]string{"--project", root}, supervisor...)...)...),
		{"set-option", "-t", session, "remain-on-exit", "on"},
		append([]string{"split-window", "-h", "-t", target + "0", "-c", root}, pane("watch", runID, "--json", "--follow", "--project", root)...),
	}
	if len(agents) > 0 {
		for i, id := range agents {
			args := pane("view", "--run", runID, "--follow", "--project", root, "--", id)
			if i == 0 {
				cmds = append(cmds, append([]string{"new-window", "-t", target, "-n", "agents", "-c", root}, args...))
			} else {
				cmds = append(cmds, append([]string{"split-window", "-t", target + "1", "-c", root}, args...))
			}
			cmds = append(cmds, []string{"select-layout", "-t", target + "1", "tiled"})
		}
	}
	return append(cmds, []string{"select-window", "-t", target + "0"}, []string{"select-pane", "-t", target + "0.0"})
}

func tmuxFinish(session string, inside bool) []string {
	if inside {
		return []string{"switch-client", "-t", session}
	}
	return []string{"attach-session", "-t", session}
}

func (cli *CLI) tmux(ctx context.Context, p *Project, args []string, o Options) error {
	usage := fmt.Errorf("tmux run WORKFLOW TASK | tmux resume RUN [--retry-interrupted]")
	if len(args) < 3 || args[1] != "run" && args[1] != "resume" || args[1] == "resume" && len(args) != 3 || args[1] == "run" && len(args) < 4 {
		return usage
	}
	bin, err := exec.LookPath("tmux")
	if err != nil {
		return fmt.Errorf("tmux is not installed or not on PATH; `arena tmux` is optional and Arena never installs it. Install tmux with your package manager, or use `arena run` directly")
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	var r Run
	var supervisor []string
	if args[1] == "run" {
		task := strings.Join(args[3:], " ")
		if r, _, err = prepareNewExecution(ctx, p, args[2], task); err != nil {
			return err
		}
		supervisor = []string{"run", "--run", r.ID, "--", args[2], task}
	} else {
		if r, err = p.ReadRun(args[2]); err != nil {
			return err
		}
		if RunAlive(p, r.ID) {
			return fmt.Errorf("run %s already has a live supervisor; attach to its terminal or use arena watch %s", r.ID, r.ID)
		}
		supervisor = []string{"resume", r.ID}
		if o.Bool("retry-interrupted") {
			supervisor = append(supervisor, "--retry-interrupted")
		}
	}
	agents := []string{}
	for _, a := range r.Workflow.Agents {
		agents = append(agents, a.Id)
	}
	session := "arena-" + r.ID
	for _, argv := range tmuxCommands(exe, p.Root, session, supervisor, r.ID, agents) {
		if out, err := exec.CommandContext(ctx, bin, argv...).CombinedOutput(); err != nil {
			_ = exec.Command(bin, "kill-session", "-t", session).Run()
			return fmt.Errorf("tmux %s failed for run %s: %v: %s", argv[0], r.ID, err, strings.TrimSpace(string(out)))
		}
	}
	fmt.Fprintf(cli.Out, "Run %s laid out in tmux session %s; pane 0 supervises the run (ending it interrupts the run), the other panes only observe\n", r.ID, session)
	finish := exec.CommandContext(ctx, bin, tmuxFinish(session, os.Getenv("TMUX") != "")...)
	finish.Stdin, finish.Stdout, finish.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := finish.Run(); err != nil {
		fmt.Fprintf(cli.Out, "Could not attach (%v); reattach with: tmux attach -t %s\n", err, session)
	}
	return nil
}
