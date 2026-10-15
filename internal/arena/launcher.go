package arena

import (
	"context"
	"fmt"
	"os"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/term"
)

// A launcher observes configuration until the user explicitly starts a task.
// In particular, selecting an arena must not manufacture a run or journal events.
func (cli *CLI) launch(ctx context.Context, p *Project, o Options) error {
	for key := range o {
		if !Has([]string{"project", "workflow", "plain"}, key) {
			return fmt.Errorf("--%s needs a command; use arena --help", key)
		}
	}
	ids, err := p.List()
	if err != nil {
		return err
	}
	if id := o.Get("workflow"); id != "" {
		if !Has(ids, id) {
			return fmt.Errorf("arena %q is not configured in %s; use arena workflow list", id, p.Root)
		}
		ids = []string{id}
	}
	if o.Bool("plain") || !cli.isTerminal() {
		if len(ids) == 0 {
			fmt.Fprintln(cli.Out, strings.Join(launcherSetup(p), "\n"))
			return nil
		}
		if len(ids) > 1 {
			fmt.Fprintln(cli.Out, "Choose an arena in", p.Root+":")
			for _, id := range ids {
				fmt.Fprintln(cli.Out, "  "+id)
			}
			return fmt.Errorf("multiple arenas require a choice: use arena --workflow ID (or open an interactive terminal)")
		}
		r, err := loadWorkflowView(p, ids[0])
		if err != nil {
			return err
		}
		fmt.Fprintf(cli.Out, "A2rena · %s · idle\n", r.Workflow.Id)
		fmt.Fprintln(cli.Out, strings.Join(renderGraph(r.Workflow, RunState{Status: "idle"}, nil, nil, 120, false, false), "\n"))
		fmt.Fprintln(cli.Out, "On-call rail: arena-orchestrator (permanent project control)")
		for _, a := range r.Workflow.Agents {
			if a.Activation == "on-call" {
				fmt.Fprintln(cli.Out, "  "+a.Id+" · configured triggers only")
			}
		}
		for _, runtime := range Required([]Workflow{r.Workflow}, r.Settings) {
			fmt.Fprintf(cli.Out, "Provider %s / %s: unknown (use arena status --workflow %s)\n", runtime.Provider, runtime.Model, r.Workflow.Id)
		}
		fmt.Fprintf(cli.Out, "Start a task: arena run %s 'TASK'\n", r.Workflow.Id)
		return nil
	}
	session := newUISession(ctx, p)
	defer session.close()
	model := launcherModel{Project: p, IDs: ids, Session: session, Width: 80, Height: 24, Loading: len(ids) == 1}
	_, err = tea.NewProgram(model, tea.WithContext(session.ctx), tea.WithInput(cli.input), tea.WithOutput(cli.Out)).Run()
	return err
}

func (cli *CLI) isTerminal() bool {
	input, inputFile := cli.input.(*os.File)
	output, outputFile := cli.Out.(*os.File)
	return inputFile && outputFile && term.IsTerminal(input.Fd()) && term.IsTerminal(output.Fd()) && os.Getenv("TERM") != "dumb"
}

func loadWorkflowView(p *Project, id string) (Run, error) {
	w, profiles, err := p.Load(id)
	if err != nil {
		return Run{}, err
	}
	s, err := p.Settings()
	if err != nil {
		return Run{}, err
	}
	head, err := Git(p.Root, "rev-parse", "HEAD")
	if err != nil {
		head = "unborn"
	}
	return Run{Workflow: w, Profiles: profiles, Settings: s, BaseCommit: head}, nil
}

func unknownReadiness(r Run) []Readiness {
	results := []Readiness{}
	for _, runtime := range Required([]Workflow{r.Workflow}, r.Settings) {
		results = append(results, Readiness{Provider: runtime.Provider, Model: runtime.Model, State: "unknown", Auth: "checking readiness"})
	}
	return results
}

type uiWorkflowLoaded struct {
	Run Run
	Err error
}

type launcherModel struct {
	Project       *Project
	IDs           []string
	Session       *uiSession
	Live          *LiveModel
	Selected      int
	Width, Height int
	Loading       bool
	Error         string
}

