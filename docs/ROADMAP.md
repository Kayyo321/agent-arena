# Roadmap

This roadmap sequences a usable local product. Milestones are outcomes with exit checks; exact implementation choices can evolve while preserving the documented contracts.

## Final TODOs

The last items before release, in order. Item 4 closes the list.

1. **`/consult` the orchestrator from the live view.** Ask the permanent `arena-orchestrator` about the loaded arena or run, for example why a run stalled, how to raise the grade, or which runtime fits a role. It runs on the project default runtime, does not start a run or change configuration, and any edit it proposes goes through the normal draft preview and `/save`.
2. **Spinning wordmark.** Each character of the `A2rena` wordmark spins in place, one at a time: `A`, then `2`, `r`, `e`, `n`, and the final `a`, then restarting at `A`. The wordmark's position and width never change, only the spinning glyph's frames. Keep the static wordmark for the text fallback, no-color, reduced-motion and non-TTY output (`internal/arena/wordmark.go`).
3. **`/walkthrough` in arena setup.** For users who arrive without a design: the orchestrator asks about the project, its goals and pain points, proposes two or three arena ideas with their roles, providers, rough cost and grade, and lets the user pick or combine one before the normal interview drafts it. Provider, model and effort stay the user's choice unless they delegate it.
4. **Production readiness sweep, approved by Fable and Astra.** Run a final sweep as an arena whose review team is Claude Fable 5.1 (`claude-code`, `claude-fable-5-1`) and Codex GPT-6-Astra (`codex`, `gpt-6-astra`) with unanimous consensus. Cover security (fences, credentials, share codes, blanket-approval modes), correctness, documentation against behavior, packaging, and the pending M3 and M6 live exits. Release only when both reviewers return `Approved`; `no-consensus` stays unresolved.

## M0 — Product and protocol foundation

**Status: complete.** Product contracts, schemas, examples and skill entry points are present; generated validators and fixture tests now exercise them. Implementation status is tracked below and in [validation evidence](VALIDATION.md).

- Product brief, architecture, interoperability contract (including team verdicts and on-call triggers), configuration schemas, examples, skill entry points, and audit/rollback policy.
- Shared-context lifecycle and terminal layout contracts, including cross-provider context packets and the required-provider status filter.
- Keep implemented behavior and unverified provider capabilities clearly distinguished.

**Exit:** contributors can implement the first CLI without inventing configuration, handoff, or rollback semantics.

## M1 — CLI skeleton and workflow authoring

**Status: implemented; local authoring exit checks pass.** `arena setup` creates a project and starts its default orchestrator interview inside Arena; bare `arena` enters setup when no workflows exist. Drafts are previewed and applied only on `/save`, without exposing a provider's native terminal UI. The guided builder, schema-backed automation path, configuration transactions, runtime inquiry, context commands and permanent control profile are implemented. M1 choices are recorded in [ADR 0001](adr/0001-implementation-foundation.md).

- Select the implementation language and TUI framework.
- Implement `arena init`, `arena workflow create/list/show/edit/delete`, and `arena workflow validate`.
- Implement `arena status` with adapter readiness and truthful model/effort discovery for required providers only; bootstrap `arena init` through the selected provider's installed skill.
- Implement project settings show/edit and validate the permanent orchestrator runtime, approval policies, audit controls, and bounded limits.
- Create the permanent on-call Arena orchestrator profile and route structured coworker requests through its configured approval policy.
- Add the reviewer result contract with stable verdict outcomes.
- Have the guided builder create the workflow file and all role profiles from schema-backed forms.
- Author decision-router blocks with structured packet fields, exact-match branches, explicit fallback behavior, and visible graph edges.
- Author teams with member selection, unanimous-deliberation policy, a bounded round count, and allowed verdict labels.
- Author on-call agents with event/manual triggers, scoped context, and per-run invocation limits.
- Validate IDs, profile paths, provider settings, graph reachability, delegation acyclicity, team membership, on-call isolation, and communication permissions.
- Validate decision-router branch targets, unique match values, fallback targets, and all route edges as part of graph reachability/acyclicity checks.
- Require explicit provider/model/effort choices where supported; the setup agent asks the user when any required choice is missing.
- Add a dry-run rendering of the graph and policy before save.
- Add per-workflow shared-context commands and create the configured context store without requiring hand-authored entries.

**Exit:** a developer can create, inspect, edit, and delete workflows, including teams and on-call rules, without hand-writing generated JSON; invalid graphs are rejected with actionable messages.

## M2 — Event journal and local run model

**Status: implemented; local recovery exit checks pass.** Atomic/hash-chained events, mirror repair, state transitions, process-owner locks, retention/redaction and context version recovery are covered by tests. Normal adapter fixtures are labelled as simulated.

- Define event and message storage implementations with atomic writes, versioning, recovery, and retention controls.
- Implement run/agent state machines and one-process locking for concurrent CLI sessions.
- Implement history, pause, resume, cancel, and event inspection before starting providers.
- Add secret redaction and safe diagnostics.
- Implement the shared-context manager, provenance, stale marking, per-workflow access, policy checks, and recoverable entry versions.

**Exit:** simulated adapter events render as a live graph and survive process interruption without losing or corrupting history.

## M3 — Provider adapters and handoffs

