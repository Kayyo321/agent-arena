# Shared context

## Scope and purpose

Each workflow arena has a durable context store shared by the agents in that workflow. It carries useful facts, decisions, conventions, and findings from one run to later runs. It is separate from transient handoff context and the run event journal: messages remain in run history, while selected knowledge becomes a context entry only through an explicit user or agent action.

An agent may search and read the context for the workflow it is running in. The permanent `arena-orchestrator` may access that workflow's context while handling a request for it. Context is not implicitly shared between different workflows. Arena supplies relevant entries by ID and records the entry versions used in each run; it does not inject the entire store into every provider prompt. Relevance is word-based: an entry matches when it shares a meaningful word (three or more letters, common stop words ignored) with the query, title and tag matches rank above body matches, and newer entries win ties. Each task's objective is used as the query for up to ten entries in that agent's prompt; an empty search query lists every active entry.

## Storage

For `sharedContext.storage: "git-tracked"`, entries live under the project root:

```text
.arena/context/<workflow-id>/entries/<entry-id>.context.json
```

These are portable, reviewable project files. For `"local-only"`, the same entry format lives under `.arena/.state/context/<workflow-id>/entries/` and remains on that machine across sessions; it is ignored by Git and is not synced or exported automatically. Run `arena context` commands to inspect either store without hand-editing generated files.

Each entry follows [`../schemas/context-entry.schema.json`](../schemas/context-entry.schema.json). It has a concise title and body, tags, confidence (`verified`, `inferred`, or `unverified`), status, provenance, evidence references, and timestamps. Entries can be marked stale after the configured interval without verification; if an entry has never been verified, its age is measured from creation. Stale entries remain visible and usable with that label; Arena never silently deletes them.

## Agent writes

Agents submit typed `context-proposal` packets through the Arena context API. They do not write context files directly from their worktree. A proposal adds, revises, or requests archiving an entry. A revision creates a new entry ID whose `supersedes` field points to the prior entry; the previous entry remains recoverable. Restoring a version archives any other active version in the same lineage. The configured `agentWritePolicy` governs additions and revisions; `agentDeletePolicy` governs archive requests. An archive hides an entry from normal search but remains recoverable.

Every accepted mutation is serialized by the context manager, checked against entry/size limits, and recorded in the event journal with the actor, run/task, policy decision, entry ID, and before/after hashes. A rejected or user-pending proposal receives a `context-result` packet and does not change the store. Rollback restores a prior entry version or its active/archive state without erasing the audit event.

Agents should save information only when it is likely to help a later task in the same workflow. Entries should be short, actionable, and supported by evidence. Preserve uncertainty: mark an inference as `inferred` or `unverified`, and do not turn an agent's guess into a verified fact. Do not store credentials, secrets, private user information, hidden reasoning, or full transcripts. Arena's secret checks are best-effort; provider secrets must never be submitted in the first place.

## CLI surface

The implemented commands are:

```text
arena context list <workflow-id>
arena context search <workflow-id> <query>
arena context show <workflow-id> <entry-id>
arena context add <workflow-id>
arena context revise <workflow-id> <entry-id>
arena context archive <workflow-id> <entry-id>
arena context restore <workflow-id> <entry-id>
```

Interactive commands can prompt for missing fields. Agent calls use the same schema-backed proposal protocol and must include the evidence and confidence they can support. Context read/write/archive events appear in run history and the live terminal view. User changes and agent writes follow the same audit and rollback rules.

## Interoperability contract

`context-proposal` and `context-result` use protocol version `1.2` or newer. Proposal content is provider-neutral; the sender's provider stays in the outer message envelope. Adapters preserve the packet unchanged. The context manager replies with `arena.context-result.v1` and an outcome of `accepted`, `needs-user`, `refused`, or `not-found`. See [`INTEROPERABILITY.md`](INTEROPERABILITY.md) for the packet contract and [`AUDIT-AND-ROLLBACK.md`](AUDIT-AND-ROLLBACK.md) for recovery behavior.
