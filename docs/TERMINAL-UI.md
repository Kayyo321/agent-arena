# Terminal interface

## Opening an arena

Running `arena` without a command opens the terminal interface. A project with one
configured workflow opens its graph automatically; several workflows require an
explicit choice in a keyboard-navigable list. An empty directory or a project with
no workflows starts `arena setup`, including Git initialization when needed.
Setup asks for missing runtime choices and opens the configured default
orchestrator's interview inside Arena. Codex runs as a background app-server;
its native terminal interface is never launched. Type answers in Arena, review
the proposed graph, and use `/save` to apply it and open the graph in the same TUI.
`arena --workflow ID` selects one directly. Discovery uses the resolved project root
and never inherits workflows across a nested Git project boundary.

The initial graph is idle: it has no run ID or invented history. Required-provider
readiness checks do not start inference. The prompt accepts `run TASK` to create and
start an attached run with the normal runtime, policy and Git checks. Exiting waits
for that supervisor to stop. Non-TTY startup prints a static graph and requires an
explicit workflow ID when several are available.

## Screen layout

The live CLI is a full-terminal view. A small `A2rena` wordmark sits at the upper
right, with the Git worktree tree at the upper left. The wordmark uses dotted
Braille lettering with a lavender-to-violet gradient and no backdrop or shadow.
It is at most 27 columns by three rows, leaving the main viewport for the graph. The status
row and command prompt span the bottom.

```text
╭─ git trees ─────────────────╮                                  A2rena
│ base 1a2b3c4d               │                            (purple dots)
│ ├─ builder arena/task       │
╰────────────────────────────╯

             ╭──────────────────────────────╮
             │ HEAD · codex                 │
             │ gpt-6.1-sol · high            │
             │ running                      │
             ╰──────────────────────────────╯
                            │
                            ↓
             ╭──────────────────────────────╮
             │ BUILDER · claude-code        │
             │ sonnet-5.5 · medium           │
             │ waiting                      │
             ╰──────────────────────────────╯

code-review · codex: good, claude-code: good · running · decisions: 0
─ /help · ↑↓ history · PgUp/PgDn scroll ────────────────────────────────
> memento mori
```

The dotted letters spell `A2rena`: `A2` is larger and `rena` is lowercase, smaller
and aligned to its baseline. The compact plain-text form is `A2rena`. Narrow
or short terminals use the compact title, aligned to the right. Non-Unicode
locales and dumb terminals also use text. `NO_COLOR` removes color while preserving
the same dotted lettering. The title appears in both the chooser and
graph; `display.asciiTitle` retains its existing show/compact/hide controls.
The Git tree, agent cards and detail windows share thin, rounded Unicode borders.
ASCII borders remain the fallback for non-Unicode locales and dumb terminals.
Agent ownership colors highlight card labels; the frame and footer stay quiet.
The bottom prompt's idle text is `> memento mori`; typing replaces it. A thin
full-width rule above the composer shows the most useful keyboard controls when
space allows. The purple title keeps its existing placement and lettering.

## Prompt commands and agent windows

`/help` lists commands, and Tab completes command names and agent IDs. Ordinary
text is an answer in the setup interview or a task in the loaded workflow. Letter
keys remain text input; Ctrl+E/G/T/V open events/Git/teams/changes, and Ctrl+Q or
`/quit` exits. The workflow chooser retains its arrow/j/k/Enter/q controls.

The composer has a visible caret. Left/Right move one displayed character,
including combined accents and emoji; Home/End move to the start/end of the draft.
Alt+Left/Right (or Ctrl+Left/Right) move by word. Backspace/Delete edit at the caret,
Ctrl+W removes the preceding word, and Ctrl+U/K clear before/after the caret.
Up/Down recall submitted messages and commands, then restore the unfinished draft
and its caret when returning to the newest position. History is bounded to the last
100 distinct consecutive submissions in this UI session and is not separately
persisted. Recalling or editing history never submits a task.

Enter sends. Alt+Enter or Ctrl+J inserts a newline; Shift+Enter also works in
terminals that report it distinctly. Bracketed paste inserts at the caret and
preserves line breaks, stripping terminal control sequences. The composer wraps,
grows to at most five rows, and keeps the caret visible after resizing. PageUp/
PageDown scroll the conversation or current view; Ctrl+Home/End jump to its
beginning/latest content. Alt+Up/Down select audit events or review changes,
leaving ordinary arrows available for message editing and history.


