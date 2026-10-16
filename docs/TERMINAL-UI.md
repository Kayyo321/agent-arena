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
The chooser shows each arena's grade; `c` copies the selected arena as a share
code, `C` copies only its shader, and `p` (or pasting a code) previews an arena
code for import, saved with Enter. `arena --workflow ID` selects one directly. Discovery uses the resolved project root
and never inherits workflows across a nested Git project boundary.

The initial graph is idle: it has no run ID or invented history. Required-provider
readiness checks do not start inference. The prompt accepts `run TASK` to create and
start an attached run with the normal runtime, policy and Git checks. Exiting waits
for that supervisor to stop. Non-TTY startup prints a static graph and requires an
explicit workflow ID when several are available.

## Screen layout

The live CLI is a full-terminal view. A three-row status band spans the top and
is closed by a hairline that starts in the brand color under the title. The small
`A2rena` wordmark leads the band from a two-column left margin; the run's vital
signs sit beside it in labelled columns, and the Git tree closes the band flush
right on wide terminals. The wordmark uses dotted Braille lettering with a
lavender-to-violet gradient and no backdrop or shadow. It is at most 27 columns by
three rows, leaving the main viewport for the graph. While animation is on, one
letter at a time spins in place about its vertical axis, `A` first and the final
`a` last, then `A` again: it narrows to its edge, turns mirrored and returns,
eight 150 ms frames per letter. Each letter stays in its own columns, so the
wordmark's position and width never change. The text fallback, `NO_COLOR`,
`display.animation: off` (reduced motion) and non-TTY output keep the static
wordmark. The prompt spans the bottom.

```text
  A2rena    ledger-loop          ACTIVE          NODES         CHANGES             GIT  ca79e2f · 13 worktrees
  (dots)    ⠋ running  4m12s     ● tester        ✓ 4/6 done    +342 −13            ├ builder  +186 −0
            ━━━ ━━━ ━━━ ━━━      ◌ 1 queued      0 failed      33 files            └ tester   +101 −13  +1 more
────────────────────────────────────────────────────────────────────────────────────────────────────────────

                     ┏━ ◆ HEAD · lead ━━━━━━━━ codex ┓
                     ┃ gpt-6.1-sol · high            ┃
                     ┃ ✓ succeeded                   ┃
                     ┗━━━━━━━━━━━━━━┯━━━━━━━━━━━━━━━━┛
                                    ▼ delegates
                     ╭─ builder ───────────── codex ╮
                     │ sonnet-5.5 · medium          │
                     │ ⠋ running             pass 2 │
                     │ +186 −0 · 16 files           │
                     ╰──────────────────────────────╯
```

The band reports, from audited state: the workflow ID, run status with the
loading animation while running, elapsed time since the run started, and one
progress segment per agent colored by its state; the agents running now, then
pending decisions (`⚑ N waiting on you`), queued agents or packets in flight;
completed and failed node counts; and line changes (`+added −deleted`) and files
summed across every writing worktree. Each worktree is measured against its own
base, so loop passes that build on earlier changes are not counted twice; a
removed worktree falls back to its recorded change patch. Worktrees are scanned
off the UI loop at most every two seconds. The Git column shows the run base,
the worktree count and the agents with the most churn.

Columns join by priority while they fit: identity, changes, activity, nodes, Git.
Without the Git column the worktree count joins the change size, and without the
changes column the change size sits beside the workflow ID, so it is never
dropped. Narrow or short terminals use a single row led by the compact title.
Non-Unicode locales and dumb terminals use text. `NO_COLOR` removes color while
preserving the same layout. The workflow chooser and `arena setup` lead with the
same wordmark at the left margin; `display.asciiTitle` retains its existing
show/compact/hide controls.

