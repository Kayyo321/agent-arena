---
name: arena-orchestrator
description: Create, inspect, edit, run, and remove Agent Arena workflows through the Arena CLI. Use when a user asks to manage an arena or coordinate agents across provider CLIs.
---

# Manage Agent Arena workflows

Use this skill only for Arena workflow management. The CLI owns generated `.arena` configuration and run state; do not create or edit those files directly.

1. Check whether `arena` is installed with `arena --help` and inspect the available command help before acting. CLI syntax may change; prefer the installed CLI's help over remembered examples.
2. For a new or changed workflow, gather only the choices the CLI cannot infer: the head agent, worker roles, provider/model selectors, which agents may message each other, and merge/review policy. Keep each provider's model and reasoning-effort values in that provider's own vocabulary.
3. Use the CLI's guided workflow builder or management commands. Let it validate the graph and write workflow and profile files. Show the resulting graph and policy summary before starting a run.
4. Before running, check provider readiness and the selected workspace isolation/review policy. The provider owns authentication; never ask for or copy passwords, browser cookies, or provider tokens.
5. During a run, use Arena's live view and structured handoffs. Forward task scope, relevant findings, files, validation results, risks, and unresolved questions; do not forward hidden chain-of-thought.
6. Before accepting changes, inspect the run history and diff. Use Arena's rollback command for a selected change; do not use a destructive Git reset to imitate rollback.
7. If the CLI is missing or lacks the requested operation, explain that the installed CLI does not support it yet. Do not fall back to hand-authoring generated `.arena` files.

For the message envelope and lifecycle rules, consult the repository's `docs/INTEROPERABILITY.md`. For merge and rollback expectations, consult `docs/AUDIT-AND-ROLLBACK.md`.
