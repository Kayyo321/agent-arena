# Audit and rollback

## Audit record

Each run has a local ordered event journal under `.arena/.state/<run-id>/`. Events include a monotonic sequence, unique event ID, timestamp, run/workflow/agent IDs, event type, correlation/message IDs where relevant, and references to any files or Git objects involved.

The initial event vocabulary includes workflow started/paused/resumed/cancelled, agent queued/started/waiting/succeeded/failed, message sent/delivered, process output captured, checkpoint created, workspace rejected, change proposed/reviewed/integrated, resolution ready/applied/stopped/error (`resolution.*`), context proposed/accepted/revised/archived/restored, and rollback created/completed. Team events record team start, member positions, each deliberation round, member affirmations, facilitator output, and whether the result reached unanimous consensus. On-call events record trigger matches, invocation start/completion, deduplication or cap decisions, the context sent, and the resulting agent invocation. Coworker requests record the requesting agent, requested role and runtime, policy decision, user response when required, and the resulting config snapshot/change. Reviewer events preserve the standard verdict packet.

For auditability, the journal records the project settings, default runtime, permanent orchestrator profile, workflow definition, and effective policy snapshot or content hash used by the run, along with the initial repository HEAD, each agent's worktree and branch, base commits, process identity and exit status, handoff links, diffs/commit IDs, review decisions, integration/rollback operations, and context entry IDs/versions read or proposed. Context mutations record actor, source task, policy decision, and before/after hashes. An event chain may include hashes to detect accidental edits, but local hashes are not a security guarantee against an owner who can rewrite the entire state directory.

## Message and output retention

Full local retention gives the strongest review trail and stores prompts, replies, and selected process output on the developer's machine. Redacted retention can omit or redact message bodies and output while retaining metadata and content hashes. The UI must show the chosen retention level before a run and mark any redactions or missing payloads. Provider credentials and detected secrets are always excluded.

The default should be full local retention with secret redaction. Run data can contain source code, prompts, and tool output, so `.arena/.state/` is ignored by Git and is not synced by Arena. Arena must not record provider credentials or process environment values; secret scanning is best-effort and cannot guarantee detection of every secret in arbitrary source text. Users inspect history with `arena history` and the live view, and move one run's redacted trail between projects with `arena history RUN export FILE` and `arena history import FILE` (see [Audit export and import](CLI.md#audit-export-and-import)). A bundle holds journal events, change records and patches, retained packets and checkpoint metadata; it omits whatever the run's retention excluded, and an import verifies the hash chain and manifest checksum, lands read-only under `.arena/.state/imported/` and can never resume a run. Retention and deletion controls must state whether checkpoint data will also be removed: retirement keeps checkpoints, and purge deletes the run's checkpoint refs and records.

## Worktree and integration policy

1. Record the starting HEAD and the user's current staged, unstaged, and untracked changes.
2. Create a dedicated branch and worktree for each writing task. Read-only agents may use a pinned snapshot.
3. Keep agent worktrees separate. A result includes its base commit and a diff or commit reference.
4. Checkpoint before launching a writer and before integrating its change. Capture uncommitted and untracked content in a recoverable Arena checkpoint.
5. Review each proposed patch, status, and validation result in an Arena-owned integration queue.
6. If the base changed or patches overlap, prepare a conflict view or integration worktree. Never silently overwrite the newer change.
7. Integrate with a recorded commit or patch application under the selected policy. The default is explicit human review.
8. Preserve the original agent branch and checkpoint until the integrated result is accepted or its rollback retention expires.

When a run succeeds, Arena prepares a resolution of its proposed changes. It dry-runs them in dependency order against the checkout in a temporary index, writes no refs or root files, and journals `resolution.ready`. The whole set is applied only by one explicit approval (`/integrate all` or `arena integrate RUN --all`), and only if every item is ready and the checkout is clean. Each change still lands as its own commit, so rollback stays per change. Automatic per-change integration remains opt-in through `auto-if-clean` in both `approvalPolicies.change-integration` and `settings.integration.policy`.

Workers do not merge each other's branches directly. The integration manager is the sole writer to the selected integration target.

