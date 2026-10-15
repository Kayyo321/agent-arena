# Architecture

## Local control plane

Arena is the control plane around provider-owned agents. In the MVP it runs on the developer's machine and launches supported provider CLIs in controlled workspaces, uses approved provider integrations, or connects to supported same-machine local inference servers. Authentication stays in the provider's supported CLI or credential store; Arena does not capture or persist credentials.

```mermaid
flowchart LR
  User[Developer] --> TUI[arena CLI and live graph]
  TUI --> Scheduler[Workflow scheduler]
  Scheduler --> Router[Handoff and event router]
  Scheduler --> Decisions[Decision-router blocks]
  Router --> Control[Permanent Arena orchestrator]
  Router --> Context[Shared context manager]
  Router --> Codex[Codex CLI adapter]
  Router --> Claude[Claude Code adapter]
  Router --> Gemini[Gemini adapter: API key or Vertex AI]
  Router --> Custom[Future adapter interface]
  Router --> Local[Local inference adapter]
  Codex --> WT1[Agent worktree]
  Claude --> WT2[Agent worktree]
  WT1 --> Queue[Review and merge queue]
  WT2 --> Queue
  Scheduler --> Journal[Local event journal]
  Queue --> Journal
  Context --> Journal
```

## Components

- **CLI/TUI:** Creates and validates workflow graphs, starts and pauses runs, renders agent states and links, and provides history and rollback commands.
- **Provider status:** Probes required adapters without exposing credentials, reports CLI/version and compatible authentication readiness, and lists models/effort values and skill discovery only when the adapter can verify them.
- **Bootstrap flow:** `arena init` installs the skill in the selected provider's documented project skill location, asks for the default provider/model/effort if missing, and launches a guided conversation through that provider CLI. The model uses CLI operations to create workflows and profiles. It asks the user for missing required choices instead of filling them with assumptions. If the selected provider cannot load the skill or use Arena's supported CLI operations, initialization explains the limitation and asks the user to choose a compatible option.
- **Permanent Arena orchestrator:** A project-level on-call control agent using the configured default model and `arena-orchestrator` skill. It is available across workflow runs to process structured coworker requests, inspect current workflow state, and propose validated CLI changes. The workflow's user-selected head agent remains separate.
- **Workflow loader:** Loads `.arena/<id>.arena.json` and its referenced profiles, validates schema versions, team membership, on-call triggers, and graph invariants, and reports actionable errors.
- **Scheduler:** Starts eligible path nodes and composite teams, respects level/dependency rules and concurrency limits, runs team consensus rounds, matches on-call events, handles retries and cancellation, and tracks run state.
- **Decision routers:** Evaluate a configured JSON Pointer against a structured packet and take one exact-match branch or the declared fallback. They are graph blocks with no model process, worktree, or provider identity.
- **Provider adapters:** Translate a normalized task envelope into the provider CLI or local inference backend's supported invocation and translate its result into a normalized completion. Each adapter declares model selection, streaming, permissions, cancellation, and readiness capabilities.
- **Local inference adapter:** Connects to a same-machine supported model backend, verifies the requested model, and exposes only the tool, streaming, and reasoning controls the backend actually supports.
- **Message router:** Persists and delivers task, result, question, and peer-message envelopes. Agents communicate through Arena; no cross-provider session sharing is assumed.
- **Shared context manager:** Serves durable per-workflow context entries across runs, accepts typed agent proposals, enforces project policy and size limits, and versions every mutation.
- **Terminal layout:** Renders the A2rena wordmark, required-provider status, live graph, packet activity, and a colored Git worktree tree while adapting to terminal dimensions.
- **Workspace manager:** Creates one Git worktree per writing agent/task, tracks branch and base commit, captures checkpoints, and prepares changes for integration.
- **Integration manager:** Reviews changes in an Arena-owned queue, checks for conflicts and stale bases, and applies a selected change according to workflow policy.
- **Event journal:** Stores ordered state events and (according to retention settings) message bodies, process output, diffs, and checkpoint references under `.arena/.state/`.

The permanent orchestrator occupies a project-level control slot in the on-call rail across every workflow. It is always available and wakes on a valid coworker request; it has no path edges. Arena displays its configured model and skill readiness. Agents submit coworker requests to the control plane; Arena applies the project `arena-subagent-modification-inquiry` policy before the orchestrator can add a profile or node. Accepted changes are a validated, reviewable config transaction with a journal entry and a reversible snapshot. `auto-accept` only applies to complete requests within project limits and supported adapter capabilities.

## Graph execution model

