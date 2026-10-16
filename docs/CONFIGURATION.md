# Configuration format

## CLI-owned layout

For a workflow called `code-review`, the CLI creates:

```text
.arena/
├── settings.arena.json
├── code-review.arena.json
├── context/
│   └── code-review/entries/  # shared context when Git-tracked
├── .agents/
│   ├── arena-system/
│   │   └── arena-orchestrator.agent.json
│   └── code-review/
│       ├── orchestrator.agent.json
│       ├── implementer.agent.json
│       ├── reviewer.agent.json
│       └── security-reviewer.agent.json
└── .state/                  # run data and local-only context; ignored by Git
```

Users create and change workflows through Arena's guided builder. JSON is public, versioned, reviewable configuration; `.state` is mutable run data. The CLI validates the file and any referenced profiles before starting a provider process.

Project-wide defaults live in `.arena/settings.arena.json`, separate from workflow-specific run policy. It selects the default model used during bootstrap and by the permanent on-call Arena orchestrator, sets coworker and data-sharing policies, and bounds agent activity. It does not silently become the model for newly requested workers. See [`../schemas/project-settings.schema.json`](../schemas/project-settings.schema.json). If `defaultModel` or a supported reasoning-effort choice is missing, `arena init` asks the user to choose before launching the setup conversation.

`arena status` reports only providers required by the selected arena, including the permanent orchestrator if its provider is not already present. With no selected arena, it reports the union required by configured workflows plus the permanent orchestrator. It includes CLI/version state, sign-in readiness, skill-discovery support, selectable models and effort controls when verifiable, and the configured default model for those providers. A provider may not expose a complete subscription catalog; in that case Arena distinguishes configured choices from verified availability and does not claim broader access. It never prints credentials. `arena init` installs the skill to the documented project skill locations for supported providers that will host agents, then launches the selected default model after verifying skill discovery.

The permanent project-level control agent is reserved as `arena-orchestrator`. Its profile is `.arena/.agents/arena-system/arena-orchestrator.agent.json`; it uses `defaultModel` and the `arena-orchestrator` skill. It stays on call across workflows and is not the user-selected head agent in any one workflow.

## Project policies and limits

Project policies govern agent-initiated control-plane actions. In particular, `approvalPolicies.arena-subagent-modification-inquiry` is one of:

- `auto-refuse`: reject coworker requests made by workflow agents and journal the reason.
- `ask-user`: show the requested role, objective, provider/model/effort, permissions, and reason; wait for the user's decision.
- `auto-accept`: accept a complete request when its provider is available and it fits configured limits.

This policy does not change how Arena handles a setup request made directly by the user. The setup flow must ask the user for missing required choices in every policy mode. `auto-accept` never invents a provider, model, or reasoning effort. Under `auto-refuse`, an incomplete workflow-agent request is recorded as refused without asking the user; a direct user request still receives clarification.

Other useful settings include:

