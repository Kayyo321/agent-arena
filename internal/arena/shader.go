package arena

import (
	"context"
	"fmt"
	"io"
	"os"
	"strings"

	assets "github.com/Kayyo321/agent-arena"
	c "github.com/Kayyo321/agent-arena/internal/contract"
	"github.com/Kayyo321/agent-arena/internal/shader"
)

func defaultShader(id string) *c.WorkflowSettingsBackgroundShader {
	return &c.WorkflowSettingsBackgroundShader{Enabled: true, Source: id + ".background.hlsl", Glyphs: "."}
}
func shaderSettings(w Workflow) c.WorkflowSettingsBackgroundShader {
	if w.Settings.BackgroundShader == nil {
		return *defaultShader(w.Id)
	}
	return *w.Settings.BackgroundShader
}
func readShader(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("shader must be a regular file")
	}
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	info, err = f.Stat()
	if err != nil {
		return "", err
	}
	if !info.Mode().IsRegular() {
		return "", fmt.Errorf("shader must be a regular file")
	}
	b, err := io.ReadAll(io.LimitReader(f, shader.MaxSource+1))
	if err != nil {
		return "", err
	}
	if len(b) > shader.MaxSource {
		return "", fmt.Errorf("shader exceeds %d bytes", shader.MaxSource)
	}
	return string(b), nil
}
func (p *Project) shaderSource(w Workflow) (string, error) {
	if w.Settings.BackgroundShader == nil {
		return shader.DefaultSource, nil
	}
	if w.Settings.BackgroundShader.Source != w.Id+".background.hlsl" {
		return "", fmt.Errorf("shader source must be %s.background.hlsl", w.Id)
	}
	path, err := SafePath(p.Path(), w.Settings.BackgroundShader.Source)
	if err != nil {
		return "", err
	}
	return readShader(path)
}
func validateGlyphs(s string) error {
	if len(s) < 1 || len(s) > 16 {
		return fmt.Errorf("glyph ramp must contain 1–16 ASCII characters")
	}
	for _, ch := range s {
		if !strings.ContainsRune(" .,:;~+=*#%@-", ch) {
			return fmt.Errorf("glyph ramp contains unsupported character")
		}
	}
	return nil
}

// ShaderDraft is source returned as data, never a model writing the control tree.
type ShaderDraft struct{ Source, Glyphs string }

func generateShader(ctx context.Context, p *Project, w Workflow, description string, invoker Invoker, probe func(context.Context, string, Runtime) Readiness) (draft ShaderDraft, err error) {
	if strings.TrimSpace(description) == "" || len(description) > 16384 {
		return draft, fmt.Errorf("describe the shader in 1–16384 bytes")
	}
	s, err := p.Settings()
	if err != nil {
		return draft, err
	}
	lock, err := p.Lock("orchestrator")
	if err != nil {
		return draft, err
	}
	defer lock.Close()
	ctx, cancel := inferenceDeadline(ctx, s.Limits.MaxAgentRunMinutes)
	defer cancel()
	ready := probe(ctx, p.Root, s.DefaultModel)
	if !ready.CanStart {
		return draft, fmt.Errorf("default model unavailable: %s (%s)", ready.State, ready.Detail)
	}
	head, e := Git(p.Root, "rev-parse", "HEAD")
	if e != nil {
		head = "unborn"
	}
	packet := NewPacket("project", "task", "user", "arena", "arena-orchestrator", s.DefaultModel.Provider, "", taskPayload("Background Shader Creation for "+w.Id, p.Root, head, []string{Redact(description)}, false), s.DefaultModel)
	run := Run{ID: "project", Workflow: w, Settings: s, Retention: s.Audit.Retention}
	if _, err = p.Send(run, packet); err != nil {
		return draft, err
	}
	emit := func(kind string, data map[string]any) error {
		_, e := p.Append("project", w.Id, "arena-orchestrator", kind, data)
		return e
	}
	defer func() {
		if err != nil {
			_ = emit("shader.failed", map[string]any{"error": err.Error(), "messageId": packet["messageId"]})
		}
	}()
	if err = emit("shader.started", map[string]any{"trigger": "explicit-user", "model": s.DefaultModel.Model, "maxInvocations": 1, "maxMinutes": s.Limits.MaxAgentRunMinutes}); err != nil {
		return draft, err
	}
	guide, _ := assets.Files.ReadFile("docs/BACKGROUND-SHADERS.md")
	in := Invocation{Runtime: s.DefaultModel, Dir: p.Root, AgentID: "arena-orchestrator", Prompt: "Use the Background Shader Creation skill below. Return source and glyphs only. You are invoked inside the Arena host: return source as data; do not call the CLI or write files.\n" + string(assets.Skill()) + "\n" + string(guide) + "\nExample:\n" + shader.DefaultSource + "\nTask envelope:\n" + string(SafeJSON(packet)), Schema: objectSchema(map[string]any{"source": map[string]any{"type": "string"}, "glyphs": map[string]any{"type": "string"}}), Delivered: func() error { return p.Acknowledge(run, packet) }, Diagnostic: emit, Usage: func(data map[string]any) error { return emit("usage.reported", map[string]any{"usage": data}) }}
	out, err := invoker.Invoke(ctx, in)
	if err != nil {
		return draft, err
	}
	draft = ShaderDraft{str(out["source"]), str(out["glyphs"])}
	if _, err = shader.Compile(draft.Source); err != nil {
		return draft, fmt.Errorf("generated shader rejected: %w", err)
	}
	if err = validateGlyphs(draft.Glyphs); err != nil {
		return draft, err
	}
	result := NewPacket("project", "result", "arena-orchestrator", s.DefaultModel.Provider, "user", "arena", str(packet["correlationId"]), map[string]any{"summary": "Validated background shader for " + w.Id, "changedFiles": []string{}, "validation": []any{map[string]any{"command": "Arena HLSL profile compiler", "outcome": "passed"}}, "limitations": []string{"CPU HLSL subset; source not yet applied"}, "nextAction": "Review and apply the shader draft"}, s.DefaultModel)
	if _, err = p.Deliver(run, result); err != nil {
		return draft, err
	}
	err = emit("shader.generated", map[string]any{"sourceHash": Hash([]byte(draft.Source)), "glyphs": draft.Glyphs})
	return draft, err
}

