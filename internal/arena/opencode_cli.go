package arena

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// The OpenCode CLI (npm opencode-ai) owns its sign-in like Codex and Claude Code:
// `opencode auth login`. Arena never reads its credential file (auth.json) and
// probes only the counts `opencode auth list` prints. It starts `opencode run`
// headless, streams public activity into the agent window and takes only the
// final structured result. Contract and probe coverage only: live inference is
// unverified, because the sandbox the adapter was written in was refused by
// OpenCode's free tier and had no paid credentials.

// openCodeVersion is the one release whose flags, catalog and event shapes were
// inspected. providers/compatibility.json records the same version.
const openCodeVersion = "1.19.3"

// openCodeMaxModels bounds the catalog, as the Gemini catalog is bounded.
const openCodeMaxModels = 1000

// openCodeReadNote and openCodeWriteNote tell the model what the permission map
// below leaves it, so it does not spend turns on tools OpenCode would refuse.
const openCodeReadNote = "Shell commands, web access, subagents, user questions, edits and access outside your current working directory (the assigned workspace) are denied in this headless run. Inspect files only inside it, with the read, glob, grep and list tools."
const openCodeWriteNote = "Shell commands, web access, subagents, user questions and access outside your current working directory (the assigned workspace) are denied in this headless run. Work only inside it, with the read, glob, grep and list tools, and change files with the edit tools."

// openCodeDenied are permission names OpenCode 1.19.3 accepts that no Arena worker
// may use. They are denied by name as well as through the "*" rule, so a later
// rule for the same name cannot reopen them.
//
// One external_directory rule is not Arena's to deny. `opencode debug agent build`
// on 1.19.3, with this exact map, resolves 59 rules and the last is an
// external_directory allow for <XDG_DATA_HOME>/opencode/tool-output/*. OpenCode
// appends it after the user's config and evaluates the last matching rule, and
// read is allowed for "*", so a worker can still list and read the truncated tool
// outputs of every OpenCode session on the machine. Config cannot override that
// rule, and moving XDG_DATA_HOME would move OpenCode's own sign-in store, so the
// adapter documents the read exposure as a residual risk (docs/PROVIDERS.md,
// OpenCode limitations).
//
// Writes there are a separate check and Arena does deny them. The write, edit and
// apply_patch tools pass the external_directory rule above, then ask the edit
// permission with the path relative to the worktree (openCodeEditPermission).
var openCodeDenied = []string{"bash", "webfetch", "websearch", "task", "skill", "todowrite", "question", "lsp", "external_directory", "doom_loop", "plan_enter", "plan_exit", "workflow_tool_approval"}

// openCodeEditPermission is the edit rule of a writer: every path inside the
// worktree, and none outside it. The edit tools ask with the path relative to the
// worktree (Instance.worktree, which is the git worktree's own top level for the
// linked worktrees Arena gives a writer: checked on 1.19.3 through opencode
// serve's /path, no model call). A path outside it starts with "../", and
// OpenCode takes the last rule that matches, so the deny follows the allow;
// json.Marshal sorts "../*" after "*" and OpenCode reads rules in key order. This
// is what keeps a writer from creating or overwriting files under
// <XDG_DATA_HOME>/opencode/tool-output/, which every OpenCode session on the
// machine, the user's own included, reads back, and which the external_directory
// rule OpenCode appends allows.
func openCodeEditPermission() map[string]any {
	return map[string]any{"*": "allow", "../*": "deny"}
}

// openCodePermissions is the permission map of one worker: the read tools, plus
// edit for a writer, held to the worktree. Reads keep OpenCode's own default of
// refusing .env files (its default asks, which headless runs auto-reject).
func openCodePermissions(write bool) map[string]any {
	permission := map[string]any{"*": "deny", "glob": "allow", "grep": "allow", "list": "allow", "edit": "deny",
		"read": map[string]any{"*": "allow", "*.env": "deny", "*.env.*": "deny", "*.env.example": "allow"}}
	if write {
		permission["edit"] = openCodeEditPermission()
	}
	for _, name := range openCodeDenied {
		permission[name] = "deny"
	}
	return permission
}

