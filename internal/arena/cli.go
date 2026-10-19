package arena

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	c "github.com/Kayyo321/agent-arena/internal/contract"
)

type CLI struct {
	In    *bufio.Reader
	Out   io.Writer
	Err   io.Writer
	input io.Reader
}

func NewCLI(in io.Reader, out, err io.Writer) *CLI {
	return &CLI{In: bufio.NewReader(in), Out: out, Err: err, input: in}
}

type Options map[string][]string

func (o Options) Get(k string) string {
	a := o[k]
	if len(a) == 0 {
		return ""
	}
	return a[len(a)-1]
}
func (o Options) Bool(k string) bool { return o.Get(k) == "true" }
func parseArgs(args []string) ([]string, Options, error) {
	o := Options{}
	pos := []string{}
	bools := []string{"help", "yes", "json", "plain", "no-bootstrap", "dry-run", "all", "replace", "user", "including-checkpoints", "approve", "retry-interrupted", "shader", "copy", "clipboard", "follow", "control", "discard-unintegrated", "check", "force"}
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "--") {
			pos = append(pos, arg)
			continue
		}
		key, value, has := strings.Cut(arg[2:], "=")
		if !has {
			if Has(bools, key) {
				value = "true"
			} else {
				if i+1 >= len(args) {
					return nil, o, fmt.Errorf("missing value for --%s", key)
				}
				i++
				value = args[i]
			}
		}
		o[key] = append(o[key], value)
	}
	return pos, o, nil
}
func (cli *CLI) print(v any) { fmt.Fprint(cli.Out, string(SafeJSON(v))) }
func (cli *CLI) ask(label, existing string, required bool) (string, error) {
	suffix := ""
	if existing != "" {
		suffix = " [" + existing + "]"
	}
	fmt.Fprintf(cli.Out, "%s%s: ", label, suffix)
	line, e := cli.In.ReadString('\n')
	if e != nil && len(line) == 0 {
		return "", fmt.Errorf("%s is required; use interactive input or explicit flags", label)
	}
	line = strings.TrimSpace(line)
	if line == "" {
		line = existing
	}
	if required && line == "" {
		return "", fmt.Errorf("%s must be selected explicitly", label)
	}
	return line, nil
}
func (cli *CLI) confirm(label string, o Options) error {
	if o.Bool("yes") {
		return nil
	}
	answer, e := cli.ask(label+" (yes/no)", "", true)
	if e != nil {
		return e
	}
	if answer != "yes" {
		return fmt.Errorf("not confirmed; no changes saved")
	}
	return nil
}

