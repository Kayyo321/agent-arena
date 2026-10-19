# Provider integrations

Reviewed on 2026-10-15; the OpenCode entries were reviewed on 2026-10-19. Interface availability, authentication and inference access
are separate checks. An installed CLI or advertised model is not proof of account
access, quota, tool support or a successful paid request. Provider options remain
opaque in Arena envelopes; adapters translate only supported controls.

| Adapter | Interface and authentication | Verification in this workspace |
| --- | --- | --- |
| Codex | Installed CLI, local app-server stdio, provider-owned sign-in | CLI 0.162.0-alpha.17.2 inspected; real `gpt-6.1-sol` / `high` read-only workflow and live Arena MCP context search passed |
| Claude Code | `--bare -p`, streamed JSON, API-key or supported cloud auth | CLI 2.1.224 and flags inspected; no approved API/cloud credentials available for live inference |
| Claude Code (sign-in) | `claude -p` with its own tools, stream-json, Claude Code-owned sign-in | CLI 2.1.301: one-turn read-only probe confirmed tool restriction and `structured_output`; Arena workflows not yet run live |
| Antigravity | `agy` headless stream-json input/output, Antigravity-owned sign-in, Arena write fence | CLI 1.4.0: one-turn probes confirmed stream format and `structured_output`; live multi-provider team workflow passed on 2026-10-16 with `gemini-3.8-flash-medium` as the second route ([evidence](VALIDATION.md#production-readiness-sweep-2026-10-16)) |
| OpenCode | `opencode run --format json` headless, OpenCode-owned sign-in (`opencode auth login`) | CLI 1.19.3 (npm `opencode-ai`) inspected; contract and probe coverage only; live inference unverified (free tier refused, no paid credentials) |
| Gemini | Official Google GenAI Go SDK, AI Studio API-key or Vertex AI ADC | SDK v1.75.0 compiled; live inference requires configured approved credentials |
| Local | Same-machine Ollama or OpenAI-compatible HTTP server | Contract fixtures; a real backend/model still needs user selection and verification |

## OpenAI

Arena invokes the installed Codex CLI and uses its documented stdio protocol for
model discovery, threads, turns, streaming and approval requests. Authentication
stays with Codex. Arena does not read auth files, implement an identity-provider
flow, copy tokens, pool accounts or bypass limits. MCP tools expose workflow-scoped
messaging, context proposals and user questions. Undocumented server requests are
refused. The local Arena bridge is discovered before inference; only its empty tool-call confirmations are accepted automatically. External servers, secret inputs and explicit sensitive/automatic-review requirements are refused. Native Codex sandbox defaults are supported; Arena refuses stricter shell
policies that this adapter cannot enforce.

The observed CLI is an alpha build. Compatibility remains capability-based, and
new versions are reported as drift until inspected. Model catalogs can be bundled,
so status marks advertised access as unverified. The live test explicitly opts into
provider usage and is excluded from normal test runs.

Setup uses the same documented app-server worker interface in the background:
Arena owns the transcript and renders questions in its TUI, using a read-only
structured interview. A completed draft is validated and saved only by Arena
after `/save`. No native Codex terminal interface is launched. Public tool/status
events and commentary are projected into agent windows; reasoning item content
and raw thought streams are ignored. See the current official
[app-server client lifecycle and events](https://learn.chatgpt.com/docs/app-server).

References: [Codex integration interfaces](https://developers.openai.com/blog/codex-as-a-platform),
[app-server protocol](https://learn.chatgpt.com/docs/app-server),
[Sign in with ChatGPT terms](https://openai.com/policies/sign-in-with-chatgpt-terms/),
[official MCP approval metadata](https://github.com/openai/codex/blob/main/codex-rs/protocol/src/mcp_approval_meta.rs).

## Anthropic

Anthropic's current guidance directs third-party products to API-key or supported
cloud authentication. Arena therefore requires `ANTHROPIC_API_KEY` or a documented
Claude Code cloud route. It uses bare mode and does not rely on subscription OAuth.
Provider-native tools are disabled for worker calls; Arena's bounded tool protocol
mediates file access, commands, packets and questions. Commands require the project's
policy and a supported OS sandbox. Credentials are never included in those command
environments. Each command gets private temporary/home/cache directories that are removed after supervision ends; other worktrees and global temporary directories remain unwritable, and only system toolchain roots, the assigned worktree and resolved toolchain directories are readable (no home directories, other worktrees or host processes; see [Architecture](ARCHITECTURE.md#mediated-command-sandbox)). Networking is disabled. Dependency downloads must be prepared outside the run; existing module caches are read-only. Model/effort access stays unverified until actually exercised.

### Claude Code sign-in route

Selecting `options.authMethod: "claude-code-sign-in"` runs Claude Code the way
Arena runs Codex: a headless background worker under the CLI's own login, with
Arena never reading its credentials. This is a user choice under the user's own
plan terms; Anthropic's guidance above still directs third-party products to
API-key or cloud auth, so Arena never selects it by default. Readiness reads only
`claude auth status`'s logged-in flag and auth kind.

Confinement on this route is Claude Code's own permission model and sandbox, not
an Arena fence: Arena chooses the tool set and permission mode below, and Claude
Code refuses anything outside them.

Workers get `claude -p --output-format stream-json` with `--tools` limited to
Read/Glob/Grep (plus Edit/Write/NotebookEdit for writers), `--permission-mode
dontAsk` or `acceptEdits`, `--permission-prompts none`, project settings only,
no slash commands or subagents, and Arena's worker instructions appended to the
system prompt. In a run, `--mcp-config --strict-mcp-config` attaches only the
local Arena bridge and pre-approves its four tools. Agents with `run_commands`
also get Bash, which Claude Code's own sandbox auto-allows only when sandboxed;
anything else is refused. Like Codex this requires the `adapter-default` shell
policy. Text attachments are inlined; images remain Codex-only.

References: [programmatic CLI](https://code.claude.com/docs/en/headless),
[authentication restrictions](https://code.claude.com/docs/en/legal-and-compliance).

## Google

Gemini uses the official Google GenAI SDK. Select `google-ai-studio-api-key` with
provider-supported environment configuration, or `vertex-ai` with project/location
and Google-managed ADC. The SDK owns auth handling; Arena never journals credentials.
Personal Gemini CLI OAuth is not an Arena path, including when a signed-in Gemini
CLI happens to be installed. Status asks the selected API/Vertex route for the named
model; it does not fall back to another auth method or model.

`options.generation` accepts documented SDK generation controls. Transport overrides,
external tools, caches and replacement system instructions are refused. Automatic
billable continuation is disabled. Arena does not persist model thought content.

References: [official SDKs](https://ai.google.dev/gemini-api/docs/libraries),
[Vertex ADC](https://cloud.google.com/vertex-ai/generative-ai/docs/start/gcp-auth),
[Gemini CLI FAQ boundary](https://geminicli.com/docs/faq/).

## Antigravity

Arena runs the installed Antigravity CLI (`agy`) headless under its own sign-in,
as a user choice under the user's own plan terms. The prompt is sent as one
`stream-json` input event and the reply is read from `stream-json` output, with
`--json-schema` for the result and `--disable-slash-commands`. `agy models` is
the model catalog and the sign-in check; the CLI's model IDs carry their thinking
level, and `--effort` is passed only when selected.

Version 1.3.6 headless mode writes files without asking, ignores `--mode plan`
alongside disabled slash commands, and auto-denies terminal commands. Arena
therefore wraps it in a write fence (Linux, bubblewrap 0.11 or newer; Arena refuses Antigravity elsewhere, because it cannot keep agy's own state disposable there):
the whole filesystem, including the home directory and the project, is read-only;
a writer's own worktree is writable again, `/tmp` is private, caches go to that
`/tmp`, and `agy`'s state (`~/.gemini/antigravity-cli`) is writable only through a
throwaway overlay, so nothing a reader or writer writes there survives the run.
Without bubblewrap's `--tmp-overlay`, and on macOS, readiness reports Antigravity
as unsupported rather than mount that state writable. The fence confines writes, not
reads or networking: `agy` reaches its keyring sign-in and its model as usual. Agents with `run_commands` are
refused unless the user opts the runtime into `commandApproval: "sandboxed-blanket"`:
Arena then adds `--sandbox --dangerously-skip-permissions` and swaps the write fence
for a two-layer command fence (Linux only; see
[Antigravity command approval](CONFIGURATION.md#antigravity-command-approval)). The
outer layer runs `agy` with the home directory emptied to an allowlist and the
network it needs for inference; each command `agy` runs is re-sandboxed in a nested
layer with no network, no session bus and no access to `agy`'s sign-in.
`agy` 1.3.6 keeps its sign-in in the desktop keyring (Secret Service over the
session bus), which the outer fence reaches only through a filtering relay.
`agy` reads MCP servers only from user-level configuration, so
Antigravity workers have no live Arena MCP bridge; they still return structured
results and packets.

## OpenCode

Arena runs the installed OpenCode CLI (`opencode`, npm `opencode-ai`, pinned to 1.19.3) headless as provider `opencode`, through `opencode run`. Arena uses neither `opencode serve` nor `opencode acp`. This is an execution adapter with contract and probe coverage only. Live inference is unverified: the sandbox was refused by OpenCode's free tier and no paid credentials were available.

Sign-in is OpenCode's own `opencode auth login`. It is a user choice under the user's own terms and is never a default. Arena never reads OpenCode's credential file (`auth.json`). Readiness reads only the two counts `opencode auth list` prints (stored credentials and provider environment variables), never a credential or its file. Environment-only entries report "auth unknown, environment variables present" and do not count as ready.

Each worker runs `opencode run --pure --format json --dir <worktree> --model <provider/model>` with the prompt on stdin, and adds `--variant <effort>` when effort is set. Its environment carries `OPENCODE_CONFIG_CONTENT`, which overrides a worktree's own `opencode.json` on conflicting permission keys. Arena drops the parent's `OPENCODE_CONFIG_CONTENT`, `OPENCODE_PERMISSION` and `OPENCODE_PURE` and sets `OPENCODE_DISABLE_PROJECT_CONFIG=1` and `OPENCODE_PURE=1`, so the parent's variables and the worktree's own config cannot loosen the map or add providers, and external plugins are disabled. The user's own OpenCode configuration still applies to workers and probes alike (the global `opencode.json` under `XDG_CONFIG_HOME`, an `OPENCODE_CONFIG` file and global agent definitions), because it holds their providers and models. That configuration can add tools to a worker, so readiness checks it and fails closed (next paragraph). External plugins are disabled for workers, readiness probes and catalog calls alike: the worker passes `--pure`, and `OPENCODE_PURE=1` in the shared environment gives every other call the same setting, because `--pure` only sets that variable. A plugin's config hook can add providers, models and variants and change `agent.build.permission`, so a probe that loaded plugins could approve a model or `--variant` that the worker cannot select (an unknown `--variant` makes `opencode run` hang) or judge permissions the worker does not run with. The readiness probe and the catalog calls (`--version`, `run --help`, `auth list`, `debug agent build`, `models --verbose`) run in that same environment, so a model or variant that passes readiness is one the worker can select. The cost is that your own OpenCode plugins do not run under Arena, for workers or for probes. When readiness passes, read-only workers may use `read`, `glob`, `grep` and `list`; Arena does not start a worker where the resolved rules allow another tool. Writers may also use `edit`, but only for paths inside the worktree: their `edit` rule is `{"*": "allow", "../*": "deny"}`, because OpenCode asks `edit` with the path relative to the worktree. `bash`, `webfetch`, `task`, `skill`, `todowrite`, `lsp`, `external_directory` and `doom_loop` are denied by name, with one exception OpenCode itself adds (see the limitations). The map also sets `*` to deny, and the writer's `edit` rule uses the patterns `*` and `../*`. `opencode debug agent build` resolves them, and the bundle's own matcher, run offline on the resolved rules, allows `src/a.go` and denies `../x`. No worker ran, so wildcard behaviour in a live run is unverified, and the named denies stay the explicit guard.

Readiness fails closed on the user's own configuration. A user-level `agent.build.permission` merges with Arena's map in the user's key order: Arena's `"*": "deny"` replaces the user's `"*"` in place, so a key the user lists after `"*"` that Arena does not name keeps its allow, because the last matching rule wins. Offline on 1.19.3 (isolated `HOME` and `XDG_*`, no model call), a global `agent.build.permission` of `{"*": "allow", "codesearch": "allow", "mymcp_*": "allow"}` resolved to 61 rules for a reader, with `*` deny at rule 36 followed by allows for `codesearch` and `mymcp_*`: every `mymcp_*` MCP tool and `codesearch` resolve to allow for a read-only worker under the last-match rule, while the keys Arena names (`bash`, `edit`, `webfetch` and the rest of the named denies) stay denied. The same holds for an `OPENCODE_CONFIG` file and a global agent definition. After the sign-in check, the probe therefore runs `opencode debug agent build` through the worker's environment, first as a reader and then as a writer (the probe does not know which role will use the runtime), parses the resolved rule list and reports state `unsupported`, not ready, when any permission name other than `read`, `glob`, `grep` and `list` (and `edit` for the writer's run) is left allowed. The check takes the last matching rule: a later `deny` or `ask` whose pattern is a bare `*` and whose permission covers every name the earlier `allow`'s permission can match overrides that `allow`, so Arena's own `*` deny clears the defaults OpenCode starts from. An `ask` is treated as not allowed; how a headless run answers it is unverified (see the last limitation). A narrower later pattern does not clear it, and neither does a deny for one MCP tool clear a wildcard such as `mymcp_*`. A `?` in the later permission is exactly one character, as in OpenCode's matcher, so it does not cover a `*` in the allowed one: a later `mymcp_?` leaves `mymcp_search` allowed by `mymcp_*`, and a later `a?*` leaves the name `a` allowed by `a*`. The only exemption is the `external_directory` allow for `<XDG_DATA_HOME>/opencode/tool-output/*` described in the limitations. Output that is not a readable rule list reports `incompatible version`, and a failed call reports `unavailable`, so a changed format cannot pass. A top-level `permission` widening alone is already closed, because the `agent.build` copy of Arena's map puts `*` deny after it. The check runs when a run starts, with the other readiness probes, not before each worker, so a configuration edited after that is not re-checked. Remove or deny the extra tools in your OpenCode configuration and check again. This costs two more `opencode` starts per probe, which is why the OpenCode probe has a 30-second budget instead of 12.

Output is one JSON object per line. An `error` event fails the worker with its message. The last `text` part is the reply. `step-finish` parts carry the token counts Arena uses for usage. A non-zero exit fails the worker with a stderr tail. OpenCode has no structured-output flag, so the prompt asks for exactly one JSON object matching the node's output schema. Arena parses the last text as one JSON object and checks that object's top-level keys against the schema: every `required` key must be present and, where the schema sets `additionalProperties: false`, no other key may appear. The adapter does not check property types or nested objects. Result and review-result payloads are later checked in full by the envelope contract (`ValidatePacket`) when their packet is stored.

Effort is optional. When set, it maps to `--variant` and must be a variant that the catalog lists for the selected model. Variant names differ by model (for example `low`, `medium`, `high`, `xhigh`, `max`, or `none`, `minimal`, `low`, `medium`, `high`). Arena checks effort against the catalog before launch, because `opencode run` hangs on an unknown variant. The `provider/model#variant` form is rejected by 1.19.3, so `--variant` is the only intensity control. OpenCode runtimes take no `options`, so `authMethod`, `commandApproval` and an `options.effort` are all refused; effort is the runtime's `reasoningEffort` field, an opaque variant name.

The catalog comes from `opencode models --verbose`, which lists `provider/model` IDs with their variants. A listed model is not proof of inference access. The catalog can include `github-copilot/...` and `amazon-bedrock/...` models; these are OpenCode provider routes. Copilot itself stays skill-only, with no Arena Copilot adapter.

Limitations:

- No live Arena MCP bridge: OpenCode workers cannot call Arena tools or ask the user questions during a run. Packets travel only inside the final JSON result's `packets` array.
- No shell commands. The adapter has no command sandbox for OpenCode, so an agent with `run_commands` is refused when it starts and earns no score credit.
- User configuration is checked, not isolated. Arena keeps your OpenCode configuration in place (it holds your providers and models), so a user-level `agent.build.permission` that lists tools after `"*"` would grant them to workers, MCP tools included. The readiness check above refuses that setup instead of accepting the risk. What it does not cover: a configuration changed after the run started; and pattern-level rules (an allow for one pattern of a tool is a grant unless a later bare-`*` rule for that tool overrides it, and the check does not weigh narrower patterns against each other). No worker ran with a widened configuration to call such a tool; the conclusion rests on the resolved rules.
- Residual risk, read only: workers can read the tool outputs of other OpenCode sessions. `opencode debug agent build` on 1.19.3, offline, with Arena's exact read-only permission map and `OPENCODE_DISABLE_PROJECT_CONFIG=1`, resolves 59 rules, and the last is an `external_directory` allow for `<XDG_DATA_HOME>/opencode/tool-output/*`. OpenCode appends that rule after the user's configuration and evaluates the last matching rule, so Arena's `external_directory: deny` does not cover it, and `read` and `list` are allowed for `*`. A worker can therefore list and read the truncated tool outputs of every OpenCode session on the machine, including other Arena workers and the user's own sessions in other repositories. This follows from the resolved rules; no worker run was made to read such a file. The adapter cannot override the rule through config, because OpenCode appends it after user config. Isolating the worker's data directory is not possible without also moving OpenCode's own sign-in store (`auth.json` lives under the same `XDG_DATA_HOME/opencode`), which Arena leaves alone. Do not run an OpenCode worker where another OpenCode session's tool output on the same machine must stay unread. Every other path outside the workspace stays denied by the resolved rules.
- Writes to `tool-output` are denied. The `external_directory` allow above also lets the `write`, `edit` and `apply_patch` tools pass their first check, but each then asks the separate `edit` permission with the path relative to the worktree. A writer's `edit` rule is `{"*": "allow", "../*": "deny"}`, at the top level and under `agent.build.permission`, so every path outside the worktree is denied and a writer cannot create or overwrite files that other OpenCode sessions read back. Offline with the writer's map, `opencode debug agent build` resolves 61 rules: the `edit` deny for `../*` follows the `edit` allow for `*` in both places, and no later rule names `edit` or is the catch-all `*`. The bundle's own matcher, run on those resolved rules, denies the relative path of a `tool-output` file and allows `src/a.go`. The paths are relative to OpenCode's worktree, which for the linked git worktrees Arena gives a writer is the worktree itself (checked offline through `opencode serve`'s `/path`); outside a git repository it is `/`, where `../*` would not match. The rule tests the path as written, not where a symlink inside the worktree leads. This follows from the resolved rules and the bundled source; no worker ran to attempt a write.
- Not verified against a live run: success event shapes beyond the bundled source, the token field names, `auth list` environment semantics and headless permission-denial behaviour, including a refused write outside the worktree.

References: [OpenCode CLI](https://opencode.ai/docs/cli/) (`run`, `--format`,
`--variant`, `--pure`, `models`, `auth login`),
[providers and credentials](https://opencode.ai/docs/providers/) (`opencode auth list`),
[config](https://opencode.ai/docs/config/) (`OPENCODE_CONFIG_CONTENT`, `permission`),
[agents](https://opencode.ai/docs/agents/).

## Local inference

Local endpoints must be loopback HTTP URLs without embedded credentials or redirect
chains. Models are discovered through Ollama `/api/tags` or an OpenAI-compatible
`/models` endpoint. Chat responses stream through a bounded JSON tool protocol.
Arena does not assume native function calling or a universal reasoning scale. Local
backends use the same workflow, messages, context, worktrees and integration queue.
Ollama generation values stay in `options.generation`; OpenAI-compatible generation
controls are limited to documented request fields. Unknown capabilities remain unknown.

References: [Ollama chat](https://docs.ollama.com/api/chat),
[LM Studio compatibility](https://lmstudio.ai/docs/developer/openai-compat/tools).

## Skill-only hosts

Copilot receives the same portable skill as the other hosts. No execution adapter or account
access is advertised for it. OpenCode also receives the skill; its execution adapter is described
[above](#opencode). Locations follow the hosts' documented discovery paths:
[Copilot customization](https://docs.github.com/en/copilot/reference/customization-cheat-sheet),
[OpenCode skills](https://docs.opencode.ai/docs/skills/),
[Gemini skill locations](https://geminicli.com/docs/cli/using-agent-skills/).

## Running live verification

Only run this after explicitly selecting a model/effort and accepting provider usage:

```sh
make build
ARENA_INTEGRATION_BINARY="$PWD/bin/arena" \
ARENA_LIVE_CODEX_MODEL='YOUR_SELECTED_MODEL' \
ARENA_LIVE_CODEX_EFFORT='YOUR_SELECTED_EFFORT' \
go test ./internal/arena -run TestLiveCodex -v -count=1
```

These are real inference tests. Other adapter/HTTP contract fixtures are labelled in
tests and do not establish live readiness. The M3 multi-provider live exit passed on
2026-10-16 with Codex `gpt-6.1-sol` / `high` and Antigravity `gemini-3.8-flash-medium`;
see [validation evidence](VALIDATION.md#production-readiness-sweep-2026-10-16). Claude
Code, Gemini and local routes still have only contract or probe coverage.