// openCodeEnv is the environment of every opencode call Arena makes: the parent's,
// minus the two variables that would let the parent's configuration reach it (and
// OPENCODE_PURE, which is set below), plus a config that no file in the worktree
// can loosen and OPENCODE_PURE=1. Checked on 1.19.3
// against a hostile worktree with `opencode debug config` and
// `opencode debug agent build`, which show the resolved configuration; no model
// call was made:
//   - OPENCODE_PERMISSION is merged after OPENCODE_CONFIG_CONTENT and beat it
//     (bash allow won over deny), so the parent's value is dropped.
//   - OPENCODE_CONFIG_CONTENT can also define providers, models and variants, so
//     the parent's value is dropped too and replaced by Arena's own. A probe that
//     kept it would approve a model or variant the worker cannot select, and an
//     unknown --variant makes opencode run hang.
//   - A worktree's opencode.json or .opencode/agent/build.md set
//     agent.build.permission, which also beat the top-level map, and its mcp
//     entries and default_agent survived. OPENCODE_DISABLE_PROJECT_CONFIG=1
//     ignores the worktree's config entirely; the map is repeated under
//     agent.build as well, for the user's own global config.
//
// The user's own configuration is not isolated: it is where their providers,
// models and OpenCode's sign-in live. It can still add tools to a worker. A
// user-level agent.build.permission (the global opencode.json, an OPENCODE_CONFIG
// file or a global agent definition) merges with Arena's map in the user's key
// order. Arena's "*": "deny" replaces the user's "*" in place, so a key the user
// lists after "*" and Arena does not name keeps its allow, and the last matching
// rule wins. Checked on 1.19.3 with `opencode debug agent build`, no model call:
// a global agent.build.permission of {"*": "allow", "codesearch": "allow",
// "mymcp_*": "allow"} left codesearch and every mymcp_* MCP tool allowed to a
// read-only worker, while the keys Arena names (bash, edit, webfetch and the rest
// of openCodeDenied) stayed denied. probeOpenCode therefore resolves the build
// agent in the worker's environment and refuses to start (openCodeCheckPermissions)
// when any other tool is left allowed.
//
// External plugins are off for every call, not only for `opencode run --pure`.
// `--pure` is a root-level option whose middleware sets OPENCODE_PURE=1, and the
// runtime reads that variable, so the variable reaches `models --verbose` and
// `debug agent build` as well as `--version`, `run --help` and `auth list`. A
// plugin's config hook can add providers, models and variants and change
// agent.build.permission. Checked offline on the 1.19.3 binary, with an isolated
// HOME and XDG_*, no inference and a local plugin whose hook adds the provider
// plugprov (model m1, variant turbo) and agent.build.permission {plugtool: allow}:
// without pure, `models --verbose` lists plugprov/m1 and `debug agent build` holds
// a plugtool allow; with `--pure` or OPENCODE_PURE=1 it lists no plugprov model and
// no plugtool rule. A probe that loaded plugins would approve a model or variant,
// and judge a permission list, that the worker (which runs with --pure) never sees,
// and an unknown --variant makes opencode run hang. The parent's own OPENCODE_PURE
// is dropped so the setting here is the one that applies. With no plugin installed
// the resolved build agent is byte-identical with and without it (59 rules for a
// reader), so the recorded fixtures hold.
//
// Auto-update is off so the binary the probe inspected is the one that runs.
func openCodeEnv(write bool) []string {
	env := []string{}
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); name != "OPENCODE_PERMISSION" && name != "OPENCODE_CONFIG_CONTENT" && name != "OPENCODE_PURE" {
			env = append(env, kv)
		}
	}
	permission := openCodePermissions(write)
	config := map[string]any{"permission": permission, "default_agent": "build", "agent": map[string]any{"build": map[string]any{"permission": permission}}}
	return append(env, "OPENCODE_CONFIG_CONTENT="+compactJSON(config), "OPENCODE_PURE=1", "OPENCODE_DISABLE_PROJECT_CONFIG=1", "OPENCODE_DISABLE_AUTOUPDATE=1")
}

