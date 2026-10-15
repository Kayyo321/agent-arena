# Background Shader Creation

Arena evaluates its **HLSL profile v1** on the CPU and maps RGB luminance × alpha
into ASCII glyphs. This is a bounded subset, not a DirectX/GPU runtime or a general
HLSL compiler. Unsupported syntax is rejected. No graphics driver is needed.

## Authoring and storage

Every new arena gets `settings.backgroundShader` with `enabled`, `source`, and
`glyphs` in `.arena/ID.arena.json`. Its script is `.arena/ID.background.hlsl`, beside
the arena settings. Old arenas without this setting use the embedded preset.
The default `glyphs` is `.`. The default shader, reactive dots, is a sparse dot field that fades
from the center toward every edge and responds to almost every input below:

- At rest it breathes slowly; each arena's `seed` offsets the phase.
- Work (`energy`, `activity`) speeds up a drifting aurora through `workTime`,
  fills in an offset dot grid and tints toward cyan.
- Every journal event sends a ring out from the watched agent (`eventAge`,
  `focus`); a standing wave stays while activity is high.
- A question for the user (`attention`, `awaitingUser`, `decisionAge`) pulses
  amber around the asking agent and at the screen rim. The pulse gets stronger
  the longer the question waits.
- Failures (`tension`, `failureAge`) flash red and flicker interference rows.
  Successes (`success`, `successAge`) sweep a green shimmer across the field.
- Git changes (`changeAge`) fall from the top in violet. Context lookups
  (`contextAge`) sparkle. A run start (`startAge`) briefly lifts the field.
- Typing (`typing`) sends ripples up from the composer. `progress` brightens the
  field behind a completion front. Pausing or interruption dims the breathing.

On monochrome terminals, ordered dithering turns brightness into dot density.

Use Arena commands to change the generated settings and script:

```sh
arena shader create ID "slow ripples reacting to running agents"
arena shader set ID --input /path/to/custom.hlsl --glyphs ' .:-=+*#%@'
arena shader preview ID
arena shader validate ID
arena shader show ID
arena shader default ID
arena shader off ID
```

`create` invokes the project's explicitly configured default provider/model/effort,
under the permanent orchestrator lock, readiness check and invocation time limit.
It makes one generation attempt, validates before saving, and shows a preview.
Use `--yes` to apply without a prompt, or `--dry-run` to generate/preview only.
An invalid model result leaves the previous shader intact; revise the description
or use `set` with corrected source. No provider is started by viewing a background.
`set` copies the source into the managed location; the external file is not linked.
Changes retain the previous source/settings in recoverable configuration transactions.
Edits conflict if settings, profiles, or shader source changed since loading.

In the graph use `/shader DESCRIPTION` to generate a preview with the default model,
then `/shader-save` to apply it. `/shader-off` disables the current background.
`/refresh` reloads a shader changed from another CLI session.

## HLSL profile v1

Write optional global uniform declarations and exactly one function:

```hlsl
float time;
float aspect;
float4 main(float2 uv) : SV_Target {
    float2 p = (uv - 0.5) * float2(aspect, 1.0);
    float wave = 0.5 + 0.5 * sin(length(p) * 24.0 - time);
    return float4(wave, wave, wave, 1.0);
}
```

Return `float4(red, green, blue, alpha)`. Luminance × alpha, clamped to [0,1],
chooses the glyph and its brightness. Non-finite luminance becomes transparent. On
color terminals the RGB ratio tints the glyph: equal channels stay neutral grey;
other hues are softened and kept at the same lifted brightness. To change hue
without changing brightness, divide by the color's luminance, as the default does.
Terminal foreground colors remain untouched. The renderer lifts midtones for
visibility on charcoal terminal themes, caps
highlights below ordinary foreground text, and protects the
entire occupied span of each foreground row, including spaces within cards and
labels, with one cell of clearance. Popups are composed last as opaque panels.
Headers, status, composer, chat, detail views and tiny terminals have no shader.

