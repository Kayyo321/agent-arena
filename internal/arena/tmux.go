package arena

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// tmuxMinMajor is the first tmux release whose new-session, new-window and
// split-window take shell-command as multiple arguments "executed directly
// (without sh -c)". tmux CHANGES, "CHANGES FROM 1.9a TO 2.0, 06 March 2015":
// "The 'new-session', 'new-window', 'split-window', and 'respawn-pane' commands
// now understand multiple arguments and handle quoting problems correctly";
// the 2.0 manual (tmux.1) states the without-sh-c behaviour. Arena never
// builds a shell string, so it refuses older tmux instead of falling back.
const tmuxMinMajor = 2

var tmuxVersionPattern = regexp.MustCompile(`^tmux (\d+)\.(\d+)`)

// tmuxVersionSupported reads `tmux -V` output. Releases such as "tmux 3.3a"
// parse; development builds ("tmux next-3.5", "tmux master") and anything else
// are unparseable and therefore unsupported, since their behaviour cannot be
// told from the version string.
func tmuxVersionSupported(output string) (supported, parsed bool) {
	m := tmuxVersionPattern.FindStringSubmatch(strings.TrimSpace(output))
	if m == nil {
		return false, false
	}
	major, _ := strconv.Atoi(m[1])
	return major >= tmuxMinMajor, true
}

func checkTmuxVersion(ctx context.Context, bin string) error {
	out, err := exec.CommandContext(ctx, bin, "-V").Output()
	version := strings.TrimSpace(Redact(string(out)))
	if err != nil {
		return fmt.Errorf("could not read the tmux version (tmux -V failed); arena tmux needs tmux %d.0 or newer", tmuxMinMajor)
	}
	if ok, parsed := tmuxVersionSupported(version); !ok {
		if !parsed {
			return fmt.Errorf("cannot parse the tmux version %q; arena tmux needs a released tmux %d.0 or newer (development builds are not accepted)", version, tmuxMinMajor)
		}
		return fmt.Errorf("%s is too old; arena tmux needs tmux %d.0 or newer, which runs multi-argument commands without a shell", version, tmuxMinMajor)
	}
	return nil
}

// tmuxStep is one tmux command of a run's layout.
type tmuxStep struct {
	argv []string
	// required marks the setup the documented behaviour depends on: the session
	// whose pane 0 supervises the run, and keeping that pane, with the run's final
	// output, on screen after the supervisor exits. A required step that fails is
	// an error even when it comes after new-session. Everything else only places
	// optional observer panes and focus, and costs only that when it fails.
	required bool
}

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
	steps := tmuxLayout(exe, root, session, supervisor, runID, agents)
	cmds := make([][]string, len(steps))
	for i, step := range steps {
		cmds[i] = step.argv
	}
	return cmds
}

func tmuxLayout(exe, root, session string, supervisor []string, runID string, agents []string) []tmuxStep {
	pane := func(args ...string) []string { return append([]string{"--", exe}, args...) }
	target := session + ":"
	required := func(argv ...string) tmuxStep { return tmuxStep{argv: argv, required: true} }
	optional := func(argv ...string) tmuxStep { return tmuxStep{argv: argv} }
	steps := []tmuxStep{
		required(append([]string{"new-session", "-d", "-s", session, "-n", "run", "-c", root}, pane(append([]string{"--project", root}, supervisor...)...)...)...),
		required("set-option", "-t", session, "remain-on-exit", "on"),
		optional(append([]string{"split-window", "-h", "-t", target + "0", "-c", root}, pane("watch", runID, "--json", "--follow", "--project", root)...)...),
	}
	for i, id := range agents {
		args := pane("view", "--run", runID, "--follow", "--project", root, "--", id)
		if i == 0 {
			steps = append(steps, optional(append([]string{"new-window", "-t", target, "-n", "agents", "-c", root}, args...)...))
		} else {
			steps = append(steps, optional(append([]string{"split-window", "-t", target + "1", "-c", root}, args...)...))
		}
		steps = append(steps, optional("select-layout", "-t", target+"1", "tiled"))
	}
	return append(steps, optional("select-window", "-t", target+"0"), optional("select-pane", "-t", target+"0.0"))
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
	if err := checkTmuxVersion(ctx, bin); err != nil {
		return err
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	exe = pinnableExecutable(exe)
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
	skipped := []string{}
	for i, step := range tmuxLayout(exe, p.Root, session, supervisor, r.ID, agents) {
		out, err := exec.CommandContext(ctx, bin, step.argv...).CombinedOutput()
		if err == nil {
			continue
		}
		failure := fmt.Sprintf("tmux %s failed for run %s: %v: %s", step.argv[0], r.ID, err, strings.TrimSpace(string(out)))
		switch {
		case i == 0:
			// The supervisor never started, so nothing is running.
			return fmt.Errorf("%s", failure)
		case step.required:
			// The supervisor is already running. The session is left alone,
			// since killing it would interrupt the run, and the error says so.
			return fmt.Errorf("required tmux setup step failed: %s; the run is already live in tmux session %s, so attach with `tmux attach -t %s`, or `tmux kill-session -t %s` to interrupt it (resume with `arena resume %s`)", failure, session, session, session, r.ID)
		default:
			// A pane that cannot be laid out (no room, an old tmux) costs only
			// that observer.
			skipped = append(skipped, failure)
		}
	}
	fmt.Fprintf(cli.Out, "Run %s laid out in tmux session %s; pane 0 supervises the run (ending it interrupts the run), the other panes only observe\n", r.ID, session)
	if len(skipped) > 0 {
		for _, failure := range skipped {
			fmt.Fprintf(cli.Err, "Warning: optional tmux step skipped, the run is unaffected: %s\n", failure)
		}
		// Attaching replaces the screen, so a person at a terminal reads the
		// warning first.
		if cli.isTerminal() {
			fmt.Fprint(cli.Err, "Press Enter to attach: ")
			_, _ = cli.In.ReadString('\n')
		}
	}
	finish := exec.CommandContext(ctx, bin, tmuxFinish(session, os.Getenv("TMUX") != "")...)
	finish.Stdin, finish.Stdout, finish.Stderr = os.Stdin, os.Stdout, os.Stderr
	if err := finish.Run(); err != nil {
		fmt.Fprintf(cli.Out, "Could not attach (%v); reattach with: tmux attach -t %s\n", err, session)
	}
	return nil
}
