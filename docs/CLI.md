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
Arena validates and saves it, then opens its graph in the same TUI. Choices you
delegate ("you pick the efforts") are made by the orchestrator from the reported
catalog and listed in its reply. If a draft fails validation, Arena returns the
error to the orchestrator for up to two automatic repairs (journaled as
`setup.repair`) before reporting it. Until saved, bare `arena` re-enters
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

`settings edit --set /pointer=JSON-value` is repeatable and validates every workflow
against the proposed settings. Runtime IDs/options retain provider-specific meaning.

## Running and inspecting

```sh
arena
arena --workflow development
arena run development 'Describe the task and acceptance criteria'
arena watch RUN_ID
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
| `/view NAME` | Open live agent activity in a mini-window |
| `/context [NAME]` | Inspect role, actual task and available workflow context |
| `/chat`, `/graph` | Open the control-agent interview or return to the graph |
| `/save` | Apply an explicitly reviewed setup draft; never overwrite another workflow |
| `/run TASK`, `/pause`, `/resume`, `/cancel` | Control attached work |
| `/events`, `/git`, `/changes`, `/teams` | Inspect audit, worktrees, patches and team members |
| `/status`, `/refresh` | Inspect required providers or refresh the selected context/status |
| `/decisions`, `/answer ID TEXT` | Inspect and answer pending questions/approvals |
| `/integrate CHANGE_ID` | Integrate a reviewed change |
| `/clear`, `/close`, `/quit` | Reset scroll/errors, close a panel, or exit |

Tab completes slash commands and agent IDs. In the mini-window, Tab switches
activity/context, PageUp/PageDown scroll, Home/End jump, and Esc closes it. Activity
shows public progress summaries, tool activity and results; private chain-of-thought
is not displayed or retained. Context distinguishes the actual retained task from
shared entries merely available to that workflow. `/chat` creates new workflow
drafts; editing existing workflows continues to use `arena workflow edit`.

`run --plain` prints events without terminal control sequences. `watch --plain` and
`history --json` provide non-TUI inspection. `NO_COLOR` disables owner colors.
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

## Code review, recovery, and context

```sh
arena changes RUN_ID
arena changes RUN_ID CHANGE_ID
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

Integration prepares and commits a patch in an isolated integration worktree before
fast-forwarding a clean target. Conflicts preserve the integration workspace. A dirty
or changed target is never overwritten. Both policies must explicitly permit
`auto-if-clean` for automatic integration; other proposals enter human review.
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
erasure or Git-object garbage collection. Review retention before exporting source
or prompts. Audit export/import and automatic permanent deletion are not implemented.

## Skills and compatibility

`arena skill targets` lists project locations. `skill install|status|remove --agent
TARGET [--user]` operates on Codex, Claude Code, Gemini, Gemini's native location,
Copilot, and OpenCode. `--agent all` selects the initial three hosts. All installations
come from the same embedded source; modified files require `--replace` and are backed
up locally. Skill support does not imply an execution adapter.

`arena adapters` prints the versioned compatibility manifest. Status checks required
providers only and reports version drift without inventing model access. See
[Provider integrations](PROVIDERS.md) for auth boundaries and verification evidence.
