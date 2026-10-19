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
	"time"
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
// appends it after the user's config and evaluates the last matching rule. That
// directory holds the complete, untruncated text of every tool output OpenCode cut
// short in context (default limit 2,000 lines or 51,200 bytes, set by config
// tool_output.max_lines and max_bytes), from every OpenCode session on the machine,
// Arena OpenCode workers' own large reads and greps of their worktrees included.
// OpenCode's cleanup removes only files older than 7 days. Read, glob, grep and
// list are allowed, so a worker can list those files, read them and grep their
// contents. Config cannot close it: a read rule "../*": "deny" would stop the read
// tool, which asks with the path relative to the worktree, but grep asks its
// permission on the search regex, not the path, and its directory check is the same
// external_directory allow. Moving XDG_DATA_HOME would move OpenCode's own sign-in
// store, so the adapter documents the read exposure as a residual risk
// (docs/PROVIDERS.md, OpenCode limitations).
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

// openCodeDroppedEnv are the parent variables openCodeEnv does not pass on: the
// two that would let the parent's configuration reach opencode, OPENCODE_PURE and
// OPENCODE_DISABLE_LSP_DOWNLOAD (both set below, so each appears once) and
// OPENCODE_AUTO_SHARE (see openCodeEnv). OPENCODE_TEST_MANAGED_CONFIG_DIR moves
// the managed-config directory, which opencode loads after OPENCODE_CONFIG_CONTENT,
// so a parent value would let that directory override Arena's config.
// OPENCODE_EXPERIMENTAL_WORKSPACES and OPENCODE_WORKSPACE_ID belong to the
// experimental workspace sync, which no worker, probe or catalog call may join.
// Every OTEL_* variable (openCodeDroppedEnvPrefix) is the OpenTelemetry SDK's
// exporter, sampler or resource setting (OTEL_EXPORTER_OTLP_ENDPOINT and _HEADERS
// among them, all named in the 1.19.3 binary), so no inherited telemetry export
// reaches a call.
var openCodeDroppedEnv = map[string]bool{"OPENCODE_PERMISSION": true, "OPENCODE_CONFIG_CONTENT": true, "OPENCODE_PURE": true, "OPENCODE_AUTO_SHARE": true, "OPENCODE_DISABLE_LSP_DOWNLOAD": true, "OPENCODE_TEST_MANAGED_CONFIG_DIR": true, "OPENCODE_EXPERIMENTAL_WORKSPACES": true, "OPENCODE_WORKSPACE_ID": true}

// openCodeDroppedEnvPrefix is the prefix of the OpenTelemetry variables that
// openCodeEnv drops.
const openCodeDroppedEnvPrefix = "OTEL_"

// The OPENCODE_CONFIG_CONTENT keys that switch off OpenCode's language servers and
// formatters, and the variable that stops it downloading language servers. See
// openCodeEnv for why a worker may use none of them.
const (
	openCodeLSPKey       = "lsp"
	openCodeFormatterKey = "formatter"
)

// openCodeShareKey is the key of OPENCODE_CONFIG_CONTENT that controls sharing and
// openCodeShareDisabled the value Arena sets. The key and its three values
// (manual, auto, disabled) are in the opencode 1.19.3 config schema (grep -a -o on
// the binary: share: "manual" | "auto" | "disabled", "'disabled' disables all
// sharing"). Verified with `opencode debug config`, run from a temporary git
// repository with an isolated HOME and XDG_* and no model call: this key and value,
// alone and inside the whole map openCodeEnv builds for a reader and for a writer,
// resolve to share "disabled", also over a user-level opencode.json that sets share
// "auto" and autoshare true (which on its own resolves to share "auto"). That shows
// the resolved configuration only; no session was shared or attempted.
const (
	openCodeShareKey      = "share"
	openCodeShareDisabled = "disabled"
)