// Run executes one command. After a successful one it may print a one-line
// notice that a newer release exists (selfupdate.go).
func (cli *CLI) Run(ctx context.Context, args []string) error {
	if e := cli.run(ctx, args); e != nil {
		return e
	}
	cli.noticeUpdate(ctx, args)
	return nil
}
func (cli *CLI) run(ctx context.Context, args []string) error {
	if len(args) > 0 && (args[0] == "--help" || args[0] == "help") {
		fmt.Fprint(cli.Out, helpText)
		return nil
	}
	if len(args) > 0 && (args[0] == "--version" || args[0] == "version") {
		fmt.Fprintln(cli.Out, "arena", Version)
		return nil
	}
	pos, o, e := parseArgs(args)
	if e != nil {
		return e
	}
	for _, arg := range pos {
		if arg == "--help" {
			fmt.Fprint(cli.Out, helpText)
			return nil
		}
	}
	if _, ok := o["help"]; ok {
		fmt.Fprint(cli.Out, helpText)
		return nil
	}
	allowed := split("help,project,provider,model,effort,backend,endpoint,auth-method,command-approval,yes,json,plain,no-bootstrap,dry-run,all,replace,user,including-checkpoints,approve,retry-interrupted,name,head,capabilities,input,set,workflow,agent,from,id,role,objective,reason,workspace-mode,files,run,actor,title,content,confidence,tags,evidence,glyphs,shader,copy,clipboard,as,into,skip,follow,control,discard-unintegrated,check,to,force")
	for key := range o {
		if !Has(allowed, key) {
			return fmt.Errorf("unknown flag --%s", key)
		}
	}
	dir := o.Get("project")
	if dir == "" {
		dir = "."
	}
	if len(pos) == 0 {
		return cli.startup(ctx, dir, o)
	}
	if pos[0] == "setup" {
		if len(pos) != 1 {
			return fmt.Errorf("arena setup [--project PATH] [--provider ID --model ID --effort VALUE]")
		}
		return cli.setup(ctx, dir, o)
	}
	if pos[0] == "doctor" {
		if len(pos) != 1 {
			return fmt.Errorf("arena doctor [--project PATH] [--json]")
		}
		return cli.doctor(ctx, dir, o)
	}
	if pos[0] == "update" || pos[0] == "uninstall" {
		if len(pos) != 1 {
			return fmt.Errorf("arena update [--check] [--to VERSION] [--yes] | arena uninstall [--yes] [--dry-run] [--force]")
		}
		exe, e := runningExecutable()
		if e != nil {
			return e
		}
		if pos[0] == "update" {
			return cli.update(ctx, exe, dir, o)
		}
		return cli.uninstall(ctx, exe, dir, o)
	}
	p, e := Open(dir)
	if e != nil {
		return e
	}
	switch pos[0] {
	case "bootstrap":
		s, err := p.Settings()
		if err != nil {
			return err
		}
		return cli.bootstrap(ctx, p, s)
	case "init":
		return cli.init(ctx, p, o)
	case "workflow":
		return cli.workflow(ctx, p, pos[1:], o)
	case "shader":
		return cli.shader(ctx, p, pos[1:], o)
	case "score":
		return cli.score(p, pos[1:], o)
	case "share":
		return cli.share(ctx, p, pos[1:], o)
	case "settings":
		return cli.settings(p, pos[1:], o)
	case "status", "auth":
		return cli.status(ctx, p, pos[1:], o)
	case "context":
		return cli.context(p, pos[1:], o)
	case "skill":
		return cli.skill(p, pos[1:], o)
	case "lock":
		return cli.lock(p, pos[1:], o)
	case "transaction":
		return cli.transaction(p, pos[1:], o)
	default:
		return cli.runtimeCommand(ctx, p, pos, o)
	}
}

// choose lists options and accepts either a number or an exact value. With an
// empty list it falls back to free entry; strict rejects unlisted values.
func (cli *CLI) choose(label string, options, notes []string, strict bool) (string, error) {
	if len(options) == 0 {
		return cli.ask(label, "", true)
	}
	for i, option := range options {
		line := fmt.Sprintf("  %2d) %s", i+1, option)
		if i < len(notes) && notes[i] != "" {
			line += "  " + notes[i]
		}
		fmt.Fprintln(cli.Out, line)
	}
	for {
		answer, e := cli.ask(label+" (number or exact value)", "", true)
		if e != nil {
			return "", e
		}
		if n, err := strconv.Atoi(answer); err == nil && n >= 1 && n <= len(options) {
			return options[n-1], nil
		}
		if !strict || Has(options, answer) {
			return answer, nil
		}
		fmt.Fprintf(cli.Out, "%q is not in the list above.\n", answer)
	}
}