`/view AGENT_NAME` opens a centered, live mini-window with the selected agent's
runtime, status, public progress/plan summaries, tools, and structured result.
Agent IDs and unique display names are supported; ambiguous names require an ID.
Tab switches the window between activity and context, PageUp/PageDown scrolls,
Home/End selects the beginning/latest activity, and Esc closes it. Small terminals
use a compact panel while retaining the prompt.

`/context [AGENT_NAME]` opens the context side directly. It shows the role profile,
instructions, capabilities, actual retained task envelope, and separately labelled
shared workflow entries with provenance and confidence. Missing retained packets
are reported as unavailable. Available entries are not presented as proof that
they were sent to that agent. `/refresh` reloads the selected context. The control
agent's context includes this setup conversation and its embedded skill/schema
instructions. It does not assume access to worker sessions or another workflow.

Only public activity is projected. Private reasoning items and raw thought streams
are excluded from diagnostic callbacks, context stores and these windows. Redaction
and retention policies apply to recorded setup messages, packets and activity.

## Arena graph

The graph is the primary viewport and reflects scheduler and journal state. Each agent card shows its short name, provider, model, provider-specific reasoning effort/intensity when configured, and current status. Team nodes show the team verdict/round state and can expand to member cards. Decision-router blocks show their input field, configured branch labels, and the branch selected for the current packet; an unmatched packet visibly takes the declared fallback. On-call agents occupy a distinct rail; the permanent `arena-orchestrator` is identified as the project control agent. Delegation/authority edges, router branches, and peer communication edges have distinct line styles and labels. Edge direction is always visible.

When a packet is sent, Arena animates a small type marker along the actual routed edge, then records delivered, delayed, or failed state. Examples include a task marker, a result marker, and `<!>` for denial/error. The marker may carry a short safe label such as `review` or `context`; it never displays packet bodies, prompts, or secret-like content. The animation ends when delivery is acknowledged or times out, and the event remains inspectable from the edge or event list.

The graph also shows the active task or event, the selected run state, pending user decisions, and concise failure reasons. Detailed packet payloads, provider output, and diffs open in a selected-agent or event detail view instead of overflowing the graph.

## Git tree and colors

The upper-left tree lists the base snapshot and active worktree/branch ownership
for the currently loaded arena, with a count and `/git` detail view when space runs
out. Each writing agent has a stable color within the run; that color is reused
for its node and branch line. Colors identify ownership only, never health or
approval. The tree refreshes from audited workspace state.

The tree box may shrink or scroll when there are many worktrees. It must not hide worktree identity: selecting a row reveals full agent, branch, path, and state. Read-only agents without a dedicated worktree are shown under the base snapshot rather than as invented branches.

## Provider status

The bottom status row reports the loaded arena name, run state, pending decision count, and readiness only for providers required by that arena. The required set is the union of providers used by its configured agents plus the permanent project orchestrator when its provider is distinct. Unused installed adapters are not probed or displayed in this view. Missing or unverifiable capabilities use explicit states such as `missing`, `signed out`, `unsupported authentication`, or `unknown`; `good` means the selected provider CLI or integration is ready for the configured model and options, not that every model in a subscription is available.

`arena status` without a selected workflow shows the union required by configured workflows plus the permanent orchestrator. A workflow-specific status view shows only that workflow and its required control agent. Readiness distinguishes missing CLI, signed-out account, unsupported authentication mode, incompatible version, and unavailable model; `good` requires a compatible auth path for Arena's integration. The live view updates readiness when provider processes or configuration change.

## Fit and accessibility

Arena measures terminal dimensions and lays out the graph to fit. It first shortens labels, then hides nonessential decoration, then collapses distant graph sections behind selectable summaries. Users can pan or focus a node to inspect larger graphs. The status row and prompt stay visible. At very small sizes, the title and Git tree collapse to compact headers and the graph becomes a navigable list; no agent, edge, pending decision, or provider failure is silently dropped.

Color is supplementary: every state has a text label or symbol. `display.colorMode` follows terminal capability by default and honors `NO_COLOR`; `display.animation` follows terminal capability and can be disabled. Unicode tree connectors fall back to ASCII when the terminal cannot render them. Non-TTY output uses a readable event stream or static graph summary, never terminal escape animations. Resizing recomputes layout without changing workflow state.

## Preferences

Project `display` settings may choose color mode (`auto`, `always`, `never`), packet animation (`auto`, `on`, `off`), Git tree visibility, and title size (`show`, `compact`, `hide`). Automatic layout still protects legibility and adapts to the current terminal. These settings affect presentation only and do not change scheduling, permissions, or audit behavior.