// openCodeEnv is the environment of every opencode call Arena makes: the parent's,
// minus openCodeDroppedEnv, plus a config that no file in the worktree can loosen
// and OPENCODE_PURE=1. Checked on 1.19.3
// against a hostile worktree with `opencode debug config` and
// `opencode debug agent build`, which show the resolved configuration; no model
// call was made:
//   - OPENCODE_PERMISSION is merged after OPENCODE_CONFIG_CONTENT and beat it
//     (bash allow won over deny), so the parent's value is dropped.
//   - OPENCODE_CONFIG_CONTENT can also define providers, models and variants, so
//     the parent's value is dropped too and replaced by Arena's own. A probe that
//     kept it would approve a model or variant the worker cannot select, and an
//     unknown --variant makes opencode run hang.
//   - Session sharing is off for every call. A worker reads files and its session
//     can hold their contents, so it must never be published to OpenCode's share
//     service. The parent's OPENCODE_AUTO_SHARE is dropped: of the OPENCODE_* names
//     in the 1.19.3 binary (grep -a -o) it is the only one that names sharing, and
//     none other is a share, upload or publish switch (OPENCODE_EXPERIMENTAL_WORKSPACES
//     and OPENCODE_WORKSPACE_ID belong to the experimental workspace sync). The
//     config also sets share to "disabled" (openCodeShareKey), which wins over the
//     user's own opencode.json. The 1.19.3 bundle already answers a share request
//     with "Session sharing has been discontinued", so this is defence in depth that
//     holds if a later or patched build brings sharing back, not a sign that 1.19.3
//     shares. Only the resolved configuration was checked; no session was shared.
//   - A worktree's opencode.json or .opencode/agent/build.md set
//     agent.build.permission, which also beat the top-level map, and its mcp
//     entries and default_agent survived. OPENCODE_DISABLE_PROJECT_CONFIG=1
//     ignores the worktree's config entirely; the map is repeated under
//     agent.build as well, for the user's own global config.
//
// Arena's values beat the user's global opencode.json and the OPENCODE_CONFIG and
// OPENCODE_CONFIG_DIR configuration, but not managed configuration (the managed
// directory, /etc/opencode or /Library/Application Support/opencode, and macOS MDM
// preferences) or the active Console organisation's remote config: opencode loads
// those after OPENCODE_CONFIG_CONTENT. Checked on 1.19.3 with
// OPENCODE_TEST_MANAGED_CONFIG_DIR set to a directory whose opencode.json holds
// {"lsp": true, "formatter": true, "share": "auto"}: `opencode debug config` then
// printed formatter true, lsp true and share "auto" although Arena's config said
// false, false and "disabled". Arena cannot override them, so readiness
// (openCodeCheckSettings) refuses a setup in which the resolved configuration
// enables lsp, formatter or sharing, and the parent's
// OPENCODE_TEST_MANAGED_CONFIG_DIR is dropped.
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
// when any other tool is left allowed, or when a user's edit or read object has
// reordered Arena's pattern rules (openCodeFlippedRules).
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
// Language servers and formatters are off for every call (openCodeLSPKey and
// openCodeFormatterKey are false). They are not covered by the permission map: they
// run programs of their own, outside any sandbox and with network access, and the
// programs read the worktree's contents. In the 1.19.3 bundle the read tool's
// warm-up calls LSP.touchFile on every read, and the edit, write and apply_patch
// tools call it after each change; the LSP layer then starts language servers
// (the built-in ones include eslint and rust servers), which run the repository's
// own code (eslint.config.js, Cargo build scripts and proc macros) and are
// downloaded from the network unless OPENCODE_DISABLE_LSP_DOWNLOAD is set. The
// Format service runs the formatters with the worktree as its working directory,
// for example `prettier --write $FILE` when the worktree's package.json lists
// prettier, which loads the repository's prettier config and plugins. A
// user-level `"lsp": true` or `"formatter": true` (or the object form that
// configures servers or formatters) in the global opencode.json would switch both
// on for every worker, because those keys are not in Arena's map. Checked offline
// on the real 1.19.3 binary, with an isolated HOME and XDG_*, no inference and this
// exact kind of OPENCODE_CONFIG_CONTENT, PURE and DISABLE_PROJECT_CONFIG: with a
// global {"lsp": true, "formatter": true}, `opencode debug config` printed
// lsp true and formatter true, and `opencode debug lsp diagnostics a.go` in a
// scratch Go repository started a fake `gopls` on PATH, which wrote its marker
// file. With "lsp": false and "formatter": false added to OPENCODE_CONFIG_CONTENT,
// both resolved to false, also over an object-shaped user lsp and formatter, and
// the fake gopls was not started. OPENCODE_DISABLE_LSP_DOWNLOAD=1 is defence in
// depth: it stops the download of a language server if one were ever enabled. The
// parent's value is dropped so this one is the one that applies. Only the resolved
// configuration and the language-server start were checked; no formatter ran and
// no worker made an edit.
//
// Auto-update is off so the binary the probe inspected is the one that runs.
func openCodeEnv(write bool) []string {
	env := []string{}
	for _, kv := range os.Environ() {
		if name, _, _ := strings.Cut(kv, "="); !openCodeDroppedEnv[name] && !strings.HasPrefix(name, openCodeDroppedEnvPrefix) {
			env = append(env, kv)
		}
	}
	permission := openCodePermissions(write)
	config := map[string]any{"permission": permission, "default_agent": "build", openCodeShareKey: openCodeShareDisabled, openCodeLSPKey: false, openCodeFormatterKey: false, "agent": map[string]any{"build": map[string]any{"permission": permission}}}
	return append(env, "OPENCODE_CONFIG_CONTENT="+compactJSON(config), "OPENCODE_PURE=1", "OPENCODE_DISABLE_PROJECT_CONFIG=1", "OPENCODE_DISABLE_AUTOUPDATE=1", "OPENCODE_DISABLE_LSP_DOWNLOAD=1")
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
	cmd.WaitDelay = openCodeWaitDelay
	return cmd
}

