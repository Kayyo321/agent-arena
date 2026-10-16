# Arena CLI

Build with `make build`; run `./bin/arena --help`. `make install PREFIX="$HOME/.local"`
installs the standalone binary locally. Add that directory to PATH before bootstrap
so the selected agent can invoke `arena`. No language runtime is needed to run a
packaged executable. Git and the selected provider CLI/backend remain prerequisites.

## Setup and authoring

Run `arena` in an empty directory to start `arena setup`, or use an explicit path:

```sh
arena setup --project ~/my-new-project
```

Setup can create that directory and initialize Git. It asks for the permanent
orchestrator's provider, exact model and supported effort before writing anything,
then confirms initialization. The model prompt lists the exact IDs the provider
reports (Codex app-server catalog, Gemini SDK, local `/api/tags` or `/models`) with
their supported efforts; enter a number or type an exact ID. Claude Code exposes
no catalog, so its model is typed. A listed model is not proof of inference access. It installs the skill and launches the selected
default orchestrator: describe what you want to build and answer its questions
about the workflow in Arena's own TUI. Codex runs invisibly through app-server,
with no native Codex terminal screen. Review the proposed graph and enter `/save`;
Arena validates and saves it, commits the project scaffold (`git add -A` and
`git commit -m "Arena scaffold"`, refused if staged files look secret; use
`/save no-commit` to skip), then opens its graph in the same TUI. Choices you
delegate ("you pick the efforts") are made by the orchestrator from the reported
catalog and listed in its reply. If a draft fails validation, Arena returns the
error to the orchestrator for up to two automatic repairs (journaled as
`setup.repair`) before reporting it. If you arrive without a design, type
`/walkthrough` (optionally with a note): the orchestrator asks about the project,
its goals and pain points, then offers two or three arena ideas. Arena validates
each and lists its roles, suggested providers, a rough cost (agent turns per run
weighted by reasoning effort, including capped loop reruns) and its grade. Pick
or combine ideas in your reply; the orchestrator then asks for every provider,
model and effort you have not chosen or delegated and drafts the arena as usual.
It never drafts before you pick. Until saved, bare `arena` re-enters
setup using the saved control runtime. No worker runtime is inferred from it.

To prepare configuration without starting the conversation:

```sh
arena setup --project ~/my-new-project --provider codex --model YOUR_MODEL --effort YOUR_EFFORT --no-bootstrap --yes
cd ~/my-new-project
arena
```

The conversation requires an interactive terminal. `--no-bootstrap` is also useful
for scripts. Setup preserves existing files and staged changes, creates no run or
Git commit, and refuses conflicting runtime flags on an already initialized project.

Run `arena init` inside a Git repository. Select the provider, exact model ID and
supported effort explicitly. API/local providers also require the named auth route
or backend/endpoint. Settings select the permanent project control agent, not a
default for workers. Init installs its skill and starts Arena's embedded setup
conversation. `arena bootstrap` starts another setup conversation.

For automation, supply choices as flags and use `--no-bootstrap --yes`; then create
workflows explicitly. Commit the initial repository state before running workers.

```sh
arena init --provider codex --model YOUR_MODEL --effort YOUR_EFFORT --no-bootstrap --yes
arena workflow create development
arena workflow list
arena workflow show development
arena workflow validate development
arena workflow edit development
arena settings show
arena settings edit --set '/limits/maxConcurrentAgents=2'
arena status --workflow development
```

The guided builder edits agents, hierarchy/peer links, teams, router branches,
on-call rules, and policies in memory. `finish` validates and previews the graph
before confirmation. Existing agent/team/router IDs edit those objects. Removed
profiles and all transaction snapshots remain recoverable. The builder never asks
users to edit generated JSON.

A supported automation path is `workflow create|edit ID --input REQUEST`: the
request contains `workflow` and `profiles` objects following the public schemas.
The exact same validation, preview, transaction, and conflict checks apply.
`--dry-run` previews without saving. This is an optional programmatic input, not a
manual setup prerequisite.

Every preview ends with the arena's grade (S+ to F) and its costliest deductions.
`arena score ID [--json]` prints the full breakdown: points per category, the
findings with fixes, any ceiling that capped the grade, and what the next tier
needs. The score sheet is [docs/ARENA-SCORING.md](ARENA-SCORING.md).

