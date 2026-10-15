# Roadmap

This roadmap sequences a usable local product. Milestones are outcomes with exit checks; exact implementation choices can evolve while preserving the documented contracts.

## M0 — Product and protocol foundation

**Status: prepared in this repository.**

- Product brief, architecture, interoperability contract (including team verdicts and on-call triggers), configuration schemas, examples, skill entry points, and audit/rollback policy.
- Shared-context lifecycle and terminal layout contracts, including cross-provider context packets and the required-provider status filter.
- Keep CLI behavior clearly marked as planned until an implementation lands.

**Exit:** contributors can implement the first CLI without inventing configuration, handoff, or rollback semantics.

## M1 — CLI skeleton and workflow authoring

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

- Define event and message storage implementations with atomic writes, versioning, recovery, and retention controls.
- Implement run/agent state machines and one-process locking for concurrent CLI sessions.
- Implement history, pause, resume, cancel, and event inspection before starting providers.
- Add secret redaction and safe diagnostics.
- Implement the shared-context manager, provenance, stale marking, per-workflow access, policy checks, and recoverable entry versions.

**Exit:** simulated adapter events render as a live graph and survive process interruption without losing or corrupting history.

## M3 — Provider adapters and handoffs

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

- Create worktrees and branches per writing task; add pinned read-only workspace support.
- Capture baseline and checkpoints including uncommitted/untracked files.
- Build review queue with patch view, conflict detection, and human-approved integration.
- Implement change history and non-destructive rollback.

**Exit:** two workers can propose independent changes, one can be accepted, and either proposal or accepted change can be recovered or rolled back without removing later work.

## M5 — Live terminal graph and operational polish

- Render the full A2rena layout: ASCII wordmark upper left, colored vertical Git tree upper right, agent graph cards and authority/peer arrows, packet markers, required-provider status row, and full-width `> memento mori` prompt.
- Fit the graph to the terminal, with compact/list navigation for constrained screens and labels for every state independent of color.
- Add keyboard navigation, event detail, and accessible non-TUI output for logs and automation.
- Add usage/concurrency controls, process cleanup, crash recovery, and diagnostics.
- Package installation and document supported operating systems and provider CLI versions.

**Exit:** a developer can operate and inspect a complete run, team verdicts, and on-call events from the live graph and recover after interruption.

## M6 — Skill distribution and additional adapters

- Extend the bootstrap/skill installer to more supported project/user skill locations beyond the initial Codex, Claude Code, and Gemini CLI targets.
- Add adapter compatibility manifests and version drift reporting.
- Add additional providers only when a supported local integration and authentication path exists.
- Consider explicit audit export/import and team policies after local state and privacy behavior are stable.

**Exit:** the same workflow and handoff format work across the documented adapter set, and the skill installer can place/update/remove skills without duplicating the source.

## Decisions to resolve during M1

- Implementation language, TUI framework, packaging, and minimum runtime.
- Whether configuration files use JSON Schema validation directly or generated typed models.
- Cross-platform worktree locking and process supervision behavior.
- Whether the first OpenAI adapter uses Codex CLI directly, a supported partner integration, or both; the answer must follow current official documentation and terms.