func (m launcherModel) load() tea.Cmd {
	id := m.IDs[m.Selected]
	return func() tea.Msg {
		r, err := loadWorkflowView(m.Project, id)
		return uiWorkflowLoaded{r, err}
	}
}

func (m launcherModel) Init() tea.Cmd {
	if len(m.IDs) == 1 {
		return m.load()
	}
	return nil
}

func (m launcherModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok {
		m.Width, m.Height = size.Width, size.Height
	}
	if m.Live != nil {
		live, cmd := m.Live.Update(msg)
		value := live.(LiveModel)
		m.Live = &value
		return m, cmd
	}
	switch v := msg.(type) {
	case uiWorkflowLoaded:
		m.Loading = false
		if v.Err != nil {
			m.Error = v.Err.Error()
			return m, nil
		}
		m.Live = &LiveModel{Project: m.Project, Run: v.Run, Readiness: unknownReadiness(v.Run), Width: m.Width, Height: m.Height, State: RunState{Status: "idle", Agents: map[string]string{}}, Context: m.Session.ctx, Session: m.Session}
		return m, m.Live.Init()
	case tea.KeyPressMsg:
		switch v.String() {
		case "q", "esc", "ctrl+c":
			return m, tea.Quit
		case "up", "k":
			if !m.Loading {
				m.Selected = max(0, m.Selected-1)
			}
		case "down", "j":
			if !m.Loading {
				m.Selected = min(max(0, len(m.IDs)-1), m.Selected+1)
			}
		case "home":
			if !m.Loading {
				m.Selected = 0
			}
		case "end":
			if !m.Loading {
				m.Selected = max(0, len(m.IDs)-1)
			}
		case "enter":
			if !m.Loading && len(m.IDs) > 0 {
				m.Loading, m.Error = true, ""
				return m, m.load()
			}
		}
	}
	return m, nil
}

func launcherSetup(p *Project) []string {
	lines := []string{"A2rena · no arenas configured", "Project: " + p.Root}
	if _, err := os.Stat(p.Path("settings.arena.json")); os.IsNotExist(err) {
		lines = append(lines, "Run arena init to select the project control runtime.")
	}
	return append(lines, "Run arena workflow create NAME to build your first arena.", "Setup asks for required provider, model and effort choices.")
}

func (m launcherModel) View() tea.View {
	if m.Live != nil {
		return m.Live.View()
	}
	width, height := max(8, m.Width), max(4, m.Height)
	left := []string{"Choose an arena", "Project: " + m.Project.Root}
	footer := "↑/↓ choose · Enter open · q quit"
	if len(m.IDs) == 0 {
		left = launcherSetup(m.Project)
	}
	lines := brandedHeader(left, width, height, "show", terminalColor("auto"))
	if len(m.IDs) == 0 {
		footer = "q quit · arena --help for commands"
	} else {
		if height < 8 {
			lines = nil
		}
		// Keep the selected row visible even in a short terminal or a long list.
		rows := max(1, height-len(lines)-3)
		start := max(0, m.Selected-rows+1)
		for i := start; i < min(len(m.IDs), start+rows); i++ {
			marker := "  "
			if i == m.Selected {
				marker = "> "
			}
			lines = append(lines, marker+m.IDs[i])
		}
		footer = fmt.Sprintf("%d/%d · %s", m.Selected+1, len(m.IDs), footer)
	}
	if m.Loading || len(m.IDs) == 1 && m.Error == "" {
		footer = "Opening arena… · q quit"
	}
	if m.Error != "" {
		footer = "Error: " + Redact(m.Error) + " · Enter retry · q quit"
	}
	lines = lines[:min(len(lines), height-3)]
	for len(lines) < height-3 {
		lines = append(lines, "")
	}
	lines = append(lines, footer, promptRule(width, terminalColor("auto")), "> memento mori")
	for i := range lines {
		lines[i] = ansi.Truncate(lines[i], width, "")
	}
	view := tea.NewView(strings.Join(lines, "\n"))
	view.AltScreen = true
	return view
}
