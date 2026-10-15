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
│       └── reviewer.agent.json
└── .state/                  # generated per-run data; ignored by Git
```

Users create and change workflows through Arena's guided builder. JSON is public, versioned, reviewable configuration; `.state` is mutable run data. The CLI validates the file and any referenced profiles before starting a provider process.

## Workflow file

The workflow file defines the graph and run policy. Each agent node points to one role profile and specifies the provider runtime for that placement. See [`../schemas/workflow.schema.json`](../schemas/workflow.schema.json). The common cross-provider message envelope is defined in [`../schemas/message-envelope.schema.json`](../schemas/message-envelope.schema.json).

Core fields:

- `schemaVersion`: schema version, independent of the CLI version.
- `id`, `name`, and optional `description`: stable workflow identity and display text.
- `headAgent`: the node that receives the user's task and coordinates completion.
- `agents`: nodes with profile path, integer level, and provider runtime selector.
- `relationships`: explicit delegation and communication links.
- `settings`: concurrency, worktree, audit, and integration policy.

Levels group nodes visually. Relationships define actual task flow and message permissions. The delegation graph must be acyclic. Every referenced agent and profile must exist exactly once.

## Agent profile

Profiles live under `.arena/.agents/<workflow-id>/` and describe the role's objective, instructions, capabilities, and constraints. They do not contain credentials. See [`../schemas/agent.schema.json`](../schemas/agent.schema.json).

The CLI may offer built-in roles and custom role prompts. Editing a profile through the TUI should create a normal reviewable file change.

## Provider runtime values

`provider` chooses an installed adapter. `model` and optional `reasoningEffort` are passed through according to that adapter's documented interface. Arena does not maintain a fictional universal effort scale. Adapter metadata describes which options are supported and what they mean locally.

## Example fixture

[`../examples/project/.arena/`](../examples/project/.arena/) illustrates a coordinator with an implementer and reviewer, explicit coordination links, isolated worktrees, local audit data, and review before integration. It is a format example, not a required hand-authored setup path.

## Compatibility rules

- The CLI must reject unsupported `schemaVersion` values before making changes.
- Adding optional fields is backward-compatible if old readers preserve unknown fields when rewriting.
- Removing or changing a field's meaning requires a schema version change and migration path.
- Migrations create a checkpoint and report a diff before replacing a definition.
- Agent profile references must stay within `.arena/.agents/`; path traversal and external absolute paths are rejected.
- The validator checks unique IDs, valid references, supported relationships, graph acyclicity for delegation links, reachability from the head, and sensible level ordering.
