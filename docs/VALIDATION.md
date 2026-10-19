# Validation evidence

Checked locally on macOS arm64 with Go 1.27.2 and Git 2.50.1 (Apple Git-155) on 2026-10-15. Readiness, contract fixtures and actual
inference are different evidence. The original validation snapshot was local; subsequent changes may be published through pull requests.

## Local exit checks

| Area | Evidence |
| --- | --- |
| M0/M1 contracts and authoring | All repository fixtures validate; generation is reproducible. CLI create/show/validate/delete and stale-profile conflict checks pass. Setup tests cover creating Git/control configuration, bare empty-directory entry, explicit runtime requirements, cancellation, saved-control reuse and preserving staged files. Embedded-interview fixtures exercise questions, native provider question callbacks, reviewed draft saving, attached cancellation and versioned audited handoffs. Graph validation covers reachability, DAGs, teams, routers, on-call isolation, profile paths and explicit runtimes. |
| M2 journal and context | Reopening state, detecting a corrupt mirror, preserving/repairing it, redacting secrets, context revision/archive/restore and workflow isolation pass. Reduced-retention decisions are compacted after use. |
| M3 packets and orchestration | Fixture tests cover unanimous affirmation, no-consensus, unavailable members, writing-team artifacts/read-only composition, durable on-call caps, coworker control-slot/wave additions, peer permissions, MCP scope, routers and the local HTTP/tool contracts. They do not establish real provider readiness. |
| M4 Git | Real temporary repositories exercise independent writer proposals, review, integration, preserving later commits, rollback and rollback-of-rollback, dirty/index/untracked checkpoints, prerequisite snapshots and isolated checkpoint restoration. A deterministic [conflict and rollback benchmark](BENCHMARKS.md) (`make bench`, fake writers, no inference) counts lost or silently overwritten writes in conflict, user-edit, rollback and loop scenarios against a naive shared-checkout baseline; it is not a benchmark of any other product. |
| M5 terminal and supervision | Width/height checks cover 140x40 down to 8x4. Bare-command tests cover sole-arena auto-open, explicit multiple-arena choice, cancellation, invalid/empty projects, subdirectory/nested-project discovery, no invented runs and attached-engine interruption (fixture invoker). Real PTY startup checks cover single/multiple/empty projects, prompt readiness refusal, resize and clean exit using metadata fixtures only. Earlier PTY checks cover init/create, Git proposal/patch pane, Git detail and resize (fixture events; no inference). Packet tests distinguish pending, delivered and failed states. Native sandbox execution checks private scratch space, read-only refusal, prevention of neighboring-tree writes, refusal to read a planted home directory or neighboring worktree, a fresh process table on Linux and an in-worktree Go build (Linux bubblewrap exercised; macOS profile not run). |
| M6 skills and compatibility | Project/user target code uses one embedded canonical skill. Installation tests cover host locations, modified-file refusal and backed-up replacement. Installed CLI versions and compatibility drift are reported separately from inference. |

`make check` (vet and race-enabled tests) passes. The standalone release builder cross-builds all four target executables and emits a corresponding source archive, third-party notices and SHA-256 checksums. All archive checksums, extraction, packaged file inventories and host-executable smoke checks pass. The extracted source archive rebuilds, passes generated-contract checks and installs successfully into a temporary prefix. Dependency checking with the
Go 1.27.2-built `govulncheck` found **zero reachable vulnerabilities** after upgrading
networking/auth transitives. It reported one advisory in a required module with no
imported affected package; this is not a claim that every dependency is advisory-free.

The compact dotted purple title is checked for left-margin placement in the status band, gradient colors,
absence of filled blocks/background/shadows, matching color/monochrome geometry,
monochrome rendering, size limits, compact/hide preferences and non-Unicode fallback.
Spin tests step through seven letter turns and check that one letter turns at a
time in A2rena order, every frame keeps each row's width and the gaps between
letters, each turn starts facing front, and the text fallback, `NO_COLOR` and
`display.animation: off` keep the static wordmark.

Fixes after the first Fable sweep review: the plain Antigravity write fence is
tested from inside the fence with a planted home, state directory and outside
directory; a reader reaches only its state overlay and private `/tmp`, a writer
also its worktree, and nothing leaks to the host. With the same fence layout, the
installed `agy` 1.4.0 reported its version and listed models through its own
keyring sign-in (no inference turn was run). `Redact` covers the share-code token
shapes (AWS, Slack, JWT, GitHub fine-grained, Stripe live, Google OAuth); command
fences empty `/run/user` and refuse to start without `XDG_RUNTIME_DIR`; walkthrough
ideas and `/consult` revisions that turn on Antigravity blanket approval are refused
in code.

Final-TODO fixtures (fixture invoker; no inference): `/consult` runs read-only on
the control runtime with the saved arena, score and run evidence, journals
`consult.*`, changes nothing and starts no run; a proposed revision is repaired
when it changes the workflow ID, stays a draft until `/save`, is audited with
source `consult`, and is refused when the arena changed after it was proposed.
`/walkthrough` asks about the project first, sends a premature draft back for
repair, validates two or three ideas, lists their roles, suggested providers,
rough cost and grade, and drafts only after the user's pick.
A real PTY test of the new setup entry completed bare `arena` from an empty non-Git
directory, explicit runtime answers, Git/control/skill creation, an orchestrator
interview, a CLI-generated workflow, graph entry, resize and clean exit. Subsequent
embedded-interview tests replace native CLI launch coverage: slash commands,
activity/context mini-windows and profile/task/available-context separation pass.
A PTY fixture completed chat, public activity streaming, popup context switching,
explicit `/save` into the graph, resize and clean exit; it verified no native
Codex screen, private reasoning sentinel or invented run. Private reasoning is
explicitly excluded, and mediated non-Codex tool calls also emit public activity.

