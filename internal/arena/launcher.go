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
			grades := arenaGrades(p, ids)
			for _, id := range ids {
				fmt.Fprintln(cli.Out, "  "+id+"  "+grades[id])
			}
			return fmt.Errorf("multiple arenas require a choice: use arena --workflow ID (or open an interactive terminal)")
		}
		r, err := loadWorkflowView(p, ids[0])
		if err != nil {
			return err
		}
		fmt.Fprintf(cli.Out, "A2rena · %s · idle · score %s\n", r.Workflow.Id, r.scorecard().Grade)
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
	model := launcherModel{Project: p, IDs: ids, Grades: arenaGrades(p, ids), Session: session, Width: 80, Height: 24, Loading: len(ids) == 1}
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
	score := ScoreArena(w, profiles)
	return Run{Workflow: w, Profiles: profiles, Settings: s, BaseCommit: head, Score: &score}, nil
}

// arenaGrades scores each arena for the chooser; one that fails to load shows
// no grade and reports its error when opened.
func arenaGrades(p *Project, ids []string) map[string]string {
	grades := map[string]string{}
	for _, id := range ids {
		if w, profiles, err := p.Load(id); err == nil {
			grades[id] = ScoreArena(w, profiles).Grade
		}
	}
	return grades
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
	Grades        map[string]string
	Session       *uiSession
	Live          *LiveModel
	Selected      int
	Width, Height int
	Loading       bool
	Error         string
	Notice        string
	Import        *WorkflowDraft // a pasted arena awaiting Enter
}

type launcherShared struct {
	Notice, Code string
	Err          error
}
type launcherImport struct {
	Draft *WorkflowDraft
	Err   error
}

// launcherCopy copies the selected arena (or its shader) as a share code.
func (m launcherModel) launcherCopy(kind string) tea.Cmd {
	id, p, ctx := m.IDs[m.Selected], m.Project, m.Session.ctx
	return func() tea.Msg {
		export, what := p.ExportArena, "arena "+id
		if kind == "shader" {
			export, what = p.ExportShader, id+"'s shader"
		}
		code, err := export(id)
		if err != nil {
			return launcherShared{Err: err}
		}
		tool, err := writeClipboard(ctx, code)
		if err != nil {
			return launcherShared{Notice: "Sent " + what + " to the terminal clipboard (OSC 52)", Code: code}
		}
		return launcherShared{Notice: fmt.Sprintf("Copied %s (%d characters) with %s", what, len(code), tool), Code: code}
	}
}

// launcherPaste previews a pasted arena code; text is read from the clipboard
// when empty.
func (m launcherModel) launcherPaste(text string) tea.Cmd {
	p, ctx := m.Project, m.Session.ctx
	return func() tea.Msg {
		if text == "" {
			result := readClipboardText(ctx)
			if result.Err != nil {
				return launcherImport{Err: result.Err}
			}
			text = result.Text
		}
		b, err := DecodeShare(text)
		if err != nil {
			return launcherImport{Err: err}
		}
		if b.Kind == "shader" {
			return launcherImport{Err: fmt.Errorf("this is a shader code; open an arena and paste it there")}
		}
		d, err := p.PrepareImport(ctx, b, "", shareProbe)
		return launcherImport{d, err}
	}
}