// openCodeCommand is a read-only opencode call for probes and the catalog. It runs
// in the read-only worker's environment (openCodeEnv), so it sees the same
// configuration a worker does: the same filtered base, the same
// OPENCODE_CONFIG_CONTENT and the same plugin set (OPENCODE_PURE=1, which the
// worker's --pure also sets). A writer differs only in its edit permission, which
// only the permission check below reads (openCodeCommandFor). A model that passes
// the probe is therefore one the worker's configuration lists, not one that only
// the parent's configuration defines.
func openCodeCommand(ctx context.Context, args ...string) *exec.Cmd {
	return openCodeCommandFor(ctx, false, args...)
}

// openCodeCommandFor is an opencode call in the environment of a reader (write
// false) or of a writer.
func openCodeCommandFor(ctx context.Context, write bool, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "opencode", args...)
	cmd.Env = openCodeEnv(write)
	return cmd
}

// openCodeArgError refuses a value that opencode's argument parser could read
// as another flag (--variant --auto), or that carries whitespace or control
// characters.
func openCodeArgError(label, value string) error {
	if strings.HasPrefix(value, "-") || strings.IndexFunc(value, func(r rune) bool { return r <= ' ' || r == 0x7f }) >= 0 {
		return fmt.Errorf("opencode %s %q must not start with - or contain whitespace", label, value)
	}
	return nil
}

// openCodeRuntimeError reports why r cannot run on this adapter, or nil. Effort
// is Runtime.ReasoningEffort: an opaque OpenCode variant name that readiness
// checks against the model's catalog entry, never interpreted here.
func openCodeRuntimeError(r Runtime) error {
	if len(r.Options) > 0 {
		return fmt.Errorf("opencode takes no runtime options (found %q); set effort with reasoningEffort, and OpenCode's own CLI owns sign-in and model settings", sortedKeys(r.Options)[0])
	}
	if provider, model, ok := strings.Cut(r.Model, "/"); !ok || provider == "" || model == "" {
		return fmt.Errorf("opencode models are provider/model IDs; choose one that opencode models --verbose lists")
	}
	if err := openCodeArgError("model", r.Model); err != nil {
		return err
	}
	if r.ReasoningEffort != "" {
		return openCodeArgError("effort", r.ReasoningEffort)
	}
	return nil
}

// Event shapes, from the opencode 1.19.3 binary (grep -a -o on its bundled
// source), not from a live response:
//   - `run --format json` writes one {type, timestamp, sessionID, ...} object per
//     line through Z(name, data) with the names step_start, step_finish, tool_use,
//     text, reasoning and error. The event type uses underscores; the part inside
//     keeps the hyphen (part.type "step-finish"). Both spellings are accepted.
//   - text: {part: {type "text", text, synthetic?, ignored?, time: {start, end?}}},
//     emitted only once part.time.end is set.
//   - step_finish: {part: {type "step-finish", reason, cost, tokens: {total?,
//     input, output, reasoning, cache: {read, write}}}}, one per model call.
//   - tool_use: {part: {type "tool", callID, tool, state: {status, input, ...}}},
//     emitted only when state.status is completed or error, never while running.
//   - error: {error: {name, data: {message}}}; the exit code is then non-zero.
//
// The run prints other notices as plain lines, which streamNative skips.
func invokeOpenCode(ctx context.Context, in Invocation) (map[string]any, error) {
	if in.Commands { // adapterLimits records this for scoring and validation.
		return nil, fmt.Errorf("OpenCode has no command sandbox in Arena; remove run_commands from this agent or use another provider")
	}
	if err := openCodeRuntimeError(in.Runtime); err != nil {
		return nil, err
	}
	prompt, err := nativePrompt(in)
	if err != nil {
		return nil, err
	}
	// opencode run has no system-prompt flag, so the instructions lead the turn.
	note := openCodeReadNote
	if in.Write {
		note = openCodeWriteNote
	}
	prompt = nativeWorkerInstructions + " " + note + "\n" + prompt
	if in.Schema != nil {
		prompt += "\nOpenCode has no structured-output mode, so finish with exactly one JSON object that matches this JSON Schema, with nothing after it: " + compactJSON(in.Schema)
	}
	args := []string{"run", "--pure", "--format", "json", "--dir", in.Dir, "--model", in.Runtime.Model}
	if in.Runtime.ReasoningEffort != "" {
		args = append(args, "--variant", in.Runtime.ReasoningEffort)
	}
	cmd := exec.Command("opencode", args...)
	cmd.Dir = in.Dir
	cmd.Env = openCodeEnv(in.Write)
	cmd.Stdin = strings.NewReader(prompt)
	stderr := &cappedBuffer{}
	cmd.Stderr = stderr
	reply, steps := "", 0
	err = streamNative(ctx, in, cmd, func(event map[string]any) error {
		part, _ := event["part"].(map[string]any)
		switch strings.ReplaceAll(str(event["type"]), "-", "_") {
		case "error":
			return errors.New(openCodeErrorText(event["error"]))
		case "text":
			text := strings.TrimSpace(str(part["text"]))
			if text == "" || part["synthetic"] == true || part["ignored"] == true {
				return nil
			}
			reply = text
			if in.Output != nil {
				return in.Output(text)
			}
		case "tool_use":
			state, _ := part["state"].(map[string]any)
			status := "completed"
			if state["status"] == "error" {
				status = "failed"
			}
			return nativeActivity(in, str(part["tool"]), status, str(part["callID"]), "")
		case "step_finish":
			// Each step is one model call with its own counts, so the roll-up must
			// add the steps instead of keeping the highest (see turnKey).
			if tokens, ok := part["tokens"].(map[string]any); ok && in.Usage != nil {
				steps++
				usage := map[string]any{turnKey: steps}
				for key, value := range tokens {
					usage[key] = value
				}
				return in.Usage(usage)
			}
		}
		return nil
	})
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("OpenCode run failed: %s%s", short(strings.TrimSpace(Redact(err.Error())), 300), openCodeStderrTail(stderr))
	}
	if reply == "" {
		return nil, fmt.Errorf("OpenCode run ended without a reply%s", openCodeStderrTail(stderr))
	}
	out, err := openCodeResult(reply)
	if err != nil {
		return nil, err
	}
	if err := openCodeCheckSchema(in.Schema, out); err != nil {
		return nil, err
	}
	return out, nil
}

