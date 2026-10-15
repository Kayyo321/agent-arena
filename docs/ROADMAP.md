# Roadmap

This roadmap sequences a usable local product. Milestones are outcomes with exit checks; exact implementation choices can evolve while preserving the documented contracts.

## M0 — Product and protocol foundation

**Status: prepared in this repository.**

- Product brief, architecture, interoperability contract, configuration schemas, examples, skill entry points, and audit/rollback policy.
- Keep CLI behavior clearly marked as planned until an implementation lands.

**Exit:** contributors can implement the first CLI without inventing configuration, handoff, or rollback semantics.

## M1 — CLI skeleton and workflow authoring

- Select the implementation language and TUI framework.
- Implement `arena init`, `arena workflow create/list/show/edit/delete`, and `arena workflow validate`.
- Have the guided builder create the workflow file and all role profiles from schema-backed forms.
- Validate IDs, profile paths, provider settings, graph reachability, delegation acyclicity, and communication permissions.
- Add a dry-run rendering of the graph and policy before save.

**Exit:** a developer can create, inspect, edit, and delete workflows without hand-writing generated JSON; invalid graphs are rejected with actionable messages.

## M2 — Event journal and local run model

- Define event and message storage implementations with atomic writes, versioning, recovery, and retention controls.
- Implement run/agent state machines and one-process locking for concurrent CLI sessions.
- Implement history, pause, resume, cancel, and event inspection before starting providers.
- Add secret redaction and safe diagnostics.

**Exit:** simulated adapter events render as a live graph and survive process interruption without losing or corrupting history.

## M3 — Provider adapters and handoffs

- Implement capability-based adapter interface and readiness checks.
- Add Codex CLI and Claude Code adapters through their current supported local interfaces.
- Translate to/from the common envelope; preserve raw provider options and stream status/output.
- Implement question, result, error, cancellation, retry, timeout, and peer messaging.
- Gate provider-specific account use on reviewed official integration and terms requirements.

**Exit:** one user-authored task can pass through a multi-provider workflow, with clear preflight errors and a complete handoff history.

## M4 — Isolated work and reversible integration

- Create worktrees and branches per writing task; add pinned read-only workspace support.
- Capture baseline and checkpoints including uncommitted/untracked files.
- Build review queue with patch view, conflict detection, and human-approved integration.
- Implement change history and non-destructive rollback.

**Exit:** two workers can propose independent changes, one can be accepted, and either proposal or accepted change can be recovered or rolled back without removing later work.

## M5 — Live terminal graph and operational polish

- Render hierarchy, communication edges, agent state, current task, provider/model, worktree, and pending decisions.
- Add keyboard navigation, event detail, and accessible non-TUI output for logs and automation.
- Add usage/concurrency controls, process cleanup, crash recovery, and diagnostics.
- Package installation and document supported operating systems and provider CLI versions.

**Exit:** a developer can operate and inspect a complete run from the live graph and recover after interruption.

## M6 — Skill distribution and additional adapters

- Implement `arena skill install` for supported project/user skill locations, with Codex and Claude Code as initial targets.
- Add adapter compatibility manifests and version drift reporting.
- Add additional providers only when a supported local integration and authentication path exists.
- Consider explicit audit export/import and team policies after local state and privacy behavior are stable.

**Exit:** the same workflow and handoff format work across the documented adapter set, and the skill installer can place/update/remove skills without duplicating the source.

## Decisions to resolve during M1

- Implementation language, TUI framework, packaging, and minimum runtime.
- Whether configuration files use JSON Schema validation directly or generated typed models.
- Cross-platform worktree locking and process supervision behavior.
- Whether the first OpenAI adapter uses Codex CLI directly, a supported partner integration, or both; the answer must follow current official documentation and terms.