func (cli *CLI) shader(ctx context.Context, p *Project, args []string, o Options) error {
	if len(args) < 2 {
		return fmt.Errorf("shader requires show|validate|preview|set|create|default|off WORKFLOW [description]; set requires --input FILE")
	}
	w, profiles, version, err := p.LoadVersion(args[1])
	if err != nil {
		return err
	}
	cfg := shaderSettings(w)
	draft := ShaderDraft{Glyphs: cfg.Glyphs}
	switch args[0] {
	case "off":
		cfg.Enabled = false
	case "default":
		cfg = *defaultShader(w.Id)
		draft = ShaderDraft{shader.DefaultSource, "."}
	case "set":
		if o.Get("input") == "" {
			return fmt.Errorf("shader set requires --input FILE")
		}
		draft.Source, err = readShader(o.Get("input"))
		if err != nil {
			return err
		}
		cfg.Enabled = true
	case "create":
		draft, err = generateShader(ctx, p, w, strings.Join(args[2:], " "), Providers{}, Probe)
		if err != nil {
			return err
		}
		cfg.Enabled = true
	case "show", "validate", "preview":
		draft.Source, err = p.shaderSource(w)
		if err != nil {
			return err
		}
	default:
		return fmt.Errorf("unknown shader command %s", args[0])
	}
	if o.Get("glyphs") != "" {
		draft.Glyphs = o.Get("glyphs")
	}
	if err = validateGlyphs(draft.Glyphs); err != nil {
		return err
	}
	var program *shader.Program
	if draft.Source != "" {
		program, err = shader.Compile(draft.Source)
		if err != nil {
			return err
		}
	}
	switch args[0] {
	case "show":
		fmt.Fprintf(cli.Out, "%s · enabled=%t · glyphs=%q\n%s", cfg.Source, cfg.Enabled, draft.Glyphs, draft.Source)
		return nil
	case "validate":
		fmt.Fprintln(cli.Out, "Valid Arena HLSL profile v1")
		return nil
	case "preview":
		fmt.Fprintln(cli.Out, shaderPreview(program, draft.Glyphs))
		return nil
	}
	if program != nil {
		fmt.Fprintln(cli.Out, shaderPreview(program, draft.Glyphs))
	}
	if o.Bool("dry-run") {
		return nil
	}
	if err = cli.confirm("Save this arena's background shader", o); err != nil {
		return err
	}
	cfg.Glyphs = draft.Glyphs
	w.Settings.BackgroundShader = &cfg
	var source *string
	if draft.Source != "" {
		source = &draft.Source
	}
	tx, err := p.saveWithShader(w, profiles, version, source)
	if err != nil {
		return err
	}
	fmt.Fprintln(cli.Out, "Saved", cfg.Source, "transaction", tx)
	return p.Audit(w.Id, "shader.saved", map[string]any{"transaction": tx, "source": cfg.Source, "enabled": cfg.Enabled})
}