// openCodeErrorText is the message of an error event, else the error's name.
func openCodeErrorText(v any) string {
	failure, _ := v.(map[string]any)
	data, _ := failure["data"].(map[string]any)
	if message := str(data["message"]); message != "" {
		return message
	}
	if name := str(failure["name"]); name != "" {
		return name
	}
	return "unknown error"
}

// openCodeStderrTail is "; " and the end of the run's stderr, or nothing.
func openCodeStderrTail(stderr *cappedBuffer) string {
	text := strings.ReplaceAll(strings.TrimSpace(Redact(stderr.String())), "\n", " ")
	if runes := []rune(text); len(runes) > 300 {
		text = "…" + string(runes[len(runes)-300:])
	}
	if text == "" {
		return ""
	}
	return "; " + text
}

// openCodeResult reads the structured result from the last reply. The prompt asks
// for the JSON object alone, but a model may explain itself first, so a reply
// that is not itself an object is searched for the object that ends it.
func openCodeResult(text string) (map[string]any, error) {
	if out, err := parseOutput(text); err == nil {
		return out, nil
	}
	text = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(text), "```"))
	for start, tries := 0, 0; tries < 64; tries++ {
		next := strings.Index(text[start:], "{")
		if next < 0 {
			break
		}
		start += next
		var out map[string]any
		if DecodeJSON([]byte(text[start:]), &out) == nil && out != nil {
			return out, nil
		}
		start++
	}
	return nil, fmt.Errorf("provider returned invalid structured output")
}

// openCodeCheckSchema holds the reply to the node's output schema, which OpenCode
// cannot enforce: it has no structured-output mode, so the reply is only text the
// prompt asked to match. It checks the top level of the object, the keys the
// schema lists in required and, when the schema sets additionalProperties to
// false, that no other key appears. objectSchema and resultSchema state exactly
// those two things at that level. It does not check property types or nested
// objects; result and review-result payloads are checked in full by the envelope
// contract (ValidatePacket) when their packet is stored. A nil schema asks for no
// structure and passes.
func openCodeCheckSchema(schema, out map[string]any) error {
	if schema == nil {
		return nil
	}
	var top struct {
		Properties           map[string]json.RawMessage `json:"properties"`
		Required             []string                   `json:"required"`
		AdditionalProperties any                        `json:"additionalProperties"`
	}
	if err := DecodeJSON(JSON(schema), &top); err != nil {
		return fmt.Errorf("provider returned invalid structured output: the output schema could not be read")
	}
	for _, key := range top.Required {
		if _, ok := out[key]; !ok {
			return fmt.Errorf("provider returned invalid structured output: required key %q is missing", key)
		}
	}
	if top.AdditionalProperties == false {
		for _, key := range sortedKeys(out) {
			if _, ok := top.Properties[key]; !ok {
				return fmt.Errorf("provider returned invalid structured output: key %q is not in the output schema", short(Redact(key), 60))
			}
		}
	}
	return nil
}

