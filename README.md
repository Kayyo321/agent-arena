# Agent Arena

Agent Arena is a local-first CLI for coordinating coding and research agents across
provider integrations and same-machine model backends. It generates workflow and
agent files, routes versioned packets, isolates writing agents in Git worktrees,
and keeps a local history with reviewable integration and reversible rollback.

The Go implementation includes workflow authoring, the permanent project control
agent, teams, reviewers, deterministic routers, on-call agents, workflow context,
and a Bubble Tea terminal interface. A real Codex read-only workflow has passed, and the live multi-provider exit passed
with Codex and Antigravity (each under its own sign-in) in the production-readiness
sweep. Claude Code, Gemini, OpenCode and local adapters have contract or probe coverage only;
that coverage does not establish live readiness.
See [roadmap and exit checks](docs/ROADMAP.md), the [final TODOs](docs/ROADMAP.md#final-todos)
and [validation evidence](docs/VALIDATION.md). To keep runs going over SSH, see
[running on a devbox](docs/DEVBOX.md).

## Build and install

Supported targets are macOS and Linux on arm64/amd64, including Linux under WSL2.
Packaged executables require no language runtime. Git and the selected provider
CLI or model backend remain prerequisites. Building from source uses the Go
toolchain selected by `go.mod` (Go 1.27.2 in CI).

Install the latest release (first choice):

```sh
curl -fsSL https://github.com/Kayyo321/agent-arena/releases/latest/download/install.sh | bash
```

The installer places the binary at `$PREFIX/lib/arena/<version>/arena` and links it as
`$PREFIX/bin/arena`, where `PREFIX` is set by `ARENA_PREFIX` and defaults to `$HOME/.local`.
`ARENA_VERSION` pins a version, `ARENA_RELEASE_BASE_URL` overrides the release host,
and `ARENA_NO_UPDATE_CHECK=1` turns off the update notice. Verify a downloaded archive
with `gh attestation verify <archive> --repo Kayyo321/agent-arena`. Update with
`arena update` and remove with `arena uninstall` ([CLI guide](docs/CLI.md#install-update-and-uninstall)).

To build from source instead:

```sh
make build
./bin/arena --help
make install PREFIX="$HOME/.local"
export PATH="$HOME/.local/bin:$PATH"
```

From an extracted standalone archive, install the executable directly:

```sh
mkdir -p "$HOME/.local/bin"
install -m 755 arena "$HOME/.local/bin/arena"
```

`make dist` builds four standalone archives, a corresponding source archive,
checksums, provider compatibility metadata and third-party license notices under
`dist/`. It does not upload or publish anything. Native Windows and network
filesystems are outside this release's supported scope.

## Create and run a workflow

From a new directory, run `arena` (or `arena setup --project PATH`). Setup initializes
Git when needed, asks for the project control runtime, installs its skill and starts
the default orchestrator's conversation inside Arena's TUI. Tell it what you want
to build; review its proposed workflow and use `/save` to create it and open the
graph. Codex stays in the background. Once a workflow exists, `arena` opens
its graph instead. Setup does not choose worker runtimes on your behalf.

To use the direct guided builder inside an existing Git repository:

```sh
arena init --no-bootstrap
arena workflow create development
arena workflow validate development
arena status --workflow development
arena
```

Every arena gets a grade from S+ to F for how it spreads trust, verifies work
and avoids needless agents ([score sheet](docs/ARENA-SCORING.md)); `arena score ID`
and `/score` explain it. Share an arena with `/copy` (or
`arena share export ID --copy`) and paste the code into another Arena to import
it; `/copy-shader` shares only the background shader.

Running `arena` opens the graph TUI. It automatically opens the sole arena in the
current project, or asks you to choose when several exist. Opening a graph starts
no task. Enter `run TASK` in its prompt to start an attached run. Use
`arena --workflow development` to select one explicitly, or
`arena run development 'Describe the task and acceptance criteria'` to start directly.

Init asks for the exact default provider, model and supported effort, installs the
shared skill, and starts Arena's embedded setup conversation. That runtime
belongs to the permanent `arena-orchestrator`; each worker requires its own explicit
runtime choice. The guided builder authors agents, links, teams, router branches,
on-call rules and policy, then previews and validates before saving. Commit the
initial repository state before running workers.

For automation, `init --no-bootstrap --yes` and `workflow create` accept explicit
runtime flags. `--input` is an optional schema-backed builder request. Users do not
need to hand-author generated `.arena` configuration. Full command syntax and
recovery procedures are in the [CLI guide](docs/CLI.md).

```sh
arena watch RUN_ID
arena watch RUN_ID --json --follow
arena view AGENT --run RUN_ID --follow
arena history RUN_ID --json
arena changes RUN_ID
arena changes RUN_ID CHANGE_ID
arena integrate RUN_ID --all --dry-run
arena integrate RUN_ID --all --yes
arena integrate RUN_ID CHANGE_ID
arena rollback RUN_ID CHANGE_ID
arena context search development 'project convention'
```

Writing workers get isolated worktrees; downstream workers receive pinned snapshots
of prerequisite proposals. Changes enter an audited review queue. When a run succeeds,
Arena prepares a resolution: it dry-runs every proposed change in dependency order
against the checkout and journals `resolution.ready`. You apply the whole set with one
approval (`/integrate all` in the TUI, or `arena integrate RUN_ID --all`). Nothing
applies unless the whole plan is ready. Use `--skip CHANGE_ID` to leave out a change.
Each change lands as its own commit. Integration checks for conflicts and later user
edits. Rollback makes a revert or a separate recovery worktree. It never silently
resets or discards later work.

The live interface opens with a status band: the small purple dotted A2rena wordmark
at the left, the run's status, active agents, progress and line changes across
worktrees, and the Git tree at the right. Below it are the graph (with distinct head,
worker, team and router cards), packet activity,
teams and on-call rail, required providers, decisions and the `> memento mori` prompt.
`/help` lists slash commands. `/view AGENT` opens a live mini-window and `/context AGENT`
shows its role, task and workflow context. `/chat` talks to the control agent,
`/walkthrough` has it propose a few arena ideas when you have no design yet, and
`/consult` asks it about the loaded arena or run (advice only; edits stay drafts until `/save`).
Ctrl+E/G/T/V open events/Git/teams/patches, arrows or PageUp/PageDown navigate, and
`/quit` exits. Plain output is available for logs and automation.

Concurrency is capped by `maxConcurrentAgents` in `.arena/settings.arena.json`. The default is 4, the allowed range is 1-64, and the cap applies per run and project-wide. See [configuration](docs/CONFIGURATION.md).

## Providers and credentials

- Codex uses the installed CLI's documented app-server interface and provider-owned authentication.
- Claude Code uses bare programmatic mode with API-key or supported cloud authentication, or, when you select `claude-code-sign-in`, runs in the background under its own sign-in like Codex.
- Antigravity (`agy`, Linux with bubblewrap 0.11+) runs in the background under its own sign-in, inside a write fence: the filesystem is read-only except a writer's own worktree, a private `/tmp` and a throwaway copy of agy's own state. It runs commands only when you opt its runtime into sandboxed blanket approval (Linux): agy keeps the network for inference, but each command it runs is re-sandboxed with an emptied home and no network at all.
- OpenCode (`opencode` 1.19.3) runs headless under OpenCode's own sign-in (`opencode auth login`), a user choice under your own terms. It is an execution adapter with contract and probe coverage only; live inference is unverified. Its workers have no Arena MCP bridge and no shell commands, and readiness refuses to start them when your own OpenCode configuration allows tools beyond Arena's read (and, for writers, edit) permissions, MCP tools included.
- Gemini uses the official Google GenAI SDK with an explicitly selected API-key or Vertex AI route.
- Local agents use loopback Ollama or OpenAI-compatible services with an explicitly selected model.

Arena does not collect passwords, read or copy provider tokens, pool subscriptions,
or bypass usage limits. Gemini CLI personal OAuth is not an Arena authentication
route. The Claude Code, Antigravity and OpenCode sign-in routes are explicit user choices:
Arena starts the official CLI, which keeps its own credentials, and the applicable
terms are the user's to check. Readiness distinguishes interface, auth,
advertised models and verified inference. See [provider boundaries and evidence](docs/PROVIDERS.md).

## Project files and contracts

```text
.arena/
  settings.arena.json       # project control agent, approval policies and limits
  <workflow>.arena.json     # generated workflow
  .agents/                 # generated project control and workflow profiles
  context/                 # workflow context when configured as Git-tracked
  locks/                   # atomic ownership locks; Git-ignored
  .state/                  # local journal, packets, worktrees and recovery data
```

The canonical portable skill is [arena-orchestrator](.agents/skills/arena-orchestrator/SKILL.md).
`arena skill targets` lists supported project/user host locations. Installations
come from that single embedded source and preserve modified files through backups.
Copilot is a skill host; no execution adapter is advertised for it. OpenCode is both a skill host and an execution adapter (see [providers](docs/PROVIDERS.md)).

Product contracts: [product](docs/PRODUCT.md), [architecture](docs/ARCHITECTURE.md),
[interoperability](docs/INTEROPERABILITY.md), [configuration](docs/CONFIGURATION.md),
[schemas](schemas/), [shared context](docs/SHARED-CONTEXT.md),
[terminal UI](docs/TERMINAL-UI.md), [arena scoring](docs/ARENA-SCORING.md), and [audit/rollback](docs/AUDIT-AND-ROLLBACK.md).
Implementation choices are recorded in [ADR 0001](docs/adr/0001-implementation-foundation.md).

## Development checks

```sh
make generate
make check
make build
make dist
```

Normal checks use local fixtures and temporary Git repositories, without paid
inference. Live tests require explicit opt-in runtime choices; see
[validation evidence](docs/VALIDATION.md). Submodules/nested Git repositories are
refused rather than incompletely checkpointed. Worktrees and recovery objects are
retained for inspection. `arena history RUN export FILE` and `arena history import FILE`
move a redacted audit bundle between projects, read-only, and `arena history RUN purge --yes`
permanently deletes a retired run's worktrees, branches and checkpoint refs (Git objects
are left for `git gc`).

## License

GNU GPL v3.0; see [LICENSE](LICENSE). Distribution archives include dependency notices.