func (cli *CLI) runtimeChoice(ctx context.Context, p *Project, r Runtime, o Options) (Runtime, error) {
	var e error
	if v := o.Get("provider"); v != "" {
		r.Provider = v
	}
	if r.Provider == "" {
		fmt.Fprintln(cli.Out, "Provider:")
		r.Provider, e = cli.choose("Provider", []string{"codex", "claude-code", "antigravity", "gemini", "local", "opencode"}, []string{"Codex CLI, its own sign-in", "Claude Code CLI, its own sign-in or API key/cloud auth", "Antigravity CLI (agy), its own sign-in", "Google GenAI SDK, API key or Vertex AI", "Ollama or OpenAI-compatible server on this machine", "OpenCode CLI, its own sign-in; no shell commands"}, true)
		if e != nil {
			return r, e
		}
	}
	if r.Options == nil {
		r.Options = map[string]any{}
	}
	// Connection choices come first so the model catalog can be queried.
	if r.Provider == "local" {
		for _, x := range []struct{ key, label string }{{"backend", "Backend (ollama, lm-studio, llama.cpp, vllm, openai-compatible)"}, {"endpoint", "Loopback HTTP endpoint"}} {
			value := o.Get(x.key)
			if value == "" {
				value = str(r.Options[x.key])
			}
			if value == "" {
				value, e = cli.ask(x.label, "", true)
				if e != nil {
					return r, e
				}
			}
			r.Options[x.key] = value
		}
	}
	if r.Provider == "claude-code" {
		v := o.Get("auth-method")
		if v == "" {
			v = str(r.Options["authMethod"])
		}
		if v == "" {
			v, e = cli.choose("Auth method", []string{claudeSignInAuth, "api-key-or-cloud"}, []string{"background Claude Code with its own login and tools", "bare mode, Arena-mediated tools, ANTHROPIC_API_KEY or cloud route"}, true)
			if e != nil {
				return r, e
			}
		}
		r.Options["authMethod"] = v
	}
	// Blanket approval is never asked for or defaulted: only an explicit flag
	// (or an edited runtime) selects it.
	if v := o.Get("command-approval"); v != "" {
		r.Options["commandApproval"] = v
	}
	if r.Provider == "gemini" {
		v := o.Get("auth-method")
		if v == "" {
			v = str(r.Options["authMethod"])
		}
		if v == "" {
			v, e = cli.choose("Auth method", []string{"google-ai-studio-api-key", "vertex-ai"}, nil, true)
			if e != nil {
				return r, e
			}
		}
		r.Options["authMethod"] = v
	}
	if v := o.Get("model"); v != "" {
		r.Model = v
	}
	var catalog []ModelOption
	if r.Model == "" {
		fmt.Fprintf(cli.Out, "Discovering models from %s...\n", r.Provider)
		catalog, e = DiscoverModels(ctx, p.Root, r)
		if e != nil || len(catalog) == 0 {
			if e == nil {
				e = fmt.Errorf("the provider reported no models")
			}
			fmt.Fprintf(cli.Out, "Model discovery unavailable: %s\n", Redact(e.Error()))
		} else {
			fmt.Fprintln(cli.Out, "Models reported by the provider (listing does not prove inference access):")
		}
		ids, notes := []string{}, []string{}
		for _, model := range catalog {
			ids = append(ids, model.ID)
			note := model.Name
			if len(model.Efforts) > 0 {
				note = strings.TrimSpace(note + "  efforts: " + strings.Join(model.Efforts, ", "))
			}
			notes = append(notes, note)
		}
		r.Model, e = cli.choose("Exact model ID", ids, notes, false)
		if e != nil {
			return r, e
		}
	}
	if v := o.Get("effort"); v != "" {
		r.ReasoningEffort = v
	}
	if (r.Provider == "codex" || r.Provider == "claude-code") && r.ReasoningEffort == "" {
		efforts := []string{}
		for _, model := range catalog {
			if model.ID == r.Model {
				efforts = model.Efforts
			}
		}
		if len(efforts) == 0 {
			efforts = Probe(ctx, p.Root, r).Efforts
		}
		if len(efforts) > 0 {
			fmt.Fprintf(cli.Out, "Reasoning efforts supported by %s:\n", r.Model)
		}
		r.ReasoningEffort, e = cli.choose("Provider-specific reasoning effort", efforts, nil, len(efforts) > 0)
		if e != nil {
			return r, e
		}
	}
	return r, ValidateRuntime(r)
}
func (cli *CLI) init(ctx context.Context, p *Project, o Options) error {
	if _, e := os.Stat(p.Path("settings.arena.json")); e == nil {
		return fmt.Errorf("project is already initialized; use arena settings edit")
	}
	r, e := cli.runtimeChoice(ctx, p, Runtime{}, o)
	if e != nil {
		return e
	}
	s := DefaultSettings(r)
	fmt.Fprintf(cli.Out, "Default runtime: %s / %s / %s\nPolicies: human integration review; ask before coworker/workflow changes and cross-provider sharing.\nRetention: full local history with secret redaction; process output summaries.\n", r.Provider, r.Model, r.ReasoningEffort)
	if o.Bool("dry-run") {
		cli.print(s)
		return nil
	}
	if e := cli.confirm("Create project settings and permanent orchestrator", o); e != nil {
		return e
	}
	l, e := p.Lock("config")
	if e != nil {
		return e
	}
	defer l.Close()
	if _, err := os.Stat(p.Path("settings.arena.json")); err == nil {
		return fmt.Errorf("another session initialized this project; reload settings")
	}
	profile := NewProfile("arena-orchestrator", "control", "Manage arenas through the Arena CLI", "Use the arena-orchestrator skill. Ask for required choices. Honor project inquiry policy. Do not hand-edit generated configuration.", []string{"read", "run_commands"})
	if _, e = p.Commit("initialize project", map[string][]byte{"settings.arena.json": JSON(s), s.PermanentOnCallAgent.Profile: JSON(profile)}); e != nil {
		return e
	}
	ignore := filepath.Join(p.Root, ".gitignore")
	b, e := os.ReadFile(ignore)
	if e != nil && !os.IsNotExist(e) {
		return e
	}
	for _, pattern := range []string{".arena/.state/", ".arena/locks/", ".arena-root"} {
		if !strings.Contains(string(b), pattern) {
			b = append(b, []byte("\n"+pattern+"\n")...)
		}
	}
	if e := Atomic(ignore, b, 0644); e != nil {
		return e
	}
	target := map[string]string{"codex": "codex", "claude-code": "claude", "gemini": "gemini", "antigravity": "gemini"}[r.Provider]
	if target != "" {
		path, e := p.InstallSkill(target, false, false)
		if e != nil {
			return e
		}
		fmt.Fprintln(cli.Out, "Skill installed:", path)
	}
	if e := p.Audit("", "project.initialized", map[string]any{"defaultRuntime": r, "retention": s.Audit.Retention}); e != nil {
		return e
	}
	fmt.Fprintln(cli.Out, "Project initialized. Create a workflow with arena workflow create.")
	if !o.Bool("no-bootstrap") {
		if err := l.Close(); err != nil {
			return err
		}
		return cli.bootstrap(ctx, p, s)
	}
	return nil
}
func (cli *CLI) workflow(ctx context.Context, p *Project, args []string, o Options) error {
	if len(args) == 0 {
		return fmt.Errorf("workflow requires create/list/show/edit/delete/validate")
	}
	if args[0] == "list" {
		ids, e := p.List()
		if e != nil {
			return e
		}
		cli.print(ids)
		return nil
	}
	id := ""
	if len(args) > 1 {
		id = args[1]
	}
	if id == "" {
		var e error
		id, e = cli.ask("Workflow ID", "", true)
		if e != nil {
			return e
		}
	}
	if e := validID(id); e != nil {
		return e
	}
	switch args[0] {
	case "create", "edit":
		var w Workflow
		profiles := map[string]Profile{}
		expected := ""
		if args[0] == "edit" {
			var e error
			w, profiles, expected, e = p.LoadVersion(id)
			if e != nil {
				return e
			}
		} else {
			w = NewWorkflow(id, o.Get("name"))
			if w.Name == "" {
				w.Name = id
			}
		}
		if input := o.Get("input"); input != "" {
			var request struct {
				Workflow Workflow           `json:"workflow"`
				Profiles map[string]Profile `json:"profiles"`
			}
			if e := ReadJSON(input, &request); e != nil {
				return e
			}
			if request.Workflow.Id != id {
				return fmt.Errorf("input workflow ID differs from selected ID")
			}
			w = request.Workflow
			profiles = request.Profiles
		} else if args[0] == "create" && o.Get("provider") != "" {
			r, e := cli.runtimeChoice(ctx, p, Runtime{}, o)
			if e != nil {
				return e
			}
			head := o.Get("head")
			if head == "" {
				return fmt.Errorf("--head is required for noninteractive creation")
			}
			w.HeadAgent = head
			w.Agents = []Agent{{Id: head, Level: Int(0), Profile: ".agents/" + id + "/" + head + ".agent.json", Runtime: r}}
			caps := split(o.Get("capabilities"))
			if len(caps) == 0 {
				caps = []string{"read"}
			}
			profiles[head] = NewProfile(head, "coordinator", "Carry out the user task and coordinate delegated work", "Use Arena envelopes for all handoffs and report a structured result.", caps)
		} else {
			var e error
			w, profiles, e = cli.buildWorkflow(ctx, p, w, profiles)
			if e != nil {
				return e
			}
		}
		s, e := p.Settings()
		if e != nil {
			return e
		}
		if e := ValidateWorkflow(w, profiles, s); e != nil {
			return e
		}
		fmt.Fprintln(cli.Out, GraphSummary(w))
		for _, warning := range WorkflowWarnings(w, profiles) {
			fmt.Fprintln(cli.Out, warning)
		}
		if note := blanketApprovalNote(w, profiles); note != "" {
			fmt.Fprintln(cli.Out, note)
		}
		fmt.Fprintln(cli.Out, ScoreArena(w, profiles).Summary())
		if o.Bool("dry-run") {
			return nil
		}
		if e := cli.confirm("Save workflow and generated profiles", o); e != nil {
			return e
		}
		tx, e := p.Save(w, profiles, expected)
		if e != nil {
			return e
		}
		fmt.Fprintln(cli.Out, "Saved", id, "transaction", tx)
		return p.Audit(id, "workflow.saved", map[string]any{"transaction": tx})
	case "validate", "show":
		w, profiles, e := p.Load(id)
		if e != nil {
			return e
		}
		if args[0] == "validate" {
			fmt.Fprintln(cli.Out, id, "valid")
			for _, warning := range WorkflowWarnings(w, profiles) {
				fmt.Fprintln(cli.Out, warning)
			}
		} else if o.Bool("json") {
			cli.print(w)
		} else {
			fmt.Fprintln(cli.Out, GraphSummary(w))
		}
		return nil
	case "delete":
		w, _, e := p.Load(id)
		if e != nil {
			return e
		}
		s, e := p.Settings()
		if e != nil {
			return e
		}
		if s.ApprovalPolicies.WorkflowDeletion == "auto-refuse" {
			return fmt.Errorf("workflow-deletion policy refuses deletion")
		}
		if e := cli.confirm("Delete workflow (profiles, context and history remain recoverable)", o); e != nil {
			return e
		}
		l, e := p.Lock("config")
		if e != nil {
			return e
		}
		defer l.Close()
		tx, e := p.Commit("delete workflow "+id, map[string][]byte{w.Id + ".arena.json": nil})
		if e != nil {
			return e
		}
		fmt.Fprintln(cli.Out, "Deleted", id, "transaction", tx)
		return p.Audit(id, "workflow.deleted", map[string]any{"transaction": tx})
	}
	return fmt.Errorf("unknown workflow command %s", args[0])
}
func (cli *CLI) settings(p *Project, args []string, o Options) error {
	if len(args) == 0 {
		return fmt.Errorf("settings requires show/edit")
	}
	s, e := p.Settings()
	if e != nil {
		return e
	}
	if args[0] == "show" {
		cli.print(s)
		return nil
	}
	if args[0] != "edit" {
		return fmt.Errorf("unknown settings command")
	}
	before, e := os.ReadFile(p.Path("settings.arena.json"))
	if e != nil {
		return e
	}
	m := convert[map[string]any](s)
	if len(o["set"]) == 0 {
		return fmt.Errorf("use --set /json/pointer=JSON-value (repeatable); arena settings show lists fields")
	}
	for _, assignment := range o["set"] {
		pointer, value, ok := strings.Cut(assignment, "=")
		if !ok {
			return fmt.Errorf("--set requires /pointer=JSON-value")
		}
		var v any
		if e := DecodeJSON([]byte(value), &v); e != nil {
			return e
		}
		if e := SetPointer(m, pointer, v); e != nil {
			return e
		}
	}
	s = convert[Settings](m)
	if e := ValidateSettings(s); e != nil {
		return e
	}
	cli.print(s)
	if o.Bool("dry-run") {
		return nil
	}
	if e := cli.confirm("Save project settings", o); e != nil {
		return e
	}
	l, e := p.Lock("config")
	if e != nil {
		return e
	}
	defer l.Close()
	current, e := os.ReadFile(p.Path("settings.arena.json"))
	if e != nil {
		return e
	}
	if Hash(current) != Hash(before) {
		return fmt.Errorf("settings changed during edit; reload")
	}
	ids, e := p.List()
	if e != nil {
		return e
	}
	for _, id := range ids {
		w, profiles, e := p.loadUnlocked(id)
		if e != nil {
			return e
		}
		if e := ValidateWorkflow(w, profiles, s); e != nil {
			return fmt.Errorf("workflow %s would become invalid: %w", id, e)
		}
	}
	tx, e := p.Commit("edit settings", map[string][]byte{"settings.arena.json": JSON(s)})
	if e != nil {
		return e
	}
	return p.Audit("", "settings.saved", map[string]any{"transaction": tx})
}
func SetPointer(m map[string]any, pointer string, v any) error {
	parts := strings.Split(strings.TrimPrefix(pointer, "/"), "/")
	if !strings.HasPrefix(pointer, "/") || len(parts) == 0 {
		return fmt.Errorf("expected JSON pointer")
	}
	for i, raw := range parts {
		part := strings.ReplaceAll(strings.ReplaceAll(raw, "~1", "/"), "~0", "~")
		if i == len(parts)-1 {
			m[part] = v
			return nil
		}
		next, ok := m[part].(map[string]any)
		if !ok {
			return fmt.Errorf("pointer parent %s is not an object", part)
		}
		m = next
	}
	return nil
}
func (cli *CLI) status(ctx context.Context, p *Project, args []string, o Options) error {
	id := o.Get("workflow")
	if len(args) > 0 && args[0] != "status" {
		id = args[0]
	}
	results, e := p.readiness(ctx, id)
	if e != nil {
		return e
	}
	if o.Bool("json") {
		cli.print(results)
	} else {
		for _, v := range results {
			fmt.Fprintf(cli.Out, "%s / %s: %s | %s | %s\n  %s; %s\n", v.Provider, v.Model, v.State, v.Version, v.Auth, v.ModelState, v.Detail)
			efforts := strings.Join(v.Efforts, ", ")
			if efforts == "" {
				efforts = "not reported by this adapter"
			}
			fmt.Fprintf(cli.Out, "  verified effort options: %s; skill: %s\n", efforts, v.Skill)
		}
	}
	return nil
}
func (cli *CLI) skill(p *Project, args []string, o Options) error {
	if len(args) == 0 {
		return fmt.Errorf("skill requires install/status/remove")
	}
	if args[0] == "targets" {
		cli.print(skillPaths)
		return nil
	}
	agent := o.Get("agent")
	if agent == "" {
		return fmt.Errorf("--agent codex|claude|gemini|all is required")
	}
	agents := []string{agent}
	if agent == "all" {
		agents = []string{"codex", "claude", "gemini"}
	}
	for _, a := range agents {
		rel, ok := skillPaths[a]
		if !ok {
			return fmt.Errorf("unknown skill target")
		}
		base := p.Root
		if o.Bool("user") {
			var e error
			base, e = os.UserHomeDir()
			if e != nil {
				return e
			}
			rel = userSkillPath(a, rel)
		}
		path, e := SafePath(base, rel)
		if e != nil {
			return e
		}
		switch args[0] {
		case "install":
			path, e = p.InstallSkill(a, o.Bool("user"), o.Bool("replace"))
			if e != nil {
				return e
			}
			fmt.Fprintln(cli.Out, a, path)
		case "status":
			b, e := os.ReadFile(path)
			state := "missing"
			if e == nil {
				state = Hash(b)
			}
			fmt.Fprintln(cli.Out, a, path, state)
		case "remove":
			if e := cli.confirm("Remove "+path, o); e != nil {
				return e
			}
			b, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			if e := Atomic(p.Path(".state", "skill-backups", ID("skill-")+".md"), b, 0600); e != nil {
				return e
			}
			if e := os.Remove(path); e != nil {
				return e
			}
			if o.Bool("user") {
				if e := forgetUserSkill(path); e != nil {
					return e
				}
			}
		default:
			return fmt.Errorf("unknown skill command")
		}
	}
	return nil
}
func (cli *CLI) lock(p *Project, args []string, o Options) error {
	if len(args) == 0 || args[0] == "list" {
		dirs, e := os.ReadDir(p.Path("locks"))
		if os.IsNotExist(e) {
			return nil
		}
		if e != nil {
			return e
		}
		for _, d := range dirs {
			var owner LockOwner
			e := ReadJSON(p.Path("locks", d.Name(), "owner.json"), &owner)
			if e != nil {
				fmt.Fprintln(cli.Out, d.Name(), "owner unknown")
			} else {
				cli.print(map[string]any{"name": d.Name(), "owner": owner})
			}
		}
		return nil
	}
	if args[0] == "recover" && len(args) == 2 {
		if e := cli.confirm("Recover lock only if its owner is proven stopped", o); e != nil {
			return e
		}
		return p.RecoverLock(args[1])
	}
	return fmt.Errorf("use lock list or lock recover <name>")
}
func (cli *CLI) transaction(p *Project, args []string, o Options) error {
	if len(args) == 0 {
		return fmt.Errorf("transaction requires list/show/recover/restore")
	}
	if args[0] == "list" {
		dirs, e := os.ReadDir(p.Path(".state", "transactions"))
		if e != nil {
			return e
		}
		for _, d := range dirs {
			fmt.Fprintln(cli.Out, d.Name())
		}
		return nil
	}
	if len(args) < 2 {
		return fmt.Errorf("transaction ID required")
	}
	if e := validID(args[1]); e != nil {
		return e
	}
	var t Transaction
	if e := ReadJSON(p.Path(".state", "transactions", args[1], "manifest.json"), &t); e != nil {
		return e
	}
	if args[0] == "show" {
		cli.print(t)
		return nil
	}
	if args[0] != "recover" && args[0] != "restore" {
		return fmt.Errorf("unknown transaction command")
	}
	if e := cli.confirm("Apply recorded transaction with conflict checks", o); e != nil {
		return e
	}
	l, e := p.Lock("config")
	if e != nil {
		return e
	}
	defer l.Close()
	if args[0] == "recover" {
		if e := p.applyTransaction(t, false); e != nil {
			return e
		}
		return Atomic(p.Path(".state", "transactions", t.ID, "complete"), []byte(Now()), 0600)
	}
	files := map[string][]byte{}
	for _, m := range t.Mutations {
		current, e := os.ReadFile(p.Path(m.Path))
		if e != nil && !os.IsNotExist(e) {
			return e
		}
		if Hash(current) != Hash(m.After) {
			return fmt.Errorf("later edits to %s; restore in an isolated recovery checkout", m.Path)
		}
		files[m.Path] = m.Before
	}
	tx, e := p.Commit("restore transaction "+t.ID, files)
	if e != nil {
		return e
	}
	fmt.Fprintln(cli.Out, tx)
	return p.Audit("", "transaction.restored", map[string]any{"original": t.ID, "transaction": tx})
}
func split(s string) []string {
	a := []string{}
	for _, x := range strings.Split(s, ",") {
		if x = strings.TrimSpace(x); x != "" {
			a = append(a, x)
		}
	}
	return a
}
func (cli *CLI) integer(label string, existing, min, max int) (int, error) {
	v, e := cli.ask(label, strconv.Itoa(existing), true)
	if e != nil {
		return 0, e
	}
	n, e := strconv.Atoi(v)
	if e != nil || n < min || n > max {
		return 0, fmt.Errorf("%s must be between %d and %d", label, min, max)
	}
	return n, nil
}
func (cli *CLI) context(p *Project, args []string, o Options) error {
	if len(args) < 2 {
		return fmt.Errorf("context requires list/search/show/add/revise/archive/restore <workflow> [entry-id]")
	}
	command, workflow := args[0], args[1]
	if command == "list" || command == "search" || command == "show" {
		entries, e := p.ContextEntries(workflow, o.Bool("all") || command == "show")
		if e != nil {
			return e
		}
		if command == "search" {
			if len(args) < 3 {
				return fmt.Errorf("search query required")
			}
			entries = ContextSearch(entries, strings.Join(args[2:], " "))
		}
		if command == "show" {
			if len(args) < 3 {
				return fmt.Errorf("entry ID required")
			}
			for _, entry := range entries {
				if entry.Id == args[2] {
					cli.print(entry)
					return nil
				}
			}
			return fmt.Errorf("context entry not found")
		}
		cli.print(entries)
		return nil
	}
	q := ContextProposal{WorkflowID: workflow, Operation: command}
	if command != "add" {
		if len(args) < 3 {
			return fmt.Errorf("entry ID required")
		}
		q.EntryID = &args[2]
	}
	if command == "add" || command == "revise" {
		var e error
		q.Title = o.Get("title")
		if q.Title == "" {
			q.Title, e = cli.ask("Title", "", true)
			if e != nil {
				return e
			}
		}
		q.Content = o.Get("content")
		if q.Content == "" {
			q.Content, e = cli.ask("Concise content", "", true)
			if e != nil {
				return e
			}
		}
		q.Confidence = o.Get("confidence")
		if q.Confidence == "" {
			q.Confidence, e = cli.ask("Confidence (verified, inferred, unverified)", "", true)
			if e != nil {
				return e
			}
		}
		q.Tags = split(o.Get("tags"))
		q.Evidence = []c.ContextEntryEvidenceItem{}
		for _, ref := range o["evidence"] {
			q.Evidence = append(q.Evidence, c.ContextEntryEvidenceItem{Kind: "other", Reference: ref})
		}
	}
	q.Reason = o.Get("reason")
	if command == "archive" && q.Reason == "" {
		var e error
		q.Reason, e = cli.ask("Archive reason", "", true)
		if e != nil {
			return e
		}
	}
	result, e := p.ProposeContext(q, "user", "", "", "", false)
	if e != nil {
		return e
	}
	cli.print(result)
	return nil
}