// launcherSave imports the previewed arena and opens it.
func (m launcherModel) launcherSave() tea.Cmd {
	d, p := *m.Import, m.Project
	return func() tea.Msg {
		if _, err := p.SaveDraft(d); err != nil {
			return uiWorkflowLoaded{Err: err}
		}
		if err := p.Audit(d.Workflow.Id, "workflow.saved", map[string]any{"source": "share-import", "approvedBy": "user"}); err != nil {
			return uiWorkflowLoaded{Err: fmt.Errorf("arena imported, audit failed: %w", err)}
		}
		r, err := loadWorkflowView(p, d.Workflow.Id)
		return uiWorkflowLoaded{r, err}
	}
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
	case launcherShared:
		m.Loading = false
		if v.Err != nil {
			m.Error = v.Err.Error()
			return m, nil
		}
		m.Notice = v.Notice + " · paste it into any Arena to import"
		return m, tea.SetClipboard(v.Code)
	case launcherImport:
		m.Loading = false
		if v.Err != nil {
			m.Error = v.Err.Error()
			return m, nil
		}
		m.Import, m.Error, m.Notice = v.Draft, "", ""
		return m, nil
	case tea.PasteMsg:
		if _, _, ok := FindShareCode(v.Content); ok && !m.Loading {
			m.Loading, m.Error, m.Notice = true, "", "Checking the share code…"
			return m, m.launcherPaste(v.Content)
		}
	case uiWorkflowLoaded:
		m.Loading = false
		if v.Err == nil && !Has(m.IDs, v.Run.Workflow.Id) {
			m.IDs = append(m.IDs, v.Run.Workflow.Id)
		}
		if v.Err != nil {
			m.Error = v.Err.Error()
			return m, nil
		}
		m.Live = &LiveModel{Project: m.Project, Run: v.Run, Readiness: unknownReadiness(v.Run), Width: m.Width, Height: m.Height, State: RunState{Status: "idle", Agents: map[string]string{}}, Context: m.Session.ctx, Session: m.Session}
		return m, m.Live.Init()
	case tea.KeyPressMsg:
		if m.Import != nil && !m.Loading {
			switch v.String() {
			case "enter":
				m.Loading, m.Error = true, ""
				return m, m.launcherSave()
			case "esc", "n":
				m.Import, m.Notice = nil, "Import cancelled"
				return m, nil
			case "ctrl+c":
				return m, tea.Quit
			}
			return m, nil
		}
		switch v.String() {
		case "c", "C":
			if !m.Loading && len(m.IDs) > 0 {
				kind := "arena"
				if v.String() == "C" {
					kind = "shader"
				}
				m.Loading, m.Error, m.Notice = true, "", ""
				return m, m.launcherCopy(kind)
			}
		case "p", "ctrl+v":
			if !m.Loading {
				m.Loading, m.Error, m.Notice = true, "", "Reading the clipboard…"
				return m, m.launcherPaste("")
			}
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
	footer := "↑/↓ choose · Enter open · c copy · C copy shader · p paste · q quit"
	if len(m.IDs) == 0 {
		left = launcherSetup(m.Project)
	}
	lines := brandedHeader(left, width, height, "show", terminalColor("auto"))
	if len(m.IDs) == 0 {
		footer = "p paste an arena code · q quit · arena --help for commands"
	} else {
		if height < 8 {
			lines = nil
		}
		// Keep the selected row visible even in a short terminal or a long list.
		rows := max(1, height-len(lines)-3)
		start := max(0, m.Selected-rows+1)
		pad := 0
		for _, id := range m.IDs {
			pad = max(pad, len(id))
		}
		for i := start; i < min(len(m.IDs), start+rows); i++ {
			marker := "  "
			if i == m.Selected {
				marker = "> "
			}
			line := marker + m.IDs[i]
			if grade := m.Grades[m.IDs[i]]; grade != "" {
				line += strings.Repeat(" ", pad-len(m.IDs[i])+3) + styled(grade, gradeSGR(grade), terminalColor("auto"))
			}
			lines = append(lines, line)
		}
		footer = fmt.Sprintf("%d/%d · %s", m.Selected+1, len(m.IDs), footer)
	}
	if m.Import != nil {
		score := ScoreArena(m.Import.Workflow, m.Import.Profiles)
		// Trust notes and the grade come first so a large graph cannot push
		// them off a short terminal.
		preview := append(append([]string{score.Summary()}, m.Import.Notes...), strings.Split(strings.TrimRight(GraphSummary(m.Import.Workflow), "\n"), "\n")...)
		lines = append(brandedHeader([]string{"Import a pasted arena", "Project: " + m.Project.Root}, width, height, "show", terminalColor("auto")), preview...)
		footer = "Enter import " + m.Import.Workflow.Id + " · Esc cancel"
	}
	switch {
	case m.Loading && m.Notice != "":
		footer = m.Notice
	case m.Loading || len(m.IDs) == 1 && m.Error == "" && m.Import == nil:
		footer = "Opening arena… · q quit"
	case m.Notice != "":
		footer = m.Notice + " · " + footer
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
