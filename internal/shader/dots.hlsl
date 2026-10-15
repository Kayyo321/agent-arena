// Arena HLSL profile v1. One sample per terminal cell; RGB brightness + alpha.
float2 cell;
float2 resolution;
float time;
float deltaTime;
float frame;
float aspect;
float agentCount;
float runningAgents;
float pendingPackets;
float pendingDecisions;
float runActive;

float4 main(float2 uv) : SV_Target {
    float2 edge = abs(uv * 2.0 - 1.0);
    float fade = 1.0 - smoothstep(0.0, 1.0, max(edge.x, edge.y));
    float2 grid = frac(cell / float2(3.0, 2.0));
    float dots = (1.0 - step(0.3, grid.x)) * (1.0 - step(0.4, grid.y));
    float light = dots * fade * 0.8;
    return float4(light, light, light, 1.0);
}
