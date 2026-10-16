# Repository guidance for coding agents

This repository implements Agent Arena, a local-first coordinator for provider coding CLIs and approved model integrations. Build with `make build` and check with `make check`. Keep implemented behavior distinct from contract-fixture coverage and real provider verification; consult `docs/VALIDATION.md` and `docs/ROADMAP.md` before claiming an adapter is verified.

## Product invariants

- Arena coordinates provider CLIs through documented, provider-approved interfaces. Do not design credential scraping, password collection, token copying, account rotation, subscription pooling, or limit bypasses.
- Workflow definitions and agent profiles are generated and validated by Arena. Treat `.arena/*.arena.json` and `.arena/.agents/` as a CLI-managed format; do not make user setup depend on hand-editing it.
- Keep the protocol provider-neutral while preserving provider-specific model IDs, effort controls, permissions, and capabilities as opaque values.
- Route communication through Arena's versioned message envelope. Do not assume two vendor agents share a session, memory, tool API, or filesystem.
- A team returns one versioned verdict packet. Call it consensus only after unanimous affirmation; preserve unresolved disagreement as no-consensus.
- On-call agents stay outside the ordinary graph and start only through a configured event or explicit user invocation. Record trigger, context, and invocation limits.
- The project-level `arena-orchestrator` is the permanent on-call control agent. Route coworker requests through the CLI and honor the configured inquiry policy; never fill missing provider/model/effort choices by guessing. An explicit user delegation ("you pick") is a choice: select from the provider's reported catalog and state the values chosen.
- The delegation graph is acyclic. The only back edge is an explicit loop route on a decision router, capped by `maxIterations` (1-5) and pointing at an ancestor of the router. Each loop iteration is journaled (`loop.iteration`, `loop.capped`), reruns its subgraph in new worktrees built on the previous changes, and keeps every earlier proposal; at the cap the router's fallback applies. A declared fan-out node is the only run-time multiplication of agent instances: its planner's items start at most `maxChildren` copies of its worker, never past the project's per-run `maxFanOutChildrenPerRun`, each in its own worktree, after the user approves the plan (or, for `auto-within-cap`, when it fits the cap). A plan over the cap is truncated only with the user's consent.
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
