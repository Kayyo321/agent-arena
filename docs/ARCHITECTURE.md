# Architecture

## Local control plane

Arena is the control plane around provider-owned agents. In the MVP it runs on the developer's machine and launches each supported provider CLI as a child process in a controlled workspace. Authentication remains in the provider CLI's own local sign-in store.

```mermaid
flowchart LR
  User[Developer] --> TUI[arena CLI and live graph]
  TUI --> Scheduler[Workflow scheduler]
  Scheduler --> Router[Handoff and event router]
  Router --> Codex[Codex CLI adapter]
  Router --> Claude[Claude Code adapter]
  Router --> Custom[Future adapter interface]
  Codex --> WT1[Agent worktree]
  Claude --> WT2[Agent worktree]
  WT1 --> Queue[Review and merge queue]
  WT2 --> Queue
  Scheduler --> Journal[Local event journal]
  Queue --> Journal
```

## Components

- **CLI/TUI:** Creates and validates workflow graphs, starts and pauses runs, renders agent states and links, and provides history and rollback commands.
- **Workflow loader:** Loads `.arena/<id>.arena.json` and its referenced profiles, validates schema versions, team membership, on-call triggers, and graph invariants, and reports actionable errors.
- **Scheduler:** Starts eligible path nodes and composite teams, respects level/dependency rules and concurrency limits, runs team consensus rounds, matches on-call events, handles retries and cancellation, and tracks run state.
- **Provider adapters:** Translate a normalized task envelope into the provider CLI's supported invocation and translate its result into a normalized completion. Each adapter declares model selection, streaming, permissions, cancellation, and authentication readiness capabilities.
- **Message router:** Persists and delivers task, result, question, and peer-message envelopes. Agents communicate through Arena; no cross-provider session sharing is assumed.
- **Workspace manager:** Creates one Git worktree per writing agent/task, tracks branch and base commit, captures checkpoints, and prepares changes for integration.
- **Integration manager:** Reviews changes in an Arena-owned queue, checks for conflicts and stale bases, and applies a selected change according to workflow policy.
- **Event journal:** Stores ordered state events and (according to retention settings) message bodies, process output, diffs, and checkpoint references under `.arena/.state/`.

## Graph execution model

- **Level** controls the vertical grouping in the graph and the order in which the user expects work to progress. It is presentation and scheduling metadata, not a substitute for a relationship.
- **Delegation links** carry task assignments from a coordinator to workers or to a team group node. The delegation graph must be acyclic.
- **Communication links** grant explicit message routes. They may be one-way or two-way, and can connect agents at the same level.
- **Return results** go back to the delegating agent by default. A worker may message another worker only when a communication edge permits it.
- **Teams** are composite execution nodes with at least two member agents and one facilitator. Arena gives each member the same scoped task, records independent positions, runs bounded deliberation rounds, and asks each member to affirm a final verdict. Only a shared final verdict is `consensus`; otherwise Arena returns one `no-consensus` team result with the positions and dissent preserved. An unavailable member prevents consensus. It never labels a majority vote as consensus. Reaching a team node makes its members reachable for graph validation; members cannot also be reached as standalone path targets.
- **On-call agents** are excluded from ordinary delegation and communication edges. Their trigger rules subscribe to supported run events or a developer's manual invocation. Arena starts them only when a rule matches, supplies the triggering event and allowed context, then routes their result to the event's originating task or the head agent. On-call invocations count against a per-run cap and use the same worktree, permissions, and audit rules as other agents.
- **Read-only agents** can share the caller's base revision or a read-only snapshot. Any agent that writes gets its own worktree.

The graph renders each team as a single connected group node with its member agents nested inside or shown on expansion. On-call agents appear in a separate rail, with trigger badges and event links shown only when they fire. Neither member containment nor an event trigger is drawn as a normal delegation arrow.

## Team coordinator

The team coordinator owns the deliberation state machine. Every round and member position is an auditable event. Member messages use the common envelope and are scoped to the team's task, not to an assumed shared provider session. The facilitator is a member of the team and composes the final `arena.team-verdict.v1` packet only after all members affirm the same configured verdict. If members still disagree at `maxRounds`, the facilitator emits a packet with `outcome: "no-consensus"`, a null verdict, the final positions, and an explicit next action. The caller must handle both outcomes.

Team settings define the available verdict labels and round limit. The first protocol version uses unanimous agreement; quorum or majority modes would need a separately named outcome and must not be presented as consensus.

## On-call event router

The event router matches the run journal against a workflow's on-call rules. Initial event triggers are team no-consensus, agent failure, validation failure, and changed paths matching configured patterns; the developer can also invoke an on-call agent manually. An event-triggered call receives the event ID, source, concise task context, and file/workspace policy. Arena deduplicates delivery by event and agent, enforces `maxInvocationsPerRun`, and records unmatched or capped triggers so the live graph explains why a specialist did or did not run.

On-call nodes have no path level and cannot be a head agent, team member, or endpoint in ordinary relationships. They may be writing agents; writable calls receive a dedicated worktree and still enter the integration queue.

The live graph is a view of scheduler and journal events. It must not be a second source of truth.

## Adapter contract

Every adapter exposes a capability description before launch:

```text
provider id and adapter version
readiness check that does not reveal credentials
supported invocation mode and streaming format
model selector behavior
provider-specific effort/reasoning options
permission mode and workspace boundary behavior
cancel/timeout behavior
normalized result and failure mapping
```

The scheduler may only request capabilities the adapter declares. If an adapter cannot enforce a required workspace or permission policy, Arena must refuse that workflow configuration or explain the limitation before launch.

## State and storage

```text
.arena/
  <workflow-id>.arena.json
  .agents/<workflow-id>/<agent-id>.agent.json
  .state/<run-id>/events.ndjson
  .state/<run-id>/messages/<message-id>.json
  .state/<run-id>/checkpoints/...
```

Definitions are portable and may be committed. Run data is local, ignored by Git by default, and excluded from remote synchronization unless a future explicit feature provides a reviewed export. Keep provider secrets out of all these files. See [Configuration format](CONFIGURATION.md) and [Audit and rollback](AUDIT-AND-ROLLBACK.md).

## Failure handling

- A provider process crash marks the node failed and records exit information; it does not silently reassign the task.
- Retry requires an idempotency decision from the scheduler. A retry is a new attempt linked to the prior event history.
- A user cancellation stops new work first, then requests cancellation from active adapters, checkpoints writable worktrees, and records any process that did not stop cleanly.
- A missing adapter feature, invalid auth state, or unsupported model selector is surfaced before task launch.
- A corrupt or unsupported journal is preserved and reported; Arena does not silently truncate it.