Graph nodes are told apart by shape before color. The head agent has a heavy
violet frame with a `◆ HEAD` badge; worker agents have rounded frames; teams have
dashed amber frames with a `◈ TEAM` badge and a roster of member states; and
decision routers have chamfered teal frames with a `◇ ROUTER` badge, their
input, routes and the branch taken. The name and provider sit in the top border,
so each card keeps three rows for runtime, status and detail: an agent's line
changes across its loop passes once worktree statistics exist, and its role
profile before that. Frames are dim until a node starts, bright while it works,
and red when it fails; delegation edges are grey, router branches teal and
capped loops amber. ASCII borders remain the fallback for non-Unicode locales
and dumb terminals, with a distinct frame per kind.
The bottom prompt's idle text is `> memento mori`; typing replaces it. A thin
full-width rule above the composer shows only `/help`. Run status and required
provider readiness stay in the graph and `/status`, with pending questions in `/decisions`.

## Prompt commands and agent windows

`/help` lists commands. Typing `/` shows matching commands, or agent IDs after `/view` and `/context`, in a menu above the prompt; ↑/↓ move the highlight and Tab or Enter fills it in without running a partial command. Ordinary
text is an answer in the setup interview or a task in the loaded workflow. Letter
keys remain text input; Ctrl+E/G/T open events/Git/teams, `/changes` opens the review queue,
and Ctrl+V pastes clipboard images or text, and Ctrl+Q or
`/quit` exits. The workflow chooser retains its arrow/j/k/Enter/q controls.

The composer has a visible caret. Left/Right move one displayed character,
including combined accents and emoji; Home/End move to the start/end of the current line.
Alt+Left/Right (or Ctrl+Left/Right) move by word. Backspace/Delete edit at the caret,
Ctrl+W removes the preceding word, and Ctrl+U/K clear before/after the caret.
Up/Down move through visual lines in multiline or wrapped text; in a single line
they recall history. Ctrl+P/N always recall submitted messages and commands,
then restore the unfinished draft
and its caret when returning to the newest position. History is bounded to the last
100 distinct consecutive submissions in this UI session and is not separately
persisted. Recalling or editing history never submits a task.

Enter sends. Shift+Enter inserts a newline in terminals that support enhanced
keyboard reporting (requested automatically). Alt+Enter or Ctrl+J also inserts a
newline, including in older terminals that cannot distinguish Shift+Enter. Bracketed paste inserts at the caret and
preserves line breaks, stripping terminal control sequences. The composer wraps,
grows to at most five rows, and keeps the caret visible after resizing. PageUp/
PageDown and the mouse wheel scroll the conversation, current view or open agent
window; Ctrl+Home/End jump to its beginning/latest content. Arena captures the
wheel so terminals cannot turn it into Up/Down history recall; hold Shift while
dragging to select text with the terminal. Alt+Up/Down select audit events or
review changes, leaving ordinary arrows available for message editing and history.

Pending decisions are pinned above the composer with their choices. When only
one is pending, typing one of its choices answers it (`/answer TEXT` also omits
the ID). If a run stops making progress (nothing executing and a decision older
than 15 seconds, or no events for a minute) Arena wakes the permanent
`arena-orchestrator` once per stall (at most three times per run). It reads the
run's evidence and pins a short diagnosis. For a router fallback it picks a
branch and Arena applies it (policy `orchestrator-stall-recovery: answer-routing`,
the default); the banner counts down so you can answer first. Other decisions,
or `recommend-only`, get a recommendation that `/accept` applies. Every
diagnosis and applied answer is journaled as `orchestrator.stall-diagnosed`. The note clears from the banner once the run ends; the journal keeps it. A router waiting on you shows `⚑ waiting for you`.

`/consult QUESTION` asks the same orchestrator about the loaded arena or run at
any time, in the conversation view; typing there asks a follow-up and `/graph`
returns. It only advises: an edit it proposes appears as a scored revision
preview that `/save` applies. See [CLI](CLI.md) for what it is sent.

A line starting with `$` runs that shell command in the project root with your
environment (two-minute limit, 256 KiB of output) and shows the output in a
panel; the command and exit code are journaled in project history.

Graph edges leave a card through a `┬` on its bottom border and end in `▼`
just above the target. Cards that are working show the same rotating loading
animation as the status line.

