package arena

import (
	"bytes"
	"compress/flate"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"regexp"
	"strings"
	"unicode"

	c "github.com/Kayyo321/agent-arena/internal/contract"
	"github.com/Kayyo321/agent-arena/internal/shader"
)

// A share code is one line of text:
//
//	arena1.<a|s>.<check>.<payload>
//
// payload is base64url (no padding) of raw DEFLATE over the compact JSON of a
// ShareBundle (schemas/share-bundle.schema.json); check is the first 8 hex
// digits of SHA-256 over payload, so a truncated copy fails before decoding.
const (
	shareFormat   = "arena.share.v1"
	maxShareText  = 1 << 20   // pasted text, including wrapping and chatter
	maxShareCode  = 512 << 10 // the code itself
	maxShareJSON  = 1 << 20   // inflated bundle
	shareCodeHelp = "copy it again with /copy or arena share export"
)

// shareProbe checks an imported arena's runtimes; tests replace it.
var shareProbe = Probe

type ShareShader struct {
	Source string `json:"source"`
	Glyphs string `json:"glyphs"`
}

// ShareBundle is everything a share code carries: an arena's workflow,
// profiles and shader, or a shader alone. Shared context, run history and
// project settings never travel.
type ShareBundle struct {
	Format   string             `json:"format"`
	Kind     string             `json:"kind"`
	Rubric   int                `json:"rubric,omitempty"`
	From     string             `json:"from,omitempty"`
	Workflow *Workflow          `json:"workflow,omitempty"`
	Profiles map[string]Profile `json:"profiles,omitempty"`
	Shader   *ShareShader       `json:"shader"`
}

func shareCheck(payload string) string {
	sum := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(sum[:])[:8]
}

// EncodeShare packs a bundle into a share code, refusing secret-like values.
func EncodeShare(b ShareBundle) (string, error) {
	data, err := json.Marshal(b)
	if err != nil {
		return "", err
	}
	if Redact(string(data)) != string(data) {
		return "", fmt.Errorf("arena contains secret-like values; remove them before sharing")
	}
	if err := c.ValidateShareBundle(data); err != nil {
		return "", err
	}
	var packed bytes.Buffer
	w, err := flate.NewWriter(&packed, flate.BestCompression)
	if err != nil {
		return "", err
	}
	if _, err := w.Write(data); err != nil {
		return "", err
	}
	if err := w.Close(); err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(packed.Bytes())
	if len(payload) > maxShareCode {
		return "", fmt.Errorf("arena is too large to share (%d KiB)", len(payload)>>10)
	}
	return "arena1." + b.Kind[:1] + "." + shareCheck(payload) + "." + payload, nil
}

// FindShareCode reports whether text contains a share code and its kind.
// Terminals wrap long lines and chat apps add text around a code, so the code
// may be split by whitespace: pieces are joined while they stay in the
// base64url alphabet, and the code ends at the first piece boundary where
// the checksum matches. A damaged code is still found, so decoding can say
// it was cut off instead of treating it as ordinary text.
func FindShareCode(text string) (code, kind string, ok bool) {
	if len(text) > maxShareText {
		return "", "", false
	}
	for start := strings.Index(text, "arena1."); start >= 0; {
		if code, kind, ok := scanShareCode(text[start:]); ok {
			return code, kind, true
		}
		next := strings.Index(text[start+1:], "arena1.")
		if next < 0 {
			break
		}
		start += 1 + next
	}
	return "", "", false
}