const helpText = `Agent Arena — local workflow coordination

arena [--workflow ID] [--plain]   Open the graph; choose when multiple arenas exist
arena setup [--project PATH] [--provider ID --model ID --effort VALUE] [--no-bootstrap] [--yes]
arena init [--provider ID --model ID --effort VALUE] [--no-bootstrap] [--yes]
arena bootstrap
arena workflow create|edit ID [--input BUILDER-REQUEST] [--dry-run] [--yes]
arena workflow list|show|validate|delete [ID]
arena shader show|validate|preview|default|off ID
arena shader set ID --input FILE [--glyphs RAMP] [--yes]
arena shader create ID "description" [--yes] [--dry-run]
arena score ID [--json]           Grade an arena S+ to F (docs/ARENA-SCORING.md)
arena share export ID [--shader] [--copy]
arena share import [CODE|-] [--clipboard] [--as ID] [--into ID] [--dry-run] [--yes]
arena settings show|edit [--set /pointer=JSON-value] [--yes]
arena status [--workflow ID] [--json]
arena auth status
arena doctor [--json]             Read-only preflight: git, provider CLIs, sandbox, WSL2, tmux
arena update [--check] [--to VERSION] [--yes]   Install a newer release (installs made by the installer only)
arena uninstall [--yes] [--dry-run] [--force]   Remove arena; project .arena/ directories are never touched
arena adapters
arena context list|search|show|add|revise|archive|restore WORKFLOW [ENTRY|QUERY]
arena skill targets
arena skill install|status|remove --agent TARGET|all [--user] [--replace]
arena agent request WORKFLOW --from ID --id ID --role ROLE --objective TEXT --reason TEXT
  --provider ID --model ID [--effort VALUE] --capabilities LIST --workspace-mode read-only|write
arena agent resolve REQUEST --approve
arena history [RUN] [--json] | arena history repair RUN
arena history RUN export FILE | arena history import FILE   Redacted audit bundle; imports are read-only
arena history RUN purge --yes [--discard-unintegrated]   Permanently delete a retired run
arena run WORKFLOW TASK [--plain]
arena watch RUN [--plain]
arena watch RUN --json [--follow]   One journal event per line; --follow tails until the run ends
arena view AGENT --run RUN [--follow]   One agent's activity as plain text
arena resume RUN [--retry-interrupted] [--plain]
arena tmux run WORKFLOW TASK | arena tmux resume RUN   Lay a run out in tmux (pane 0 supervises)
arena pause|cancel RUN
arena checkpoint list|show|restore [ID] [--yes]
arena changes RUN [CHANGE]
arena integrate RUN CHANGE [--yes]
arena integrate RUN --all [--dry-run] [--skip CHANGE]... [--yes]
arena rollback RUN CHANGE [--yes]
arena on-call invoke RUN AGENT
arena decision list RUN | arena decision answer RUN ID ANSWER
arena context-request REQUEST --approve
arena lock list|recover [NAME]
arena transaction list|show|recover|restore [ID]

All commands accept --project PATH. Generated configuration is owned by Arena.
Use --help on any command for this reference. Runtime commands are listed as implemented.
`