// openCodeEffortRank orders the variant names seen in 1.19.3 catalogs for
// display only. Efforts stay opaque: a name outside the list sorts last.
var openCodeEffortRank = map[string]int{"none": 0, "minimal": 1, "low": 2, "medium": 3, "high": 4, "xhigh": 5, "max": 6}

func sortOpenCodeEfforts(efforts []string) {
	rank := func(name string) int {
		if n, ok := openCodeEffortRank[name]; ok {
			return n
		}
		return len(openCodeEffortRank)
	}
	sort.Slice(efforts, func(i, j int) bool {
		if a, b := rank(efforts[i]), rank(efforts[j]); a != b {
			return a < b
		}
		return efforts[i] < efforts[j]
	})
}

// openCodeHeader reports whether line starts a catalog block: a bare
// provider/model ID at column 0. The JSON after it is pretty-printed, so its own
// lines are indented, braces, or hold quotes and spaces.
func openCodeHeader(line string) bool {
	provider, model, ok := strings.Cut(line, "/")
	return ok && provider != "" && model != "" && !strings.ContainsAny(line, " \t\"{}[]")
}

// parseOpenCodeModels reads `opencode models --verbose`: a provider/model header
// line, then that model's metadata as one JSON object. A block that does not
// parse is skipped and counted, so one bad entry does not hide the catalog.
func parseOpenCodeModels(out string) (models []ModelOption, skipped int) {
	id, body := "", []string{}
	flush := func() {
		if id == "" {
			return
		}
		var meta struct {
			Name     string         `json:"name"`
			Variants map[string]any `json:"variants"`
		}
		if json.Unmarshal([]byte(strings.Join(body, "\n")), &meta) != nil {
			skipped++
			return
		}
		option := ModelOption{ID: id, Name: meta.Name, Efforts: []string{}}
		if option.Name == "" {
			option.Name = id
		}
		for variant := range meta.Variants {
			option.Efforts = append(option.Efforts, variant)
		}
		sortOpenCodeEfforts(option.Efforts)
		models = append(models, option)
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, " \t\r")
		if !openCodeHeader(line) {
			body = append(body, line)
			continue
		}
		flush()
		if len(models) >= openCodeMaxModels {
			id = ""
			break
		}
		id, body = line, nil
	}
	flush()
	return models, skipped
}

// openCodeModels lists the catalog and how many blocks of it were unparseable.
func openCodeModels(ctx context.Context) ([]ModelOption, int, error) {
	cmd := openCodeCommand(ctx, "models", "--verbose")
	stderr := &cappedBuffer{}
	cmd.Stderr = stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, 0, fmt.Errorf("opencode could not list models%s", openCodeStderrTail(stderr))
	}
	models, skipped := parseOpenCodeModels(string(out))
	if len(models) == 0 {
		return nil, skipped, fmt.Errorf("opencode models --verbose listed no usable models (%d unparseable blocks skipped)", skipped)
	}
	return models, skipped, nil
}

// openCodeCatalog is the model catalog OpenCode reports: provider/model IDs with
// the variants each accepts as efforts. Listing a model is not proof of access.
func openCodeCatalog(ctx context.Context) ([]ModelOption, error) {
	models, _, err := openCodeModels(ctx)
	return models, err
}

var (
	ansiEscape         = regexp.MustCompile(`\x1b\[[0-9;]*m`)
	openCodeCredential = regexp.MustCompile(`(?i)\b(\d+) credentials?\b`)
	openCodeEnvVars    = regexp.MustCompile(`(?i)\b(\d+) environment variables?\b`)
)