func scanShareCode(text string) (code, kind string, ok bool) {
	const headerLength = len("arena1.a.01234567.")
	var b strings.Builder
	i := 0
	// The header may itself be wrapped; collect it without whitespace.
	for ; i < len(text) && b.Len() < headerLength; i++ {
		if !unicode.IsSpace(rune(text[i])) {
			b.WriteByte(text[i])
		}
	}
	header := shareHeaderPattern.FindStringSubmatch(b.String())
	if header == nil {
		return "", "", false
	}
	kind = map[string]string{"a": "arena", "s": "shader"}[header[1]]
	digest := sha256.New()
	payload := 0
	for i < len(text) {
		for i < len(text) && unicode.IsSpace(rune(text[i])) {
			i++
		}
		end := i
		for end < len(text) && shareAlphabet(text[end]) {
			end++
		}
		if end == i {
			break // the next piece is not base64url
		}
		b.WriteString(text[i:end])
		digest.Write([]byte(text[i:end]))
		payload += end - i
		if payload > maxShareCode {
			break
		}
		if hex.EncodeToString(digest.Sum(nil))[:8] == header[2] {
			return b.String(), kind, true
		}
		if end < len(text) && !unicode.IsSpace(rune(text[end])) {
			break // punctuation ends the code, as in "…code)."
		}
		i = end
	}
	if payload == 0 {
		return "", "", false
	}
	// No boundary matched the checksum: report the longest run as damaged.
	return b.String(), kind, true
}

var shareHeaderPattern = regexp.MustCompile(`^arena1\.([as])\.([0-9a-f]{8})\.$`)

func shareAlphabet(ch byte) bool {
	return ch >= 'a' && ch <= 'z' || ch >= 'A' && ch <= 'Z' || ch >= '0' && ch <= '9' || ch == '-' || ch == '_'
}

// DecodeShare unpacks and checks a share code without touching the project:
// the bundle shape, profiles, runtimes and shader are validated here, and the
// graph is validated against project settings on import.
func DecodeShare(text string) (ShareBundle, error) {
	code, kind, ok := FindShareCode(text)
	if !ok {
		return ShareBundle{}, fmt.Errorf("no Arena share code found (codes start with arena1.)")
	}
	parts := strings.SplitN(code, ".", 4)
	if len(parts[3]) > maxShareCode {
		return ShareBundle{}, fmt.Errorf("share code is too large")
	}
	if shareCheck(parts[3]) != parts[2] {
		return ShareBundle{}, fmt.Errorf("this share code was cut off or altered; %s", shareCodeHelp)
	}
	packed, err := base64.RawURLEncoding.DecodeString(parts[3])
	if err != nil {
		return ShareBundle{}, fmt.Errorf("share code is not valid base64url: %w", err)
	}
	data, err := io.ReadAll(io.LimitReader(flate.NewReader(bytes.NewReader(packed)), maxShareJSON+1))
	if err != nil {
		return ShareBundle{}, fmt.Errorf("share code does not decompress: %w", err)
	}
	if len(data) > maxShareJSON {
		return ShareBundle{}, fmt.Errorf("share code expands beyond %d KiB", maxShareJSON>>10)
	}
	if err := c.ValidateShareBundle(data); err != nil {
		return ShareBundle{}, fmt.Errorf("share code: %w", err)
	}
	var raw struct {
		Format   string                     `json:"format"`
		Kind     string                     `json:"kind"`
		Rubric   int                        `json:"rubric"`
		From     string                     `json:"from"`
		Workflow json.RawMessage            `json:"workflow"`
		Profiles map[string]json.RawMessage `json:"profiles"`
		Shader   *ShareShader               `json:"shader"`
	}
	if err := DecodeJSON(data, &raw); err != nil {
		return ShareBundle{}, err
	}
	if raw.Kind != kind {
		return ShareBundle{}, fmt.Errorf("share code prefix says %s but carries %s", kind, raw.Kind)
	}
	b := ShareBundle{Format: raw.Format, Kind: raw.Kind, Rubric: raw.Rubric, From: raw.From, Shader: raw.Shader}
	if b.Shader != nil {
		if _, err := shader.Compile(b.Shader.Source); err != nil {
			return ShareBundle{}, fmt.Errorf("shared shader: %w", err)
		}
		if err := validateGlyphs(b.Shader.Glyphs); err != nil {
			return ShareBundle{}, fmt.Errorf("shared shader: %w", err)
		}
	}
	if kind == "shader" {
		return b, nil
	}
	if err := c.ValidateWorkflow(raw.Workflow); err != nil {
		return ShareBundle{}, fmt.Errorf("shared workflow: %w", err)
	}
	var w Workflow
	if err := DecodeJSON(raw.Workflow, &w); err != nil {
		return ShareBundle{}, err
	}
	b.Workflow, b.Profiles = &w, map[string]Profile{}
	for _, a := range w.Agents {
		if err := ValidateRuntime(a.Runtime); err != nil {
			return ShareBundle{}, fmt.Errorf("shared agent %s: %w", a.Id, err)
		}
		data, ok := raw.Profiles[a.Id]
		if !ok {
			return ShareBundle{}, fmt.Errorf("share code has no profile for agent %s", a.Id)
		}
		if err := c.ValidateProfile(data); err != nil {
			return ShareBundle{}, fmt.Errorf("shared profile %s: %w", a.Id, err)
		}
		var p Profile
		if err := DecodeJSON(data, &p); err != nil {
			return ShareBundle{}, err
		}
		if p.Id != a.Id {
			return ShareBundle{}, fmt.Errorf("shared profile %s is labelled %s", a.Id, p.Id)
		}
		b.Profiles[a.Id] = p
	}
	if len(raw.Profiles) != len(w.Agents) {
		return ShareBundle{}, fmt.Errorf("share code carries profiles for agents that are not in the arena")
	}
	if cfg := w.Settings.BackgroundShader; cfg != nil && cfg.Enabled && b.Shader == nil {
		return ShareBundle{}, fmt.Errorf("share code enables a background shader but does not include it")
	}
	return b, nil
}