Supported: `float`, `float2`, `float3`, `float4`; initialized local declarations;
parentheses; `+ - * /`; unary plus/minus; vector constructors; `xyzw` or `rgba`
swizzles; `sin cos tan asin acos atan atan2 abs sign sqrt rsqrt rcp exp exp2 log
log2 pow floor ceil round trunc frac fmod saturate min max clamp step lerp mad
smoothstep length distance dot normalize`. Scalar operands broadcast in componentwise math.
Use explicit constructors when initializing vectors from scalars. Intrinsic
semantics follow [Microsoft's HLSL reference](https://learn.microsoft.com/en-us/windows/win32/direct3dhlsl/dx-graphics-hlsl-intrinsic-functions).

No assignment after declaration, loops, branches, helper functions, matrices,
textures, arrays, includes, macros, integer/bool types or shader resources. Use
branchless `step`, `smoothstep`, `lerp`, and periodic math for procedural effects.
Limits: 16 KiB source, 512 compiled operations, expression depth 32. Only the
inputs a program references count as operations, and equal literals share one.
Glyph ramps
contain 1–16 characters from ` .,:;~+=*#%@-`, ordered from faint to dense.
The `.` ramp keeps a dot at every nonzero color sample; zero produces a space.

## Inputs

Inputs are supplied by Arena; optional declarations must match these types.
Locals cannot reuse an input name. All coordinates have their origin at the top left. They are
visual projections, never permissions, prompts, paths, agent identities, secrets
or packet contents. Every input is numeric; nothing from message text reaches a shader.

Value kinds:

- **Counts** are whole numbers.
- **Flags** are 0 or 1.
- **Ages** are seconds since the latest matching journal event, or 10000 if none
  has happened. They grow continuously, so `exp(-age * k)` makes a smooth pulse
  that decays and `abs(distance - age * speed)` makes an expanding ring.
- **Envelopes** are 0–1 levels that Arena eases toward the current state on every
  frame. Questions, failures and work fade in and out instead of switching.

### Viewport and clocks

| Input | Type | Meaning |
| --- | --- | --- |
| `uv` | float2 parameter | Cell center normalized to the entire terminal [0,1] |
| `cell` | float2 | Zero-based terminal column/row |
| `resolution` | float2 | Terminal columns/rows, updated on resize |
| `aspect` | float | Width / (height × 2); assumes cells twice as tall as wide |
| `time` | float | Animation time in seconds since this view started |
| `deltaTime` | float | Time since the previous visual frame |
| `frame` | float | Visual frame counter |
| `workTime` | float | Clock that runs faster with `energy` and `activity`; use it instead of `time × speed` so speed changes never jump phase |
| `seed` | float | Stable per-arena value in [0,1) derived from the arena ID |

### Run lifecycle

| Input | Type | Meaning |
| --- | --- | --- |
| `runActive` | float | Flag: the run is running |
| `runPaused` | float | Flag: the run is paused |
| `runInterrupted` | float | Flag: the run was interrupted |
| `runFailed` | float | Flag: the run failed |
| `runSucceeded` | float | Flag: the run succeeded |
| `runCancelled` | float | Flag: the run was cancelled |
| `runIdle` | float | Flag: no run, or a run not yet started; exactly one lifecycle flag is 1 |
| `runTime` | float | Seconds since the run first started; 0 without one |

### Agents, packets and decisions

| Input | Type | Meaning |
| --- | --- | --- |
| `agentCount` | float | Number of configured workflow agents |
| `runningAgents` | float | Count of agents running |
| `queuedAgents` | float | Count of agents queued |
| `waitingAgents` | float | Count of agents waiting |
| `succeededAgents` | float | Count of agents that succeeded |
| `failedAgents` | float | Count of agents that failed |
| `cancelledAgents` | float | Count of agents cancelled |
| `interruptedAgents` | float | Count of agents interrupted |
| `pendingPackets` | float | Pending or delayed audited packets |
| `deliveredPackets` | float | Delivered packets |
| `failedPackets` | float | Failed packets |
| `totalPackets` | float | All packets sent in the run |
| `pendingQuestions` | float | Pending packets of kind question |
| `pendingTasks` | float | Pending packets of kind task |
| `pendingResults` | float | Pending result, review-result and team-result packets |
| `pendingDecisions` | float | Pending user decisions |
| `awaitingUser` | float | Flag: an agent is waiting on the user (a pending decision, a setup question, or a question packet addressed to the user) |
| `decisionAge` | float | Seconds the oldest pending decision has waited; 0 when none |

### Git, teams, providers and journal

| Input | Type | Meaning |
| --- | --- | --- |
| `changes` | float | Proposed changes in the run |
| `changesPending` | float | Changes not yet integrated, conflicted, refused or superseded |
| `changesIntegrated` | float | Integrated changes |
| `changesConflicted` | float | Conflicted changes |
| `worktrees` | float | Isolated worktrees |
| `writingWorktrees` | float | Worktrees with write access |
| `teamRounds` | float | Team deliberation rounds recorded |
| `providersReady` | float | Required providers that can start |
| `providersBlocked` | float | Required providers checked and unable to start |
| `eventCount` | float | Journal events in the run |

### Event ages

| Input | Type | Meaning |
| --- | --- | --- |
| `eventAge` | float | Any journal event |
| `messageAge` | float | Any `message.*` event (sent, delivered, delayed, failed) |
| `progressAge` | float | Agent progress, plans, provider activity, process output or usage |
| `startAge` | float | Workflow start/resume, agent start or team start |
| `successAge` | float | Agent success, node completion, workflow success, team consensus, completed rollback |
| `failureAge` | float | Any `*.failed` event, team no-consensus, refused coworker or interruption |
| `changeAge` | float | Change, checkpoint, rollback, worktree or integration events |
| `decisionRequestAge` | float | A decision was requested from the user |
| `decisionAnswerAge` | float | A decision was answered |
| `contextAge` | float | Any `context.*` event |
| `teamAge` | float | Any `team.*` event |
| `inputAge` | float | The user last typed or pasted in the composer |

### Composer and focus

| Input | Type | Meaning |
| --- | --- | --- |
| `promptLength` | float | Characters in the composer |
| `attachments` | float | Files attached to the composer |
| `busy` | float | Flag: Arena is starting a run, generating a reply or shader, or reading the clipboard |
| `errorActive` | float | Flag: an error is shown |
| `focus` | float2 | uv of the agent most worth watching. Priority: the agent asking the user, then the most recently active running agent, then the most recent failure |
| `focusStrength` | float | Envelope: 1 for a question, 0.8 for running work, 0.7 for a failure, 0 when there is no focus or its card is off screen. It fades in again when the focus moves |

### Envelopes

| Input | Type | Meaning |
| --- | --- | --- |
| `energy` | float | Share of agents running or queued, plus packet flow, damped while not running |
| `activity` | float | Recent journal event rate; each event adds an impulse that decays over seconds |
| `attention` | float | 1 while anything waits on the user; 0.35 for questions between agents |
| `tension` | float | Failed run or agents, conflicts, failed packets, errors, blocked providers, interruption |
| `success` | float | Recent successes and a succeeded run |
| `progress` | float | Share of agents finished; 1 once the run succeeds |
| `typing` | float | Composer typing activity |

The display animation preference freezes time/frame/deltaTime/workTime at zero
when off (or auto on a dumb terminal). Envelopes then snap to the current state;
`activity` and `typing` are 0, and every event age is 10000, so no transient
effects play. Static backgrounds still respond to resize and workflow state.
Arena computes everything that does not depend on `uv` or `cell` once per frame,
so state-derived math is nearly free. Only per-cell operations count against
the per-frame sample budget for large viewports. Backgrounds never modify graph,
routing, or run state.
