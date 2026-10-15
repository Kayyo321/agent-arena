// Package shader implements Arena's bounded, expression-only HLSL profile.
// Programs have no loops, recursion, textures, includes, IO or native execution.
package shader

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"math"
	"strconv"
	"strings"
	"text/scanner"
)

const MaxSource = 16384
const MaxOps = 256

type Value struct {
	V [4]float64
	N int
}

func Scalar(v float64) Value  { return Value{V: [4]float64{v}, N: 1} }
func Vec2(x, y float64) Value { return Value{V: [4]float64{x, y}, N: 2} }
func (v Value) at(i int) float64 {
	if v.N == 1 {
		return v.V[0]
	}
	return v.V[i]
}

type Inputs struct {
	UV, Cell, Resolution                                                   Value
	Time, DeltaTime, Frame, Aspect                                         float64
	AgentCount, RunningAgents, PendingPackets, PendingDecisions, RunActive float64
}

var uniforms = []string{"uv", "cell", "resolution", "time", "deltaTime", "frame", "aspect", "agentCount", "runningAgents", "pendingPackets", "pendingDecisions", "runActive"}

func (i Inputs) values() [12]Value {
	return [12]Value{i.UV, i.Cell, i.Resolution, Scalar(i.Time), Scalar(i.DeltaTime), Scalar(i.Frame), Scalar(i.Aspect), Scalar(i.AgentCount), Scalar(i.RunningAgents), Scalar(i.PendingPackets), Scalar(i.PendingDecisions), Scalar(i.RunActive)}
}

type instruction struct {
	op    string
	args  []int
	value Value
	width int
}
type Program struct {
	code   []instruction
	result int
}

func (p *Program) Eval(in Inputs) Value {
	var stack [MaxOps]Value
	values := in.values()
	for n, ins := range p.code {
		if ins.op == "input" {
			stack[n] = values[ins.args[0]]
			continue
		}
		if ins.op == "literal" {
			stack[n] = ins.value
			continue
		}
		var args [4]Value
		for j, index := range ins.args {
			args[j] = stack[index]
		}
		stack[n] = evaluate(ins.op, args[:len(ins.args)], ins.width)
	}
	return stack[p.result]
}
func Luminance(v Value) float64 {
	x := v.V[0]
	if v.N >= 3 {
		x = .2126*v.V[0] + .7152*v.V[1] + .0722*v.V[2]
	}
	if v.N == 4 {
		x *= clamp(v.V[3], 0, 1)
	}
	if math.IsNaN(x) || math.IsInf(x, 0) {
		return 0
	}
	return clamp(x, 0, 1)
}
func clamp(x, a, b float64) float64 { return math.Max(a, math.Min(b, x)) }
func evaluate(op string, a []Value, width int) Value {
	r := Value{N: width}
	if strings.HasPrefix(op, "swizzle:") {
		for i, c := range strings.TrimPrefix(op, "swizzle:") {
			r.V[i] = a[0].V[strings.IndexRune("xyzw", c)]
		}
		return r
	}
	if strings.HasPrefix(op, "float") {
		n := 0
		if len(a) == 1 && a[0].N == 1 {
			for j := 0; j < width; j++ {
				r.V[j] = a[0].V[0]
			}
			return r
		}
		for _, v := range a {
			for j := 0; j < v.N; j++ {
				r.V[n] = v.V[j]
				n++
			}
		}
		return r
	}
	if op == "length" || op == "dot" {
		for j := 0; j < a[0].N; j++ {
			if op == "dot" {
				r.V[0] += a[0].V[j] * a[1].at(j)
			} else {
				r.V[0] += a[0].V[j] * a[0].V[j]
			}
		}
		if op == "length" {
			r.V[0] = math.Sqrt(r.V[0])
		}
		return r
	}
	for j := 0; j < width; j++ {
		x := a[0].at(j)
		y, z := 0.0, 0.0
		if len(a) > 1 {
			y = a[1].at(j)
		}
		if len(a) > 2 {
			z = a[2].at(j)
		}
		switch op {
		case "+":
			r.V[j] = x + y
		case "-":
			r.V[j] = x - y
		case "*":
			r.V[j] = x * y
		case "/":
			r.V[j] = x / y
		case "neg":
			r.V[j] = -x
		case "sin":
			r.V[j] = math.Sin(x)
		case "cos":
			r.V[j] = math.Cos(x)
		case "abs":
			r.V[j] = math.Abs(x)
		case "sqrt":
			r.V[j] = math.Sqrt(x)
		case "floor":
			r.V[j] = math.Floor(x)
		case "ceil":
			r.V[j] = math.Ceil(x)
		case "frac":
			r.V[j] = x - math.Floor(x)
		case "saturate":
			r.V[j] = clamp(x, 0, 1)
		case "min":
			r.V[j] = math.Min(x, y)
		case "max":
			r.V[j] = math.Max(x, y)
		case "pow":
			r.V[j] = math.Pow(x, y)
		case "fmod":
			r.V[j] = math.Mod(x, y)
		case "atan2":
			r.V[j] = math.Atan2(x, y)
		case "step":
			if y >= x {
				r.V[j] = 1
			}
		case "clamp":
			r.V[j] = clamp(x, y, z)
		case "lerp":
			r.V[j] = x + (y-x)*z
		case "smoothstep":
			t := clamp((z-x)/(y-x), 0, 1)
			r.V[j] = t * t * (3 - 2*t)
		}
	}
	return r
}

