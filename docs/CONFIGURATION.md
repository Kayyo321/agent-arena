# Configuration format

## CLI-owned layout

For a workflow called `code-review`, the CLI creates:

```text
.arena/
├── code-review.arena.json
├── .agents/
│   └── code-review/
│       ├── orchestrator.agent.json
│       ├── implementer.agent.json
│       ├── reviewer.agent.json
│       └── security-reviewer.agent.json
└── .state/                  # generated per-run data; ignored by Git
```

Users create and change workflows through Arena's guided builder. JSON is public, versioned, reviewable configuration; `.state` is mutable run data. The CLI validates the file and any referenced profiles before starting a provider process.

## Workflow file

The workflow file defines the graph and run policy. Each agent node points to one role profile and specifies the provider runtime for that placement. `teams` and `onCall` are optional; an omitted field means an empty list. An omitted agent `activation` means `path`, preserving the original path-agent shape. See [`../schemas/workflow.schema.json`](../schemas/workflow.schema.json). The common cross-provider message envelope is defined in [`../schemas/message-envelope.schema.json`](../schemas/message-envelope.schema.json).

Core fields:

- `schemaVersion`: schema version, independent of the CLI version.
- `id`, `name`, and optional `description`: stable workflow identity and display text.
- `headAgent`: the node that receives the user's task and coordinates completion.
- `agents`: path or on-call nodes with profile path and provider runtime selector. Path nodes have an integer level; on-call nodes stay outside the normal graph.
- `teams`: composite nodes that name their member agents, facilitator, consensus policy, and output contract.
- `onCall`: trigger rules for on-call agents. Rules can match a supported event or allow manual invocation.
- `relationships`: explicit delegation and communication links. A delegation target may be an agent or a team; communication links always connect agents.
- `settings`: concurrency, worktree, audit, and integration policy.

`settings.maxConcurrentAgents` limits simultaneously running provider processes. Team members count individually, as do the head and any active on-call agents.

Levels group path nodes visually. Relationships define ordinary task flow and peer message permissions. A team is addressed as one graph node; its members run inside the team boundary at their shared level. Team members cannot be the head or endpoints in ordinary relationships, so a member is never assigned both directly and through its team. The delegation graph must be acyclic. Agent and team IDs share one unique namespace. Every referenced agent, team, and profile must exist exactly once. On-call agents cannot appear in relationships or teams.

## Teams

A team has at least two distinct path agents, one of whom is its facilitator. A given agent belongs to at most one team in a workflow. Team members receive the same task scope, then deliberate for a bounded number of rounds. In schema version 1, consensus is unanimous: every member must affirm the same configured verdict. If they disagree or a member cannot return a position, the team returns one packet with `outcome: "no-consensus"`, `verdict: null`, member positions, dissent, and a next action.

Every team output uses `format: "arena.team-verdict.v1"`. Its stable fields are `teamId`, `outcome`, `verdict`, `summary`, `rationale`, `evidence`, `memberPositions`, `dissent`, `rounds`, and `nextAction`. The workflow declares allowed verdict labels so downstream agents can interpret the packet consistently. A team is not complete until it emits this packet, even when the outcome is no-consensus.

## On-call agents

An on-call agent is an agent node with `activation: "on-call"` and a matching entry in the workflow's `onCall` rules. It has no graph level and no ordinary relationship edges. Trigger rules can respond to a team no-consensus result, an agent failure, a validation failure, changed paths, or manual invocation. Each rule declares a maximum invocation count per run. Automatic triggers include the triggering event ID and scoped context in the task handoff; manual calls are recorded with the invoking user. A capped, duplicate, or unmatched event is still visible in the run journal.

The first authoring flow should ask which cases deserve a specialist call and summarize the trigger before saving. It should not silently add on-call agents to the normal delegation path.

## Agent profile

Profiles live under `.arena/.agents/<workflow-id>/` and describe the role's objective, instructions, capabilities, and constraints. They do not contain credentials. See [`../schemas/agent.schema.json`](../schemas/agent.schema.json).

The CLI may offer built-in roles and custom role prompts. Editing a profile through the TUI should create a normal reviewable file change.

## Provider runtime values

`provider` chooses an installed adapter. `model` and optional `reasoningEffort` are passed through according to that adapter's documented interface. Arena does not maintain a fictional universal effort scale. Adapter metadata describes which options are supported and what they mean locally.

## Example fixture

[`../examples/project/.arena/`](../examples/project/.arena/) illustrates an implementer, a cross-provider review team with a structured verdict, and an on-call specialist for unresolved consensus. It is a format example, not a required hand-authored setup path.

## Compatibility rules

- The CLI must reject unsupported `schemaVersion` values before making changes.
- Adding optional fields is backward-compatible if old readers preserve unknown fields when rewriting.
- Removing or changing a field's meaning requires a schema version change and migration path.
- Migrations create a checkpoint and report a diff before replacing a definition.
- Agent profile references must stay within `.arena/.agents/`; path traversal and external absolute paths are rejected.
- The validator checks IDs are unique across agents and teams, valid references, supported relationships, graph acyclicity for delegation links, reachability of every path agent directly or through a reachable team, and sensible level ordering. On-call agents are deliberately outside this reachability check.
- Teams have at least two distinct path-agent members at one shared level, exactly one member facilitator, and a configured verdict contract; an agent belongs to at most one team. Team members cannot be the head or relationship endpoints.
- On-call entries reference on-call agents exactly once, include at least one valid trigger, and enforce the per-run invocation cap. On-call agents cannot be the head, team members, or relationship endpoints.
- A delegation edge may target a team ID. Communication edges may target only path-agent IDs. Internal team deliberation does not require normal graph edges.