// ExportArena packs a saved arena with its profiles and shader.
func (p *Project) ExportArena(id string) (string, error) {
	w, profiles, err := p.Load(id)
	if err != nil {
		return "", err
	}
	b := ShareBundle{Format: shareFormat, Kind: "arena", Rubric: RubricVersion, Workflow: &w, Profiles: profiles}
	if cfg := w.Settings.BackgroundShader; cfg == nil || cfg.Enabled {
		source, err := p.shaderSource(w)
		if err != nil {
			return "", err
		}
		b.Shader = &ShareShader{source, shaderSettings(w).Glyphs}
	} else if source, err := p.shaderSource(w); err == nil {
		b.Shader = &ShareShader{source, cfg.Glyphs}
	}
	return EncodeShare(b)
}

// ExportShader packs only an arena's background shader.
func (p *Project) ExportShader(id string) (string, error) {
	w, _, err := p.Load(id)
	if err != nil {
		return "", err
	}
	source, err := p.shaderSource(w)
	if err != nil {
		return "", err
	}
	return EncodeShare(ShareBundle{Format: shareFormat, Kind: "shader", From: w.Name, Shader: &ShareShader{source, shaderSettings(w).Glyphs}})
}

// rekeyWorkflow renames an arena, rewriting the paths that embed its ID.
func rekeyWorkflow(w Workflow, id string) Workflow {
	w.Id = id
	agents := append([]Agent{}, w.Agents...)
	for i := range agents {
		agents[i].Profile = ".agents/" + id + "/" + agents[i].Id + ".agent.json"
	}
	w.Agents = agents
	if cfg := w.Settings.BackgroundShader; cfg != nil {
		copied := *cfg
		copied.Source = id + ".background.hlsl"
		w.Settings.BackgroundShader = &copied
	}
	return w
}

// freeArenaID reports whether neither the arena nor its shader file exists.
func (p *Project) freeArenaID(id string) (bool, error) {
	for _, name := range []string{id + ".arena.json", id + ".background.hlsl"} {
		if _, err := os.Lstat(p.Path(name)); err == nil {
			return false, nil
		} else if !os.IsNotExist(err) {
			return false, err
		}
	}
	return true, nil
}

