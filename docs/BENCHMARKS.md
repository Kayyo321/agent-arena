# Conflict and rollback benchmark

This benchmark counts how many writes survive when several writers touch the same
repository: the workspace, integration and rollback code in `workspace.go` and
`workspace_git.go` against a **naive shared checkout**.

- The baseline is a naive shared checkout: every writer edits one directory, and
  nothing records what a later write replaces. It is a small model written for
  this benchmark, not a measurement of any real tool.
- **This is not a benchmark of any competitor product.** It compares Arena's
  isolated worktrees, integration queue and rollback with that naive model, and
  nothing else.
- It does not measure model quality, speed, cost or any provider adapter. It
  spends no inference, so it says nothing about whether a real agent makes good
  edits, only about what Arena does with the edits it is given.

## Run it

```sh
make bench
```

This runs `TestBenchConflictAndRollback` in `internal/arena/bench_test.go` and
prints the table below. The same test runs under `make check`, where it fails if
Arena loses or silently overwrites any write. The numbers are counts, not
timings, and are identical on every run.

## Method

Every scenario starts from the same committed repository (`notes.txt`, thirty
numbered lines, and `base.txt`) and the same list of writes. A write is either
one line of a file set to a unique text, or a whole new file. Writers are fake:
a Go invoker, or a fake `claude` executable behind the real Claude Code
sign-in adapter (`invokeClaudeNative`). The disjoint-writers scenario runs through
both and requires identical results. Repositories are temporary Git repositories.

**Arena path.** A real workflow run executes: a head agent delegates to the
writing agents, each gets an isolated worktree (`Workspace`), `FinishWorkspace`
records a proposal, and the test integrates proposals one at a time with
`Integrate` as the user would. Rollback uses `Rollback`, and revision loops use
the engine's loop route and `composeBase`. After the run, each write is scored:

| Outcome | Meaning |
| --- | --- |
| landed | The write is in the final checkout. |
| preserved | Not in the final checkout, but recoverable from what Arena recorded: the writer's proposal commit under `refs/arena/checkpoints`. The change is `conflicted`, refused, or still pending, and the user can act on it. |
| lost | In neither place: dropped, or silently overwritten. |

**Naive path.** The same writes are applied to a plain directory with last write
wins, in two variants, because the outcome depends on how writers overlap in time:

- *Serial*: each writer starts after the previous one finished and reads the
  current files. This is the best case for a shared checkout.
- *Concurrent*: all writers start from the same snapshot, and each writes whole
  files back when it finishes, so the last writer to touch a file keeps it.

