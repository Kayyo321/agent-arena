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

The owner authorized Codex `gpt-6.1-sol` / `high` and, for the production-readiness
sweep, chose Antigravity under its own sign-in (`gemini-3.8-flash-medium`, agy 1.4.0)
as the second route. **The M3 live multi-provider exit passed** on 2026-10-16 (Linux
amd64): see the sweep evidence below. The adapter-portability part of M6 is met for
Codex and Antigravity; Claude Code, Gemini and local adapters still have fixture or
probe evidence only.

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

### Not yet run: Claude Code, Gemini and local live tests

**Nothing in this subsection has been run against a real provider, so it is not
evidence.** These opt-in tests exist in `internal/arena/live_test.go`, compile
under `go vet`, and skip unless their own variables are set. Their plumbing was
only exercised against throwaway fake CLIs and a fake loopback server, which says
nothing about any provider. Until someone runs a test below with their own
sign-in or credentials and it passes, the Claude Code, Gemini and local entries in
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
Ignored files are excluded from checkpoints. Retained worktrees/refs need owner
cleanup; retirement is recoverable local storage, not secure deletion. Reduced
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
