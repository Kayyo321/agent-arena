# Repository guidance for coding agents

This repository is defining and building Agent Arena, a local-first coordinator for provider coding CLIs. The CLI is not implemented yet; do not describe planned commands as working features.

## Product invariants

- Arena coordinates provider CLIs through documented, provider-approved interfaces. Do not design credential scraping, password collection, token copying, account rotation, subscription pooling, or limit bypasses.
- Workflow definitions and agent profiles are generated and validated by Arena. Treat `.arena/*.arena.json` and `.arena/.agents/` as a CLI-managed format; do not make user setup depend on hand-editing it.
- Keep the protocol provider-neutral while preserving provider-specific model IDs, effort controls, permissions, and capabilities as opaque values.
- Route communication through Arena's versioned message envelope. Do not assume two vendor agents share a session, memory, tool API, or filesystem.
- A team returns one versioned verdict packet. Call it consensus only after unanimous affirmation; preserve unresolved disagreement as no-consensus.
- On-call agents stay outside the ordinary graph and start only through a configured event or explicit user invocation. Record trigger, context, and invocation limits.
- The project-level `arena-orchestrator` is the permanent on-call control agent. Route coworker requests through the CLI and honor the configured inquiry policy; never fill missing provider/model/effort choices by guessing.
- Reviewers return the versioned review verdict contract. Keep outcome labels stable and make their meanings visible to callers.
- Keep persistent context scoped to one workflow. Require provenance and confidence, route agent mutations through the context manager, and make every accepted change recoverable.
- The live status row shows only providers required by the loaded arena. The graph, packet animation, and Git tree are projections of audited state and must adapt to terminal size.
- Give each writing worker an isolated worktree. Integrate through an auditable queue, and never silently discard user or agent changes.
- Persist a local event history for workflow state, messages, command outcomes, checkpoints, and code changes. Redact secrets and make retention visible.
- Make rollback create a reversible change, such as a revert commit or restored checkpoint. Do not implement rollback as an unrecorded destructive reset.

## Where to start

- Product scope: `docs/PRODUCT.md`
- Components and runtime: `docs/ARCHITECTURE.md`
- Message format and handoffs: `docs/INTEROPERABILITY.md`
- Shared context and terminal layout: `docs/SHARED-CONTEXT.md` and `docs/TERMINAL-UI.md`
- Configuration and schemas: `docs/CONFIGURATION.md` and `schemas/`
- Git isolation, audit, and rollback: `docs/AUDIT-AND-ROLLBACK.md`
- Milestones: `docs/ROADMAP.md`

Keep changes consistent with these documents. If implementation evidence reveals a mismatch, update the relevant contract and schema with the code.