- `approvalPolicies.agent-workflow-edit`: `auto-refuse`, `ask-user`, or `auto-accept` for graph edits proposed by agents.
- `approvalPolicies.cross-provider-context-sharing`: `deny`, `ask-user`, or `task-scoped` for forwarding selected task context across providers. `ask-user` asks once per route (one source provider to one recipient agent) per run; see [Cross-provider data boundary](INTEROPERABILITY.md#cross-provider-data-boundary).
- `approvalPolicies.shell-command-execution`: `adapter-default`, `ask-user`, or `auto-refuse`; Arena never weakens the provider CLI's own permission mode. The one exception is a runtime the user opts into [Antigravity sandboxed blanket approval](#antigravity-command-approval), which needs `adapter-default` like every background CLI.
- `approvalPolicies.change-integration`: `human-review` or `auto-if-clean` for eligible patches.
- `approvalPolicies.workflow-deletion`: `auto-refuse`, `ask-user`, or `auto-accept` for agent-originated deletion requests.
- `approvalPolicies.orchestrator-stall-recovery` (optional): `answer-routing` (default when absent) lets the permanent orchestrator answer a stalled router fallback itself, choosing only among that router's declared branches or `stop`; `recommend-only` makes it suggest an answer for `/accept`. Agent questions and permission approvals are never answered by the orchestrator.
- `limits.maxConcurrentAgents`, `maxTeamRounds`, `maxOnCallInvocationsPerRun`, `maxCoworkerAdditionsPerRun`, and `maxAgentRunMinutes` bound parallelism, deliberation, escalations, config growth, and execution time.
- `context.defaultHandoffScope` and `context.maxFilesPerHandoff` bound what Arena forwards to provider sessions.
- `sharedContext.storage` chooses Git-tracked project files or persistent local-only entries. `agentWritePolicy` controls agent additions/revisions, `agentDeletePolicy` controls archive requests, and entry/total limits bound the store. Stale entries are marked for review rather than deleted.
- `display.colorMode`, `display.animation`, `display.gitTree`, and `display.asciiTitle` control presentation while the terminal layout continues to adapt to available space.
- `audit.retention`, `audit.redactSecrets`, and `audit.processOutput` control local message/output retention. Secret redaction is best-effort.

The TUI should show the effective policy before a run, coworker change, or persistent-context mutation. Effective limits use the stricter project and workflow value; workflow-level settings can be stricter but cannot raise a project cap or override a project-level refusal. `settings.sharedContext` in a workflow may lower entry/size caps or tighten write/archive policies; storage remains project-wide. The same rule applies to integration and audit retention. See [Shared context](SHARED-CONTEXT.md) and [Terminal interface](TERMINAL-UI.md).

When creating settings, the CLI initializes shared context as Git-tracked, accepts bounded agent additions/revisions, asks before agent archive requests, and marks entries stale after 180 days without verification. It initializes display to automatic color and animation, a visible Git tree, and the small dotted title leading the status band. Existing settings that omit these optional blocks use the same defaults until the user edits them.

## Workflow file

The workflow file defines the graph and run policy. Each agent node points to one role profile and specifies the provider runtime for that placement. `teams` and `onCall` are optional; an omitted field means an empty list. An omitted agent `activation` means `path`, preserving the original path-agent shape. See [`../schemas/workflow.schema.json`](../schemas/workflow.schema.json). The common cross-provider message envelope is defined in [`../schemas/message-envelope.schema.json`](../schemas/message-envelope.schema.json).

Core fields:

- `schemaVersion`: schema version, independent of the CLI version.
- `id`, `name`, and optional `description`: stable workflow identity and display text.
- `headAgent`: the node that receives the user's task and coordinates completion.
- `agents`: path or on-call nodes with profile path and provider runtime selector. Path nodes have an integer level; on-call nodes stay outside the normal graph.
- `teams`: composite nodes that name their member agents, facilitator, consensus policy, and output contract.
- `decisionRouters`: deterministic blocks that inspect a structured packet field and select a conditional outgoing branch.
- `onCall`: trigger rules for on-call agents. Rules can match a supported event or allow manual invocation.
- `relationships`: explicit delegation and communication links. A delegation target may be an agent or a team; communication links always connect agents.
- `settings`: concurrency, worktree, audit, and integration policy.

`settings.maxConcurrentAgents` limits simultaneously running provider processes. Team members count individually, as do the head and any active on-call agents.

Decision routers use `inputKind` and a JSON Pointer into the packet `payload`. Each branch matches one exact scalar value and points to an agent, team, or another router. Their branch edges count toward reachability and cycle checks, except a loop route: a route with `"loop": true` and `"maxIterations"` (1-5) may return to an ancestor of the router to start a bounded revision pass (see [loop routes](INTEROPERABILITY.md#loop-routes)). Every router declares what happens when the packet kind/field/value does not match: ask the user, stop, or route to a named fallback. Routes are deterministic; they do not ask an LLM to infer a branch from prose. Router IDs share the workflow node namespace, but routers are not agents, team members, or on-call targets. Incoming work uses a `delegates` relationship; conditional outgoing edges are defined by the router's route table. Peer communication links remain agent-to-agent.

Levels group path nodes visually. Relationships define ordinary task flow and peer message permissions. A team is addressed as one graph node; its members run inside the team boundary at their shared level. Team members cannot be the head or endpoints in ordinary relationships, so a member is never assigned both directly and through its team. The delegation graph must be acyclic; back edges are allowed only as explicitly capped router loop routes. Agent and team IDs share one unique namespace. Every referenced agent, team, and profile must exist exactly once. On-call agents cannot appear in relationships or teams.

## Teams

A team has at least two distinct path agents, one of whom is its facilitator. A given agent belongs to at most one team in a workflow. Team members receive the same task scope, then deliberate for a bounded number of rounds. In schema version 1, consensus is unanimous: every member must affirm the same configured verdict. If they disagree or a member cannot return a position, the team returns one packet with `outcome: "no-consensus"`, `verdict: null`, member positions, dissent, and a next action.

Every team output uses `format: "arena.team-verdict.v1"`. Its stable fields are `teamId`, `outcome`, `verdict`, `summary`, `rationale`, `evidence`, `memberPositions`, `dissent`, `rounds`, and `nextAction`. The workflow declares allowed verdict labels so downstream agents can interpret the packet consistently. A team is not complete until it emits this packet, even when the outcome is no-consensus.

## On-call agents

An on-call agent is an agent node with `activation: "on-call"` and a matching entry in the workflow's `onCall` rules. It has no graph level and no ordinary relationship edges. Trigger rules can respond to a team no-consensus result, an agent failure, a validation failure, changed paths, or manual invocation. Each rule declares a maximum invocation count per run. Automatic triggers include the triggering event ID and scoped context in the task handoff; manual calls are recorded with the invoking user. A capped, duplicate, or unmatched event is still visible in the run journal.

The first authoring flow should ask which cases deserve a specialist call and summarize the trigger before saving. It should not silently add on-call agents to the normal delegation path.

## Agent profile

Workflow profiles live under `.arena/.agents/<workflow-id>/` and describe the role's objective, instructions, capabilities, and constraints. The reserved project control profile lives at `.arena/.agents/arena-system/arena-orchestrator.agent.json`. Profiles do not contain credentials. See [`../schemas/agent.schema.json`](../schemas/agent.schema.json).

The CLI may offer built-in roles and custom role prompts. Editing a profile through the TUI should create a normal reviewable file change.

Profiles with `role: "review"` must declare `reviewContract` with `format: "arena.review-verdict.v1"` and the stable outcomes `Approved`, `ChangesRequested`, `Deny`, and `NeedsMoreInformation`. Reviewer messages use the matching `review-result` protocol kind, with evidence, severity, open questions, and a next action. A team of reviewers uses these outcomes in its team contract.

## Provider runtime values

`provider` chooses an installed adapter. `model` and optional `reasoningEffort` are passed through according to that adapter's documented interface. Arena does not maintain a fictional universal effort scale. Adapter metadata describes which options are supported and what they mean locally.

For same-machine local inference, use `provider: "local"` and set `options.backend` to `ollama`, `lm-studio`, `llama.cpp`, `vllm`, or `openai-compatible`. The model ID is required; `options.endpoint` may identify a loopback server when the adapter cannot discover its configured endpoint. If the backend, model, or endpoint is ambiguous or unavailable, the builder asks instead of choosing a model or server. The local adapter verifies the selected model and advertises its actual tool, streaming, and reasoning controls. Local agents use the same task envelopes, graph links, teams, context, worktree isolation, review, and rollback as other agents. Their model servers must stay on the same machine for the local adapter; a remote inference service requires a separately configured provider integration.

For Gemini, use `provider: "gemini"` and explicitly select `options.authMethod` as `google-ai-studio-api-key` or `vertex-ai`. The selected model ID is still required. Arena checks the selected route without collecting or writing the key or cloud credentials into project files; credential values stay in the provider-supported environment or Google Application Default Credentials. Gemini CLI personal OAuth is not a supported Arena-managed auth path; the setup flow must guide the user through Google's documented API-key or Vertex AI setup and verify readiness. If neither route is supported by the installed Arena adapter, update Arena through its supported installer before creating a Gemini agent.

Example Gemini runtime fragment (the model ID must come from the selected adapter or the user):

```json
{
  "provider": "gemini",
  "model": "<model-id-reported-by-the-selected-adapter>",
  "options": {
    "authMethod": "vertex-ai"
  }
}
```

### Antigravity command approval

Antigravity's headless mode has no per-command approval: `agy` either refuses
terminal commands or approves every tool request. By default Arena refuses to
start an Antigravity agent whose profile has `run_commands`. The user can opt a
runtime into the second behaviour:

```json
{
  "provider": "antigravity",
  "model": "<model-id-reported-by-agy-models>",
  "options": {
    "commandApproval": "sandboxed-blanket"
  }
}
```

`commandApproval` is the only Antigravity option and `sandboxed-blanket` its only
value; validation rejects anything else, and the option on any other provider.
It is never a default, the builder never asks for it, and neither the setup
interview nor the orchestrator chooses it, even when the user delegates other
choices. It is set by an explicit `--command-approval sandboxed-blanket` flag or
by editing the runtime, and coworker requests from agents cannot carry it. The
share-code import preview, `workflow create` and the interview's draft preview
name every agent that uses it.

With the option set and `run_commands` granted, Arena runs
`agy --sandbox --dangerously-skip-permissions` inside its *command fence*, which
is Linux only (bubblewrap 0.11 or newer). On macOS Arena refuses the mode: agy
keeps its sign-in in the Keychain, and `sandbox-exec` cannot let it reach that
item while keeping commands away from every other Keychain item.

The fence has two layers, because agy needs the network and its sign-in to run
inference while the commands it is told to run need neither. agy runs in an
**outer fence**; each command it launches is re-sandboxed, without the network,
in a **nested fence**. agy resolves the shell for its `run_command` tool through
`PATH` (verified: agy 1.3.6 runs `bash -c <command>`), so the fence leads `PATH`
with a directory of `bash`/`sh` wrappers that re-exec the real shell inside the
nested fence. A command that calls `bash` by an absolute path is already inside
the nested fence by then, so it cannot escape; the only assumption is that agy's
first hop uses `PATH` (it does, and its own prepended `bin` holds no shell).

| | Outer fence (agy) | Nested fence (each command) |
| --- | --- | --- |
| Root filesystem | read-only (`--ro-bind / /`) | read-only, inherited from the outer fence |
| Home directory | emptied: a tmpfs over `$HOME`, every other user's home, `/root`, `/mnt`, `/media`, `/srv`, with only an allowlist bound back read-only — agy's `~/.gemini` config, the `PATH` and toolchain directories under home (e.g. `~/.local/bin`, `GOPATH`), and `ARENA_ANTIGRAVITY_EXTRA_READS` | inherited empty home |
| Writable | the assigned worktree (writers only); a private `/tmp`; agy's state (`~/.gemini/antigravity-cli`) through a throwaway overlay, so nothing written there survives | **only** the assigned worktree (writers only) and a private `/tmp`; agy's state is hidden |
| Network | **open**: agy needs it for inference | **none** (`--unshare-net`): a fresh, empty network namespace, which also cuts off abstract Unix sockets (X11, the bus) |
| Session bus / keyring | a relay that forwards only the Secret Service calls for agy's own keyring item; other bus services (`systemd-run --user`, portals) and other keyring items are refused | hidden: the relay socket (`$XDG_RUNTIME_DIR`) is tmpfs'd and the netns has no abstract bus socket, so a command cannot reach the keyring at all |
| Also hidden | the reviewed credential table `hiddenCredentialPaths` in `internal/arena/antigravity_sandbox.go` (`~/.codex`, `~/.claude`, `~/.ssh`, `~/.gnupg`, `~/.aws`, `~/.config/gh`, `~/.docker`, `~/.kube`, browser profiles, shell history …) as defense in depth under the tmpfs home; sensitive `/etc` files; the project's `.arena/.state` except the agent's own worktree | all of the outer fence's, plus agy's state and the relay |
| Environment | locale, terminal, `PATH`, home and toolchain locations, `AGY_*`/`ANTIGRAVITY_*`; credentials, agent sockets and display variables are dropped | inherited, minus the (now unreachable) bus address |
| Processes | new PID and IPC namespaces, a new session, killed with Arena | its own new PID, IPC, UTS and network namespaces |

agy stores its sign-in in the desktop keyring through the D-Bus Secret Service.
The relay learns which keyring service agy uses from agy's own search while it
starts, before Arena sends the first turn, and then accepts searches and reads
for that service only. Arena never reads, copies or logs a keyring item; it parses
messages only for the service attribute and object paths.

Residual risks, stated plainly:

- **agy itself keeps the network and the sign-in.** This is irreducible — agy
  cannot do inference otherwise. A prompt-injected agy can still, through its own
  file tools (not a command), read whatever the outer fence leaves readable and
  describe it in a network turn. The outer fence shrinks that to agy's `~/.gemini`,
  the project, the toolchains and the worktree; the rest of the home directory
  and other credentials are gone.
- **Commands are contained but the fence is Linux-only.** On macOS the mode is
  refused, not weakened.
- **The home allowlist is the boundary, with the credential table as a second
  layer.** A secret placed inside a bound-back path (for example under
  `~/.gemini`, or an `ARENA_ANTIGRAVITY_EXTRA_READS` directory) is readable by
  agy; the credential table blanks the known ones even there.

Arena does not rely on whatever agy's own `--sandbox` restricts; the two fences
are the boundary. Use the mode for agents whose inputs you trust.

Example local runtime fragment:

```json
{
  "provider": "local",
  "model": "<model-id-reported-by-the-selected-backend>",
  "options": {
    "backend": "ollama",
    "endpoint": "http://127.0.0.1:11434"
  }
}
```

The placeholder model ID is intentional: Arena must use the model the local backend reports or the user selects, not substitute one.

## Example fixture

[`../examples/project/.arena/`](../examples/project/.arena/) illustrates a coordinator that delegates to an implementer, a cross-provider review team downstream of the implementer with a structured verdict, a review gate that loops `ChangesRequested` back to the implementer at most twice, and an on-call specialist for unresolved consensus. It scores S+ on the [score sheet](ARENA-SCORING.md). It is a format example, not a required hand-authored setup path.

## Compatibility rules

- The CLI must reject unsupported `schemaVersion` values before making changes.
- Adding optional fields is backward-compatible if old readers preserve unknown fields when rewriting.
- Removing or changing a field's meaning requires a schema version change and migration path.
- Migrations create a checkpoint and report a diff before replacing a definition.
- Agent profile references must stay within `.arena/.agents/`; path traversal and external absolute paths are rejected.
- The validator checks IDs are unique across agents and teams, valid references, supported relationships, graph acyclicity for delegation links (loop routes excepted only when capped and pointing at an ancestor of their router), reachability of every path agent directly or through a reachable team, and sensible level ordering. On-call agents are deliberately outside this reachability check.
- Teams have at least two distinct path-agent members at one shared level, exactly one member facilitator, and a configured verdict contract; an agent belongs to at most one team. Team members cannot be the head or relationship endpoints.
- On-call entries reference on-call agents exactly once, include at least one valid trigger, and enforce the per-run invocation cap. On-call agents cannot be the head, team members, or relationship endpoints.
- A delegation edge may target a team ID. Communication edges may target only path-agent IDs. Internal team deliberation does not require normal graph edges.

## Per-arena background shader

New arenas have `settings.backgroundShader` (`enabled`, `source`, `glyphs`), with
source stored in `.arena/ID.background.hlsl` next to `.arena/ID.arena.json`.
The default is a reactive dot grid that fades toward the screen edges and responds to
run, agent, question, failure, success, Git and typing activity. Existing arenas
without this optional setting get the embedded default. `arena shader create ID
"description"` uses the project default model; `set`, `default`, and `off` manage
it without hand-editing JSON. Source and settings have recoverable transactions,
and concurrent source changes invalidate an outstanding edit. See
[Background Shader Creation](BACKGROUND-SHADERS.md) for commands and HLSL limits.

## Share codes

A share code carries an arena or its shader as one line of text:

```text
arena1.<kind>.<check>.<payload>
```

- `kind` is `a` for an arena or `s` for a shader.
- `payload` is base64url without padding of raw DEFLATE over compact JSON
  following [share-bundle.schema.json](../schemas/share-bundle.schema.json):
  `{"format": "arena.share.v1", "kind", "rubric", "workflow", "profiles", "shader"}`
  for an arena, `{"format", "kind": "shader", "from", "shader"}` for a shader.
  `shader` is `{source, glyphs}`, or `null` when the arena has none.
- `check` is the first 8 hex digits of SHA-256 over `payload`. A truncated or
  edited code is rejected before decoding. Whitespace inside a code is ignored,
  so line-wrapped codes and codes inside a chat message still paste.

Export fails closed. Every string and key in the bundle is checked, and the
export is refused, naming the field but not its value, when it finds:
- credentials or keys: anything Arena's secret redaction recognizes, private
  keys, AWS/Slack/GitHub/Google/Stripe-style tokens, JWTs,
  `password=`/`token:`-style assignments and long random-looking tokens;
- details that identify a machine or person: URLs carrying a user name or
  password, home-directory paths, this project's path, email addresses,
  non-loopback IP addresses and internal host names;
- terminal control characters.

Runtime options must be ones the adapter documents, with plain values:
- Codex `config.model_reasoning_effort`/`service_tier`;
- Gemini `authMethod` and generation settings;
- local `backend`, a loopback `endpoint` and generation settings;
- Antigravity `commandApproval` (the import preview names every agent that uses it);
- no options for Claude Code.

Values are never redacted inside a code: the receiver gets exactly what the
author saw, or no code. Import runs the same checks, so a code carrying any of
these is refused rather than saved.

Codes are capped at 512 KiB and 1 MiB once inflated; export refuses an arena
whose JSON exceeds 1 MiB so every code it produces can be imported. The workflow and each
profile are validated against their schemas, runtimes are refused if their
options look secret, the profile set must match the agents exactly, and the
shader must compile. On import the graph is validated against this project's
settings and limits, the arena is renamed when its ID or shader file is taken
(rewriting every profile path and the shader source), and it is saved through a
recoverable configuration transaction. Codes never contain shared context, run
history, audit data, project settings or the orchestrator profile.