- **Level** controls the vertical grouping in the graph and the order in which the user expects work to progress. It is presentation and scheduling metadata, not a substitute for a relationship.
- **Delegation links** carry task assignments from a coordinator to workers or to a team group node. The delegation graph must be acyclic.
- **Communication links** grant explicit message routes. They may be one-way or two-way, and can connect agents at the same level.
- **Return results** go back to the delegating agent by default. A worker may message another worker only when a communication edge permits it.
- **Teams** are composite execution nodes with at least two member agents and one facilitator. Arena gives each member the same scoped task, records independent positions, runs bounded deliberation rounds, and asks each member to affirm a final verdict. Only a shared final verdict is `consensus`; otherwise Arena returns one `no-consensus` team result with the positions and dissent preserved. An unavailable member prevents consensus. It never labels a majority vote as consensus. Reaching a team node makes its members reachable for graph validation; members cannot also be reached as standalone path targets.
- **On-call agents** are excluded from ordinary delegation and communication edges. Their trigger rules subscribe to supported run events or a developer's manual invocation. Arena starts them only when a rule matches, supplies the triggering event and allowed context, then routes their result to the event's originating task or the head agent. On-call invocations count against a per-run cap and use the same worktree, permissions, and audit rules as other agents.
- **Decision routers** accept structured packets from incoming delegation edges. Each route compares one named payload field to an exact scalar value and points to an agent, team, or another router. An unmatched packet, missing field, or wrong kind follows an explicit `ask-user`, `stop`, or fallback-route action. Branch edges participate in reachability and acyclicity checks. Routers never interpret free-form prose or silently discard a packet.
- **Local model agents** are ordinary agent nodes with provider ID `local` and a selected backend such as Ollama, LM Studio, llama.cpp, vLLM, or a local OpenAI-compatible server. They use the same message router, concurrency limit, worktree, integration queue, context, and audit trail as other agents. The MVP local adapter targets loopback services on the developer's machine.
- **Read-only agents** can share the caller's base revision or a read-only snapshot. Any agent that writes gets its own worktree.

The graph renders each team as a single connected group node with its member agents nested inside or shown on expansion. On-call agents appear in a separate rail, with trigger badges and event links shown only when they fire. Neither member containment nor an event trigger is drawn as a normal delegation arrow.

## Team coordinator

The team coordinator owns the deliberation state machine. Every round and member position is an auditable event. Member messages use the common envelope and are scoped to the team's task, not to an assumed shared provider session. The facilitator is a member of the team and composes the final `arena.team-verdict.v1` packet only after all members affirm the same configured verdict. If members still disagree at `maxRounds`, the facilitator emits a packet with `outcome: "no-consensus"`, a null verdict, the final positions, and an explicit next action. The caller must handle both outcomes.

Team settings define the available verdict labels and round limit. The first protocol version uses unanimous agreement; quorum or majority modes would need a separately named outcome and must not be presented as consensus.

## On-call event router

The event router matches the run journal against a workflow's on-call rules. Initial event triggers are team no-consensus, agent failure, validation failure, and changed paths matching configured patterns; the developer can also invoke an on-call agent manually. An event-triggered call receives the event ID, source, concise task context, and file/workspace policy. Arena deduplicates delivery by event and agent, enforces `maxInvocationsPerRun`, and records unmatched or capped triggers so the live graph explains why a specialist did or did not run.

On-call nodes have no path level and cannot be a head agent, team member, or endpoint in ordinary relationships. They may be writing agents; writable calls receive a dedicated worktree and still enter the integration queue.

The live graph is a view of scheduler and journal events. It must not be a second source of truth.

The shared context manager is the only writer for agent context changes. It serializes proposals from separate worktrees, records provenance and policy decisions, and preserves prior versions for rollback. Each workflow's context is isolated by workflow ID; only the active workflow's relevant entries are offered to its agents. See [Shared context](SHARED-CONTEXT.md).

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
  settings.arena.json
  <workflow-id>.arena.json
  .agents/arena-system/arena-orchestrator.agent.json
  .agents/<workflow-id>/<agent-id>.agent.json
  context/<workflow-id>/entries/<entry-id>.context.json
  .state/<run-id>/events.ndjson
  .state/<run-id>/messages/<message-id>.json
  .state/<run-id>/checkpoints/...
  .state/context/<workflow-id>/entries/... # when configured local-only
```

Definitions, project settings, and profiles are portable and may be committed. Run data is local, ignored by Git by default, and excluded from remote synchronization unless a future explicit feature provides a reviewed export. Keep provider secrets out of all these files. See [Configuration format](CONFIGURATION.md) and [Audit and rollback](AUDIT-AND-ROLLBACK.md).

The terminal's Git tree shows active branches and worktrees for the loaded arena, with stable agent colors. The status row probes only providers used by that arena and its permanent control agent. The graph and packet animation are projections of journaled events; their layout and small-terminal behavior are defined in [Terminal interface](TERMINAL-UI.md).

## Failure handling

- A provider process crash marks the node failed and records exit information; it does not silently reassign the task.
- Retry requires an idempotency decision from the scheduler. A retry is a new attempt linked to the prior event history.
- A user cancellation stops new work first, then requests cancellation from active adapters, checkpoints writable worktrees, and records any process that did not stop cleanly.
- A missing adapter feature, invalid auth state, or unsupported model selector is surfaced before task launch.
- A corrupt or unsupported journal is preserved and reported; Arena does not silently truncate it.
