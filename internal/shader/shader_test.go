package shader

import (
	"fmt"
	"math"
	"strings"
	"testing"
)

func TestHLSLMathAndInputs(t *testing.T) {
	source := `float time; float aspect; float runningAgents;
 float4 main(float2 uv) : SV_Target {
 float2 p = uv * float2(aspect, 2.0);
 float light = dot(p, p) + sin(time) + runningAgents;
 float3 color = float3(light, frac(-0.25), smoothstep(0.0, 1.0, 0.5));
 return float4(color.zyx, saturate(2.0));
 }`
	p, err := Compile(source)
	if err != nil {
		t.Fatal(err)
	}
	v := p.Eval(Inputs{UV: Vec2(.5, .25), Aspect: 2, Time: math.Pi / 2, RunningAgents: 2})
	want := [4]float64{.5, .75, 4.25, 1}
	for i, n := range want {
		if math.Abs(v.V[i]-n) > 1e-9 {
			t.Fatalf("component %d: got %v want %v", i, v, n)
		}
	}
}
func TestDefaultDotsFadeTowardEveryEdge(t *testing.T) {
	sample := func(x, y float64) float64 { return Luminance(Default.Eval(Inputs{UV: Vec2(x, y), Cell: Vec2(3, 2)})) }
	center := sample(.5, .5)
	if center < .7 {
		t.Fatalf("center not bright: %f", center)
	}
	for _, p := range [][2]float64{{.01, .5}, {.99, .5}, {.5, .01}, {.5, .99}, {.01, .01}, {.99, .99}} {
		if sample(p[0], p[1]) >= center/20 {
			t.Fatalf("edge not dark: %v", p)
		}
	}
	if Luminance(Default.Eval(Inputs{UV: Vec2(.5, .5), Cell: Vec2(1, 1)})) != 0 {
		t.Fatal("grid gaps not empty")
	}
}
func TestRejectUnsupportedAndUnboundedShaders(t *testing.T) {
	sources := []string{
		`#include "secret"`,
		`float4 main(float2 uv){ while(true){} return float4(1); }`,
		`float4 main(float2 uv){ return float4(system(1)); }`,
		`float4 main(float2 uv){ return float4(uv.z,0,0,1); }`,
		`float4 main(float2 uv){ return float4(sin(1,2)); }`,
		`float4 main(float2 uv){ float2 p = 1; return float4(p,0,1); }`,
		`float4 main(float2 uv){ float p=1; p=2; return float4(p); }`,
		`float4 main(float2 uv){ return float4(1); } float4 other(){return float4(1);}`,
		`float4 main(float2 uv){ return float4(sin(uv...)); }`,
		`float4 main(float2 uv){ return float4(float2(1,2)+float3(1,2,3),1); }`,
		strings.Repeat(" ", MaxSource+1),
		`float4 main(float2 uv){return float4(` + strings.Repeat("sin(", 40) + "1" + strings.Repeat(")", 40) + `);}`,
	}
	var many strings.Builder
	many.WriteString("float4 main(float2 uv){")
	for i := 0; i < MaxOps; i++ {
		fmt.Fprintf(&many, "float v%d = sin(%d.0);", i, i)
	}
	many.WriteString("return float4(1);}")
	sources = append(sources, many.String())
	for _, source := range sources {
		if _, err := Compile(source); err == nil {
			t.Fatalf("accepted unsupported shader: %.150s", source)
		}
	}
}
func TestNonFiniteOutputIsTransparent(t *testing.T) {
	for _, expr := range []string{"0.0/0.0", "1.0/0.0", "sqrt(-1.0)"} {
		p, err := Compile("float4 main(float2 uv){return float4(" + expr + ",0,0,1);}")
		if err != nil {
			t.Fatal(err)
		}
		if got := Luminance(p.Eval(Inputs{})); got != 0 {
			t.Fatalf("nonfinite luminance: %f", got)
		}
	}
}
func FuzzCompileAndEval(f *testing.F) {
	f.Add(DefaultSource)
	f.Add(`float4 main(float2 uv){return float4(uv,0,1);}`)
	f.Fuzz(func(t *testing.T, source string) {
		p, err := Compile(source)
		if err != nil {
			return
		}
		_ = Luminance(p.Eval(Inputs{UV: Vec2(.5, .5), Cell: Vec2(3, 2), Resolution: Vec2(80, 24)}))
	})
}
func BenchmarkDefaultEval(b *testing.B) {
	in := Inputs{UV: Vec2(.5, .5), Cell: Vec2(3, 2)}
	for b.Loop() {
		Default.Eval(in)
	}
}