// openCodeWaitDelay bounds how long a probe or catalog call keeps waiting for the
// output pipes after opencode has exited or the context is done. Without it,
// Output and CombinedOutput wait for the pipes to close, and a child that opencode
// started and left running keeps them open past the probe budget (status.go), so
// the probe would stall instead of reporting. No other exec call in the repository
// sets a WaitDelay, so this is a short delay of its own: opencode has already
// exited, or been killed by the context, when the clock starts, and its output is
// then in the pipe. When the delay runs out Wait closes the pipes and returns
// exec.ErrWaitDelay, which the callers report as a failed call.
const openCodeWaitDelay = 2 * time.Second

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
	// An agent with run_commands is refused before the run starts (execute_cli.go);
	// this check backs that up, and adapterLimits records the limit for scoring and
	// validation.
	if in.Commands {
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
// configuration can remove (toolOutput is its pattern). It skips every read rule
// and, for a writer, every edit rule, because those are granted; their patterns
// are held to Arena's guarantees by openCodeFlippedRules instead.
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

// openCodeWildcard reports whether input matches pattern the way OpenCode 1.19.3's
// Wildcard.match reads it. In the bundle, both strings have "\" turned into "/",
// the pattern's regular-expression characters are escaped, "*" becomes ".*" and
// "?" becomes ".", a pattern ending in " *" also matches without that tail, and the
// expression is anchored at both ends and matches newlines. So "*" is any run of
// characters, "/" included ("*.env" matches "a/.env" and ".env"), and "?" is any
// one. Unlike openCodeGlob, which compares two patterns, this matches one concrete
// input.
func openCodeWildcard(input, pattern string) bool {
	input, pattern = strings.ReplaceAll(input, "\\", "/"), strings.ReplaceAll(pattern, "\\", "/")
	if openCodeWildcardRunes([]rune(input), []rune(pattern)) {
		return true
	}
	rest, ok := strings.CutSuffix(pattern, " *")
	return ok && openCodeWildcardRunes([]rune(input), []rune(rest))
}

func openCodeWildcardRunes(input, pattern []rune) bool {
	ii, pi, star, mark := 0, 0, -1, 0
	for ii < len(input) {
		switch {
		case pi < len(pattern) && pattern[pi] == '*':
			star, mark = pi, ii
			pi++
		case pi < len(pattern) && (pattern[pi] == '?' || pattern[pi] == input[ii]):
			pi++
			ii++
		case star >= 0:
			mark++
			ii, pi = mark, star+1
		default:
			return false
		}
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}

// openCodeDecide is OpenCode's Permission.evaluate for one permission name and
// pattern over the merged rule list: the last rule whose permission and pattern
// both match decides (findLast, wildcards on both sides), and with no match the
// action is ask. It returns the action and the index of the deciding rule, -1 when
// none matched.
func openCodeDecide(rules []openCodeRule, permission, pattern string) (action string, index int) {
	for i := len(rules) - 1; i >= 0; i-- {
		if openCodeWildcard(permission, rules[i].Permission) && openCodeWildcard(pattern, rules[i].Pattern) {
			return rules[i].Action, i
		}
	}
	return "ask", -1
}

// openCodeProbe is one concrete tool call and what Arena's map makes of it.
type openCodeProbe struct{ permission, pattern, want string }

// openCodeReadProbes and openCodeEditProbes are fixed inputs for the guarantees that
// pattern rules carry, which openCodeUnexpectedTools cannot see because it skips
// every read rule and, for a writer, every edit rule. The read tool and the edit
// tools ask with the path relative to the worktree. Arena's read map refuses .env
// files (a bare ".env", one in a directory, and a ".env.local" style name) and
// allows ".env.example" and ordinary files; a writer's edit map allows a path inside
// the worktree and denies one outside it, among them the relative path of a file in
// <XDG_DATA_HOME>/opencode/tool-output/, which every OpenCode session reads back.
// OpenCode merges a user's agent.build.permission objects key by key in the user's
// key order, with Arena's values, so a user's own edit or read object can put an
// Arena allow after an Arena deny; these probes evaluate the merged rules with
// OpenCode's own last-match semantics (openCodeDecide) and catch that.
var (
	openCodeReadProbes = []openCodeProbe{
		{"read", ".env", "deny"}, {"read", "a/.env", "deny"}, {"read", "a/.env.local", "deny"},
		{"read", ".env.example", "allow"}, {"read", "src/a.go", "allow"},
	}
	openCodeEditProbes = []openCodeProbe{
		{"edit", "src/a.go", "allow"}, {"edit", "../x", "deny"}, {"edit", "../../d/opencode/tool-output/tool_1", "deny"},
	}
)

// openCodeFlippedRules lists, one sentence each, the probe inputs on which the
// resolved rules do not give the action Arena's map intends: the read probes for
// every worker and the edit probes for a writer. A sentence names the action the
// rules give, the one expected and the rule that decides, so the user can find the
// entry of their own configuration that reversed Arena's.
func openCodeFlippedRules(rules []openCodeRule, write bool) []string {
	probes := openCodeReadProbes
	if write {
		probes = append(append([]openCodeProbe{}, probes...), openCodeEditProbes...)
	}
	flipped := []string{}
	for _, probe := range probes {
		action, index := openCodeDecide(rules, probe.permission, probe.pattern)
		if action == probe.want {
			continue
		}
		by := "because no rule matches"
		if index >= 0 {
			rule := rules[index]
			by = "by the rule " + short(Redact(rule.Permission+" "+rule.Pattern+" "+rule.Action), 80)
		}
		flipped = append(flipped, fmt.Sprintf("%s %q resolves to %s, not %s, %s", probe.permission, probe.pattern, action, probe.want, by))
	}
	return flipped
}

// openCodeResolved is what the merged rules of one build agent hold against Arena's
// grant: the permission names left allowed beyond it, and the probe inputs on which
// a pattern rule is reversed.
type openCodeResolved struct{ tools, flipped []string }

// openCodeResolve resolves the build agent in the environment of a reader or a
// writer, without a model call, and checks the merged rules against Arena's grant.
func openCodeResolve(ctx context.Context, write bool) (openCodeResolved, error) {
	cmd := openCodeCommandFor(ctx, write, "debug", "agent", "build")
	stderr := &cappedBuffer{}
	cmd.Stderr = stderr
	out, err := cmd.Output()
	if err != nil {
		return openCodeResolved{}, fmt.Errorf("opencode debug agent build failed%s", openCodeStderrTail(stderr))
	}
	rules, err := parseOpenCodeRules(string(out))
	if err != nil {
		return openCodeResolved{}, err
	}
	return openCodeResolved{openCodeUnexpectedTools(rules, write, openCodeToolOutputGlob()), openCodeFlippedRules(rules, write)}, nil
}

// openCodeCheckPermissions is the fail-closed readiness check for the user's own
// OpenCode configuration, which openCodeEnv leaves in place. It resolves the build
// agent as a reader and as a writer (the probe does not know which role will use
// the runtime) and returns a non-empty state and detail when it cannot confirm
// that workers get nothing beyond Arena's map and keep its pattern rules. Unreadable
// output reads as a changed format, a failed call as unavailable, and a widened or
// reordered map as unsupported. The two calls run one after the other: started
// together on a data directory OpenCode has not used yet, both create its database
// and one fails (seen on 1.19.3 with an empty XDG_DATA_HOME).
func openCodeCheckPermissions(ctx context.Context) (state, detail string) {
	names, flipped := []string{}, []string{}
	for _, write := range []bool{false, true} {
		resolved, err := openCodeResolve(ctx, write)
		switch {
		case errors.Is(err, errOpenCodeRules):
			return "incompatible version", Redact(err.Error()) + "; Arena cannot confirm that workers get nothing beyond its permission map"
		case err != nil:
			return "unavailable", Redact(err.Error()) + "; Arena cannot confirm that workers get nothing beyond its permission map"
		}
		for _, name := range resolved.tools {
			if !Has(names, name) {
				names = append(names, name)
			}
		}
		for _, sentence := range resolved.flipped {
			if !Has(flipped, sentence) {
				flipped = append(flipped, sentence)
			}
		}
	}
	if len(names) == 0 && len(flipped) == 0 {
		return "", ""
	}
	sort.Strings(names)
	parts := []string{}
	if len(names) > 0 {
		shown := []string{}
		for _, name := range names[:min(len(names), 5)] {
			shown = append(shown, short(Redact(name), 60))
		}
		list := strings.Join(shown, ", ")
		if len(names) > len(shown) {
			list += fmt.Sprintf(" and %d more", len(names)-len(shown))
		}
		parts = append(parts, "your OpenCode configuration allows tools beyond Arena's permission map for workers: "+list+
			". A user-level agent.build.permission (global opencode.json, an OPENCODE_CONFIG file or a global agent definition) that lists keys after \"*\" grants them, MCP tools included. Remove or deny them there, then check again")
	}
	if len(flipped) > 0 {
		list := strings.Join(flipped[:min(len(flipped), 3)], "; ")
		if len(flipped) > 3 {
			list += fmt.Sprintf("; and %d more", len(flipped)-3)
		}
		parts = append(parts, "your OpenCode configuration reverses a pattern rule of Arena's permission map for workers: "+list+
			". OpenCode merges a user-level agent.build.permission edit or read object with Arena's patterns in the user's key order, which can put an allow after Arena's deny. Remove the edit and read entries there, then check again")
	}
	return "unsupported", strings.Join(parts, "; ")
}

// openCodeSettings holds the keys of `opencode debug config` that Arena requires.
// Recorded on the real 1.19.3 binary, run from a scratch git repository with
// OPENCODE_CONFIG_CONTENT set (no model call): it prints one JSON object with
// top-level keys agent, mode, plugin, command, share, lsp, formatter and username.
// share is a string ("manual", "auto" or "disabled"), lsp and formatter are false
// when disabled and true or an object when enabled; with Arena's config they print
// "share": "disabled", "lsp": false and "formatter": false. A key the config does
// not set can be absent, and a banner line can precede the object. The values are
// the resolved ones: managed and Console-org config, loaded after
// OPENCODE_CONFIG_CONTENT, show up here.
type openCodeSettings struct {
	LSP       json.RawMessage `json:"lsp"`
	Formatter json.RawMessage `json:"formatter"`
	Share     json.RawMessage `json:"share"`
}

var errOpenCodeSettings = errors.New("opencode debug config did not print a readable configuration")

// parseOpenCodeSettings reads the JSON object after the banner, if any.
func parseOpenCodeSettings(out string) (openCodeSettings, error) {
	start := 0
	if !strings.HasPrefix(strings.TrimLeft(out, " \t\r\n"), "{") {
		if start = strings.Index(out, "\n{"); start < 0 {
			return openCodeSettings{}, errOpenCodeSettings
		}
		start++
	}
	var settings openCodeSettings
	if json.NewDecoder(strings.NewReader(out[start:])).Decode(&settings) != nil {
		return openCodeSettings{}, errOpenCodeSettings
	}
	return settings, nil
}

// openCodeCheckSettings is the fail-closed readiness check on resolved values.
// Managed configuration (the managed directory, macOS MDM preferences) and the
// Console organisation's remote config are loaded after OPENCODE_CONFIG_CONTENT, so
// Arena's lsp false, formatter false and share "disabled" can be overridden. It
// runs `opencode debug config` in the worker's environment, without a model call,
// and returns a non-empty state and detail unless all three resolve to Arena's
// values. Unreadable output reads as a changed format and a failed call as
// unavailable.
func openCodeCheckSettings(ctx context.Context) (state, detail string) {
	cmd := openCodeCommand(ctx, "debug", "config")
	stderr := &cappedBuffer{}
	cmd.Stderr = stderr
	out, err := cmd.Output()
	if err != nil {
		return "unavailable", "opencode debug config failed" + Redact(openCodeStderrTail(stderr)) + "; Arena cannot confirm that language servers, formatters and sharing are off for workers"
	}
	settings, err := parseOpenCodeSettings(string(out))
	if err != nil {
		return "incompatible version", err.Error() + "; Arena cannot confirm that language servers, formatters and sharing are off for workers"
	}
	problems := []string{}
	for _, check := range []struct {
		key  string
		got  json.RawMessage
		want string
	}{{openCodeLSPKey, settings.LSP, "false"}, {openCodeFormatterKey, settings.Formatter, "false"}, {openCodeShareKey, settings.Share, `"` + openCodeShareDisabled + `"`}} {
		if got := strings.TrimSpace(string(check.got)); got != check.want {
			if got == "" {
				got = "unset"
			}
			problems = append(problems, fmt.Sprintf("%s resolves to %s, not %s", check.key, short(Redact(got), 60), check.want))
		}
	}
	if len(problems) == 0 {
		return "", ""
	}
	return "unsupported", "your OpenCode setup re-enables settings Arena turns off for workers: " + strings.Join(problems, "; ") +
		". Arena's values beat your global opencode.json, OPENCODE_CONFIG and OPENCODE_CONFIG_DIR, but not managed configuration (/etc/opencode, /Library/Application Support/opencode, MDM) or your Console organisation's config. Change it there, then check again"
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
	if state, detail := openCodeCheckSettings(ctx); state != "" {
		result.State, result.Detail = state, detail
		return result
	}
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
		result.Detail = "catalog discovery does not prove model access; live inference is unverified; workers get read tools (writers also edit), no commands and no Arena MCP bridge; opencode debug config shows language servers, formatters and sharing off; opencode debug agent build shows no further tool allowed by your configuration"
		if skipped > 0 {
			result.Detail += fmt.Sprintf("; %d unparseable catalog blocks skipped", skipped)
		}
		return result
	}
	result.State = "unavailable model"
	result.Detail = "selected model is absent from opencode models --verbose"
	return result
}