// parseOpenCodeAuth reads the two counts `opencode auth list` ends its sections
// with: stored credentials and environment variables that name a provider. Only
// the counts are read, never a credential or its file.
func parseOpenCodeAuth(out string) (credentials, environment int, ok bool) {
	out = ansiEscape.ReplaceAllString(out, "")
	match := openCodeCredential.FindStringSubmatch(out)
	if match == nil {
		return 0, 0, false
	}
	credentials, _ = strconv.Atoi(match[1])
	if match = openCodeEnvVars.FindStringSubmatch(out); match != nil {
		environment, _ = strconv.Atoi(match[1])
	}
	return credentials, environment, true
}

// openCodeRule is one rule of the permission list that `opencode debug agent build`
// prints for the build agent once every configuration source is merged. The list
// is in evaluation order, and the last rule that matches a tool and its pattern
// decides (Permission.evaluate takes findLast).
type openCodeRule struct {
	Permission string `json:"permission"`
	Pattern    string `json:"pattern"`
	Action     string `json:"action"`
}

// errOpenCodeRules marks output of `opencode debug agent build` that Arena cannot
// read as a rule list. The check then fails closed instead of passing.
var errOpenCodeRules = errors.New("opencode debug agent build did not print a readable permission list")

// parseOpenCodeRules reads the resolved permission rules from the JSON object
// `opencode debug agent build` prints (shape checked on 1.19.3: {name, description,
// options, permission: [{permission, pattern, action}], mode, native, tools}).
// Lines before the object are skipped. An empty list or a rule that is not
// allow, deny or ask is unreadable, so a changed format cannot pass as clean.
func parseOpenCodeRules(out string) ([]openCodeRule, error) {
	start := 0
	if !strings.HasPrefix(strings.TrimLeft(out, " \t\r\n"), "{") {
		if start = strings.Index(out, "\n{"); start < 0 {
			return nil, errOpenCodeRules
		}
		start++
	}
	var agent struct {
		Permission []openCodeRule `json:"permission"`
	}
	if json.NewDecoder(strings.NewReader(out[start:])).Decode(&agent) != nil || len(agent.Permission) == 0 {
		return nil, errOpenCodeRules
	}
	for _, rule := range agent.Permission {
		if rule.Permission == "" || rule.Pattern == "" || (rule.Action != "allow" && rule.Action != "deny" && rule.Action != "ask") {
			return nil, errOpenCodeRules
		}
	}
	return agent.Permission, nil
}

// openCodeGlob reports whether every name that the allowed permission can match is
// also matched by the later rule's permission, with the wildcards read the way
// OpenCode's matcher reads them: "*" is any run of characters and "?" any one.
// The allowed permission is taken as a pattern of its own, not as one name. Its
// characters are compared literally, so "*" in it stands for every run of
// characters and only a "*" in the later permission covers it: "mymcp_*" is
// matched by the rules "*" and "mymcp_*" and not by "mymcp_foo", so a later deny
// for one MCP tool does not close the rest of them. A "?" in the later permission
// stands for exactly one character, so it does not cover a "*" in the allowed one:
// a later "mymcp_?" leaves "mymcp_search" allowed by "mymcp_*", and a later "a?*"
// leaves the name "a" allowed by "a*". A "?" in the allowed permission is one
// character and is covered by a "?" or "*" in the later one.
func openCodeGlob(name, pattern string) bool {
	n, p := []rune(name), []rune(pattern)
	ni, pi, star, mark := 0, 0, -1, 0
	for ni < len(n) {
		switch {
		case pi < len(p) && p[pi] == '*':
			star, mark = pi, ni
			pi++
		case pi < len(p) && ((p[pi] == '?' && n[ni] != '*') || p[pi] == n[ni]):
			pi++
			ni++
		case star >= 0:
			mark++
			ni, pi = mark, star+1
		default:
			return false
		}
	}
	for pi < len(p) && p[pi] == '*' {
		pi++
	}
	return pi == len(p)
}

