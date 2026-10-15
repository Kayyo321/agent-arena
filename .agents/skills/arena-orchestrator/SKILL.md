---
name: arena-orchestrator
description: Create, inspect, edit, run, and remove Agent Arena workflows through the Arena CLI. Use when a user asks to manage an arena, create its ASCII background shader, or coordinate agents across provider CLIs and local model backends.
---

# Manage Agent Arena workflows

Use this skill for Arena workflow management and Background Shader Creation. The CLI owns generated `.arena` configuration and run state; do not create or edit those files directly.

1. Check whether `arena` is installed with `arena --help`, inspect command help, and run `arena status`. CLI syntax may change; prefer installed help over remembered examples. If Arena itself is missing or too old to support the requested operation, guide the user through the supported installation/update path, then re-check. Never hand-author generated `.arena` files as a substitute for a missing Arena feature.
2. Treat missing prerequisites as setup work, not as the end of the request. For a requested provider agent (including Codex, Claude Code, and Gemini), inspect readiness for that provider. If the CLI/backend or Arena adapter is missing or too old, guide the user through its official installation/update path, then re-run `arena status`. If it is signed out, use only an authentication path explicitly supported for Arena's integration; start that provider-owned flow and let the user complete it directly. Re-run `arena status` to verify the CLI, compatible account/auth path, selected model, and supported options, then continue with the requested arena change. Never ask for, read, or copy passwords, browser cookies, or provider tokens. For Gemini, do not use or piggyback on Gemini CLI's personal Google OAuth session: ask whether to use the supported API-key or Vertex AI route, guide setup using Google's official instructions, and verify it with `arena status`. Do not imply that a consumer subscription is usable through Arena unless the current official integration terms explicitly allow it. If no approved adapter/auth path exists, state what is missing and finish any independent setup guidance without creating a non-runnable agent.
3. For a local model agent, ask which supported backend and model the user wants when either is unspecified. Help install/start that backend using its official instructions, confirm the selected model is available, and re-run `arena status`. Do not substitute a model, endpoint, or backend without the user's choice. Local endpoints must meet Arena's same-machine adapter requirements.
4. Decision routers are built-in graph blocks and need no model provider. If the requested router is incomplete, ask for its input packet kind, payload field, exact branch values and targets, and fallback behavior. If the installed Arena version lacks router support, guide the user through updating Arena, verify support, then insert the configured router and validate the arena.
5. For `arena init`, check project settings and readiness first. If the default provider, model, or a supported reasoning-effort choice is missing, ask the user before launching the setup conversation (the interactive prompt lists the provider's exact model IDs and efforts). Do not select a default on their behalf unless they explicitly delegate that choice. The project default is for bootstrap and the permanent control agent; do not apply it to new workers unless the user explicitly asks.
6. For a new or changed workflow, each worker's provider, model, supported reasoning effort and local backend is the user's choice; never fill one in silently. The user may delegate a choice explicitly ("you pick", "come up with the efforts"): then choose concrete values from the provider's reported model catalog and supported efforts, state each value you chose, and do not ask again. Apply shorthand such as "all" or "same as above" to every matching agent. Propose sensible roles, profiles, edges, teams and verdict labels, router rules, on-call triggers and review policy from the request instead of asking; ask only for intent you cannot infer, and batch open questions into one message. If an adapter does not support an effort control, say so and omit that field. Routers branch only on structured packet fields, never a model's interpretation of free-form text.
7. Once all requested prerequisites are ready, continue the original task: use the CLI's guided builder or management commands, validate the graph, insert/update the requested nodes, and show the resulting graph and policy summary. Do not stop after reporting that setup succeeded.
8. Before running, check provider readiness and the selected workspace isolation/review policy. During a run, use Arena's live view and structured handoffs. Forward task scope, relevant findings, files, validation results, risks, and unresolved questions; do not forward hidden chain-of-thought. Preserve `no-consensus` as unresolved and use the standard review verdict outcomes.
8a. When Arena wakes you because a run stopped making progress, explain the blocker from the supplied evidence (pending decisions, node states, recent events) in plain words and recommend one next step. If a pending decision should be answered, name its ID and exactly one listed choice; the user applies it with `/accept`. Never answer decisions yourself.
9. If an agent needs a coworker, submit a structured request through the Arena CLI to the permanent `arena-orchestrator`. Follow `arena-subagent-modification-inquiry`: refuse, ask the user, or accept a complete request within configured limits. For agent-originated requests, ask for missing choices unless `auto-refuse` declines the request; never infer a model or reasoning effort.
10. Use the workflow's shared context for durable knowledge across runs. Search before adding duplicates; propose concise, evidence-backed entries with an honest confidence level. Follow context write/archive policies, never store secrets or hidden reasoning, and use Arena's context API rather than editing context files from an agent worktree.
11. Before accepting changes, inspect run history and diffs. Use Arena's rollback command for selected code or context changes; do not use a destructive Git reset to imitate rollback.

For reviewer verdicts, team packets, decision routers, coworker requests, shared-context packets, on-call triggers, and message lifecycle rules, consult the repository's `docs/INTEROPERABILITY.md`. For local backend setup, runtime selection, context storage, and terminal readiness, read `docs/CONFIGURATION.md`, `docs/SHARED-CONTEXT.md`, and `docs/TERMINAL-UI.md`. For provider-specific install/auth setup, follow the official references linked in `docs/INTEROPERABILITY.md`; provider installation alone does not establish that its authentication mode is supported for Arena. For project policies, merge, and rollback expectations, consult `docs/AUDIT-AND-ROLLBACK.md`.

## Background Shader Creation

Use this built-in orchestrator skill when the user describes an ASCII background
for an arena. Identify the target arena explicitly when several exist. Inspect
`arena shader --help` and `arena shader show ID`, then run
`arena shader create ID "USER DESCRIPTION"`. This command invokes the configured
project default model, validates its HLSL profile v1 output, previews it, and saves
through Arena's recoverable configuration transaction. Do not ask for a new model
choice when the project already has a complete default runtime. If it is missing
or unavailable, complete the normal runtime/readiness inquiry first.

Use `--yes` when the user has authorized applying the described background; use
`--dry-run` for a requested preview only. For supplied HLSL, use
`arena shader set ID --input FILE` with an optional `--glyphs ' .:-=+*#%@'` ramp.
Use `arena shader validate ID` and `arena shader preview ID` to inspect the saved
result. `arena shader default ID` restores fading dots; `arena shader off ID`
disables the background. Source lives at `.arena/ID.background.hlsl`, beside
`.arena/ID.arena.json`; the CLI owns writes to both. Do not hand-edit settings.

When invoked by Arena's embedded shader generator, return only the requested
structured source and glyph ramp using the supplied HLSL profile/input contract;
do not recursively call `arena shader create`. Use the numeric timing, viewport,
workflow state, event-age, focus, and envelope inputs to match the description.
Prefer envelopes and ages for smooth transitions, and `workTime` for speed changes. Preserve foreground legibility;
Arena owns compositing and contrast. Unsupported HLSL must be revised rather than
executed through shell commands, external compilers, or downloaded shader runners.
A validation failure leaves the existing shader unchanged. Explain the diagnostic
and revise the description/source before another explicitly requested generation.
