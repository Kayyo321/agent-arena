# Background Shader Creation

Arena evaluates its **HLSL profile v1** on the CPU and maps RGB luminance × alpha
into ASCII glyphs. This is a bounded subset, not a DirectX/GPU runtime or a general
HLSL compiler. Unsupported syntax is rejected. No graphics driver is needed.

## Authoring and storage

Every new arena gets `settings.backgroundShader` with `enabled`, `source`, and
`glyphs` in `.arena/ID.arena.json`. Its script is `.arena/ID.background.hlsl`, beside
the arena settings. Old arenas without this setting use the embedded dots preset.
The default `glyphs` is `.`: sparse dots fade from the center toward every edge.
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

Return `float4(red, green, blue, alpha)`; output is clamped to [0,1]. Non-finite
luminance becomes transparent. RGB is converted to luminance; terminal foreground
colors remain untouched. The renderer lifts midtones for visibility on charcoal terminal themes, caps
highlights below ordinary foreground text, and protects the
entire occupied span of each foreground row, including spaces within cards and
labels, with one cell of clearance. Popups are composed last as opaque panels.
Headers, status, composer, chat, detail views and tiny terminals have no shader.

Supported: `float`, `float2`, `float3`, `float4`; initialized local declarations;
parentheses; `+ - * /`; unary plus/minus; vector constructors; `xyzw` or `rgba`
swizzles; `sin cos abs sqrt floor ceil frac saturate length min max pow fmod atan2
step dot clamp lerp smoothstep`. Scalar operands broadcast in componentwise math.
Use explicit constructors when initializing vectors from scalars. Intrinsic
semantics follow [Microsoft's HLSL reference](https://learn.microsoft.com/en-us/windows/win32/direct3dhlsl/dx-graphics-hlsl-intrinsic-functions).

No assignment after declaration, loops, branches, helper functions, matrices,
textures, arrays, includes, macros, integer/bool types or shader resources. Use
branchless `step`, `smoothstep`, `lerp`, and periodic math for procedural effects.
Limits: 16 KiB source, 256 compiled operations, expression depth 32. Glyph ramps
contain 1–16 characters from ` .,:;~+=*#%@-`, ordered from faint to dense.
The `.` ramp keeps a dot at every nonzero color sample; zero produces a space.

## Inputs

Inputs are supplied by Arena; optional declarations must match these types.
All coordinates have their origin at the top left. They are visual projections,
never permissions, prompts, paths, agent identities, secrets or packet contents.

| Input | Type | Meaning |
| --- | --- | --- |
| `uv` | float2 parameter | Cell center normalized to the entire terminal [0,1] |
| `cell` | float2 | Zero-based terminal column/row |
| `resolution` | float2 | Terminal columns/rows, updated on resize |
| `aspect` | float | Width / (height × 2); assumes cells twice as tall as wide |
| `time` | float | Animation time in seconds since this view started |
| `deltaTime` | float | Time since the previous visual frame |
| `frame` | float | Visual frame counter |
| `agentCount` | float | Number of configured workflow agents |
| `runningAgents` | float | Number currently in the running state |
| `pendingPackets` | float | Number of pending/delayed audited packets |
| `pendingDecisions` | float | Number of pending user decisions |
| `runActive` | float | 1 while the run is running; otherwise 0 |

The display animation preference freezes time/frame/deltaTime at zero when off
(or auto on a dumb terminal). Static backgrounds still respond to resize and
workflow state. Large viewports or complex programs are bounded by a per-frame
sample budget. Backgrounds never modify graph, routing, or run state.
