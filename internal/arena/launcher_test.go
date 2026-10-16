package arena

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func launchWorkflow(t *testing.T, p *Project, id string) {
	t.Helper()
	cli := NewCLI(strings.NewReader(""), io.Discard, io.Discard)
	if err := cli.Run(context.Background(), []string{"workflow", "create", id, "--head", "head", "--provider", "codex", "--model", "explicit-test-model", "--effort", "high", "--yes", "--project", p.Root}); err != nil {
		t.Fatal(err)
	}
}

func launchKey(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

func assertNoLaunchRun(t *testing.T, p *Project) {
	t.Helper()
	paths, err := filepath.Glob(p.Path(".state", "*", "run.json"))
	if err != nil || len(paths) != 0 {
		t.Fatalf("opening a workflow created a run: %v %v", paths, err)
	}
}

func TestBareArenaAutoOpensOnlyWorkflowWithoutStartingRun(t *testing.T) {
	p := testProject(t)
	launchWorkflow(t, p, "solo")
	t.Chdir(p.Root)
	var out bytes.Buffer
	cli := NewCLI(strings.NewReader(""), &out, &out)
	if err := cli.Run(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if view := out.String(); !strings.Contains(view, "solo · idle") || !strings.Contains(view, "head · codex") || strings.Contains(view, "\x1b") {
		t.Fatalf("bare non-TTY launch did not show the sole arena: %s", view)
	}

	s := newUISession(context.Background(), p)
	defer s.close()
	m := launcherModel{Project: p, IDs: []string{"solo"}, Session: s, Width: 140, Height: 40}
	cmd := m.Init()
	if cmd == nil {
		t.Fatal("single arena did not auto-open")
	}
	model, _ := m.Update(cmd()) // Do not execute provider readiness in this fixture test.
	opened := model.(launcherModel)
	if opened.Live == nil || opened.Live.Run.ID != "" || opened.Live.Run.Workflow.Id != "solo" || opened.Live.State.Status != "idle" {
		t.Fatalf("incorrect initial graph: %+v", opened.Live)
	}
	if !strings.Contains(opened.View().Content, "run TASK") {
		t.Fatal("idle graph does not explain how to start")
	}
	// An idle graph never reads a fictitious empty-ID journal.
	idle, cmd := opened.Live.Update(uiTick{})
	if cmd != nil || idle.(LiveModel).Error != "" {
		t.Fatal("idle graph tried to read a run")
	}
	if err := opened.Live.command("pause"); err == nil || !strings.Contains(err.Error(), "no run yet") {
		t.Fatalf("idle command error: %v", err)
	}
	assertNoLaunchRun(t, p)
}

func TestMultipleArenasRequireChoiceAndOpenSelectedGraph(t *testing.T) {
	p := testProject(t)
	launchWorkflow(t, p, "zeta")
	launchWorkflow(t, p, "alpha")
	ids, err := p.List()
	if err != nil || strings.Join(ids, ",") != "alpha,zeta" {
		t.Fatalf("arena listing: %v %v", ids, err)
	}
	s := newUISession(context.Background(), p)
	defer s.close()
	m := launcherModel{Project: p, IDs: ids, Session: s, Width: 100, Height: 24}
	if m.Init() != nil || m.Live != nil {
		t.Fatal("silently chose an arena")
	}
	quit, cmd := m.Update(launchKey('q'))
	if _, ok := cmd().(tea.QuitMsg); !ok || quit.(launcherModel).Live != nil {
		t.Fatal("chooser cancellation selected a workflow")
	}
	next, _ := m.Update(launchKey(tea.KeyDown))
	m = next.(launcherModel)
	if !strings.Contains(m.View().Content, "> zeta") {
		t.Fatal("selected row not visible")
	}
	next, cmd = m.Update(launchKey(tea.KeyEnter))
	m = next.(launcherModel)
	if m.Live != nil || !m.Loading || cmd == nil {
		t.Fatal("chooser did not load on confirmation")
	}
	next, _ = m.Update(cmd())
	m = next.(launcherModel)
	if m.Live == nil || m.Live.Run.Workflow.Id != "zeta" || !strings.Contains(strings.SplitN(ansi.Strip(m.View().Content), "\n", 2)[0], "zeta") {
		t.Fatal("selected arena graph not opened")
	}
	var out bytes.Buffer
	cli := NewCLI(strings.NewReader(""), &out, &out)
	if err := cli.Run(context.Background(), []string{"--project", p.Root}); err == nil || !strings.Contains(err.Error(), "--workflow ID") {
		t.Fatalf("non-TTY silently selected: %v", err)
	}
	out.Reset()
	if err := cli.Run(context.Background(), []string{"--project", p.Root, "--workflow", "zeta", "--plain"}); err != nil || !strings.Contains(out.String(), "zeta · idle") {
		t.Fatalf("explicit selection failed: %v %s", err, out.String())
	}
	assertNoLaunchRun(t, p)
}

func TestLauncherEmptyProjectAndInvalidSelectionAreActionable(t *testing.T) {
	p := testProject(t)
	var out bytes.Buffer
	cli := NewCLI(strings.NewReader(""), &out, &out)
	if err := cli.Run(context.Background(), []string{"--project", p.Root, "--plain"}); err != nil || !strings.Contains(out.String(), "arena workflow create NAME") {
		t.Fatalf("empty project: %v %s", err, out.String())
	}
	if err := cli.Run(context.Background(), []string{"--project", p.Root, "--workflow", "missing"}); err == nil {
		t.Fatal("missing explicit arena accepted")
	}
	launchWorkflow(t, p, "broken")
	if err := os.WriteFile(p.Path("broken.arena.json"), []byte("{}"), 0600); err != nil {
		t.Fatal(err)
	}
	s := newUISession(context.Background(), p)
	defer s.close()
	m := launcherModel{Project: p, IDs: []string{"broken"}, Session: s, Width: 100, Height: 24}
	next, _ := m.Update(m.Init()())
	m = next.(launcherModel)
	if m.Live != nil || m.Error == "" || !strings.Contains(m.View().Content, "Error:") {
		t.Fatal("invalid workflow hidden or replaced")
	}
	assertNoLaunchRun(t, p)
}

func TestLauncherResolvesRootFromSubdirectoryAndStopsAtNestedGit(t *testing.T) {
	p := testProject(t)
	canonical, err := filepath.EvalSymlinks(p.Root)
	if err != nil {
		t.Fatal(err)
	}
	p.Root = canonical
	launchWorkflow(t, p, "parent")
	sub := filepath.Join(p.Root, "sub")
	if err := os.Mkdir(sub, 0700); err != nil {
		t.Fatal(err)
	}
	root, err := Open(sub)
	if err != nil || root.Root != p.Root {
		t.Fatalf("subdirectory root: %v %v", root, err)
	}
	if b, err := exec.Command("git", "init", "-q", sub).CombinedOutput(); err != nil {
		t.Fatalf("nested git: %v %s", err, b)
	}
	root, err = Open(sub)
	if err != nil || root.Root != sub {
		t.Fatalf("inherited an enclosing project's arenas: %v %v", root, err)
	}
	var out bytes.Buffer
	if err := NewCLI(strings.NewReader(""), &out, &out).Run(context.Background(), []string{"--project", sub}); err == nil || !strings.Contains(out.String(), "Arena setup") || !strings.Contains(out.String(), "Provider") {
		t.Fatalf("uninitialized project: %v %s", err, out.String())
	}
	if _, err := os.Stat(root.Path()); !os.IsNotExist(err) {
		t.Fatal("launcher initialized project without explicit runtime choices")
	}
}

func TestChooserFitsAndScrollsLongList(t *testing.T) {
	ids := []string{}
	for i := range 100 {
		ids = append(ids, fmt.Sprintf("workflow-%03d", i))
	}
	for _, size := range [][2]int{{140, 40}, {60, 18}, {30, 8}, {8, 4}} {
		m := launcherModel{Project: &Project{Root: "/project"}, IDs: ids, Selected: 99, Width: size[0], Height: size[1]}
		view := m.View().Content
		lines := strings.Split(view, "\n")
		if len(lines) > size[1] {
			t.Fatalf("%v: %d rows", size, len(lines))
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > size[0] {
				t.Fatalf("%v: row overflow", size)
			}
		}
		if size[1] >= 8 && !strings.Contains(view, "> workflow-099") {
			t.Fatal("chooser hid its selected row")
		}
		if size[1] < 8 && !strings.HasPrefix(view, "> ") {
			t.Fatal("small chooser hid its selected row")
		}
	}
}

func TestTUIRunStartsOnlyOnCommandAndQuittingStopsAttachedEngine(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	if _, err := p.PrepareBaseline(r); err != nil {
		t.Fatal(err)
	}
	s := newUISession(context.Background(), p)
	defer s.close()
	prepared := make(chan string, 1)
	invoked := make(chan struct{})
	s.prepare = func(ctx context.Context, project *Project, workflow, task string) (Run, []Readiness, error) {
		prepared <- workflow + ":" + task
		return r, unknownReadiness(r), nil
	}
	s.execute = func(ctx context.Context, p *Project, r Run) error {
		return NewEngine(p, r, testInvoker(func(ctx context.Context, in Invocation) (map[string]any, error) {
			close(invoked)
			<-ctx.Done()
			return nil, ctx.Err()
		})).Execute(ctx, false, false)
	}
	m := LiveModel{Project: p, Run: Run{Workflow: r.Workflow, Profiles: r.Profiles, Settings: r.Settings}, State: RunState{Status: "idle"}, Session: s, Context: s.ctx, Width: 100, Height: 24}
	select {
	case <-prepared:
		t.Fatal("started without user command")
	default:
	}
	m.Prompt = "run Inspect  the baseline"
	next, cmd := m.Update(launchKey(tea.KeyEnter))
	m = next.(LiveModel)
	if !m.Starting || cmd == nil {
		t.Fatal("run command did not start preparation")
	}
	if task := <-prepared; task != "test:Inspect  the baseline" {
		t.Fatalf("task changed: %q", task)
	}
	result := cmd().(uiRunStarted)
	next, _ = m.Update(result)
	m = next.(LiveModel)
	if !m.Executing || m.Run.ID != r.ID {
		t.Fatal("TUI did not attach to started run")
	}
	m.Prompt = "run second task"
	next, cmd = m.Update(launchKey(tea.KeyEnter))
	if cmd != nil || !strings.Contains(next.(LiveModel).Error, "already") {
		t.Fatal("allowed concurrent run in the same view")
	}
	select {
	case <-invoked:
	case <-time.After(10 * time.Second):
		t.Fatal("fixture engine never invoked")
	}
	s.close()
	finished := (<-result.done).(uiRunFinished)
	if finished.Err == nil {
		t.Fatal("fixture engine ignored attached shutdown")
	}
	state, err := p.State(r.ID)
	if err != nil || state.Status != "interrupted" || RunAlive(p, r.ID) {
		t.Fatalf("shutdown did not preserve interrupted run/release lock: %+v %v", state, err)
	}
	// A delayed tick from a previous run must not create another polling loop.
	_, cmd = m.Update(uiTick{RunID: "previous-run"})
	if cmd != nil {
		t.Fatal("stale tick started another polling loop")
	}
}

func TestNewExecutionRefusesUncommittedProjectWithoutCreatingRun(t *testing.T) {
	p := testProject(t)
	launchWorkflow(t, p, "solo")
	if _, _, err := prepareNewExecution(context.Background(), p, "solo", "inspect"); err == nil || !strings.Contains(err.Error(), "commit the initial") {
		t.Fatalf("unborn repository: %v", err)
	}
	assertNoLaunchRun(t, p)
}