The orchestrator transcript opens each message with a full-width rule in the
speaker's label color: a heavy rule labelled in cyan for you, purple for `arena-orchestrator`, and
amber for Arena notices (failures in red). Agent replies are rendered from
Markdown by Arena: headings, bold/italic/strikethrough, inline code on a dark
background, fenced code blocks, quotes, lists, rules, tables and links. Provider
escape sequences are stripped before Arena applies its own styles. While work is
in progress the status line shows a Unicode loading animation that switches to a
different set about every four seconds; `display.animation: off` (or a
non-UTF-8 locale) shows a static ellipsis instead.


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

The bottom bar shows only `/help`. The graph reports the loaded arena and run state; `/decisions` lists pending questions, and `/status` reports readiness only for providers required by that arena. The required set is the union of providers used by its configured agents plus the permanent project orchestrator when its provider is distinct. Unused installed adapters are not probed or displayed in this view. Missing or unverifiable capabilities use explicit states such as `missing`, `signed out`, `unsupported authentication`, or `unknown`; `good` means the selected provider CLI or integration is ready for the configured model and options, not that every model in a subscription is available.

`arena status` without a selected workflow shows the union required by configured workflows plus the permanent orchestrator. A workflow-specific status view shows only that workflow and its required control agent. Readiness distinguishes missing CLI, signed-out account, unsupported authentication mode, incompatible version, and unavailable model; `good` requires a compatible auth path for Arena's integration. The live view updates readiness when provider processes or configuration change.

## Plain streams for terminal panes

The live view is the only interactive surface, but its two read-only projections are
available as line-oriented text for a pipe, a script or another terminal pane:

- `arena watch RUN --json --follow` is the event feed. It prints one flushed JSON line
  per journal event, in sequence order and never twice, and exits when the run reaches
  a terminal state or on interrupt. Without `--follow` it prints the journal once.
- `arena view AGENT --run RUN [--follow]` is the agent activity window (`/view AGENT`)
  as plain text, with the same exit behaviour. It states when raw process output is not
  retained (`audit.processOutput` other than `full`, or reduced run retention) and falls
  back to the recorded output size, as the window does.

