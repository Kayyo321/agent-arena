package shader

import (
	"fmt"
	"math"
	"os"
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
	idle := IdleInputs(80, 24)
	sample := func(x, y float64) float64 {
		in := idle
		in.UV, in.Cell = Vec2(x, y), Vec2(3, 2)
		return Luminance(Default.Eval(in))
	}
	center := sample(.5, .5)
	if center < .7 {
		t.Fatalf("center not bright: %f", center)
	}
	for _, p := range [][2]float64{{.01, .5}, {.99, .5}, {.5, .01}, {.5, .99}, {.01, .01}, {.99, .99}} {
		if sample(p[0], p[1]) >= center/20 {
			t.Fatalf("edge not dark: %v", p)
		}
	}
	idle.UV, idle.Cell = Vec2(.5, .5), Vec2(1, 1)
	if Luminance(Default.Eval(idle)) != 0 {
		t.Fatal("grid gaps not empty")
	}
}

// field renders the default shader on a small grid and returns per-cell RGB.
func field(in Inputs) [][4]float64 {
	const w, h = 60, 20
	in.Resolution, in.Aspect = Vec2(w, h), w/(h*2.0)
	frame := Default.Bind(in)
	out := [][4]float64{}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := frame.Sample(Vec2((float64(x)+.5)/w, (float64(y)+.5)/h), Vec2(float64(x), float64(y)))
			out = append(out, v.V)
		}
	}
	return out
}
func summary(cells [][4]float64) (lit int, light, warm, red, green float64) {
	for _, c := range cells {
		l := Luminance(Value{V: c, N: 4})
		if l < .015 {
			continue
		}
		lit++
		light += l
		warm += (c[0] - c[2]) / l
		red += (c[0] - c[1]) / l
		green += (c[1] - c[0]) / l
	}
	return
}
func TestDefaultReactsToArenaState(t *testing.T) {
	idle := IdleInputs(60, 20)
	baseLit, baseLight, baseWarm, baseRed, baseGreen := summary(field(idle))
	if baseLit == 0 || math.Abs(baseWarm) > 1e-6 || math.Abs(baseRed) > 1e-6 {
		t.Fatalf("idle field should be neutral dots: lit=%d warm=%f", baseLit, baseWarm)
	}

	busy := idle
	busy.RunIdle, busy.RunActive, busy.Energy, busy.Activity, busy.WorkTime = 0, 1, 1, .8, 12
	if lit, light, _, _, _ := summary(field(busy)); lit <= baseLit || light <= baseLight {
		t.Fatalf("work did not fill in or brighten the field: %d/%f vs %d/%f", lit, light, baseLit, baseLight)
	}

	asking := idle
	asking.AwaitingUser, asking.Attention, asking.PendingDecisions, asking.Focus, asking.FocusStrength = 1, 1, 1, Vec2(.3, .4), 1
	asking.Time = math.Pi / 2 / 3.2 // pulse peak
	_, light, warm, _, _ := summary(field(asking))
	if warm <= baseWarm+1 || light <= baseLight {
		t.Fatalf("question for the user did not glow amber: warm=%f light=%f", warm, light)
	}
	nearFocus := func(cells [][4]float64, fx, fy float64) float64 {
		total := 0.0
		for i, c := range cells {
			x, y := float64(i%60)/60, float64(i/60)/20
			if math.Abs(x-fx) < .1 && math.Abs(y-fy) < .15 {
				total += Luminance(Value{V: c, N: 4})
			}
		}
		return total
	}
	if nearFocus(field(asking), .3, .4) <= nearFocus(field(idle), .3, .4)*1.2 {
		t.Fatal("asking agent is not highlighted")
	}

	failing := idle
	failing.RunIdle, failing.RunFailed, failing.Tension, failing.FailureAge = 0, 1, 1, .1
	if _, _, _, red, _ := summary(field(failing)); red <= baseRed+1 {
		t.Fatalf("failure did not tint red: %f", red)
	}

	done := idle
	done.RunIdle, done.RunSucceeded, done.Success, done.Progress, done.SuccessAge = 0, 1, 1, 1, .5
	if _, _, _, _, green := summary(field(done)); green <= baseGreen+1 {
		t.Fatalf("success did not tint green: %f", green)
	}

	typing := idle
	typing.Typing, typing.InputAge = 1, 0
	if lit, _, _, _, _ := summary(field(typing)); lit <= baseLit {
		t.Fatal("typing did not ripple")
	}

	ripple := idle
	ripple.EventAge, ripple.Focus, ripple.FocusStrength = .3, Vec2(.5, .5), 1
	if _, light, _, _, _ := summary(field(ripple)); light <= baseLight {
		t.Fatal("new event did not ripple")
	}
}
func TestBindHoistsUniformWorkWithoutChangingResults(t *testing.T) {
	in := IdleInputs(80, 24)
	in.Time, in.WorkTime, in.Energy, in.Attention, in.Tension, in.Activity, in.Typing = 7.3, 4.1, .6, .4, .3, .5, .2
	in.EventAge, in.FailureAge, in.Focus, in.FocusStrength = .4, 1.2, Vec2(.2, .7), .8
	frame := Default.Bind(in)
	for y := 0; y < 24; y += 3 {
		for x := 0; x < 80; x += 7 {
			uv, cell := Vec2((float64(x)+.5)/80, (float64(y)+.5)/24), Vec2(float64(x), float64(y))
			want := in
			want.UV, want.Cell = uv, cell
			if got, expected := frame.Sample(uv, cell), Default.Eval(want); got != expected {
				t.Fatalf("hoisted sample differs at %d,%d: %v vs %v", x, y, got, expected)
			}
		}
	}
	if Default.VaryingOperations() >= Default.Operations()/2 || Default.Operations() > MaxOps {
		t.Fatalf("default shader cost: %d varying of %d", Default.VaryingOperations(), Default.Operations())
	}
}
func TestUniformsAreDeclarableAndOnlyCostWhenUsed(t *testing.T) {
	trivial, err := Compile(`float4 main(float2 uv){return float4(1);}`)
	if err != nil {
		t.Fatal(err)
	}
	if trivial.Operations() > 2 {
		t.Fatalf("unused uniforms consumed operations: %d", trivial.Operations())
	}
	var decls, sum strings.Builder
	sum.WriteString("float s0 = 0.0;")
	for i, u := range Uniforms() {
		typ := "float"
		if u.Width == 2 {
			typ = "float2"
		}
		if u.Name != "uv" {
			fmt.Fprintf(&decls, "%s %s;\n", typ, u.Name)
		}
		// One local per input keeps expression nesting within the limit.
		if u.Width == 2 {
			fmt.Fprintf(&sum, "float s%d = s%d + %s.x + %s.y;", i+1, i, u.Name, u.Name)
		} else {
			fmt.Fprintf(&sum, "float s%d = s%d + %s;", i+1, i, u.Name)
		}
	}
	p, err := Compile(decls.String() + "float4 main(float2 uv){ " + sum.String() + fmt.Sprintf(" return float4(s%d, s%d, s%d, 1.0); }", len(Uniforms()), len(Uniforms()), len(Uniforms())))
	if err != nil {
		t.Fatal(err)
	}
	in := Inputs{AwaitingUser: 1, Energy: .5, Focus: Vec2(.25, .25), InputAge: 2}
	if got := p.Eval(in).V[0]; math.Abs(got-4) > 1e-9 {
		t.Fatalf("uniform sum %f", got)
	}
	for _, bad := range []string{
		`float energy; float energy; float4 main(float2 uv){return float4(1);}`,
		`float2 energy; float4 main(float2 uv){return float4(1);}`,
		`float focus; float4 main(float2 uv){return float4(1);}`,
		`float4 main(float2 uv){ float attention = 1.0; return float4(attention); }`,
	} {
		if _, err := Compile(bad); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
	guide, err := os.ReadFile("../../docs/BACKGROUND-SHADERS.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range Uniforms() {
		if !strings.Contains(string(guide), "| `"+u.Name+"` |") {
			t.Fatalf("input %s is not documented", u.Name)
		}
	}
}
func TestExtendedIntrinsics(t *testing.T) {
	p, err := Compile(`float4 main(float2 uv){
 float a = exp(0.0) + exp2(3.0) + log(1.0) + log2(8.0);
 float b = sign(-2.0) + round(2.5) + trunc(-1.7) + rcp(4.0) + rsqrt(4.0);
 float c = distance(float2(0.0, 0.0), float2(3.0, 4.0)) + length(normalize(float3(2.0, 0.0, 0.0)));
 float d = mad(2.0, 3.0, 1.0) + atan(0.0) + tan(0.0) + asin(0.0) + acos(1.0);
 return float4(a, b, c, d);
 }`)
	if err != nil {
		t.Fatal(err)
	}
	want := [4]float64{12, .75, 6, 7}
	got := p.Eval(Inputs{})
	for i := range want {
		if math.Abs(got.V[i]-want[i]) > 1e-9 {
			t.Fatalf("component %d: got %v want %v", i, got, want)
		}
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
	in := IdleInputs(120, 40)
	in.UV, in.Cell = Vec2(.5, .5), Vec2(3, 2)
	for b.Loop() {
		Default.Eval(in)
	}
}
func BenchmarkDefaultSample(b *testing.B) {
	frame := Default.Bind(IdleInputs(120, 40))
	for b.Loop() {
		frame.Sample(Vec2(.5, .5), Vec2(3, 2))
	}
}
