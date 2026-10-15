# Audit and rollback

## Audit record

Each run has a local ordered event journal under `.arena/.state/<run-id>/`. Events include a monotonic sequence, unique event ID, timestamp, run/workflow/agent IDs, event type, correlation/message IDs where relevant, and references to any files or Git objects involved.

The initial event vocabulary includes workflow started/paused/resumed/cancelled, agent queued/started/waiting/succeeded/failed, message sent/delivered, process output captured, checkpoint created, change proposed/reviewed/integrated, context proposed/accepted/revised/archived/restored, and rollback created/completed. Team events record team start, member positions, each deliberation round, member affirmations, facilitator output, and whether the result reached unanimous consensus. On-call events record trigger matches, invocation start/completion, deduplication or cap decisions, the context sent, and the resulting agent invocation. Coworker requests record the requesting agent, requested role and runtime, policy decision, user response when required, and the resulting config snapshot/change. Reviewer events preserve the standard verdict packet.

For auditability, the journal records the project settings, default runtime, permanent orchestrator profile, workflow definition, and effective policy snapshot or content hash used by the run, along with the initial repository HEAD, each agent's worktree and branch, base commits, process identity and exit status, handoff links, diffs/commit IDs, review decisions, integration/rollback operations, and context entry IDs/versions read or proposed. Context mutations record actor, source task, policy decision, and before/after hashes. An event chain may include hashes to detect accidental edits, but local hashes are not a security guarantee against an owner who can rewrite the entire state directory.

## Message and output retention

Full local retention gives the strongest review trail and stores prompts, replies, and selected process output on the developer's machine. Redacted retention can omit or redact message bodies and output while retaining metadata and content hashes. The UI must show the chosen retention level before a run and mark any redactions or missing payloads. Provider credentials and detected secrets are always excluded.

The default should be full local retention with secret redaction. Run data can contain source code, prompts, and tool output, so `.arena/.state/` is ignored by Git and is not synced by Arena. Arena must not record provider credentials or process environment values; secret scanning is best-effort and cannot guarantee detection of every secret in arbitrary source text. Users can inspect and explicitly export an audit bundle. Retention and deletion controls must state whether checkpoint data will also be removed.

## Worktree and integration policy

1. Record the starting HEAD and the user's current staged, unstaged, and untracked changes.
2. Create a dedicated branch and worktree for each writing task. Read-only agents may use a pinned snapshot.
3. Keep agent worktrees separate. A result includes its base commit and a diff or commit reference.
4. Checkpoint before launching a writer and before integrating its change. Capture uncommitted and untracked content in a recoverable Arena checkpoint.
5. Review each proposed patch, status, and validation result in an Arena-owned integration queue.
6. If the base changed or patches overlap, prepare a conflict view or integration worktree. Never silently overwrite the newer change.
7. Integrate with a recorded commit or patch application under the selected policy. The default is explicit human review.
8. Preserve the original agent branch and checkpoint until the integrated result is accepted or its rollback retention expires.

Workers do not merge each other's branches directly. The integration manager is the sole writer to the selected integration target.

## Rollback behavior

Rollback accepts a run/change/event identifier and previews the affected paths, current HEAD, and subsequent changes before applying anything. On a clean Git history, it creates a revert commit or a new patch that inverses the selected integrated change. For uncommitted work, it restores from a checkpoint into an isolated recovery worktree and shows the diff before any copy into the user's current checkout.

If later work depends on the selected change, Arena records the rollback as a new event and preserves intervening work. It does not erase the journal, rewrite published history, force-push, or reset a user's branch. A rollback can itself be reverted by selecting the rollback event.

Context rollback restores a prior entry version or active/archive state as a new recorded event. It never removes the proposal, approval decision, or previous content from the journal. Git-tracked context is also recoverable through normal Git history; local-only context uses Arena checkpoints and local journal history. See [Shared context](SHARED-CONTEXT.md).

## Non-Git workspaces

The MVP requires a Git repository for multi-agent writes. Future non-Git support would need a complete filesystem snapshot and restore design for edits, renames, deletes, permissions, and binary files before it can promise equivalent rollback.
