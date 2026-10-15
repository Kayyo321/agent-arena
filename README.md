# Agent Arena

Agent Arena is a planned local-first CLI for composing coding and research agents from different providers into one observable workflow. Arena coordinates the work around the provider CLIs users already run, carries messages between them, isolates code changes, and keeps a local history that people can inspect and roll back.

This repository currently contains the product and interoperability foundation. The CLI is not implemented yet.

## Product goals

- Define an arena once and let the CLI create and maintain its files.
- Show the workflow as a live graph: delegated work, peer messages, progress, and failures.
- Run teams through bounded deliberation and return one recognizable verdict packet, preserving unresolved disagreement.
- Keep specialist agents off the ordinary path and invoke them for configured events or manual escalation.
- Let agents from different providers exchange a stable handoff format.
- Give each writing agent an isolated Git worktree and control how its changes enter the project.
- Keep a local audit trail and make rollback an explicit, reviewable operation.
- Use provider-approved authentication and usage paths. Arena must not collect passwords, scrape browser sessions, copy provider tokens, pool subscriptions, or bypass usage limits.

See [Product brief](docs/PRODUCT.md), [Architecture](docs/ARCHITECTURE.md), [Interoperability and handoffs](docs/INTEROPERABILITY.md), [Audit and rollback](docs/AUDIT-AND-ROLLBACK.md), and the [Roadmap](docs/ROADMAP.md).

## Intended project files

The CLI owns this generated layout; users should not need to author it by hand:

```text
.arena/
├── <workflow-id>.arena.json
├── .agents/
│   └── <workflow-id>/
│       ├── <agent-id>.agent.json
│       └── ...
└── .state/                 # local run journal, checkpoints, and provider output
```

Workflow definitions and agent profiles are ordinary reviewable project files. `.arena/.state/` is local run data and should be ignored by Git by default. It contains no provider credentials. See [Configuration format](docs/CONFIGURATION.md) and [schemas](schemas/).

## Intended CLI experience

The commands below describe the proposed interface; they are not available in this repository yet.

```text
arena init
arena workflow create
arena workflow list
arena workflow edit <workflow-id>
arena workflow delete <workflow-id>
arena auth status
arena run <workflow-id> "Implement the requested change"
arena watch <run-id>
arena history [<run-id>]
arena rollback <run-id> <change-id>
arena skill install --agent codex|claude|all
```

`arena workflow create` should launch a guided builder that asks for a head agent, workers, provider/model settings, hierarchy, peer links, optional team membership and verdict labels, on-call trigger scenarios, and workspace/review policy, then validates and writes the configuration. Arena, rather than the developer, creates profile files and keeps them in sync.

## First-time agent setup

The repository includes the `arena-orchestrator` skill in the shared Agent Skills format. Codex discovers it from `.agents/skills/arena-orchestrator/`. Claude Code uses the companion skill entry at `.claude/skills/arena-orchestrator/`. Other hosts can use the same `SKILL.md` after installation to a supported skill directory.

The skill describes how an agent should manage arenas through the CLI. Since the CLI is not implemented yet, it will report that prerequisite rather than hand-edit generated `.arena` files.

## Example

See [`examples/project/.arena/`](examples/project/.arena/) for an illustrative cross-provider workflow with a deliberating review team, a single verdict packet, and an on-call escalation agent. It demonstrates the proposed file format; normal users should create equivalent files with the future CLI.

## Development status

The immediate product milestone is a local Git-backed CLI with two supported adapters, a live terminal graph, structured handoffs, and inspectable rollback. The full milestone breakdown and acceptance criteria are in [Roadmap](docs/ROADMAP.md).

## Provider compatibility

Provider authentication and automation support can change. Each adapter must be checked against the provider's current official documentation and terms before release. Arena should launch the provider's supported local CLI or integration, allow the provider to retain credential ownership, and show the user which account and usage path they selected. The initial OpenAI integration must be reviewed against [Sign in with ChatGPT terms](https://openai.com/policies/sign-in-with-chatgpt-terms/) and the current [ChatGPT plan integration guidance](https://help.openai.com/en/articles/20001542-using-your-chatgpt-plan-in-other-apps-and-sites). Claude Code documents sign-in with Claude Pro or Max plans in its [setup guide](https://docs.anthropic.com/en/docs/claude-code/getting-started).

## License

This project is licensed under GNU GPL v3.0; see [LICENSE](LICENSE).