type compiler struct {
	p    Program
	vars map[string]int
}

func (c *compiler) add(i instruction) (int, error) {
	if len(c.p.code) >= MaxOps {
		return 0, fmt.Errorf("shader exceeds %d operations", MaxOps)
	}
	c.p.code = append(c.p.code, i)
	return len(c.p.code) - 1, nil
}
func typeWidth(s string) int {
	switch s {
	case "float":
		return 1
	case "float2":
		return 2
	case "float3":
		return 3
	case "float4":
		return 4
	}
	return 0
}
func (c *compiler) expr(e ast.Expr, depth int) (int, error) {
	if depth > 32 {
		return 0, fmt.Errorf("expression nesting exceeds 32")
	}
	switch x := e.(type) {
	case *ast.ParenExpr:
		return c.expr(x.X, depth+1)
	case *ast.BasicLit:
		if x.Kind != token.INT && x.Kind != token.FLOAT {
			break
		}
		v, err := strconv.ParseFloat(x.Value, 64)
		if err != nil {
			return 0, err
		}
		return c.add(instruction{op: "literal", value: Scalar(v), width: 1})
	case *ast.Ident:
		if i, ok := c.vars[x.Name]; ok {
			return i, nil
		}
		return 0, fmt.Errorf("unknown variable %s", x.Name)
	case *ast.UnaryExpr:
		i, err := c.expr(x.X, depth+1)
		if err != nil {
			return 0, err
		}
		if x.Op == token.ADD {
			return i, nil
		}
		if x.Op == token.SUB {
			return c.add(instruction{op: "neg", args: []int{i}, width: c.p.code[i].width})
		}
	case *ast.BinaryExpr:
		if x.Op != token.ADD && x.Op != token.SUB && x.Op != token.MUL && x.Op != token.QUO {
			break
		}
		a, err := c.expr(x.X, depth+1)
		if err != nil {
			return 0, err
		}
		b, err := c.expr(x.Y, depth+1)
		if err != nil {
			return 0, err
		}
		return c.call(x.Op.String(), []int{a, b})
	case *ast.SelectorExpr:
		a, err := c.expr(x.X, depth+1)
		if err != nil {
			return 0, err
		}
		sw := x.Sel.Name
		if len(sw) < 1 || len(sw) > 4 {
			break
		}
		rgba := strings.ContainsAny(sw, "rgba")
		for _, ch := range sw {
			alphabet := "xyzw"
			if rgba {
				alphabet = "rgba"
			}
			j := strings.IndexRune(alphabet, ch)
			if j < 0 || j >= c.p.code[a].width {
				return 0, fmt.Errorf("invalid swizzle %s", sw)
			}
		}
		sw = strings.NewReplacer("r", "x", "g", "y", "b", "z", "a", "w").Replace(sw)
		return c.add(instruction{op: "swizzle:" + sw, args: []int{a}, width: len(sw)})
	case *ast.CallExpr:
		f, ok := x.Fun.(*ast.Ident)
		if !ok || len(x.Args) > 4 || x.Ellipsis.IsValid() {
			break
		}
		args := []int{}
		for _, arg := range x.Args {
			i, err := c.expr(arg, depth+1)
			if err != nil {
				return 0, err
			}
			args = append(args, i)
		}
		return c.call(f.Name, args)
	}
	return 0, fmt.Errorf("unsupported HLSL expression")
}
func (c *compiler) call(op string, args []int) (int, error) {
	width := 0
	if w := typeWidth(op); w > 0 {
		for _, a := range args {
			width += c.p.code[a].width
		}
		if width != w && !(len(args) == 1 && width == 1) {
			return 0, fmt.Errorf("%s constructor needs %d components", op, w)
		}
		return c.add(instruction{op: op, args: args, width: w})
	}
	arity := map[string]int{"+": 2, "-": 2, "*": 2, "/": 2, "sin": 1, "cos": 1, "abs": 1, "sqrt": 1, "floor": 1, "ceil": 1, "frac": 1, "saturate": 1, "length": 1, "min": 2, "max": 2, "pow": 2, "fmod": 2, "atan2": 2, "step": 2, "dot": 2, "clamp": 3, "lerp": 3, "smoothstep": 3}
	n, ok := arity[op]
	if !ok || len(args) != n {
		return 0, fmt.Errorf("unsupported function or argument count: %s", op)
	}
	for _, a := range args {
		w := c.p.code[a].width
		if width > 1 && w > 1 && width != w {
			return 0, fmt.Errorf("vector widths differ in %s", op)
		}
		width = max(width, w)
	}
	if op == "dot" && c.p.code[args[0]].width != c.p.code[args[1]].width {
		return 0, fmt.Errorf("dot needs equal vector widths")
	}
	if op == "length" || op == "dot" {
		width = 1
	}
	return c.add(instruction{op: op, args: args, width: width})
}