// openCodeToolOutputGlob is the pattern of the one rule OpenCode appends after all
// configuration: an external_directory allow for <XDG_DATA_HOME>/opencode/tool-output/*
// (see openCodeDenied). XDG_DATA_HOME falls back to ~/.local/share, as OpenCode's
// does. It is empty when neither is known, and then nothing is exempt.
func openCodeToolOutputGlob() string {
	data := os.Getenv("XDG_DATA_HOME")
	if data == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return ""
		}
		data = filepath.Join(home, ".local", "share")
	}
	return filepath.ToSlash(filepath.Join(data, "opencode", "tool-output", "*"))
}

// openCodeUnexpectedTools lists the permission names that rules leave allowed to a
// worker beyond what Arena grants: read, glob, grep and list, and edit for a
// writer. A rule counts when it allows and no later rule overrides it. A later
// deny or ask overrides it when that rule's pattern is a bare "*", which covers
// every pattern the allow could match, and its permission covers every name the
// allowed permission can match (openCodeGlob): a later "*" does, while "mymcp_foo"
// and "mymcp_?" do not override "mymcp_*". Ask is not an
// allow: a headless run rejects what would be asked. The one exemption is the
// documented tool-output external_directory allow, which OpenCode adds last and no
// configuration can remove (toolOutput is its pattern).
func openCodeUnexpectedTools(rules []openCodeRule, write bool, toolOutput string) []string {
	names := []string{}
	for i, rule := range rules {
		if rule.Action != "allow" {
			continue
		}
		switch rule.Permission {
		case "read", "glob", "grep", "list":
			continue
		case "edit":
			if write {
				continue
			}
		case "external_directory":
			if toolOutput != "" && filepath.ToSlash(rule.Pattern) == toolOutput {
				continue
			}
		}
		overridden := false
		for _, later := range rules[i+1:] {
			if later.Action != "allow" && strings.Trim(later.Pattern, "*") == "" && openCodeGlob(rule.Permission, later.Permission) {
				overridden = true
				break
			}
		}
		if !overridden && !Has(names, rule.Permission) {
			names = append(names, rule.Permission)
		}
	}
	sort.Strings(names)
	return names
}

// openCodeResolvedTools resolves the build agent in the environment of a reader or
// a writer, without a model call, and returns the tools the merged rules leave
// allowed beyond Arena's grant.
func openCodeResolvedTools(ctx context.Context, write bool) ([]string, error) {
	cmd := openCodeCommandFor(ctx, write, "debug", "agent", "build")
	stderr := &cappedBuffer{}
	cmd.Stderr = stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("opencode debug agent build failed%s", openCodeStderrTail(stderr))
	}
	rules, err := parseOpenCodeRules(string(out))
	if err != nil {
		return nil, err
	}
	return openCodeUnexpectedTools(rules, write, openCodeToolOutputGlob()), nil
}

// openCodeCheckPermissions is the fail-closed readiness check for the user's own
// OpenCode configuration, which openCodeEnv leaves in place. It resolves the build
// agent as a reader and as a writer (the probe does not know which role will use
// the runtime) and returns a non-empty state and detail when it cannot confirm
// that workers get nothing beyond Arena's map. Unreadable output reads as a
// changed format, a failed call as unavailable, and a widened map as unsupported.
// The two calls run one after the other: started together on a data directory
// OpenCode has not used yet, both create its database and one fails (seen on
// 1.19.3 with an empty XDG_DATA_HOME).
func openCodeCheckPermissions(ctx context.Context) (state, detail string) {
	names := []string{}
	for _, write := range []bool{false, true} {
		tools, err := openCodeResolvedTools(ctx, write)
		switch {
		case errors.Is(err, errOpenCodeRules):
			return "incompatible version", Redact(err.Error()) + "; Arena cannot confirm that workers get nothing beyond its permission map"
		case err != nil:
			return "unavailable", Redact(err.Error()) + "; Arena cannot confirm that workers get nothing beyond its permission map"
		}
		for _, name := range tools {
			if !Has(names, name) {
				names = append(names, name)
			}
		}
	}
	if len(names) == 0 {
		return "", ""
	}
	sort.Strings(names)
	shown := []string{}
	for _, name := range names[:min(len(names), 5)] {
		shown = append(shown, short(Redact(name), 60))
	}
	list := strings.Join(shown, ", ")
	if len(names) > len(shown) {
		list += fmt.Sprintf(" and %d more", len(names)-len(shown))
	}
	return "unsupported", "your OpenCode configuration allows tools beyond Arena's permission map for workers: " + list +
		". A user-level agent.build.permission (global opencode.json, an OPENCODE_CONFIG file or a global agent definition) that lists keys after \"*\" grants them, MCP tools included. Remove or deny them there, then check again"
}