The terminal refresh adds rounded Unicode borders with an ASCII fallback, a visible
caret, cursor/word editing, multiline paste and a bounded session-local input history.
Regression tests cover middle-of-message edits, combining characters and emoji,
draft restoration after history recall, busy-input preservation, completion, view
shortcuts, Unicode graph geometry and composer/popup resizing down to 8×4.
A real PTY fixture exercises arrow-key editing and Delete, Up/Down recall without
submission, the visible caret, rounded borders, setup chat, activity/context windows,
explicit draft saving, resize and clean exit. This is terminal/fixture evidence,
not additional live-provider verification.

## Real provider evidence

- **Codex:** installed `codex-cli 0.162.0-alpha.17.2`; documented app-server initialization,
  account/model discovery and actual `gpt-6.1-sol` with `high` effort verified. The real
  read-only test read `base.txt` from a pinned temporary worktree and returned its
  actual `baseline` content in a validated result with retained task/result history.
  The final live test also completed an actual Arena MCP context search through the
  installed CLI, including its tool confirmation. A separate non-inference test
  exercised the real CLI/MCP bridge directly.
  The embedded setup interview also passed with `gpt-6.1-sol` / `high`: a real
  background app-server returned a structured question for missing choices,
  preserved the versioned/audited handoff, and saved no workflow or run. This is
  backend inference evidence; the full terminal interaction above uses fixtures.
- **Claude Code:** the API-key/cloud route has no live evidence (no key or cloud
  project was available). The `claude-code-sign-in` route is implemented as an
  explicit user choice under the user's own plan; one-turn probes with CLI 2.1.301
  confirmed its tool restriction and `structured_output` (see
  [Providers](PROVIDERS.md)), but no Arena workflow has run on it live. Arena never
  selects it by default and never reads its credentials.
- **Gemini:** official Google GenAI SDK v1.75.0 builds. Live API-key/Vertex access has not
  been exercised. Personal Gemini CLI OAuth is excluded.
- **Local:** HTTP streaming, discovery and mediated tool contracts have fixture coverage.
  A real backend/model has not been selected or exercised.