// Compile accepts optional uniform declarations followed by one float4 main(float2 uv).
func Compile(source string) (*Program, error) {
	if len(source) > MaxSource {
		return nil, fmt.Errorf("shader exceeds %d bytes", MaxSource)
	}
	var s scanner.Scanner
	s.Init(strings.NewReader(source))
	s.Mode = scanner.ScanIdents | scanner.ScanInts | scanner.ScanFloats | scanner.ScanComments | scanner.SkipComments
	var scanErr error
	s.Error = func(s *scanner.Scanner, msg string) { scanErr = fmt.Errorf("line %d: %s", s.Pos().Line, msg) }
	tokens := []string{}
	for t := s.Scan(); t != scanner.EOF; t = s.Scan() {
		tokens = append(tokens, s.TokenText())
	}
	if scanErr != nil {
		return nil, scanErr
	}
	c := compiler{vars: map[string]int{}}
	for i, name := range uniforms {
		w := 1
		if i < 3 {
			w = 2
		}
		idx, _ := c.add(instruction{op: "input", args: []int{i}, width: w})
		c.vars[name] = idx
	}
	pos := 0
	take := func(want string) bool {
		if pos < len(tokens) && tokens[pos] == want {
			pos++
			return true
		}
		return false
	}
	// Optional declarations make the source valid HLSL without a separate include.
	declared := map[string]bool{}
	for pos+2 < len(tokens) && tokens[pos+2] == ";" {
		typ, name := tokens[pos], tokens[pos+1]
		index, ok := c.vars[name]
		if !ok || name == "uv" || typeWidth(typ) != c.p.code[index].width || declared[name] {
			return nil, fmt.Errorf("invalid uniform declaration: %s %s", typ, name)
		}
		declared[name] = true
		pos += 3
	}
	for _, t := range []string{"float4", "main", "(", "float2", "uv", ")"} {
		if !take(t) {
			return nil, fmt.Errorf("expected float4 main(float2 uv)")
		}
	}
	if take(":") {
		if !take("SV_Target") {
			return nil, fmt.Errorf("expected SV_Target semantic")
		}
	}
	if !take("{") {
		return nil, fmt.Errorf("expected function body")
	}
	returned := false
	for pos < len(tokens) && tokens[pos] != "}" {
		isReturn := take("return")
		name := ""
		width := 0
		if !isReturn {
			if pos+2 >= len(tokens) {
				return nil, fmt.Errorf("incomplete declaration")
			}
			width = typeWidth(tokens[pos])
			name = tokens[pos+1]
			pos += 2
			if width == 0 || !token.IsIdentifier(name) || token.Lookup(name).IsKeyword() {
				return nil, fmt.Errorf("only float local declarations and return are supported")
			}
			if _, exists := c.vars[name]; exists {
				return nil, fmt.Errorf("duplicate or reserved variable %s", name)
			}
			if !take("=") {
				return nil, fmt.Errorf("local variables require an initializer")
			}
		}
		start := pos
		for pos < len(tokens) && tokens[pos] != ";" {
			pos++
		}
		if !take(";") {
			return nil, fmt.Errorf("missing semicolon")
		}
		e, err := parser.ParseExpr(strings.Join(tokens[start:pos-1], " "))
		if err != nil {
			return nil, fmt.Errorf("invalid expression: %w", err)
		}
		index, err := c.expr(e, 0)
		if err != nil {
			return nil, err
		}
		if isReturn {
			if c.p.code[index].width != 4 {
				return nil, fmt.Errorf("main must return float4")
			}
			c.p.result = index
			returned = true
			break
		}
		if c.p.code[index].width != width {
			return nil, fmt.Errorf("initializer width differs for %s; use an explicit constructor", name)
		}
		c.vars[name] = index
	}
	if !returned || !take("}") || pos != len(tokens) {
		return nil, fmt.Errorf("expected one final return and closing brace")
	}
	return &c.p, nil
}

func (p *Program) Operations() int { return len(p.code) }
