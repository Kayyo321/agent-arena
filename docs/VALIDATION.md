# Validation evidence

Checked locally on macOS arm64 with Go 1.27.2 and Git 2.50.1 (Apple Git-155) on 2026-10-15. Readiness, contract fixtures and actual
inference are different evidence. The original validation snapshot was local; subsequent changes may be published through pull requests.

## Local exit checks

| Area | Evidence |
| --- | --- |
| M0/M1 contracts and authoring | All repository fixtures validate; generation is reproducible. CLI create/show/validate/delete and stale-profile conflict checks pass. Setup tests cover creating Git/control configuration, bare empty-directory entry, explicit runtime requirements, cancellation, saved-control reuse and preserving staged files. Embedded-interview fixtures exercise questions, native provider question callbacks, reviewed draft saving, attached cancellation and versioned audited handoffs. Graph validation covers reachability, DAGs, teams, routers, on-call isolation, profile paths and explicit runtimes. |
| M2 journal and context | Reopening state, detecting a corrupt mirror, preserving/repairing it, redacting secrets, context revision/archive/restore and workflow isolation pass. Reduced-retention decisions are compacted after use. |
| M3 packets and orchestration | Fixture tests cover unanimous affirmation, no-consensus, unavailable members, writing-team artifacts/read-only composition, durable on-call caps, coworker control-slot/wave additions, peer permissions, MCP scope, routers and the local HTTP/tool contracts. They do not establish real provider readiness. |
| M4 Git | Real temporary repositories exercise independent writer proposals, review, integration, preserving later commits, rollback and rollback-of-rollback, dirty/index/untracked checkpoints, prerequisite snapshots and isolated checkpoint restoration. |
| M5 terminal and supervision | Width/height checks cover 140x40 down to 8x4. Bare-command tests cover sole-arena auto-open, explicit multiple-arena choice, cancellation, invalid/empty projects, subdirectory/nested-project discovery, no invented runs and attached-engine interruption (fixture invoker). Real PTY startup checks cover single/multiple/empty projects, prompt readiness refusal, resize and clean exit using metadata fixtures only. Earlier PTY checks cover init/create, Git proposal/patch pane, Git detail and resize (fixture events; no inference). Packet tests distinguish pending, delivered and failed states. Native sandbox execution checks private scratch space, read-only refusal and prevention of neighboring-tree writes. |
| M6 skills and compatibility | Project/user target code uses one embedded canonical skill. Installation tests cover host locations, modified-file refusal and backed-up replacement. Installed CLI versions and compatibility drift are reported separately from inference. |

`make check` (vet and race-enabled tests) passes. The standalone release builder cross-builds all four target executables and emits a corresponding source archive, third-party notices and SHA-256 checksums. All archive checksums, extraction, packaged file inventories and host-executable smoke checks pass. The extracted source archive rebuilds, passes generated-contract checks and installs successfully into a temporary prefix. Dependency checking with the
Go 1.27.2-built `govulncheck` found **zero reachable vulnerabilities** after upgrading
networking/auth transitives. It reported one advisory in a required module with no
imported affected package; this is not a claim that every dependency is advisory-free.

The compact dotted purple title is checked for top-right placement, gradient colors,
absence of filled blocks/background/shadows, matching color/monochrome geometry,
monochrome rendering, size limits, compact/hide preferences and non-Unicode fallback.
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
- **Claude Code:** installed `2.1.224`; supported flags inspected. No approved API/cloud
  route was available for inference. Subscription OAuth is refused for Arena.
- **Gemini:** official Google GenAI SDK v1.75.0 builds. Live API-key/Vertex access has not
  been exercised. Personal Gemini CLI OAuth is excluded.
- **Local:** HTTP streaming, discovery and mediated tool contracts have fixture coverage.
  A real backend/model has not been selected or exercised.

The owner authorized Codex `gpt-6.1-sol` / `high` and will supply a second approved
route. **M3 and the adapter-portability part of M6 remain pending that verification.**

## Reproduce

```sh
make generate
make check
make build
make dist
(cd dist && shasum -a 256 -c SHA256SUMS)
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
remain after a crash. Audit export/import, native Windows, and skill-only host
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
