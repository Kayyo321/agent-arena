# Terminal interface

## Screen layout

The live CLI is a full-terminal view. It keeps the requested visual hierarchy: a large ASCII `A2rena` wordmark at the upper left, a boxed vertical Git worktree tree at the upper right, the live arena graph in the main viewport, and a status row plus command prompt across the bottom.

```text
   /\     ____      ____    _____    _   _      /\   +-- git trees --------+
  /  \   |___ \    |  _ \  | ____|  | \ | |    /  \  | ● head  main         |
 / /\ \    __) |   | |_) | |  _|    |  \| |   / /\ \ | ├─ codex  codex/task  |
/ ____ \  / __/    |  _ <  | |___   | |\  |  / ____ \| └─ claude claude/rev  |
/_/  \_\|_____|    |_| \_\ |_____|  |_| \_| /_/  \_\ +----------------------+

+----------------------+            +----------------------+     +--------------------+
| HEAD · codex         |----------->| BUILDER · claude-code|---->| REVIEW · codex     |
| gpt-6.1-sol · high   |            | sonnet-5.5 · medium  |     | ...                |
| running              |            | running              |     | waiting             |
+----------------------+            +----------------------+     +--------------------+
                                               ---<!>--->

(arena loaded: code-review) (providers: codex: good, claude-code: good) (run: running)
+--------------------------------------------------------------------------------------+
> memento mori
```

The upper-left art spells `A2RENA` in a large ASCII face; the product name is written `A2rena` elsewhere in the interface. The upper-right tree is an independent bordered box. Agent cards use ASCII `+`, `-`, and `|` borders. The bottom prompt spans the terminal width. Its idle text is `> memento mori`; entering a command replaces the idle text. The single horizontal rule above the prompt has `+` at both ends, and the status row sits immediately above that rule.

## Arena graph

The graph is the primary viewport and reflects scheduler and journal state. Each agent card shows its short name, provider, model, provider-specific reasoning effort/intensity when configured, and current status. Team nodes show the team verdict/round state and can expand to member cards. Decision-router blocks show their input field, configured branch labels, and the branch selected for the current packet; an unmatched packet visibly takes the declared fallback. On-call agents occupy a distinct rail; the permanent `arena-orchestrator` is identified as the project control agent. Delegation/authority edges, router branches, and peer communication edges have distinct line styles and labels. Edge direction is always visible.

When a packet is sent, Arena animates a small type marker along the actual routed edge, then records delivered, delayed, or failed state. Examples include a task marker, a result marker, and `<!>` for denial/error. The marker may carry a short safe label such as `review` or `context`; it never displays packet bodies, prompts, or secret-like content. The animation ends when delivery is acknowledged or times out, and the event remains inspectable from the edge or event list.

The graph also shows the active task or event, the selected run state, pending user decisions, and concise failure reasons. Detailed packet payloads, provider output, and diffs open in a selected-agent or event detail view instead of overflowing the graph.

## Git tree and colors

The upper-right tree lists the base branch and every active worktree/branch used by the currently loaded arena. Each writing agent has a stable color within the run; that color is reused for its node, branch line, and tree marker. Colors identify ownership only, never health or approval. The tree refreshes on worktree and branch events and marks detached, missing, conflicted, or integrated worktrees explicitly.

The tree box may shrink or scroll when there are many worktrees. It must not hide worktree identity: selecting a row reveals full agent, branch, path, and state. Read-only agents without a dedicated worktree are shown under the base snapshot rather than as invented branches.

## Provider status

The bottom status row reports the loaded arena name, run state, pending decision count, and readiness only for providers required by that arena. The required set is the union of providers used by its configured agents plus the permanent project orchestrator when its provider is distinct. Unused installed adapters are not probed or displayed in this view. Missing or unverifiable capabilities use explicit states such as `missing`, `signed out`, `unsupported authentication`, or `unknown`; `good` means the selected provider CLI or integration is ready for the configured model and options, not that every model in a subscription is available.

`arena status` without a selected workflow shows the union required by configured workflows plus the permanent orchestrator. A workflow-specific status view shows only that workflow and its required control agent. Readiness distinguishes missing CLI, signed-out account, unsupported authentication mode, incompatible version, and unavailable model; `good` requires a compatible auth path for Arena's integration. The live view updates readiness when provider processes or configuration change.

## Fit and accessibility

Arena measures terminal dimensions and lays out the graph to fit. It first shortens labels, then hides nonessential decoration, then collapses distant graph sections behind selectable summaries. Users can pan or focus a node to inspect larger graphs. The status row and prompt stay visible. At very small sizes, the title and Git tree collapse to compact headers and the graph becomes a navigable list; no agent, edge, pending decision, or provider failure is silently dropped.

Color is supplementary: every state has a text label or symbol. `display.colorMode` follows terminal capability by default and honors `NO_COLOR`; `display.animation` follows terminal capability and can be disabled. Unicode tree connectors fall back to ASCII when the terminal cannot render them. Non-TTY output uses a readable event stream or static graph summary, never terminal escape animations. Resizing recomputes layout without changing workflow state.

## Preferences

Project `display` settings may choose color mode (`auto`, `always`, `never`), packet animation (`auto`, `on`, `off`), Git tree visibility, and title size (`show`, `compact`, `hide`). Automatic layout still protects legibility and adapts to the current terminal. These settings affect presentation only and do not change scheduling, permissions, or audit behavior.
