// Arena HLSL profile v1 default: reactive dots.
// A sparse dot field that breathes while idle and comes alive with the arena:
// work quickens a drifting aurora and fills in extra dots, every journal event
// sends a ripple out from the agent being watched, a question for you pulses
// amber around the asking agent, failures tint red and flicker, successes
// sweep a green shimmer, Git changes fall like rain and typing ripples up from
// the composer. All motion is driven by smoothed envelopes and event ages, so
// state changes ease in and out instead of snapping.
float2 cell;
float time;
float aspect;
float workTime;
float seed;
float runActive;
float runPaused;
float runInterrupted;
float runSucceeded;
float runFailed;
float awaitingUser;
float decisionAge;
float eventAge;
float successAge;
float failureAge;
float changeAge;
float contextAge;
float startAge;
float2 focus;
float focusStrength;
float energy;
float activity;
float attention;
float tension;
float success;
float progress;
float typing;

float4 main(float2 uv) : SV_Target {
    // Layout: edge vignette and the base 3x2 dot lattice.
    float2 edge = abs(uv * 2.0 - 1.0);
    float rim = max(edge.x, edge.y);
    float fade = 1.0 - smoothstep(0.0, 1.0, rim);
    float soft = 1.0 - smoothstep(0.3, 1.05, rim);
    float2 grid = frac(cell / float2(3.0, 2.0));
    float base = (1.0 - step(0.3, grid.x)) * (1.0 - step(0.4, grid.y));
    // Offset lattice that fills in as the arena gets busy or needs you.
    float2 offset = frac((cell + float2(1.5, 1.0)) / float2(3.0, 2.0));
    float between = (1.0 - step(0.3, offset.x)) * (1.0 - step(0.4, offset.y)) * (1.0 - base);

    // State-wide moods (cell independent; evaluated once per frame).
    float hold = runPaused + runInterrupted;
    float breathe = 0.95 + 0.05 * sin(time * 0.7 + seed * 6.2832) - hold * 0.12 * (0.5 + 0.5 * sin(time * 0.35));
    float pulse = 0.5 + 0.5 * sin(time * 3.2);
    float wake = exp(-startAge * 0.9);
    float settle = success * (0.6 + 0.4 * runSucceeded);

    // Drifting aurora; workTime runs faster while agents work.
    float2 p = (uv - 0.5) * float2(aspect, 1.0);
    float warp = sin(p.y * 3.7 - workTime * 0.41 + seed * 3.0) * 1.4;
    float flow = sin(p.x * 2.6 + workTime * 0.33 + warp) * cos(p.y * 2.3 - workTime * 0.27 + seed * 5.0);
    float aurora = 0.5 + 0.5 * flow;
    float auroraLift = (aurora - 0.5) * (0.12 + 0.35 * energy);

    // Ripples from the watched agent: one ring per journal event, plus a
    // standing wave while activity stays high.
    float2 f = (uv - focus) * float2(aspect, 1.0);
    float d = length(f);
    float near = focusStrength * exp(-d * 2.4);
    float close = focusStrength * exp(-d * 7.0);
    float ring = (1.0 - smoothstep(0.0, 0.07, abs(d - eventAge * 0.55))) * exp(-eventAge * 1.1);
    float standing = (0.5 + 0.5 * sin(d * 22.0 - time * 4.5)) * activity * near;
    // Extra dots fill in with work everywhere, and near the agent asking you.
    float fill = saturate(energy * 0.85 + typing * 0.5 + activity * 0.3 + attention * close * 1.5);

    // A question for the user: amber heartbeat around the asker and at the rim.
    float urgency = 1.0 + saturate(decisionAge / 90.0);
    float beacon = attention * (0.35 + 0.65 * pulse) * (close * 1.2 + near * 0.3 + smoothstep(0.55, 1.0, rim) * 0.5) * urgency;

    // Failure: brief red flash and flickering interference rows.
    float noise = frac(sin(cell.y * 12.9898 + floor(time * 7.0) * 78.233) * 43758.5453);
    float glitch = tension * step(0.84, noise) * 0.45;
    float alarm = exp(-failureAge * 1.4) * 0.5;

    // Success: a green shimmer sweeping left to right.
    float sweep = (1.0 - smoothstep(0.0, 0.1, abs(uv.x + 0.1 - successAge * 0.75))) * exp(-successAge * 0.6);

    // Git changes rain from the top; context lookups sparkle.
    float rain = (1.0 - smoothstep(0.0, 0.08, abs(uv.y + 0.05 - changeAge * 0.8))) * exp(-changeAge * 0.8);
    float twinkle = frac(sin(dot(cell, float2(127.1, 311.7)) + floor(time * 9.0)) * 43758.5453);
    float sparkle = step(0.93, twinkle) * exp(-contextAge * 1.5) * 0.6;

    // Typing ripples rise from the composer at the bottom centre.
    float dk = length((uv - float2(0.5, 1.1)) * float2(aspect, 1.0));
    float keys = typing * (0.5 + 0.5 * sin(dk * 18.0 - time * 6.0)) * exp(-dk * 1.8);

    // Progress gently lifts the field behind the completion front.
    float ahead = smoothstep(progress + 0.04, progress - 0.04, uv.x) * progress * 0.15 * (runActive + settle);

    float ambient = (auroraLift + ahead + wake * 0.2 + alarm) * fade;
    float events = (ring * 0.55 + standing * 0.35 + sweep * 0.6 + rain * 0.5 + sparkle + keys * 0.45 + glitch) * soft;
    float motion = ambient + events + beacon;
    float level = base * (0.82 * breathe * fade + motion) + between * fill * (0.18 + 0.5 * aurora * energy + motion);
    float light = saturate(level);

    // Hue: neutral at rest, cyan with work and new events, violet for Git
    // activity, green for success, red for trouble, amber for questions.
    float3 cyan = lerp(float3(1.0, 1.0, 1.0), float3(0.55, 0.9, 1.0), saturate(energy * 0.6 + exp(-eventAge * 1.5) * 0.4));
    float3 violet = lerp(cyan, float3(0.7, 0.55, 1.0), saturate(exp(-changeAge * 0.8) * 1.2));
    float3 green = lerp(violet, float3(0.45, 1.0, 0.6), saturate(settle * 1.1 + exp(-successAge * 0.6)));
    float3 red = lerp(green, float3(1.0, 0.3, 0.25), saturate(tension * 0.8 + alarm * 1.6));
    float3 tint = lerp(red, float3(1.0, 0.7, 0.25), saturate(attention * (0.55 + 0.45 * pulse) + awaitingUser * 0.2));
    // Hue depends only on frame-wide state, so it is computed once per frame.
    // Dividing by its luminance keeps hue from changing brightness.
    float3 color = tint * (light / dot(tint, float3(0.2126, 0.7152, 0.0722)));
    return float4(color, 1.0);
}
