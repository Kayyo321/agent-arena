package arena

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/x/term"
)

func (cli *CLI) startup(ctx context.Context, dir string, o Options) error {
	p, err := Open(dir)
	if err == nil {
		ids, err := p.List()
		if err != nil {
			return err
		}
		if len(ids) > 0 || o.Get("workflow") != "" || o.Bool("plain") {
			return cli.launch(ctx, p, o)
		}
	} else if o.Get("workflow") != "" {
		return err
	} else if o.Bool("plain") {
		abs, err := filepath.Abs(dir)
		if err != nil {
			return err
		}
		info, err := os.Stat(abs)
		if err != nil || !info.IsDir() {
			return fmt.Errorf("project directory does not exist: %s", abs)
		}
		return cli.launch(ctx, &Project{Root: abs}, o)
	}
	return cli.setup(ctx, dir, o)
}

// Setup is also the empty-project startup path. Runtime choices precede all
// writes, and a saved project's default is reused only for its control agent.
func (cli *CLI) setup(ctx context.Context, dir string, o Options) error {
	for key := range o {
		if !Has([]string{"project", "provider", "model", "effort", "backend", "endpoint", "auth-method", "no-bootstrap", "yes"}, key) {
			return fmt.Errorf("--%s is not a setup option; use arena --help", key)
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if info, err := os.Stat(abs); err == nil && !info.IsDir() {
		return fmt.Errorf("project path is not a directory: %s", abs)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	p := &Project{Root: abs}
	if existing, err := Open(abs); err == nil {
		p = existing
	}
	if cli.isTerminal() {
		width, height, err := term.GetSize(cli.Out.(*os.File).Fd())
		if err != nil {
			width, height = 80, 24
		}
		fmt.Fprintln(cli.Out, strings.Join(brandedHeader([]string{"Arena setup", p.Root}, width, height, "show", terminalColor("auto")), "\n"))
	} else {
		fmt.Fprintln(cli.Out, "Arena setup ·", p.Root)
	}
	_, settingsErr := os.Stat(p.Path("settings.arena.json"))
	if settingsErr != nil && !os.IsNotExist(settingsErr) {
		return settingsErr
	}
	if os.IsNotExist(settingsErr) {
		runtime, err := cli.runtimeChoice(ctx, p, Runtime{}, o)
		if err != nil {
			return err
		}
		if err := cli.confirm("Initialize this project with Git (if needed) and Arena", o); err != nil {
			return err
		}
		if err := os.MkdirAll(p.Root, 0755); err != nil {
			return err
		}
		p.Root, err = filepath.EvalSymlinks(p.Root)
		if err != nil {
			return err
		}
		if root, err := Git(p.Root, "rev-parse", "--show-toplevel"); err != nil || root != p.Root {
			if _, err := Git(p.Root, "init", "--quiet"); err != nil {
				return fmt.Errorf("initialize Git: %w", err)
			}
		}
		initOptions := Options{"provider": {runtime.Provider}, "model": {runtime.Model}, "effort": {runtime.ReasoningEffort}, "no-bootstrap": {"true"}, "yes": {"true"}}
		for key, option := range map[string]string{"backend": "backend", "endpoint": "endpoint", "auth-method": "authMethod"} {
			if value := str(runtime.Options[option]); value != "" {
				initOptions[key] = []string{value}
			}
		}
		if err := cli.init(ctx, p, initOptions); err != nil {
			return err
		}
	}
	s, err := p.Settings()
	if err != nil {
		return err
	}
	for key, configured := range map[string]string{"provider": s.DefaultModel.Provider, "model": s.DefaultModel.Model, "effort": s.DefaultModel.ReasoningEffort, "backend": str(s.DefaultModel.Options["backend"]), "endpoint": str(s.DefaultModel.Options["endpoint"]), "auth-method": str(s.DefaultModel.Options["authMethod"])} {
		if value := o.Get(key); value != "" && value != configured {
			return fmt.Errorf("project already has a different default %s; use arena settings edit to change it", key)
		}
	}
	fmt.Fprintf(cli.Out, "Project control: %s / %s / %s\n", s.DefaultModel.Provider, s.DefaultModel.Model, s.DefaultModel.ReasoningEffort)
	if o.Bool("no-bootstrap") {
		fmt.Fprintln(cli.Out, "Ready. Run arena here to describe what to build to the default orchestrator.")
		return nil
	}
	if !cli.isTerminal() {
		return fmt.Errorf("the setup conversation needs an interactive terminal; run arena setup there (or --no-bootstrap to prepare configuration only)")
	}
	fmt.Fprintln(cli.Out, "Describe what you want to build. The default orchestrator will interview you and create the workflow through Arena.")
	return cli.bootstrap(ctx, p, s)
}