func probeOpenCode(ctx context.Context, r Runtime, result Readiness) Readiness {
	if _, err := exec.LookPath("opencode"); err != nil {
		result.State = "missing"
		result.Detail = "opencode is not installed"
		return result
	}
	if err := openCodeRuntimeError(r); err != nil {
		result.State = "unsupported"
		result.Detail = err.Error()
		return result
	}
	version, err := openCodeCommand(ctx, "--version").Output()
	if err != nil {
		result.State = "incompatible version"
		result.Detail = "version probe failed"
		return result
	}
	result.Version = strings.TrimSpace(Redact(string(version)))
	result.Compatibility = VersionDrift(r.Provider, result.Version)
	if result.Version != openCodeVersion {
		result.State = "incompatible version"
		result.Detail = "installed opencode " + result.Version + " is not " + openCodeVersion + ", the only version whose flags and events Arena inspected"
		return result
	}
	help, _ := openCodeCommand(ctx, "run", "--help").CombinedOutput()
	for _, flag := range []string{"--format", "--variant", "--pure", "--dir", "--model"} {
		if !strings.Contains(string(help), flag) {
			result.State = "incompatible version"
			result.Detail = "installed opencode lacks " + flag
			return result
		}
	}
	auth, err := openCodeCommand(ctx, "auth", "list").Output()
	if err != nil {
		result.State = "unavailable"
		result.Auth = "opencode auth list unavailable"
		result.Detail = "run opencode auth login, OpenCode's own sign-in, first"
		return result
	}
	credentials, environment, ok := parseOpenCodeAuth(string(auth))
	switch {
	case !ok:
		result.State = "incompatible version"
		result.Auth = "opencode auth list output not recognised"
		result.Detail = "installed opencode did not report its sign-in state"
		return result
	case credentials == 0 && environment > 0:
		// Environment variables name a provider but prove nothing about access.
		result.Auth = "auth unknown, environment variables present"
		result.Detail = "OpenCode reports provider environment variables but no stored sign-in; Arena cannot verify them without running inference, so run opencode auth login"
		return result
	case credentials == 0:
		result.State = "signed out"
		result.Auth = "signed out"
		result.Detail = "run opencode auth login, OpenCode's own sign-in, first"
		return result
	}
	result.Auth = "OpenCode-owned sign-in present; credential validity unverified"
	// The user's own OpenCode configuration still applies to workers. It must not
	// give them anything Arena's map does not, so readiness fails closed here.
	if state, detail := openCodeCheckPermissions(ctx); state != "" {
		result.State, result.Detail = state, detail
		return result
	}
	models, skipped, err := openCodeModels(ctx)
	if err != nil {
		result.State = "unavailable"
		result.Detail = Redact(err.Error())
		return result
	}
	for _, model := range models {
		if model.ID != r.Model {
			continue
		}
		result.Efforts = append(result.Efforts, model.Efforts...)
		result.ModelState = "advertised; inference access unverified"
		// An unknown --variant makes opencode run hang instead of failing, so
		// effort is checked here, against this model's variants, before launch.
		if r.ReasoningEffort != "" && !Has(result.Efforts, r.ReasoningEffort) {
			result.State = "unsupported effort"
			result.Detail = "effort is not a variant opencode lists for this model; an unknown --variant makes opencode run hang"
			return result
		}
		result.CanStart = true
		result.Detail = "catalog discovery does not prove model access; live inference is unverified; workers get read tools (writers also edit), no commands and no Arena MCP bridge; opencode debug agent build shows no further tool allowed by your configuration"
		if skipped > 0 {
			result.Detail += fmt.Sprintf("; %d unparseable catalog blocks skipped", skipped)
		}
		return result
	}
	result.State = "unavailable model"
	result.Detail = "selected model is absent from opencode models --verbose"
	return result
}