### Host Git on worker worktrees

A writing worker's command sandbox can rewrite anything inside its worktree, including the `.git` pointer file, so Arena treats every worker-writable path as untrusted input to its own unsandboxed Git calls:

- When a worktree is created, before any worker runs, Arena records its gitdir pointer in the workspace manifest (`gitDir`).
- Before checkpointing or diffing a worker worktree, Arena requires `.git` to still be a regular gitdir pointer file naming the recorded gitdir, and that gitdir to be the main repository's `.git/worktrees/<name>` entry whose back-pointer names this worktree. Git commands then pass that verified `--git-dir` and `--work-tree` explicitly instead of letting Git rediscover the repository from the worktree.
- Those commands override every config scope with `core.fsmonitor=false`, `core.hooksPath=/dev/null`, `protocol.allow=never`, `submodule.recurse=false`, and `commit.gpgSign=false`, and diffs use `--no-ext-diff --no-textconv`. Filter drivers can then only come from the user's own system, global, or repository config, which the sandbox cannot write; a worker's `.gitattributes` can select such a driver but cannot define one.
- A worktree that fails these checks fails closed: Arena records `workspace.rejected` with the reason, marks the manifest `rejected`, never checkpoints or proposes it, refuses to reuse it for a retry, and leaves it on disk for inspection.

## Rollback behavior

Rollback accepts a run/change/event identifier and previews the affected paths, current HEAD, and subsequent changes before applying anything. On a clean Git history, it creates a revert commit or a new patch that inverses the selected integrated change. For uncommitted work, it restores from a checkpoint into an isolated recovery worktree and shows the diff before any copy into the user's current checkout.

If later work depends on the selected change, Arena records the rollback as a new event and preserves intervening work. It does not erase the journal, rewrite published history, force-push, or reset a user's branch. A rollback can itself be reverted by selecting the rollback event. A change that an integrated rollback has already reverted cannot be rolled back again until that rollback is itself reverted. When an agent asks through the control server, the rollback is a pending decision that executes once, when it is approved at the TUI or `arena decision answer`; Arena records the entry point but cannot verify a human gave the approval ([ADR 0002](adr/0002-operator-approval-boundary.md)). See [Integrate and rollback requests](INTEROPERABILITY.md#integrate-and-rollback-requests).

Context rollback restores a prior entry version or active/archive state as a new recorded event. It never removes the proposal, approval decision, or previous content from the journal. Git-tracked context is also recoverable through normal Git history; local-only context uses Arena checkpoints and local journal history. See [Shared context](SHARED-CONTEXT.md).

## Non-Git workspaces

The MVP requires a Git repository for multi-agent writes. Future non-Git support would need a complete filesystem snapshot and restore design for edits, renames, deletes, permissions, and binary files before it can promise equivalent rollback.

## Implementation limits

Checkpoints exclude ignored files and Arena runtime state, preserve the caller index, and refuse submodules or nested Git repositories because their independent object/index state is not yet covered. Dependent workers receive a combined pinned snapshot of prerequisite proposals; the queue records those dependencies and requires integration in that order. Worktrees and recovery refs remain local until the run is retired and then permanently deleted with `arena history RUN purge --yes` (see [Permanent purge](CLI.md#permanent-purge)), which lists unintegrated changes first and refuses them without `--discard-unintegrated`, removes only Arena-owned worktrees, branches, checkpoint refs and records, and journals a tombstone; it does not run `git gc`. Reduced retention omits packet/output bodies and compacts consumed or cancelled decision files; pending decisions temporarily retain the text needed for approval. A process crash can leave such a pending decision file for recovery. See [CLI operations](CLI.md) and [verification evidence](VALIDATION.md).

User attachments are an explicit full-local input: exact file snapshots live under
`.arena/.state/RUN/attachments/` (or `project/attachments/` for orchestrator chat),
with private permissions and audited name/type/size/hash references. Binary files
and attachment text are not passed through secret-redaction, since that would
change the supplied input. These snapshots are excluded from Git checkpoints and
remain until their containing history is removed. Reduced-retention modes refuse
attachments. Unsent attachments live only in memory and are discarded on exit.