**Status: implementation present; live multi-provider exit check PENDING.** Real Codex `gpt-6.1-sol` / `high` read-only inference passed. Claude API/cloud, Gemini API-key/Vertex and local contracts are implemented with fixture/interface coverage. The owner will supply a second approved route; do not mark this milestone complete before it is exercised. An opt-in multi-provider team workflow test is prepared.

- Implement capability-based adapter interface and readiness checks.
- Add Codex CLI and Claude Code adapters through their current supported local interfaces.
- Add a Gemini integration only through a provider-approved API-key or Vertex AI path; never reuse Gemini CLI personal OAuth. Verify current terms and adapter capabilities before advertising it as ready.
- Add a same-machine local inference adapter for supported backends, with model discovery and backend capability checks.
- Translate to/from the common envelope; preserve raw provider options and stream status/output.
- Run team rounds across provider adapters, collect explicit member affirmations, and emit one `arena.team-verdict.v1` result or explicit no-consensus packet.
- Route configured on-call events with deduplication, context scoping, and invocation caps.
- Implement question, result, review-result, team-result, coworker-request, error, cancellation, retry, timeout, and peer messaging.
- Implement `context-proposal` and `context-result` packets so every provider can propose knowledge through one protocol.
- Gate provider-specific account use on reviewed official integration and terms requirements.

**Exit:** one user-authored task can pass through a multi-provider workflow, teams return one auditable consensus/no-consensus packet, configured on-call triggers route safely, and the run has a complete handoff history.

## M4 — Isolated work and reversible integration

**Status: implemented; real temporary-Git-repository exit checks pass.** Two independent writers, dependency snapshots, review/integration, later user commits, reversible rollback and staged/untracked checkpoint restoration are tested without touching the development checkout. This is Git validation, not evidence of live provider writing ability.

- Create worktrees and branches per writing task; add pinned read-only workspace support.
- Capture baseline and checkpoints including uncommitted/untracked files.
- Build review queue with patch view, conflict detection, and human-approved integration.
- Implement change history and non-destructive rollback.

**Exit:** two workers can propose independent changes, one can be accepted, and either proposal or accepted change can be recovered or rolled back without removing later work.

## M5 — Live terminal graph and operational polish

**Status: implemented; local terminal and packaging exit checks pass.** Bare `arena` opens a sole workflow or a chooser, then the idle graph with explicit task start. Embedded chat, slash commands, live agent mini-windows and scoped context views are implemented. Rounded terminal borders, a visible multiline composer, cursor/word/line editing, Shift+Return, clipboard image paste, run-scoped attachments and session-local message history are implemented. Native image delivery is currently Codex-only; see validation evidence for platform and provider limits. The adaptive UI, acknowledged packet lifecycle, Git ownership colors, run controls, attached process cleanup and standalone release builder are implemented. PTY interaction/resize and standalone cross-build/archive checks pass; see [validation evidence](VALIDATION.md) for platform limits.

- Render the full A2rena layout: status band led by the compact purple-gradient dotted wordmark with run vitals and the Git tree, agent graph cards and authority/peer arrows, packet markers, required-provider status row, and full-width `> memento mori` prompt.
- Fit the graph to the terminal, with compact/list navigation for constrained screens and labels for every state independent of color.
- Add keyboard navigation, event detail, and accessible non-TUI output for logs and automation.
- Add usage/concurrency controls, process cleanup, crash recovery, and diagnostics.
- Package installation and document supported operating systems and provider CLI versions.

**Exit:** a developer can operate and inspect a complete run, team verdicts, and on-call events from the live graph and recover after interruption.

## M6 — Skill distribution and additional adapters

**Status: skill/compatibility implementation complete; adapter portability exit PENDING M3 live verification.** Project/user targets include Codex, Claude, Gemini, Copilot and OpenCode, using one embedded skill source with modified-file protection. A versioned compatibility manifest reports drift. No unsupported execution adapter is advertised; audit export/import and expanded team policies remain deferred considerations.

- Extend the bootstrap/skill installer to more supported project/user skill locations beyond the initial Codex, Claude Code, and Gemini CLI targets.
- Add adapter compatibility manifests and version drift reporting.
- Add additional providers only when a supported local integration and authentication path exists.
- Consider explicit audit export/import and team policies after local state and privacy behavior are stable.

**Exit:** the same workflow and handoff format work across the documented adapter set, and the skill installer can place/update/remove skills without duplicating the source.

## M1 decisions (resolved)

- Go, Bubble Tea, standalone executables; no end-user language runtime. Build toolchain in `go.mod`.
- Repository-generated typed Go models and lightweight validators from the public JSON Schemas.
- Portable atomic lock directories inside `.arena/`; attached process-group supervision; macOS/Linux including WSL2.
- Direct installed Codex CLI through its documented app-server interface; no partner identity implementation. See [ADR 0001](adr/0001-implementation-foundation.md) and [reviewed provider references](PROVIDERS.md).

## Graph background shaders

Implemented: per-arena settings and adjacent HLSL scripts, a bounded CPU HLSL
profile, ASCII conversion with optional hue, state-rich inputs (counts, event ages,
focus and smoothed envelopes), reactive edge-fading default dots, protected foreground spans,
and default-model Background Shader Creation through the permanent orchestrator.
Real Codex default-model generation and animated PTY playback passed; see
[validation evidence](VALIDATION.md). Full HLSL/GPU compatibility and other-provider
shader generation are not verified. See [the profile and command contract](BACKGROUND-SHADERS.md).