- **OpenCode:** installed `opencode` 1.19.3 was inspected, and its adapter has contract and probe coverage only. No live inference has run; see [OpenCode adapter evidence](#opencode-adapter-evidence-2026-10-19).

The owner authorized Codex `gpt-6.1-sol` / `high` and, for the production-readiness
sweep, chose Antigravity under its own sign-in (`gemini-3.8-flash-medium`, agy 1.4.0)
as the second route. **The M3 live multi-provider exit passed** on 2026-10-16 (Linux
amd64): see the sweep evidence below. The adapter-portability part of M6 is met for
Codex and Antigravity; Claude Code, Gemini, OpenCode and local adapters still have fixture or
probe evidence only.

## OpenCode adapter evidence (2026-10-19)

Recorded on Linux in this sandbox against `opencode` 1.19.3 from npm (`opencode-ai`). The evidence is inspection and contract coverage. No inference ran: the free tier refused the sandbox IP (403 `FreeTierError`) and no paid credentials were available, so **live inference is unverified**.

Observed on the binary:

- `run --help` lists `--format default|json`, `--variant`, `--model/-m provider/model`, `--dir`, `--pure`, `--agent` and `--auto`. The adapter never passes `--auto`. A prompt piped on stdin works, which avoids argv limits.
- `models --verbose` lists 248 models: `opencode` (12), `github-copilot` (38) and `amazon-bedrock` (198). 178 carry a `variants` object, and the variant names differ by model.
- `-m 'opencode/big-pickle#high'` is rejected, so the `#variant` suffix from the v2 docs does not apply to 1.19.3. An unknown `--variant` makes `run` hang rather than fail.
- `auth list` prints a Credentials section with a count and an Environment section with variable names. Arena reads only that presence output and never reads `auth.json`.
- `OPENCODE_CONFIG_CONTENT` overrides a worktree's `opencode.json` on conflicting permission keys (checked with `opencode debug config`: an environment `deny` beat a project `allow`). Wildcard keys were not verified.
- `OPENCODE_CONFIG_CONTENT` can also define providers, models and variants. With a value that defines provider `myprov` (model `m1`, variant `turbo`), `opencode models --verbose` lists `myprov/m1`, and with Arena's own value it does not (isolated `HOME`, no inference). Because an unknown `--variant` makes `run` hang, the probe and catalog calls run in the worker's environment, without the parent's `OPENCODE_CONFIG_CONTENT` and `OPENCODE_PERMISSION`, so readiness cannot approve a model or variant the worker cannot select.
- **External plugins are off for the probe and catalog calls too.** `--pure` is a root-level `opencode` option ("run without external plugins") whose middleware sets `OPENCODE_PURE=1`, and the runtime reads that variable, so the worker's `run --pure` and `OPENCODE_PURE=1` in the environment are the same setting. Checked offline on the real 1.19.3 binary (isolated `HOME` and `XDG_*`, `OPENCODE_DISABLE_PROJECT_CONFIG=1`, no inference) with a local plugin in `$XDG_CONFIG_HOME/opencode/plugin/` whose `config` hook adds provider `plugprov` (model `m1`, variant `turbo`) and `agent.build.permission` `{plugtool: allow}`. Without pure, `models --verbose` listed `plugprov/m1` with the variant `turbo` and `debug agent build` held a `plugtool` allow. With `--pure`, and with `OPENCODE_PURE=1` in the environment, `models --verbose` listed no `plugprov` model and `debug agent build` held no `plugtool` rule. An unpatched probe would therefore approve a model and `--variant` that `opencode run --pure` cannot select (an unknown `--variant` makes `run` hang) and judge a permission list the worker does not run with, so `openCodeEnv` sets `OPENCODE_PURE=1` for every call and drops the parent's value. With no plugin installed, `debug agent build` printed the same 59 rules for Arena's read-only map with and without pure (byte-identical output), so the recorded `debug agent build` fixtures are unaffected. No worker ran.
- **Residual risk, read only; not closed by the adapter.** `opencode debug agent build`, offline with an isolated `HOME`, Arena's exact read-only permission map (`compactJSON(openCodePermissions(false))`) and `OPENCODE_DISABLE_PROJECT_CONFIG=1`, resolves 59 rules. Arena's `external_directory: deny` comes before the last rule, which is `{"permission":"external_directory","pattern":"<XDG_DATA_HOME>/opencode/tool-output/*","action":"allow"}`. OpenCode appends it after user config and evaluates the last matching rule, so Arena's `external_directory: deny` does not cover it. The directory holds the complete, untruncated text of every tool output that OpenCode cut short in context (the 1.19.3 bundle writes a `tool_*` file there whenever an output passes the default limit of 2,000 lines or 51,200 bytes, set by config `tool_output.max_lines` and `tool_output.max_bytes`), from every OpenCode session on the machine: the user's own sessions in other repositories, other Arena workers, and Arena OpenCode workers' own large reads and greps of their worktrees. OpenCode's cleanup removes only files older than 7 days. `read`, `glob`, `grep` and `list` are allowed, so a worker can list those files, read them and grep their contents. Config cannot close it: the appended allow follows all user configuration, and a `read` rule `"../*": "deny"` would stop the `read` tool (asked with the path relative to the worktree) but not `grep`, whose permission is asked on the search regex and not the path, and whose directory check is the same `external_directory` allow. Moving `XDG_DATA_HOME` would move OpenCode's own sign-in store, so the data directory is not isolated. This was read from the resolved rules and the bundled source; no worker ran to read such a file. It is listed in the [OpenCode limitations](PROVIDERS.md#opencode).
- **Writes to `tool-output` are denied.** The write, edit and apply_patch tools pass the `external_directory` allow above, then ask `edit` with the path relative to the worktree. A writer's `edit` rule is `{"*":"allow","../*":"deny"}` (`openCodeEditPermission`), at the top level and under `agent.build.permission`. Offline with an isolated `HOME`, the writer's map (`compactJSON(openCodePermissions(true))`) and `OPENCODE_DISABLE_PROJECT_CONFIG=1`, `opencode debug agent build` resolves 61 rules. In both places the `edit` deny for `../*` comes after the `edit` allow for `*`, and no later rule names `edit` or is the catch-all `*`. The matcher and evaluator source copied from the 1.19.3 bundle (`Wildcard.match`, `findLast`), run in node on those resolved rules with `path.relative(worktree, file)`, allows `src/a.go` and denies `../x` and the `tool-output` file. `opencode serve`'s `/path` (offline) reports the linked git worktree itself as OpenCode's worktree, which is what Arena gives a writer, and `/` outside git, where `../*` would not match. The rule tests the path as written, so a symlink inside the worktree that leads out of it is not covered. No worker ran to attempt a write. A user-level `edit` object can put the allow after that deny; the pattern-rule bullet below records that case and the probe that refuses it.
- **A user-level OpenCode configuration can add tools to workers; the readiness probe now fails closed on it.** Offline, with an isolated `HOME` and `XDG_*`, `OPENCODE_DISABLE_PROJECT_CONFIG=1`, Arena's exact permission map (`openCodeEnv`) and no model call, `opencode debug agent build` was run in a scratch git repository. With `$XDG_CONFIG_HOME/opencode/opencode.json` = `{"agent":{"build":{"permission":{"*":"allow","codesearch":"allow","mymcp_*":"allow"}}}}`, it resolved 61 rules for a reader (63 for a writer). Rule 36 is `* deny` (Arena's `*` replaced the user's in place), rule 37 is `codesearch` allow and rule 38 is `mymcp_*` allow, and no later rule names them, so the last matching rule is allow for `codesearch` and for every `mymcp_*` MCP tool, while `bash`, `edit`, `webfetch`, `external_directory` and the rest of the named denies still hold. Without that user file the same command resolves 59 rules (61 for a writer) and leaves nothing outside `read`, `glob`, `grep`, `list` (and `edit` for a writer) allowed, apart from the `tool-output` `external_directory` allow above. The listings are recorded in `testdata/opencode-debug-agent-build-1.19.3-*.json` (reader, writer, and each with the widening; the data directory is rewritten to `/home/arena/.local/share`). The probe was also run end to end against the real 1.19.3 binary in the same isolated environment, with only `opencode auth list` replaced by a stub that reports one stored sign-in (no credential was read): an unwidened configuration was ready, and the global `agent.build.permission` above, an `OPENCODE_CONFIG` file of the same shape and a global `agent/build.md` with `permission` frontmatter were each refused with state `unsupported` naming the extra tools, while a top-level-only `permission` widening was ready, because the `agent.build` copy of Arena's map puts `*` deny after it. A hostile worktree `opencode.json` plus `.opencode/agent/build.md` is ignored (59 rules, `codesearch`, `bash` and `edit` all deny). The first of two `debug agent build` calls started together on a data directory that OpenCode had not used yet failed while creating its database, so the probe runs the reader's and the writer's resolution one after the other. No worker ran with a widened configuration, so that a worker actually calls `codesearch` or an MCP tool is not shown; the conclusion rests on the resolved rules.
- **A user-level `edit` or `read` object can reverse Arena's pattern rules; the readiness probe now fails closed on that too.** OpenCode merges a user's `agent.build.permission` objects key by key in the user's key order, with Arena's values, and the tool check above skips every `read` rule and a writer's `edit` rules. Offline on the real 1.19.3 binary, with an isolated `HOME` and `XDG_*`, Arena's exact maps (`openCodeEnv`), `OPENCODE_PURE=1`, `OPENCODE_DISABLE_PROJECT_CONFIG=1` and no model call: (a) a writer with the global `{"agent":{"build":{"permission":{"*":"ask","edit":{"../*":"ask"}}}}}` resolved 61 rules ending `* * deny`, `edit ../* deny`, `edit * allow` (rules 37 to 39) with no later `edit` or catch-all rule, so the last-match rule gives allow for both `src/a.go` and a path under `<XDG_DATA_HOME>/opencode/tool-output/`, the outside-worktree deny that the bullet above relies on is gone, and the tool check, which skips those rules, reports nothing for the listing; (b) a reader with the global `{"agent":{"build":{"permission":{"*":"allow","read":{"*.env":"ask","*":"allow"}}}}}` resolved 59 rules whose `read` rules end `*.env deny`, `* allow`, `*.env.* deny`, `*.env.example allow`, so `.env` and `config/.env` resolve to allow. The listings are recorded in `testdata/opencode-debug-agent-build-1.19.3-writer-flipped-edit.json` and `-reader-flipped-read.json` (the data directory is rewritten to `/home/arena/.local/share`). The probe now evaluates the resolved rules of the reader's run and of the writer's run on fixed inputs, with the bundle's matcher (`Wildcard.match`: `*` is any run of characters, `/` included, `?` any one, the pattern is anchored) and evaluator (`Permission.evaluate`: the last rule whose permission and pattern both match, `ask` when none does): `read` on `.env`, `a/.env` and `a/.env.local` must resolve to deny, and on `.env.example` and `src/a.go` to allow; for the writer, `edit` on `src/a.go` must resolve to allow, and on `../x` and `../../d/opencode/tool-output/tool_1` to deny. A miss reports `unsupported` with the input, the action the rules give and the deciding rule. Run end to end against the real binary in the same isolated environment, with only `opencode auth list` replaced by a stub that reports one stored sign-in (no credential was read): (a) and (b) were each refused with state `unsupported` and a detail naming the deciding rule, and an empty global configuration and a global `{"lsp": true, "formatter": true}` were ready. The Go matcher was also compared with the bundle's `Wildcard.match` function, copied out and run in node, on 6,009 generated pattern and input pairs (2,088 of them matches, with `*`, `?`, `\`, a trailing ` *`, regular-expression characters and newlines among the characters): no pair differed. No worker ran, so a refused edit or read in a live run is not shown; the conclusion rests on the resolved rules and the bundle's matcher.
- **Language servers and formatters are set off for every call by Arena's own configuration, because they run repository code outside any sandbox; managed and organisation configuration can re-enable them (see the next bullet).** From the 1.19.3 bundle: the config schema describes `lsp` and `formatter` as "Omit or set to false to disable, true to enable built-ins, or an object to enable built-ins with overrides"; `ReadTool.warm` calls `touchFile` on every read; the edit tool calls `touchFile(file, "document")` after "Edit applied successfully."; the LSP layer starts language servers (eslint and rust among the built-in ones) and downloads them unless `OPENCODE_DISABLE_LSP_DOWNLOAD` is set; the Format service runs each formatter with `cwd` set to the worktree, for example `prettier --write $FILE` when the worktree's `package.json` lists prettier. Offline on the real binary, with an isolated `HOME` and `XDG_*`, Arena's exact `OPENCODE_CONFIG_CONTENT` (reader map), `OPENCODE_PURE=1`, `OPENCODE_DISABLE_PROJECT_CONFIG=1`, `OPENCODE_DISABLE_AUTOUPDATE=1` and no inference: with the global `~/.config/opencode/opencode.json` set to `{"lsp": true, "formatter": true}`, `opencode debug config` printed `lsp` true and `formatter` true, and `opencode debug lsp diagnostics a.go` in a scratch Go repository started the fake `gopls` placed on `PATH`, which wrote its marker file. With `"lsp": false` and `"formatter": false` added to the same `OPENCODE_CONFIG_CONTENT`, both resolved to false, also over an object-shaped user `lsp` and `formatter`, and the fake `gopls` was not started. `openCodeEnv` sets both keys for readers and writers, probe and catalog calls included, and `OPENCODE_DISABLE_LSP_DOWNLOAD=1` as defence in depth; the variable name is in the binary. No formatter ran and no worker made an edit, so what is shown is the resolved configuration and the language-server start.
- **Managed and organisation configuration is loaded after `OPENCODE_CONFIG_CONTENT` and can re-enable `lsp`, `formatter` and sharing.** Offline on the real 1.19.3 binary, with no model call: under Arena's own configuration (`lsp` and `formatter` false, `share` disabled), `OPENCODE_TEST_MANAGED_CONFIG_DIR` pointed at a directory holding `opencode.json` with `{"lsp":true,"formatter":true,"share":"auto"}`, and `opencode debug config` printed `formatter` true, `lsp` true and `share` `"auto"`. The sources read after the environment are that managed directory (`/etc/opencode`, or `/Library/Application Support/opencode` on macOS when the test variable is unset), macOS managed preferences from device management, and the remote configuration of the active Console organisation. Only the managed directory was exercised offline; the other two sources are not verified. Readiness refuses a setup whose resolved values re-enable `lsp`, `formatter` or sharing, managed and organisation sources included. No worker ran, so the effect of those values on a live run is not shown.
- **OpenCode's token counts exclude the cache and the reasoning, and Arena adds them back.** The 1.19.3 bundle's `Session.getUsage` takes the provider's `inputTokens` (Y), `outputTokens` (W), `reasoningTokens` (H), `cacheReadInputTokens` (Z) and `cacheWriteInputTokens` (J), and returns `input = max(0, Y-Z-J)`, `output = max(0, W-H)`, `reasoning = H` and `cache = {read: Z, write: J}`; the session processor writes that as the `tokens` of the `step-finish` part. So the bare `input` leaves out the cache and the bare `output` leaves out the reasoning, and `limits.maxRunTokens` would under-count a run that reads the cache on every step. Arena adds `cache.read` and `cache.write` to the step's input and `reasoning` to its output. This is the bundle's formula read from the binary; **no live `step-finish` has been captured**, because no inference has run, so the arithmetic is unverified against a real response.
- The ACP `initialize` and `session/new` handshake works offline. The adapter does not use ACP.
- From the bundled source: `step-start`, `step-finish` (with `tokens`: `input`, `output`, `reasoning`, `cache.read`, `cache.write`, from `getUsage`, see above), `text` (emitted once its end time is set), `reasoning` and `tool` parts. Live, only `error` events have been seen (`error.name`, `error.data.message`). Other field names are unverified.

Contract coverage (fake `opencode` executables, recorded fixtures and no inference; this is not live verification):

- Catalog parsing from a fixture of seven blocks (`opencode-models-verbose-1.19.3.txt`): six trimmed from a 1.19.3 recording (a model with variants, a model without) plus one synthetic block, `opencode/truncated-example`, cut short by hand and not in any recording, which is counted as skipped.
- Readiness probe: missing binary, version drift, missing flags, signed out, environment-only sign-in, unknown model and bad effort. A parent `OPENCODE_CONFIG_CONTENT` and `OPENCODE_PERMISSION` reach none of the probe or catalog calls, which receive the worker's own `OPENCODE_CONFIG_CONTENT`, and every probe, catalog and worker call receives `OPENCODE_PURE=1` even when the parent sets `OPENCODE_PURE=0` (the fake records it per call). The fake cannot show that a real plugin is not loaded; the offline plugin check above does.
- Readiness against the user's own configuration: fake `opencode` executables print the recorded `debug agent build` listings. A clean reader and writer listing is ready; a reader listing widened with `codesearch` and `mymcp_*`, a widened writer listing, and a reader that was given `edit` are each `unsupported` before the catalog is read; a failed call is `unavailable`; output that is not a rule list, a list with no rules and a rule with an unknown action are `incompatible version`. Unit tests of the rule check cover the last-match handling (a later `deny` or `ask` with a bare `*` pattern overrides, a narrower pattern or a deny of one MCP tool does not, a later deny whose permission has a `?` where the allowed permission has a `*`, such as `mymcp_?` against `mymcp_*` or `a?*` against `a*`, does not, an allow after a deny stands), the `tool-output` exemption (only that exact path, derived from `XDG_DATA_HOME` or `~/.local/share`), the cap of five names in the detail and the redaction of names. The probe's calls run in the worker's reader environment, except the second `debug agent build`, which runs in the writer's. The pattern-rule check has its own coverage: the matcher against the bundle's `Wildcard.match` semantics (`*.env` against `.env`, `a/.env` and `a/.env.local`, `../*` against `../x` and not against `x/../y`, `?`, a trailing ` *`, `\` as `/`), the last-match evaluator, the two recorded flipped listings (`...-writer-flipped-edit.json`, `...-reader-flipped-read.json`, which the tool check alone reports as clean) and synthetic rule lists in both flipped orders, an `ask` where `deny` is expected, a deny of ordinary source files and a catch-all allowed last, and a probe through the fake CLI that is `unsupported` for each recorded flip, with the clean recordings ready.
- Invocation: argv, stdin prompt, the permission map for read-only and writer agents, `lsp` and `formatter` false in `OPENCODE_CONFIG_CONTENT` and `OPENCODE_DISABLE_LSP_DOWNLOAD=1` for readers, writers, probes and catalog calls even when the parent's configuration enables both (boolean and object form), token usage that adds the cache and reasoning counts of each `step-finish` (`TestUsageOpenCodeStepsAddUp`, built from the `getUsage` formula, not from a live capture), refusal when `run_commands` is granted, `error`, `text` and `step-finish` handling, non-zero exit, invalid structured output (not a single JSON object) and a well-formed object that misses a schema `required` key or breaks `additionalProperties: false` at the top level. Property types and nested objects are not checked by the adapter.
- Option validation: a `reasoningEffort` variant accepted; every key in `options` (`authMethod`, `commandApproval`, `effort` and unknown keys) rejected, in the runtime check and in share codes.
- `TestLiveOpenCode` (in `internal/arena/live_test.go`, run through the engine like the other live tests) skips unless `ARENA_LIVE_OPENCODE_MODEL` is set, and skips in CI when ARENA_LIVE_OPENCODE_MODEL is unset. It has not been run against a live provider.

Not verified, and therefore not claimed: live inference and success event shapes beyond the bundled source; the `step-finish` token field names; `auth list` environment semantics; headless permission-denial behaviour; wildcard permission keys in a live run (the `edit` patterns `*` and `../*` resolve and match offline); that a worker actually reads another session's tool output, or that a write outside the worktree is refused in a live run (see the two entries above); that a worker calls a tool that a widened user configuration allows (the probe refuses the setup from the resolved rules alone); and how a headless run answers a permission that resolves to `ask`, which the check treats as not allowed.

## Production readiness sweep (2026-10-16)

Evidence kinds: *fixture* tests use fake CLIs and invokers, *live* runs spend real
provider inference, and earlier sections are historical snapshots of the commits
named there. The sweep's review team, Claude Fable 5.1 and Codex GPT-6-Astra, each
reviewed independently in a scratch clone on Linux amd64 (bubblewrap 0.12).

At commit 79c3be7 both reviewers ran the live exits once each, and all passed:
`TestLiveCodexReadOnlyWorkflow`, `TestLiveCodexMCPBridgeDiscovery`, and
`TestLiveMultiProviderTeamWorkflow` with Codex `gpt-6.1-sol` / `high` plus
Antigravity `gemini-3.8-flash-medium`. In the team run agy ran read-only inside the
write fence, reached its own sign-in and returned a real member position (Fable's
run: 10.56 s, 0.58 s and 188.14 s). The team test now logs its packet outcome and
member positions. Review findings and their fixes are recorded in the commits that
follow; release waits for both reviewers to approve the same commit.

## Reproduce

```sh
make generate
make check
make build
make dist
(cd dist && shasum -a 256 -c SHA256SUMS)
make bench   # conflict and rollback benchmark, see BENCHMARKS.md
```

Normal tests require no model account. Live tests spend provider usage and skip
unless explicitly selected:

```sh
ARENA_INTEGRATION_BINARY="$PWD/bin/arena" \
ARENA_LIVE_CODEX_MODEL='YOUR_SELECTED_MODEL' \
ARENA_LIVE_CODEX_EFFORT='YOUR_SELECTED_EFFORT' \
go test ./internal/arena -run '^TestLiveCodexReadOnlyWorkflow$' -v -count=1
```

The separate `TestLiveMultiProviderTeamWorkflow` additionally requires
`ARENA_LIVE_SECOND_RUNTIME`, a JSON runtime object with the chosen provider, model,
effort where supported, and non-secret route/backend options. Credentials remain
in the provider-approved environment or credential manager. The test passes one
task through a Codex head and a two-provider team, verifies real member positions
and one valid consensus/no-consensus packet. Do not run it with an unapproved route.

### Not yet run: Claude Code, Gemini, local and OpenCode live tests

**Nothing in this subsection has been run against a real provider, so it is not
evidence.** These opt-in tests exist in `internal/arena/live_test.go`, compile
under `go vet`, and skip unless their own variables are set. Their plumbing was
only exercised against throwaway fake CLIs and a fake loopback server, which says
nothing about any provider. Until someone runs a test below with their own
sign-in or credentials and it passes, the Claude Code, Gemini, local and OpenCode entries in
[Real provider evidence](#real-provider-evidence) stay as written.

Each test sends one read-only task through the real adapter (a reader agent reads
`base.txt` in its pinned worktree and must report its actual `baseline` content in
a validated result) with retries off, so a provider failure ends the test. Run
these with your own sign-in or credentials; they can spend your plan or cloud
usage. Credentials stay in the provider-approved environment, keychain or
sign-in: the tests read only the `ARENA_*` selections below, never a key,
token or credential file, and log only redacted events. Do not paste a key into
the command line; have it configured the way the provider documents.

| Test | Route | Required variables | Also needs |
| --- | --- | --- | --- |
| `TestLiveClaudeCodeSignInReadOnlyWorkflow` | Claude Code under its own sign-in (`options.authMethod` `claude-code-sign-in`, `invokeClaudeNative`) | `ARENA_LIVE_CLAUDE_SIGNIN_MODEL`, `ARENA_LIVE_CLAUDE_SIGNIN_EFFORT` | `claude` installed and signed in with its own login, an explicit choice under your plan's terms |
| `TestLiveClaudeCodeAPIRouteReadOnlyWorkflow` | Claude API-key or cloud route (`api-key-or-cloud`, `invokeClaude`, bare mode) | `ARENA_LIVE_CLAUDE_API_MODEL`, `ARENA_LIVE_CLAUDE_API_EFFORT` | `claude` installed and `ANTHROPIC_API_KEY`, or a documented cloud route (`CLAUDE_CODE_USE_BEDROCK`, `CLAUDE_CODE_USE_VERTEX` or `CLAUDE_CODE_USE_FOUNDRY` set to `1` with that cloud's own credentials), already in your environment |
| `TestLiveGeminiReadOnlyWorkflow` | Google GenAI SDK (`invokeGemini`) | `ARENA_LIVE_GEMINI_MODEL`, `ARENA_LIVE_GEMINI_AUTH` (`google-ai-studio-api-key` or `vertex-ai`) | `GEMINI_API_KEY` (or `GOOGLE_API_KEY`) for AI Studio, or `GOOGLE_CLOUD_PROJECT`, `GOOGLE_CLOUD_LOCATION` and Google-managed ADC for Vertex, already in your environment. Gemini takes no reasoning effort |
| `TestLiveLocalBackendReadOnlyWorkflow` | Backend on this machine over loopback HTTP (`invokeLocal`) | `ARENA_LIVE_LOCAL_BACKEND` (`ollama`, `lm-studio`, `llama.cpp`, `vllm` or `openai-compatible`), `ARENA_LIVE_LOCAL_MODEL` | The backend already running with that model. Set `ARENA_LIVE_LOCAL_ENDPOINT` (a loopback `http://` URL) unless you use the `ollama` or `lm-studio` default; Arena refuses non-loopback addresses |
| `TestLiveOpenCode` | OpenCode through the adapter (`invokeOpenCode`) under its own stored sign-in | `ARENA_LIVE_OPENCODE_MODEL` (a `provider/model` ID from `opencode models --verbose`); optional `ARENA_LIVE_OPENCODE_EFFORT` (a variant that the catalog lists for that model; leave it unset to pass no `--variant`) | `opencode` 1.19.3 installed and signed in with `opencode auth login` (an explicit choice under your own terms), so that `opencode auth list` shows a stored credential. Readiness refuses a setup that has only provider environment variables, because those cannot be verified without running inference |

```sh
# Claude Code under its own sign-in
ARENA_LIVE_CLAUDE_SIGNIN_MODEL='YOUR_SELECTED_MODEL' \
ARENA_LIVE_CLAUDE_SIGNIN_EFFORT='YOUR_SELECTED_EFFORT' \
go test ./internal/arena -run '^TestLiveClaudeCodeSignInReadOnlyWorkflow$' -v -count=1

# Claude API-key or cloud route
ARENA_LIVE_CLAUDE_API_MODEL='YOUR_SELECTED_MODEL' \
ARENA_LIVE_CLAUDE_API_EFFORT='YOUR_SELECTED_EFFORT' \
go test ./internal/arena -run '^TestLiveClaudeCodeAPIRouteReadOnlyWorkflow$' -v -count=1

# Gemini (API key or Vertex)
ARENA_LIVE_GEMINI_MODEL='YOUR_SELECTED_MODEL' \
ARENA_LIVE_GEMINI_AUTH='google-ai-studio-api-key' \
go test ./internal/arena -run '^TestLiveGeminiReadOnlyWorkflow$' -v -count=1

# Local backend on loopback
ARENA_LIVE_LOCAL_BACKEND='ollama' \
ARENA_LIVE_LOCAL_MODEL='YOUR_LOADED_MODEL' \
go test ./internal/arena -run '^TestLiveLocalBackendReadOnlyWorkflow$' -v -count=1

# OpenCode under its own stored sign-in (add ARENA_LIVE_OPENCODE_EFFORT for a variant)
ARENA_LIVE_OPENCODE_MODEL='YOUR_SELECTED_PROVIDER/MODEL' \
go test ./internal/arena -run '^TestLiveOpenCode$' -v -count=1
```

Run `make build` and add `ARENA_INTEGRATION_BINARY="$PWD/bin/arena"` to the
sign-in command to also require a real call to Arena's `arena_context_search` MCP
tool through Claude Code; without it the bridge is not attached. The sign-in test
also fails if a read-only worker uses any tool beyond Read, Glob, Grep and the
Arena bridge.

A pass logs a `REAL ... VERIFIED` line with the route, model and effort, and means
only that one read-only task completed on that route. Writers, commands, teams,
attachments and the effort control are not covered. Record a real pass here, with
the date, versions and the exact selection, before changing any status in
[Providers](PROVIDERS.md) or the roadmap; a failure is also worth recording as
found.

## Limits

Linux amd64/arm64 and macOS amd64 are cross-build targets; native local execution
here is macOS arm64. CI is configured for macOS/Linux but has not run remotely
because nothing was pushed. WSL2 uses the Linux executable; native Windows is not
supported. Mediated shell tools require an operational OS sandbox (sandbox-exec on
macOS, bubblewrap on Linux); there is no unsandboxed fallback. Codex uses its native
sandbox and supports the adapter-default shell policy, with stricter unsupported
policies refused before launch.

Submodules/nested repositories and network filesystems are refused/unsupported.
Ignored files are excluded from checkpoints. Retirement is recoverable local
storage, not secure deletion; `arena history RUN purge` removes a retired run's worktrees, branches and checkpoint refs (tested against a user checkout and a second run, never `git gc`, so unreachable Git objects remain). Reduced
retention cannot replay omitted packets; pending operational decision text can
remain after a crash. Audit export/import is covered by local tests only (round trip, tampered event and manifest, ID collision, reduced retention, oversized and malformed bundles); bundles are not signed. Native Windows and skill-only host
execution adapters are not implemented.

## Background shaders

Local checks on 2026-10-15: `make check` (vet and race-enabled tests) and
`make build` pass with the shader feature. Tests cover HLSL-profile vector math,
activity inputs, center-to-edge default fading, non-finite output, syntax/resource
limits, source isolation, recoverable transactions, concurrent source conflicts,
missing-file repair, symlink refusal, and foreground/monochrome/animation behavior.
A short two-worker compiler/evaluator fuzz run completed 306,825 executions without
a failure. The default evaluator benchmark on the local Apple M2 Pro measured
about 0.91 microseconds per cell with zero heap allocations (not a cross-platform
performance guarantee).

A local rendered fixture was inspected with the graph at 140×40; foreground and
popup comparisons also passed down to 8×4. The first PNG export used Menlo alone,
which lacks the logo's Braille glyphs; the corrected export uses Apple Symbols
for those glyphs. This was preview font coverage, not corrupted terminal output.
The existing terminal logo geometry/fallback checks pass. No new real-terminal
font compatibility or provider inference verification is claimed. Default-model
shader generation, read-only permissions and versioned/audited handoffs are tested
with a fixture invoker; the subsequent live test is recorded below.


### Live default-model shader creation

On 2026-10-15, an isolated CLI-created `shader-demo` project used the user's
explicitly selected default runtime: Codex `gpt-6.1-sol` / `high`, via the real
`codex-cli 0.162.1` app-server. `arena shader create` generated a sparse dot-ripple
background with an approximately 30-second cycle. The first attempt passed the
HLSL-profile compiler, displayed its preview and applied through a recoverable
transaction. The source hash matched the journal; task/result envelopes shared a
correlation ID; the previous source remained in the transaction. No workflow head
or ordinary workflow run was started.

A real PTY session opened the saved arena and recorded 61 frames over 30.5 seconds.
All 61 frames differed; 239 background cells differed at the 15-second comparison.
All 852 non-dot foreground cells retained their text and color throughout. Resize
checks at 60×18, 8×4 and 120×32 retained the prompt, and `/quit` exited cleanly.
The recording renders actual terminal cell output with a Braille-capable font for
the logo; it is not a screenshot of a particular terminal emulator. The live call
reported 19,591 input and 823 output tokens (20,414 total).

This verifies the CLI generation → validation → application → animated graph path
for this Codex runtime. TUI `/shader` preview/apply still has fixture coverage rather
than a separate live generation call. Other provider shader generation and broader
multi-provider milestones remain unverified. Readiness still reports catalog/version
drift separately from this successful inference result.


### Shader visibility correction

A user screenshot exposed that the first renderer's 24–84 grayscale range was
barely visible on a charcoal terminal background, despite passing animation/state
checks. The renderer now lifts midtones and caps highlights at 165, preserving
zero transparency, monotonic edge fading, and foreground masks. A regression test
checks midtone visibility against a #1e1e1e theme. `make check` and `make build`
pass. The isolated demo also received its initial Git commit, so an accidental
worker-start request no longer fails merely because HEAD is unborn. Viewing the
shader does not require starting a worker.
A fresh real PTY recording on the charcoal theme produced 31 distinct frames in
15.5 seconds, with 248 changed background cells and all 854 checked foreground
cells unchanged. The same resize and clean-exit checks passed.

### State-rich shader inputs and reactive default

Local checks on 2026-10-15: `make check` and `make build` pass. Tests cover every
input being declarable and documented, inputs that cost operations only when
referenced, hoisted per-frame evaluation matching full evaluation, the extended
intrinsics, the projection of run/agent/packet/decision/Git/provider/journal/composer
state into inputs, event ages, focus selection and card location, envelopes that
ease in and out, animation-off snapping, and softened hue output. The default
shader fades to dark edges at rest and reacts to work, questions, failures,
success, events and typing. A 20-second fuzz run of the compiler/evaluator found
no failures. On the local Linux Xeon container, the hoisted default costs about
1.3 microseconds per cell. That is about 6 ms for a 120×40 frame, and it is not
a cross-platform performance guarantee. Static renders of idle, busy, asking,
failing and succeeded scenes were inspected. No new real-terminal recording is claimed.

## Composer and attachments

Local regression coverage includes Shift+Return, visual-line navigation, grapheme
editing, minimal `/help` footer, attachment add/remove/drop, failed-send draft
preservation, immutable snapshots, run isolation, hash verification, count/size
limits, retention and adapter refusals, audited workflow/interview handoffs, and
mediated attachment reads. Codex native-image input is checked against the installed
0.162.1 app-server's generated schema and fixture construction; no new live model
image-inference claim is made.

Native macOS tests use isolated named AppKit pasteboards to exercise PNG, TIFF
conversion and multiline text through the actual `osascript` bridge, without
reading or changing the user's clipboard. Linux Wayland/X11 clipboard selection
has command-fixture coverage, not a native Linux desktop verification.

`make check` (vet and race-enabled tests) and `make build` pass with the composer
changes. A real PTY smoke test verified the minimal footer, explicit file attach,
Shift+Return via the terminal keyboard protocol, draft-preserving Ctrl+V image
paste using a clipboard-command fixture, removal, resizing at 60×18, 8×4 and
120×32, and clean exit. The separate native AppKit tests above cover actual image
extraction; the PTY test did not read the user's clipboard or invoke a model.

## Usage counting

`usage_providers_test.go` feeds each provider's usage through the adapter that
parses it and checks that every input token is counted once, with a cache-heavy
request for each: Claude Code (the signed-in route's stream-json, where only the
result's cumulative usage is read and the cache categories add to input), Codex
(a scripted app server that streams three running totals), Gemini (wire JSON
parsed by the `genai` SDK, with cached content, tool-use and thinking tokens,
across two tool turns), local models (an HTTP fixture with two tool turns) and
Claude through an API key (a scripted CLI with two turns). A journal test checks
that the full provider map, with its cache categories, is stored unchanged.

The shapes come from the interfaces' own definitions, not from captured live
runs: the Codex app-server schema generated by the installed codex-cli 0.163.0,
the `genai` SDK's documented usage fields, Ollama's final frame, and the usage
objects in a local Claude Code transcript. The values are invented. Not yet
verified: a live capture of each provider's usage (all of Claude Code, Codex,
Gemini and a local model), whether Codex's `cacheWriteInputTokens` lies inside
its `inputTokens` (counted as inside), and the Antigravity usage fields, which
no test covers.