Edits made after the writers (a user's edit, a later commit) always apply to the
live files. The naive rollback restores every file the change touched to its
content from before the change. Nothing is recoverable on the naive path, so a
write that is not in the final files is lost. Loop iterations are sequential by
nature, so the loop row has no concurrent variant.

## Results

Linux amd64, Go 1.27.2, Git 2.47.3, 2026-10-16. 31 writes in total.

| Scenario | Writes | Arena landed / preserved / lost | Naive serial landed / lost | Naive concurrent landed / lost |
| --- | --: | --- | --- | --- |
| Disjoint parallel writers | 6 | 6 / 0 / 0 | 6 / 0 | 4 / 2 |
| Overlapping hunks | 5 | 3 / 2 / 0 | 4 / 1 | 3 / 2 |
| Overlapping fan-out children | 5 | 3 / 2 / 0 | 4 / 1 | 3 / 2 |
| User edit between proposal and integrate | 5 | 3 / 2 / 0 | 4 / 1 | 4 / 1 |
| Rollback, then a disjoint later edit | 3 | 3 / 0 / 0 | 1 / 2 | 1 / 2 |
| Rollback, then an overlapping later edit | 3 | 3 / 0 / 0 | 1 / 2 | 1 / 2 |
| Loop iteration (revision builds on the first pass) | 4 | 4 / 0 / 0 | 4 / 0 | n/a |
| **Total** | 31 | 25 / 6 / 0 | 24 / 7 | 16 / 11 (of 27) |

In the rollback rows the writes counted are the ones that must survive the
rollback (a second writer's change and the later edit), not the rolled-back
change itself.

### Scenarios

- **Disjoint parallel writers.** Three writers each create a file and edit a
  different line of `notes.txt`. All three integrate. A reviewer workspace built
  on all three proposals (`composeBase`) contains every edit. A serial shared
  checkout also keeps everything; a concurrent one loses the two shared-file
  edits that were not written last.
- **Overlapping hunks.** Two writers set the same line differently, a third edits
  another line. The first integrates, the second ends `conflicted` (journaled as
  `change.conflicted`) with the checkout unchanged, and the third integrates.
  `composeBase` refuses to hand a reviewer a tree that merges the two. The
  conflicted writer's work is *preserved*, not landed: Arena integrates a whole
  change, so its non-overlapping new file waits with the overlapping line. Both
  naive variants silently overwrite one of the two values.
- **Overlapping fan-out children.** The same three writers, as the children of a
  fan-out. Before the next node starts, the overlap check (`fanOutConflicts`, the
  same `git apply --check` integrate runs) names the two writers that clash and
  not the third. A reviewer workspace built on the clean child's change succeeds
  where `composeBase` refuses the full set, and integration ends as in the
  overlapping-hunks row. The scenario calls the check on the recorded proposals;
  it does not run a fan-out node (`fanout_test.go` does), so the counts equal the
  previous row by construction. It shows that the overlap is found without losing
  either proposal, not that the counts improve.
- **User edit between proposal and integrate.** After two proposals exist, the
  user edits `notes.txt` in the checkout. Integration is refused for both
  proposals while the checkout is dirty, the proposals stay `proposed` and the
  user's file is untouched. Once the user commits, the proposal that touches the
  same line is `conflicted` and the other integrates. The naive checkout lets the
  user's later write replace an agent's line without any record.
- **Rollback, then a later edit.** Two changes are integrated and a later commit
  lands, then the first change is rolled back. With a disjoint later edit the
  rollback is one new revert commit on top of the later work, history is not
  rewritten, nothing else is removed, and rolling the rollback back restores the
  change. With an overlapping later edit the revert conflicts, the rollback ends
  `conflicted` in a preserved recovery worktree, and the user's checkout is left
  as it was. The naive rollback restores whole files and takes the other writer's
  and the later edit with it.
- **Loop iteration.** A revision loop reruns the builder in a new worktree built
  on its first proposal. Both proposals stay, the second records the first as a
  dependency, integrating the revision first is refused, and in order both land. A serial
  shared checkout also loses nothing here: the numbers tie, and the difference is
  that Arena kept each iteration as a separate reviewable change and left the
  user's checkout untouched until approval (the test asserts the latter after
  every run).

Besides the counts, the test checks that no writer changed the user's checkout
before approval, that refused integrations and conflicted rollbacks do not move
`HEAD`, and that conflicts and rollbacks are journaled.

## Limits

- Small fixtures, 31 writes, one file of interest. The counts show which
  behaviors hold, not how often conflicts occur in real work.
- The naive baseline is a model with explicit assumptions (whole-file write-back
  for concurrent writers, file-level restore for rollback). A different naive
  workflow, such as committing between writers, would score differently.
- A conflicted change keeps its non-overlapping hunks out of the checkout until
  the conflict is resolved. That is why `preserved` is not the same as `landed`.
- Not covered: the window where the target moves during an integration
  (`integration-prepared`), which cannot be triggered deterministically from
  outside; binary files, renames and mode changes; large repositories; and
  nested repositories, which Arena refuses.
- Fake writers never run a sandbox or a real provider. Provider behavior is
  covered in [validation evidence](VALIDATION.md).

See [audit and rollback](AUDIT-AND-ROLLBACK.md) for the contracts under test.
