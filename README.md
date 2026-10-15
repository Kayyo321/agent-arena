# Agent Arena

Agent Arena is a planned local-first CLI for composing coding and research agents from different providers into one observable workflow. Arena coordinates the work around the provider CLIs users already run, carries messages between them, isolates code changes, and keeps a local history that people can inspect and roll back.

This repository currently contains the product and interoperability foundation. The CLI is not implemented yet.

## Product goals

- Define an arena once and let the CLI create and maintain its files.
- Show the workflow as a live graph: delegated work, peer messages, progress, and failures.
- Run teams through bounded deliberation and return one recognizable verdict packet, preserving unresolved disagreement.
- Keep specialist agents off the ordinary path and invoke them for configured events or manual escalation.
- Let agents from different providers exchange a stable handoff format.
- Mix local model agents into the same workflow as provider CLI agents.
- Route structured outcomes through deterministic decision-router blocks.
- Keep durable, evidence-backed shared context for each workflow across runs.
- Give each writing agent an isolated Git worktree and control how its changes enter the project.
- Show agent cards, routed packet activity, provider readiness, and active worktrees in the terminal view.
- Keep a local audit trail and make rollback an explicit, reviewable operation.
- Use provider-approved authentication and usage paths. Arena must not collect passwords, scrape browser sessions, copy provider tokens, pool subscriptions, or bypass usage limits.

See [Product brief](docs/PRODUCT.md), [Architecture](docs/ARCHITECTURE.md), [Interoperability and handoffs](docs/INTEROPERABILITY.md), [Shared context](docs/SHARED-CONTEXT.md), [Terminal interface](docs/TERMINAL-UI.md), [Audit and rollback](docs/AUDIT-AND-ROLLBACK.md), and the [Roadmap](docs/ROADMAP.md).

## Intended project files

The CLI owns this generated layout; users should not need to author it by hand:

```text
.arena/
├── settings.arena.json    # project model, control agent, policies, and limits
├── <workflow-id>.arena.json
├── context/               # durable, per-workflow shared context (when Git-tracked)
├── .agents/
│   ├── arena-system/      # permanent project-level Arena orchestrator profile
│   └── <workflow-id>/
│       ├── <agent-id>.agent.json
│       └── ...
└── .state/                 # local run journal, checkpoints, provider output, local-only context
```

Workflow definitions, agent profiles, and Git-tracked shared context are ordinary reviewable project files. `.arena/.state/` is local run data and local-only context, and should be ignored by Git by default. It contains no provider credentials. See [Configuration format](docs/CONFIGURATION.md), [schemas](schemas/), and [Shared context](docs/SHARED-CONTEXT.md).

## Intended CLI experience

The commands below describe the proposed interface; they are not available in this repository yet.

```text
arena init
arena status
arena workflow create
arena workflow list
arena workflow edit <workflow-id>
arena workflow delete <workflow-id>
arena agent request <workflow-id>
arena settings show
arena settings edit
arena context list <workflow-id>
arena context search <workflow-id> <query>
arena context add <workflow-id>
arena auth status
arena run <workflow-id> "Implement the requested change"
arena watch <run-id>
arena history [<run-id>]
arena rollback <run-id> <change-id>
arena skill install --agent codex|claude|gemini|all
```

`arena agent request` is the structured coworker request path for workflow agents. It wakes the permanent project orchestrator, which follows `arena-subagent-modification-inquiry` and records its decision.

`arena status` reports only providers required by the selected arena. Without a selected workflow, it reports the union required by configured workflows plus the permanent orchestrator. It includes installed/version state, sign-in readiness, and only model/effort options the adapter can verify. It must label unknown model access instead of claiming that every subscription model is available. See [Terminal interface](docs/TERMINAL-UI.md) for the status row and layout rules.

Each workflow has durable shared context for short, evidence-backed facts and decisions that help future runs. Agents use Arena's context API to propose additions or revisions; accepted entries preserve provenance and can be restored through the audit history. See [Shared context](docs/SHARED-CONTEXT.md).

In a project folder, `arena init` should create `.arena/settings.arena.json`, install the shared skill for the supported provider CLIs that will host agents, and start a guided setup conversation with the chosen default model. If the default provider, model, or supported reasoning effort is missing, ask the user to choose before starting the conversation. The selected model becomes the permanent on-call Arena orchestrator for that project; it does not silently become the model for new workers. The orchestrator helps create workflows and profiles through Arena's CLI operations; it does not hand-edit generated files.

The guided setup asks for missing choices rather than inventing them. For each requested worker, ask for provider, model, and reasoning effort when the adapter supports it. For local models, ask which supported backend and model to use if status cannot identify them. A reviewer uses the standard review outcome contract. Setup also asks for hierarchy, peer links, team membership and verdict labels, decision-router packet fields and branches, on-call triggers, and workspace/review policy, then validates and writes the configuration through Arena.

## First-time agent setup

The repository includes the `arena-orchestrator` skill in the shared Agent Skills format. Codex discovers it from `.agents/skills/arena-orchestrator/`. Claude Code uses the companion skill entry at `.claude/skills/arena-orchestrator/`. Gemini CLI also supports `.agents/skills/` as a workspace skill directory. Arena installs the same `SKILL.md` to the selected provider's documented location and verifies that the provider can discover it.

The skill describes how an agent should manage arenas through the CLI. Since the CLI is not implemented yet, it will report that prerequisite rather than hand-edit generated `.arena` files.

## Example

See [`examples/project/.arena/`](examples/project/.arena/) for project settings, the permanent orchestrator profile, a shared-context entry, and an illustrative cross-provider workflow with a deliberating review team, standard reviewer outcomes, and an on-call escalation agent. It demonstrates the proposed file format; normal users should create equivalent files with the future CLI.

## Development status

The immediate product milestone is a local Git-backed CLI with Codex, Claude Code, and Gemini adapters, the A2rena terminal layout, structured handoffs, per-workflow shared context, and inspectable rollback. Gemini uses an explicitly supported API-key or Vertex AI route; Arena must not piggyback on Gemini CLI's personal OAuth session. The full milestone breakdown and acceptance criteria are in [Roadmap](docs/ROADMAP.md).

## Provider compatibility

Provider authentication and automation support can change. Each adapter must be checked against the provider's current official documentation and terms before release. Arena should launch the provider's supported local CLI or integration, allow the provider to retain credential ownership, and show the user which account and usage path they selected. The initial OpenAI integration must be reviewed against [Sign in with ChatGPT terms](https://openai.com/policies/sign-in-with-chatgpt-terms/) and the current [ChatGPT plan integration guidance](https://help.openai.com/en/articles/20001542-using-your-chatgpt-plan-in-other-apps-and-sites). Claude Code documents sign-in with Claude Pro or Max plans in its [setup guide](https://docs.anthropic.com/en/docs/claude-code/getting-started). Gemini CLI's [official FAQ](https://github.com/google-gemini/gemini-cli/blob/main/docs/resources/faq.md) prohibits third-party OAuth piggybacking and recommends API key or Vertex AI for third-party agents; Arena must follow that boundary.

## License

This project is licensed under GNU GPL v3.0; see [LICENSE](LICENSE).