// PrepareImport turns a decoded arena bundle into a draft for this project:
// it picks a free ID (as, or the author's ID with a -2, -3 … suffix), keeps
// the author's runtimes, validates the graph against project settings and
// lists what the user should check before saving.
func (p *Project) PrepareImport(ctx context.Context, b ShareBundle, as string, probe func(context.Context, string, Runtime) Readiness) (*WorkflowDraft, error) {
	if b.Kind != "arena" || b.Workflow == nil {
		return nil, fmt.Errorf("this is a shader code; paste it into an open arena or use arena share import --into ID")
	}
	s, err := p.Settings()
	if err != nil {
		return nil, err
	}
	notes := []string{}
	id := b.Workflow.Id
	if as != "" {
		if err := validID(as); err != nil {
			return nil, err
		}
		if free, err := p.freeArenaID(as); err != nil {
			return nil, err
		} else if !free {
			return nil, fmt.Errorf("arena %s already exists; choose another --as ID", as)
		}
		id = as
	} else {
		for n := 2; ; n++ {
			free, err := p.freeArenaID(id)
			if err != nil {
				return nil, err
			}
			if free {
				break
			}
			id = fmt.Sprintf("%s-%d", strings.TrimRight(b.Workflow.Id[:min(len(b.Workflow.Id), 60)], "-"), n)
		}
		if id != b.Workflow.Id {
			notes = append(notes, "Imported as "+id+" because "+b.Workflow.Id+" already exists.")
		}
	}
	w := rekeyWorkflow(*b.Workflow, id)
	var draft *ShaderDraft
	if b.Shader != nil {
		if w.Settings.BackgroundShader == nil {
			w.Settings.BackgroundShader = defaultShader(id)
		}
		w.Settings.BackgroundShader.Glyphs = b.Shader.Glyphs
		draft = &ShaderDraft{b.Shader.Source, b.Shader.Glyphs}
	}
	if err := ValidateWorkflow(w, b.Profiles, s); err != nil {
		return nil, fmt.Errorf("shared arena does not fit this project: %w", err)
	}
	return &WorkflowDraft{Workflow: w, Profiles: b.Profiles, Shader: draft, Source: "share-import", Notes: append(notes, importNotes(ctx, p, w, b.Profiles, probe)...)}, nil
}

// importNotes lists what someone else's arena may do here: who writes or runs
// commands, how changes integrate, and which runtimes are not ready.
func importNotes(ctx context.Context, p *Project, w Workflow, profiles map[string]Profile, probe func(context.Context, string, Runtime) Readiness) []string {
	notes := []string{"Pasted from a share code: read each profile's instructions (/context NAME after saving); someone else wrote them."}
	writers, runners := []string{}, []string{}
	for _, a := range w.Agents {
		if has(profiles[a.Id], "write") {
			writers = append(writers, a.Id)
		}
		if has(profiles[a.Id], "run_commands") {
			runners = append(runners, a.Id)
		}
	}
	if len(writers) > 0 {
		notes = append(notes, "Writes code: "+strings.Join(writers, ", ")+".")
	}
	if len(runners) > 0 {
		notes = append(notes, "Runs commands: "+strings.Join(runners, ", ")+".")
	}
	if w.Settings.Integration.Policy == "auto-if-clean" {
		notes = append(notes, "Integration policy auto-if-clean: clean changes integrate without your review.")
	}
	if probe == nil {
		return append(notes, "Runtimes are the author's choices; Arena checks readiness when the graph opens.")
	}
	for _, r := range Required([]Workflow{w}, Settings{DefaultModel: Runtime{Provider: "arena"}}) {
		if r.Provider == "arena" {
			continue
		}
		label := r.Provider + " / " + r.Model
		if r.ReasoningEffort != "" {
			label += " / " + r.ReasoningEffort
		}
		if status := probe(ctx, p.Root, r); status.CanStart {
			notes = append(notes, "✓ "+label+" ready")
		} else {
			notes = append(notes, "✗ "+label+" not ready ("+Redact(status.Detail)+"): set it up, or ask /chat to change the runtime before running.")
		}
	}
	return notes
}