Both poll the run journal on the live view's 150 ms tick and hold no state of their own,
so what they print always agrees with the audited history. Provider text keeps the live
view's rules: redacted, and without escape or control sequences. Their output is the
building block for panes that mirror a run; see [CLI](CLI.md#structured-terminal-streams)
for flags and exit behaviour.

## Fit and accessibility

Arena measures terminal dimensions and lays out the graph to fit. It first shortens labels, then hides nonessential decoration, then collapses distant graph sections behind selectable summaries. Users can pan or focus a node to inspect larger graphs: `/pan` moves a camera over the graph with the arrow keys, keeps the graph block's height so the rows below it stay put, and Space snaps back to the centered layout. The `/help` bar and prompt stay visible. At very small sizes, the title and Git tree collapse to compact headers and the graph becomes a navigable list; no agent, edge, pending decision, or provider failure is silently dropped.

Color is supplementary: every state has a text label or symbol. `display.colorMode` follows terminal capability by default and honors `NO_COLOR`; `display.animation` follows terminal capability and can be disabled. Unicode tree connectors fall back to ASCII when the terminal cannot render them. Non-TTY output uses a readable event stream or static graph summary, never terminal escape animations. Resizing recomputes layout without changing workflow state.

## Preferences

Project `display` settings may choose color mode (`auto`, `always`, `never`), packet animation (`auto`, `on`, `off`), Git tree visibility, and title size (`show`, `compact`, `hide`). Automatic layout still protects legibility and adapts to the current terminal. These settings affect presentation only and do not change scheduling, permissions, or audit behavior.

## ASCII shader background

Each arena can select a CPU-evaluated HLSL-profile shader behind its graph. Shaders
receive numeric projections of run, agent, packet, decision, Git, provider,
journal and composer state, including smoothed envelopes and event ages. The
shipped reactive dots fade toward every screen edge. They pulse amber around an
agent waiting on the user, ripple from active agents, and tint red on failure and
green on success. Color terminals show the shader's hue. Full occupied text spans, card
interiors, headers, footer, composer and popups stay opaque. Detail/chat views and
very small terminals omit the decoration. `NO_COLOR` uses monochrome dithering;
`display.animation` freezes animation inputs and transient effects when off. Invalid/missing source
shows a diagnostic and disables the background, leaving the graph usable.

`/shader DESCRIPTION` invokes the default orchestrator model and previews validated
source; `/shader-save` applies it, `/shader-off` disables it, and `/refresh` reloads
external CLI changes. See [the shader contract](BACKGROUND-SHADERS.md).

## Arena score

The identity column of the status band shows the arena's grade beside its name,
from S+ down to F, and the compact one-row header keeps it after the name. While
the setup conversation has a draft, the band and the draft message show the
draft's grade and its costliest deductions. `/score` opens the breakdown for the
draft or the open arena: points per category, each finding with its fix, the
ceilings that capped the grade, and what the next tier needs. The score is
computed by Arena from the workflow and profiles; see
[Arena scoring](ARENA-SCORING.md).

## Sharing arenas and shaders

`/copy` copies the open arena as a one-line share code and `/copy-shader` copies
only its shader. Arena writes the code with `pbcopy` on macOS or `wl-copy`,
`xclip` or `xsel` on Linux, and also sends it to the terminal clipboard with OSC
52 so copying works over SSH. Pasting a share code anywhere in the composer,
through the terminal's paste or Ctrl+V, never inserts it as text:
- an arena code opens an import draft in the orchestrator chat with its graph,
  grade, trust notes (who writes, who runs commands, auto-if-clean) and the
  readiness of each runtime; `/save` imports it, and typing asks the
  orchestrator to revise it first;
- a shader code opens the shader preview for the open arena, and `/shader-save`
  applies it.

## Attachments and clipboard

`/attach PATH [PATH ...]` snapshots images and other regular files for the next
message. Quote paths containing spaces or use shell-escaped paths. Dropping files
into an empty composer also attaches them when the terminal sends their absolute
paths as a bracketed paste. `/attachments` lists names, types and sizes;
`/detach NUMBER` or `/detach all` removes pending files. A compact attachment row
appears above the draft. Enter sends the text and files together. Failed starts
preserve the draft and attachments.

Ctrl+V or `/paste` reads the system clipboard: an image becomes an attachment and
text is inserted at the caret. macOS uses AppKit through the system `osascript`,
including TIFF-to-PNG conversion for screenshots; Linux uses `wl-paste` on Wayland
or `xclip` on X11. Clipboard reading is asynchronous, bounded and cancellable.
Cmd+V is owned by the terminal; terminals that only forward text cannot forward
clipboard image bytes, so use Ctrl+V or `/paste` for images. Clipboard failures
leave the existing draft intact. Arena writes to the clipboard only for `/copy`,
`/copy-shader` and the chooser's copy keys.

Limits are eight files, 10 MiB per file and 32 MiB total (also the cumulative
attachment limit for an orchestrator chat). PNG, JPEG, GIF and WebP are delivered
as native image inputs to Codex. Other adapters explicitly refuse image input
before starting; model vision capability still depends on the selected model.
Codex receives authorized read-only snapshot paths for other file types. Mediated
adapters can read attached files up to 512 KiB through `read_attachment` (UTF-8 text
or base64 for binary files); this does not claim that a model can interpret every
binary format. Clipboard and attachments never start a run by themselves.

Attachment content is untrusted source material, not instructions. Sending files
requires full-local retention: exact snapshots, which may include sensitive data,
are retained in private `.arena/.state/RUN/attachments/` storage; they are not
redacted or committed to Git. Draft attachments stay in memory. Metadata is audited
in task envelopes, and snapshot hashes are verified before each worker invocation.
Reduced-retention modes refuse attachments rather than silently storing raw bytes.
Orchestrator chat snapshots live in the project control history and remain available
on subsequent turns. `/detach` affects only pending files, not already sent history.