`workflow validate` and `workflow create` also print a `warning:` line for each
capability an agent's provider adapter cannot exercise, such as `run_commands` on
Antigravity, whose headless mode cannot execute commands. Such an agent fails when it
starts, and the score gives the grant no credit. `--command-approval sandboxed-blanket`
on an Antigravity runtime opts it into blanket command approval inside Arena's command
fence (Linux only); `workflow create` then names each agent that uses it before asking
to save. See [Antigravity command approval](CONFIGURATION.md#antigravity-command-approval).

### Sharing arenas and shaders

```sh
arena share export development --copy           # whole arena: arena1.a.…
arena share export development --shader --copy  # shader only: arena1.s.…
arena share import 'arena1.a.…'                 # or - for stdin, or --clipboard
arena share import --clipboard --as review-copy --dry-run
arena share import 'arena1.s.…' --into development
```

A share code is a single line holding the workflow, its profiles and its
background shader (or only the shader). Export fails closed: it refuses the
whole code and names the field when any value looks like a credential, key or
token, a home-directory or project path, an email address, an IP address or
internal host name, or contains terminal control characters, and when a runtime
option is not one the adapter documents. Nothing is redacted silently.
Import validates the code, picks a free ID (`--as ID`, or the author's ID with a
`-2`, `-3` … suffix), prints the graph, score, who writes or runs commands,
the integration policy, and readiness for each of the author's runtimes, then
saves through the normal transaction after confirmation. A shader code goes
through `arena shader set` for the `--into` arena. Shared context, run history
and project settings are never part of a code. The format is described in
[Configuration format](CONFIGURATION.md#share-codes).

`settings edit --set /pointer=JSON-value` is repeatable and validates every workflow
against the proposed settings. Runtime IDs/options retain provider-specific meaning.

### Preflight

`arena doctor [--project PATH] [--json]` checks the machine before you run anything. It
needs no Arena project, makes no network calls, reads no credentials and never prints
environment values (it only notices that `WSL_DISTRO_NAME` is set). Each line is `OK`,
`WARN` or `FAIL`; only `FAIL` makes the command exit non-zero.

| Check | What it looks at | Fails when |
| --- | --- | --- |
| `platform` | Linux or macOS | any other OS (Windows uses WSL2) |
| `git`, `git-repo` | Git version; the project is a work tree with a commit; uncommitted path count | Git is missing, the directory is not a repository, or there is no commit |
| `provider-codex`, `provider-claude-code`, `provider-antigravity` | CLI on PATH, its version, and drift from `providers/compatibility.json` | never (warns: needed only by arenas that use it; sign-in is `arena status`) |
| `bubblewrap` (Linux) | `bwrap` 0.11 or newer, needed for the Antigravity command fence | never (warns) |
| `sandbox-exec` (macOS) | present for sandboxed commands | never (warns) |
| `wsl`, `wsl-filesystem` | WSL2 detected from `WSL_DISTRO_NAME` or `/proc/version`; warns when the project is under `/mnt/` (a Windows drive) | never (warns) |
| `tmux` | optional, for `arena tmux` | never (warns) |

`--json` prints `{"ok": bool, "checks": [{"id", "status", "detail"}]}`. A passing
doctor does not verify a provider; it only says the prerequisites are present.

## Running and inspecting

```sh
arena
arena --workflow development
arena run development 'Describe the task and acceptance criteria'
arena watch RUN_ID
arena watch RUN_ID --json --follow
arena view AGENT --run RUN_ID --follow
arena history RUN_ID --json
arena decision list RUN_ID
arena decision answer RUN_ID DECISION_ID yes
arena pause RUN_ID
arena resume RUN_ID
arena cancel RUN_ID
```

With no command, `arena` opens the sole configured workflow automatically. If the
project has several workflows, a chooser lists them by ID: arrows or j/k select,
Enter opens the graph, and q/Esc quits. Discovery uses the current project root,
including from subdirectories, and stops at a nested Git project's boundary.
`--project PATH` selects another project; `--workflow ID` bypasses the chooser.
An empty project enters setup without choosing a runtime for you.

Opening a workflow displays an idle graph and checks only its required providers;
it creates no run and performs no inference. Enter `run TASK` in the prompt to
start work. The task uses the same readiness, policy and Git checks as `arena run`.
Quitting waits for the attached supervisor to stop and preserve history/worktrees.
After the task finishes, another `run TASK` starts a new run of that workflow.

The live view has graph cards and arrows, the Git tree, teams/routers, the permanent
control/on-call rail, required-provider readiness, events, and pending decisions.
Ctrl+E opens event detail, `/changes` opens patches, Ctrl+G shows Git identities and
Ctrl+T expands team members. Arrows/PageUp/PageDown navigate; `/quit` or Ctrl+Q exits.
Ctrl+V (or `/paste`) pastes clipboard images or text. `/attach PATH` adds files,
`/attachments` lists them, and `/detach NUMBER|all` removes pending files. Shift+Enter
inserts a newline (Alt+Enter/Ctrl+J on legacy terminals). The bottom bar shows only
`/help`; see [terminal input and attachment limits](TERMINAL-UI.md#attachments-and-clipboard).
Typing ordinary text starts a task in a loaded workflow. The prompt also accepts `run TASK`, `pause`,
`resume`, `cancel`, `decision ID ANSWER`, and `integrate CHANGE_ID` after patch inspection. Exiting the attached run view interrupts
its supervisor; exiting a separate `watch` only closes that observer.

Slash commands are available in setup and the graph:

| Command | Action |
| --- | --- |
| `/help`, `/agents` | List commands or agent names and selected runtimes |
| `/usage` | Input and output tokens providers reported for the run and per agent; `unreported` when a provider reports none, and never a cost |
| `/view NAME` | Open live agent activity in a mini-window |
| `/context [NAME]` | Inspect role, actual task and available workflow context |
| `/chat`, `/graph` | Open the control-agent interview or return to the graph |
| `/walkthrough [NOTE]` | Compare two or three arena ideas (roles, suggested providers, rough cost, grade) before the interview drafts the one you pick |
| `/consult [QUESTION]` | Ask the control agent about the loaded arena or run; it only advises, and a proposed edit is a draft until `/save` |
| `/pan` | Pan the graph with ←↑↓→ and press Space to recenter; Esc, Enter or typing leaves pan mode and keeps the view, and `/graph` recenters |
| `/save [no-commit] [allow-blanket]` | Apply an explicitly reviewed setup draft or `/consult` revision and commit the scaffold; never overwrite another workflow or an arena changed since the revision was proposed. A draft written by the orchestrator that turns on Antigravity blanket approval saves only with `allow-blanket`, which is your own choice and is audited |
| `/run TASK`, `/pause`, `/resume`, `/cancel` | Control attached work |
| `/events`, `/git`, `/changes`, `/teams` | Inspect audit, worktrees, patches and team members |
| `/status`, `/refresh` | Inspect required providers or refresh the selected context/status |
| `/decisions`, `/answer [ID] TEXT`, `/accept` | Inspect and answer pending questions/approvals; apply the orchestrator's stall recommendation |
| `$ COMMAND` | Run a shell command in the project root and show its output |
| `/integrate`, `/integrate all [skip CHANGE_ID...]`, `/integrate CHANGE_ID` | Show the resolution panel, apply the whole resolution, or integrate one reviewed change |
| `/clear`, `/close`, `/quit` | Reset scroll/errors, close a panel, or exit |

Typing `/` opens a suggestion menu above the prompt with each matching command's
usage and summary, or the agent IDs after `/view ` and `/context `. ↑/↓ move the
highlight, and Tab or Enter fills it in; Enter on a partial command never runs it.
In the mini-window, Tab switches
activity/context, PageUp/PageDown scroll, Home/End jump, and Esc closes it. Activity
shows public progress summaries, tool activity and results; private chain-of-thought
is not displayed or retained. Context distinguishes the actual retained task from
shared entries merely available to that workflow. `/chat` creates new workflow
drafts. `/consult` asks the permanent `arena-orchestrator` about the loaded arena
or its run, for example why a run stalled, how to raise the grade, or which
runtime fits a role. It runs read-only on the project default runtime with the
saved arena, its score, required-runtime readiness and the run's status,
decisions and recent events, and it cannot start a run, answer decisions or
change configuration. Plain text in the consultation asks a follow-up; `/graph`
returns and `/chat` switches to designing a new arena. When the orchestrator
proposes an edit, Arena validates and scores it and shows it as a revision draft;
`/save` applies it (audited as `workflow.saved` with source `consult`) and refuses
when the arena changed after the revision was proposed. Consultations are
journaled in project history as `consult.started`, `consult.repair`,
`consult.replied` or `consult.failed`. `arena workflow edit` remains available.

`run --plain` prints events without terminal control sequences. `watch --plain` and
`history --json` provide non-TUI inspection. `NO_COLOR` disables owner colors.

`history RUN --json` prints one object: `runId`, `status`, `terminal`,
`pendingDecisions`, `usage` and the `events` array. `usage` is `{reported, inputTokens,
outputTokens, agents}`: the token counts providers reported, summed per agent and for
the run (`reported` is false, with zero counts, when none did; a provider's own usage
map stays opaque in the `usage.reported` events). Running totals a provider repeats
within one invocation count once. `status` is the run state replayed from
the events: `created`, `running`, `paused`, `interrupted`, or one of the terminal
states `succeeded`, `failed` and `cancelled`, which the `workflow.succeeded`,
`workflow.failed` and `workflow.cancelled` events record. `terminal` is true for
those three; a script can poll until it is. A failed run whose provider request was
interrupted can still be resumed with `arena resume RUN --retry-interrupted`. A
`running` run with `pendingDecisions` above zero is waiting on the user. If the events
do not replay, `status` is `invalid` and `stateError` says why.
Bare `arena` with a configured workflow and redirected input/output prints a static graph.
`arena --plain` explicitly requests inspection, including setup guidance for an empty directory.
If multiple workflows exist, that mode requires `--workflow ID` and lists the choices
without silently selecting one. `arena --help` always prints command help.

Pause stops new agent scheduling and lets active invocations drain. Cancel stops new
work, terminates attached process groups, and preserves worktrees. A crash leaves
locks and the journal intact. Inspect `arena lock list`, recover proven-dead owner
locks with `arena lock recover NAME --yes`, then `arena resume RUN --retry-interrupted`.
Retry can repeat tool effects and is always explicit. Run limits bound concurrency,
agent attempt duration, retries, team rounds, coworker additions and on-call calls.

Teams collect independent positions, bounded deliberation and separate final
explicit affirmations. Arena validates every member and fixes the resulting verdict;
the facilitator supplies the final prose. Missing/unavailable members prevent
consensus. Reviewers return the stable review outcome contract.

Router branches compare structured JSON Pointer values exactly. A missing field
never matches an explicit null branch. Unmatched packets follow the configured
fallback. On-call invocations use durable reservations and invocation caps, including
when called from another CLI process.

### Structured terminal streams

Two commands project a run's journal as plain streams for a pipe, a script or a
terminal pane. Both are read-only: they re-read the journal every 150 ms, as the
live view does, and keep nothing but the newest event sequence they have printed.

`watch RUN --json` prints each journal event as one JSON line, exactly as
`.state/RUN/events.ndjson` records it, so the hash chain (`previousHash`, `hash`)
can still be verified. Without `--follow` it prints the journal once and exits.
`--follow` keeps tailing: each line is flushed as it is written, every event
appears once in `sequence` order, and the command exits 0 when the run is
terminal (`succeeded`, `failed` or `cancelled`, the `terminal` flag of
`history --json`) and a poll finds nothing new. That last poll lets
`agent.cancelled` events that follow `workflow.cancelled` still arrive; an event
recorded later than one poll after the run ended is only in `history`. An
interrupt (Ctrl+C or SIGTERM) also exits 0. If the events stop replaying or the
history is retired while following, it exits non-zero with the reason. `--follow`
requires `--json`; `watch --plain` and the interactive `watch` are unchanged.

```sh
arena watch run-… --json --follow | jq -c 'select(.type == "agent.failed")'
```

`view AGENT --run RUN` prints what the live view's activity window shows for one
agent, as plain text: its state and runtime, then one line per public event
(progress, plan, tool activity, results, failures) with its time, and the
structured result once the agent has one. `--follow` appends new activity as it
is recorded and ends the way `watch --follow` does, closing with the structured
result and the run's final status (`Run succeeded`). `AGENT` must be an agent of the run's workflow or one that has events in
the run. Provider text is redacted and stripped of escape and control sequences.
Raw process output is journaled only when the run keeps full local history and
`audit.processOutput` is `full`; otherwise `view` says so under the header and
shows `Output recorded: N bytes` where the text would be.

### Control server for other agents and IDEs

```sh
arena mcp --control [--project PATH]
```

serves a project-scoped [MCP control server](INTEROPERABILITY.md#project-control-mcp-server)
on stdio, so an agent or IDE can read Arena state and start a run of a saved
workflow. It is separate from the per-agent worker bridge (`arena mcp --run RUN
--actor AGENT`, four tools for one agent in one run), which is unchanged;
`--control` cannot be combined with `--run` or `--actor`. Read tools return the
same JSON as `status --json`, `history RUN --json`, `changes`, `score --json` and
`workflow show --json`. `arena_run_start` applies `arena run`'s readiness, policy
and budget checks, and the run is supervised by the server process only while the
MCP session stays open.

`arena_integrate` and `arena_rollback` do not integrate or roll back: they
check the request as `arena integrate` / `arena rollback` would and open a
pending decision for you, returning its ID. Review it with
`arena decision list RUN`, then answer it yourself:

```sh
arena decision answer RUN DECISION approve   # or: reject
```

Approving executes the integration or rollback once, in that command, and
prints the outcome. It is refused, and journaled as `decision.refused`, if `HEAD`
moved since the request, the change is no longer eligible, or the decision was
already answered; ask the agent to request it again. See
[Integrate and rollback requests](INTEROPERABILITY.md#integrate-and-rollback-requests).

### Running a run in tmux

For SSH devboxes see [DEVBOX](DEVBOX.md).

`arena tmux run WORKFLOW TASK` and `arena tmux resume RUN [--retry-interrupted]` lay a
run out in your own `tmux` (optional: if it is missing, the command says so and exits;
Arena never installs it):

- **Window 0, pane 0 is the supervisor**: the process that executes the run
  (`arena run` or `arena resume`). Supervision is attached, so ending that pane, or
  killing the tmux session, interrupts the run exactly as quitting `arena run` does
  (resume it with `arena resume`).
- Pane 1 of window 0 is the event feed (`arena watch RUN --json --follow`).
- Window 1 tiles one `arena view AGENT --run RUN --follow` per agent.
- The observers only read the journal. Closing one never stops the run. When the run
  ends the followers exit and the panes stay open (`remain-on-exit`) so the final
  output is still readable.

Run-id handoff: the observer panes must name the run, so `tmux run` first reserves it
with the same readiness, policy and baseline checks as `arena run` (failures print
before any tmux session exists), then starts the supervisor as
`arena run --run RUN -- WORKFLOW TASK`. `--run` on `arena run` adopts that reserved,
still unstarted run instead of creating one; it refuses a run that has started or
belongs to another workflow. This keeps one code path for starting a run and needs no
polling to discover the id. A reserved run whose tmux layout never started stays
`created` and harmless.

Requires tmux 2.0 or newer (checked with `tmux -V` before anything is created; see
[DEVBOX](DEVBOX.md#tmux-version)).

Safety: every pane command is passed to tmux as separate exec arguments, never a
shell string, and workflow, task, agent and run text sits after a `--` terminator. Text
such as `$(cmd)` or `; rm -rf x` reaches Arena as plain data.

Inside tmux (`$TMUX` set) Arena does not nest: it creates the session detached and runs
`tmux switch-client` to it. Outside tmux it runs `tmux attach-session`. Either way the
session is named `arena-RUN`; detach with your usual key and return with
`tmux attach -t arena-RUN`.

## Code review, recovery, and context

```sh
arena changes RUN_ID
arena changes RUN_ID CHANGE_ID
arena integrate RUN_ID --all [--dry-run] [--skip CHANGE_ID]... [--yes]
arena integrate RUN_ID CHANGE_ID
arena rollback RUN_ID CHANGE_ID
arena rollback RUN_ID CHANGE_ID --yes
arena checkpoint list
arena checkpoint restore CHECKPOINT_ID --yes
arena transaction list
arena transaction show TRANSACTION_ID
arena transaction restore TRANSACTION_ID --yes
```

Workers write in separate branches/worktrees. Read-only agents receive pinned
snapshots. Checkpoints use an alternate index and include staged, unstaged and
non-ignored untracked files without changing the user's index. Ignored files are
not copied. Detected secret-like changes are refused before checkpointing.

When a run succeeds, Arena prepares a resolution of its proposed changes. It dry-runs each
change in dependency order against the checkout and journals `resolution.ready`. Nothing
is written to the checkout yet. `arena integrate RUN_ID --all --dry-run` prints the plan
as JSON. `arena integrate RUN_ID --all` asks for one approval (`--yes` skips the prompt)
and applies the whole set. `--skip CHANGE_ID` leaves a change out and can be repeated.
Each change lands as its own commit, so `arena rollback RUN_ID CHANGE_ID --yes` reverts
one change. A failed or cancelled run gets no automatic resolution, but
`arena integrate RUN_ID --all --dry-run` still plans one.

The gate is all-or-nothing. Nothing applies unless every item is `ready` and the
checkout is clean. If an apply fails partway, the changes already committed stay in place,
and each one can be reverted with `arena rollback RUN_ID CHANGE_ID --yes`. A change is `blocked` when a dependency was superseded or skipped, and
`conflict` when the dry run fails to apply. A run that has not ended cannot be applied.
If the checkout moves after preparation, apply stops, journals `resolution.stopped` with
the changes already applied, and keeps those commits. A successful apply journals
`resolution.applied`. If preparation fails when a run succeeds, Arena journals `resolution.error` and the run stays `succeeded`.

Integration prepares and commits a patch in an isolated integration worktree before
fast-forwarding a clean target. Conflicts preserve the integration workspace. A dirty
or changed target is never overwritten. Automatic integration is opt-in: set
`approvalPolicies.change-integration` (project) and `settings.integration.policy`
(workflow) both to `auto-if-clean`. Other proposals enter human review.
Rollback previews affected paths and later commits, then creates a revert commit or
isolated recovery tree. It can itself be reverted. Checkpoint restoration recreates
working and staged content in a separate tree, including untracked additions.

```sh
arena context add development
arena context search development 'database convention'
arena context show development ENTRY_ID
arena context revise development ENTRY_ID
arena context archive development ENTRY_ID --reason 'Superseded'
arena context restore development ENTRY_ID
```

Context is workflow-scoped and records evidence, confidence and provenance. Revisions
create new IDs and preserve previous entries. Agent proposals use the same manager;
pending proposals can be approved with `arena context-request REQUEST_ID --approve`.

Coworkers use `arena agent request WORKFLOW --from AGENT --id NEW_ID --role ROLE
--objective TEXT --reason TEXT --provider ID --model MODEL --effort EFFORT
--capabilities read,write --workspace-mode write`. Missing runtime choices are errors
or interview questions. Resolve a pending request with `arena agent resolve REQUEST
--approve`. The permanent orchestrator evaluates complete, policy-authorized requests;
accepted edits use validated CLI transactions and retain their original runtime choices. Cross-provider control requests also follow sharing policy; approval explicitly covers sending the request to the configured control runtime. Approved additions join an active run between scheduling waves. The serialized control slot is separate from ordinary worker concurrency.

## Local data and retention

Run data lives under `.arena/.state/`; locks under `.arena/locks/`. Init adds both to
Git ignore rules. Full local retention includes redacted task/result packets. Reduced
retention omits message bodies and process output, and compacts consumed/cancelled decision files. Pending approval text can remain after a crash. Omitted payloads cannot be replayed
or used to resume a task. Checkpoints/configuration remain separate recovery data.
Provider environments and credential stores are never copied into the journal.

`arena history repair RUN` rebuilds an interrupted NDJSON mirror from validated
atomic event files and preserves the previous mirror. Corrupt atomic events are
refused. `arena transaction recover ID --yes` completes a preserved configuration
transaction if its paths still match the recorded before/after versions.

`arena history RUN retire --including-checkpoints --yes` moves run history and its
checkpoints to recoverable local retired storage. This is retirement, not secure
erasure or Git-object garbage collection; `purge` below is the permanent step. Review
retention before exporting source or prompts.

### Permanent purge

```sh
arena history RUN purge                              # list what would be deleted
arena history RUN purge --yes                        # delete it
arena history RUN purge --yes --discard-unintegrated # also delete unintegrated work
```

`purge` works only on a run already retired with `arena history RUN retire`; a live
run, an imported run or an unknown ID is refused. It always prints the plan first:
the retired history directories, the git worktree registrations the run created
(`trees/`, `integrations/` and `recoveries/` under its own state directory), its
branches (`codex/arena/RUN/*`, `codex/arena/integrate/CHANGE`,
`codex/arena/recovery/ROLLBACK`) and its checkpoints (the
`refs/arena/checkpoints/ID` refs and `.arena/.state/checkpoints/ID.json` records).
Nothing is deleted without `--yes`.

Unintegrated work blocks the purge: changes still `proposed`, `conflicted`,
`integration-prepared` or in `recovery`, and writer worktrees holding files beyond their
last proposal (or that cannot be inspected). They are listed, and the purge runs only
if the same command is repeated with `--discard-unintegrated`. Integrated work is
already in the user's Git history and is not affected.

Before deleting, `history.purge.started` is journaled in the project history with
the plan; afterwards `history.purged` records what was removed, the time, whether
unintegrated work was discarded, the retired journal's event count and head hash,
and any failures. If something fails (for example git refuses to delete a branch still
checked out elsewhere) the retired history is kept, the failure is recorded and the
command can be run again. A purge removes only paths under Arena's own state
directory, branches under Arena's naming and refs under `refs/arena/checkpoints`;
it never touches the user's checkout, other runs, or worktrees made by
`arena checkpoint restore`. Git objects are not collected: unreachable proposal and
checkpoint commits stay in the object database until the user runs `git gc`, and a
purge is not secure erasure.

### Audit export and import

```sh
arena history RUN export FILE     # write one run's redacted audit bundle
arena history import FILE         # verify a bundle and land it read-only
arena history --json              # [{"id": ..., "imported": true|false}, ...]
arena history IMPORTED_RUN [--json]   # imported events; --json adds "imported": true
```

`export` writes `FILE` (private permissions; it never overwrites) in the
`arena.audit.v1` format, described by
[`audit-bundle.schema.json`](../schemas/audit-bundle.schema.json). The bundle holds the
run's journal events, change records and patches, retained packets and checkpoint
metadata (id, commit, head, tree, time, reason), all as Arena already redacted them.
It never holds provider credentials, environment values, the run manifest, project
settings, shared context, attachments, worktree contents, staged or working diffs, or
raw process output the run's retention excluded. A run whose retention is not
`full-local` exports journal events, change records and checkpoint metadata only; the
manifest lists `omitted` packets and patches. Patch text is passed through secret
redaction again, which is best-effort. Bundles are limited to 256 MiB.

The manifest lists each file with its size and SHA-256, and the checksum covers the
run ID, retention, journal head and file list. `import` checks the schema, every file
against the manifest, the checksum and the journal's hash chain (each event's
sequence, previous hash and hash), and that no event keeps raw output the run's
retention excluded. It refuses a run ID that already exists here, live, retired or
imported. A verified bundle lands under `.arena/.state/imported/RUN/` as read-only
files and a `history.imported` event is journaled in the project history. Import
never merges into live state, never creates a resumable run and never touches
worktrees, checkpoints or Git refs. Imported runs are absent from the plain
`arena history` list and from `status`, `watch` and `resume`; `arena history --json`
marks them `imported: true`. A checksum proves the file is unchanged since export and
internally consistent; it does not identify who exported it.

## Skills and compatibility

`arena skill targets` lists project locations. `skill install|status|remove --agent
TARGET [--user]` operates on Codex, Claude Code, Gemini, Gemini's native location,
Copilot, and OpenCode. `--agent all` selects the initial three hosts. All installations
come from the same embedded source; modified files require `--replace` and are backed
up locally. Skill support does not imply an execution adapter.

`arena adapters` prints the versioned compatibility manifest. Status checks required
providers only and reports version drift without inventing model access. See
[Provider integrations](PROVIDERS.md) for auth boundaries and verification evidence.