// SaveDraft saves an interview or imported draft as a new arena, with its
// shared shader when it brought one.
func (p *Project) SaveDraft(d WorkflowDraft) (string, error) {
	if d.Shader != nil {
		return p.saveWithShader(d.Workflow, d.Profiles, "", &d.Shader.Source)
	}
	return p.Save(d.Workflow, d.Profiles, "")
}

func (cli *CLI) share(ctx context.Context, p *Project, args []string, o Options) error {
	usage := fmt.Errorf("arena share export ID [--shader] [--copy] | arena share import [CODE|-] [--clipboard] [--as ID] [--into ID] [--dry-run] [--yes]")
	if len(args) == 0 {
		return usage
	}
	switch args[0] {
	case "export":
		if len(args) != 2 {
			return usage
		}
		export := p.ExportArena
		if o.Bool("shader") {
			export = p.ExportShader
		}
		code, err := export(args[1])
		if err != nil {
			return err
		}
		fmt.Fprintln(cli.Out, code)
		if o.Bool("copy") {
			tool, err := writeClipboard(ctx, code)
			if err != nil {
				return fmt.Errorf("printed the code above, but could not copy it: %w", err)
			}
			fmt.Fprintf(cli.Err, "Copied %d characters with %s.\n", len(code), tool)
		}
		return nil
	case "import":
		text, err := cli.shareInput(ctx, args[1:], o)
		if err != nil {
			return err
		}
		b, err := DecodeShare(text)
		if err != nil {
			return err
		}
		if b.Kind == "shader" {
			if o.Get("into") == "" {
				return fmt.Errorf("this is a shader code; choose the arena with --into ID")
			}
			file, err := os.CreateTemp("", "arena-shader-*.hlsl")
			if err != nil {
				return err
			}
			defer os.Remove(file.Name())
			if _, err := file.WriteString(b.Shader.Source); err != nil {
				file.Close()
				return err
			}
			if err := file.Close(); err != nil {
				return err
			}
			o["input"], o["glyphs"] = []string{file.Name()}, []string{b.Shader.Glyphs}
			if b.From != "" {
				fmt.Fprintln(cli.Out, "Shader from", b.From)
			}
			return cli.shader(ctx, p, []string{"set", o.Get("into")}, o)
		}
		if o.Get("into") != "" {
			return fmt.Errorf("--into applies to shader codes; use --as ID to name an imported arena")
		}
		draft, err := p.PrepareImport(ctx, b, o.Get("as"), shareProbe)
		if err != nil {
			return err
		}
		fmt.Fprintln(cli.Out, GraphSummary(draft.Workflow))
		fmt.Fprintln(cli.Out, strings.Join(ScoreArena(draft.Workflow, draft.Profiles).Report(), "\n"))
		fmt.Fprintln(cli.Out)
		fmt.Fprintln(cli.Out, strings.Join(draft.Notes, "\n"))
		if o.Bool("dry-run") {
			return nil
		}
		if err := cli.confirm("Import arena "+draft.Workflow.Id, o); err != nil {
			return err
		}
		tx, err := p.SaveDraft(*draft)
		if err != nil {
			return err
		}
		fmt.Fprintln(cli.Out, "Imported", draft.Workflow.Id, "transaction", tx)
		return p.Audit(draft.Workflow.Id, "workflow.saved", map[string]any{"transaction": tx, "source": "share-import", "approvedBy": "user"})
	}
	return usage
}

// shareInput reads a code from the argument, stdin ("-") or the clipboard.
func (cli *CLI) shareInput(ctx context.Context, args []string, o Options) (string, error) {
	switch {
	case o.Bool("clipboard"):
		result := readClipboardText(ctx)
		return result.Text, result.Err
	case len(args) == 1 && args[0] != "-":
		return args[0], nil
	case len(args) == 0 || args[0] == "-":
		b, err := io.ReadAll(io.LimitReader(cli.In, maxShareText+1))
		if err != nil {
			return "", err
		}
		return string(b), nil
	}
	return "", fmt.Errorf("arena share import takes one code, - for stdin, or --clipboard")
}
