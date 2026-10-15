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
const MaxOps = 512

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

type opcode uint8

const (
	opInput opcode = iota
	opLiteral
	opSwizzle
	opConstruct
	opAdd
	opSub
	opMul
	opDiv
	opNeg
	opSin
	opCos
	opTan
	opAsin
	opAcos
	opAtan
	opAbs
	opSqrt
	opRsqrt
	opRcp
	opFloor
	opCeil
	opRound
	opTrunc
	opFrac
	opSaturate
	opSign
	opExp
	opExp2
	opLog
	opLog2
	opMin
	opMax
	opPow
	opFmod
	opAtan2
	opStep
	opClamp
	opLerp
	opSmoothstep
	opMad
	opLength
	opDot
	opDistance
	opNormalize
)

// functions maps intrinsic names to opcodes and argument counts.
var functions = map[string]struct {
	op    opcode
	arity int
}{
	"sin": {opSin, 1}, "cos": {opCos, 1}, "tan": {opTan, 1}, "asin": {opAsin, 1}, "acos": {opAcos, 1}, "atan": {opAtan, 1},
	"abs": {opAbs, 1}, "sqrt": {opSqrt, 1}, "rsqrt": {opRsqrt, 1}, "rcp": {opRcp, 1}, "floor": {opFloor, 1}, "ceil": {opCeil, 1},
	"round": {opRound, 1}, "trunc": {opTrunc, 1}, "frac": {opFrac, 1}, "saturate": {opSaturate, 1}, "sign": {opSign, 1},
	"exp": {opExp, 1}, "exp2": {opExp2, 1}, "log": {opLog, 1}, "log2": {opLog2, 1}, "length": {opLength, 1}, "normalize": {opNormalize, 1},
	"min": {opMin, 2}, "max": {opMax, 2}, "pow": {opPow, 2}, "fmod": {opFmod, 2}, "atan2": {opAtan2, 2}, "step": {opStep, 2},
	"dot": {opDot, 2}, "distance": {opDistance, 2},
	"clamp": {opClamp, 3}, "lerp": {opLerp, 3}, "smoothstep": {opSmoothstep, 3}, "mad": {opMad, 3},
}
var operators = map[string]opcode{"+": opAdd, "-": opSub, "*": opMul, "/": opDiv}

type instruction struct {
	op      opcode
	nargs   int
	args    [4]int
	swizzle [4]int
	value   Value // literal value
	input   int   // uniform index
	width   int
	varying bool
}

// Program is an immutable compiled shader. Instructions that do not depend on
// uv or cell are uniform and can be evaluated once per frame with Bind.
type Program struct {
	code    []instruction
	result  int
	all     []int
	uniform []int
	varying []int
}

// Eval evaluates the whole program for one sample.
func (p *Program) Eval(in Inputs) Value {
	var stack [MaxOps]Value
	p.exec(stack[:], &in, p.all)
	return stack[p.result]
}

// Frame holds a program's per-frame uniform results; Sample evaluates only the
// instructions that vary per cell. A Frame is not safe for concurrent use.
type Frame struct {
	p     *Program
	in    Inputs
	stack []Value
}

// Bind evaluates every cell-independent instruction for one frame.
func (p *Program) Bind(in Inputs) *Frame {
	f := &Frame{p: p, in: in, stack: make([]Value, len(p.code))}
	p.exec(f.stack, &f.in, p.uniform)
	return f
}

// Sample evaluates one cell. uv and cell override the bound inputs.
func (f *Frame) Sample(uv, cell Value) Value {
	f.in.UV, f.in.Cell = uv, cell
	f.p.exec(f.stack, &f.in, f.p.varying)
	return f.stack[f.p.result]
}

func (p *Program) exec(stack []Value, in *Inputs, order []int) {
	for _, n := range order {
		ins := &p.code[n]
		switch ins.op {
		case opInput:
			stack[n] = uniforms[ins.input].get(in)
		case opLiteral:
			stack[n] = ins.value
		default:
			evaluate(ins, stack, &stack[n])
		}
	}
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
func load(dst *[4]float64, v *Value) {
	if v.N == 1 {
		*dst = [4]float64{v.V[0], v.V[0], v.V[0], v.V[0]}
	} else {
		*dst = v.V
	}
}
func evaluate(ins *instruction, stack []Value, r *Value) {
	w := ins.width
	r.N = w
	switch ins.op {
	case opSwizzle:
		src := stack[ins.args[0]].V
		for i := 0; i < w; i++ {
			r.V[i] = src[ins.swizzle[i]]
		}
		return
	case opConstruct:
		a := &stack[ins.args[0]]
		if ins.nargs == 1 && a.N == 1 {
			r.V = [4]float64{a.V[0], a.V[0], a.V[0], a.V[0]}
			return
		}
		n := 0
		for i := 0; i < ins.nargs; i++ {
			a = &stack[ins.args[i]]
			for j := 0; j < a.N; j++ {
				r.V[n] = a.V[j]
				n++
			}
		}
		return
	}
	if w == 1 && ins.op != opLength && ins.op != opDot && ins.op != opDistance && ins.op != opNormalize {
		// Scalar fast path: every argument of a width-1 operation is a scalar.
		x, y, z := stack[ins.args[0]].V[0], 0.0, 0.0
		if ins.nargs > 1 {
			y = stack[ins.args[1]].V[0]
		}
		if ins.nargs > 2 {
			z = stack[ins.args[2]].V[0]
		}
		switch ins.op {
		case opAdd:
			r.V[0] = x + y
		case opSub:
			r.V[0] = x - y
		case opMul:
			r.V[0] = x * y
		case opDiv:
			r.V[0] = x / y
		case opNeg:
			r.V[0] = -x
		default:
			r.V[0] = componentwise(ins.op, x, y, z)
		}
		return
	}
	var x, y, z [4]float64
	load(&x, &stack[ins.args[0]])
	if ins.nargs > 1 {
		load(&y, &stack[ins.args[1]])
	}
	if ins.nargs > 2 {
		load(&z, &stack[ins.args[2]])
	}
	n := stack[ins.args[0]].N
	v := &r.V
	switch ins.op {
	case opAdd:
		for j := 0; j < w; j++ {
			v[j] = x[j] + y[j]
		}
	case opSub:
		for j := 0; j < w; j++ {
			v[j] = x[j] - y[j]
		}
	case opMul:
		for j := 0; j < w; j++ {
			v[j] = x[j] * y[j]
		}
	case opDiv:
		for j := 0; j < w; j++ {
			v[j] = x[j] / y[j]
		}
	case opNeg:
		for j := 0; j < w; j++ {
			v[j] = -x[j]
		}
	case opLength, opDot, opDistance:
		sum := 0.0
		for j := 0; j < n; j++ {
			switch ins.op {
			case opDot:
				sum += x[j] * y[j]
			case opDistance:
				sum += (x[j] - y[j]) * (x[j] - y[j])
			default:
				sum += x[j] * x[j]
			}
		}
		if ins.op != opDot {
			sum = math.Sqrt(sum)
		}
		*v = [4]float64{sum}
	case opNormalize:
		sum := 0.0
		for j := 0; j < n; j++ {
			sum += x[j] * x[j]
		}
		for j := 0; j < w; j++ {
			v[j] = x[j] / math.Sqrt(sum)
		}
	default:
		for j := 0; j < w; j++ {
			v[j] = componentwise(ins.op, x[j], y[j], z[j])
		}
	}
}
func componentwise(op opcode, x, y, z float64) float64 {
	switch op {
	case opSin:
		return math.Sin(x)
	case opCos:
		return math.Cos(x)
	case opTan:
		return math.Tan(x)
	case opAsin:
		return math.Asin(x)
	case opAcos:
		return math.Acos(x)
	case opAtan:
		return math.Atan(x)
	case opAbs:
		return math.Abs(x)
	case opSqrt:
		return math.Sqrt(x)
	case opRsqrt:
		return 1 / math.Sqrt(x)
	case opRcp:
		return 1 / x
	case opFloor:
		return math.Floor(x)
	case opCeil:
		return math.Ceil(x)
	case opRound:
		return math.RoundToEven(x)
	case opTrunc:
		return math.Trunc(x)
	case opFrac:
		return x - math.Floor(x)
	case opSaturate:
		return clamp(x, 0, 1)
	case opSign:
		if x > 0 {
			return 1
		} else if x < 0 {
			return -1
		}
		return 0
	case opExp:
		return math.Exp(x)
	case opExp2:
		return math.Exp2(x)
	case opLog:
		return math.Log(x)
	case opLog2:
		return math.Log2(x)
	case opMin:
		return math.Min(x, y)
	case opMax:
		return math.Max(x, y)
	case opPow:
		return math.Pow(x, y)
	case opFmod:
		return math.Mod(x, y)
	case opAtan2:
		return math.Atan2(x, y)
	case opStep:
		if y >= x {
			return 1
		}
		return 0
	case opClamp:
		return clamp(x, y, z)
	case opLerp:
		return x + (y-x)*z
	case opMad:
		return x*y + z
	case opSmoothstep:
		t := clamp((z-x)/(y-x), 0, 1)
		return t * t * (3 - 2*t)
	}
	return 0
}

type compiler struct {
	p        Program
	vars     map[string]int
	inputs   map[string]int
	literals map[float64]int
}

func (c *compiler) add(i instruction) (int, error) {
	if len(c.p.code) >= MaxOps {
		return 0, fmt.Errorf("shader exceeds %d operations", MaxOps)
	}
	for j := 0; j < i.nargs; j++ {
		i.varying = i.varying || c.p.code[i.args[j]].varying
	}
	c.p.code = append(c.p.code, i)
	return len(c.p.code) - 1, nil
}
func (c *compiler) width(i int) int { return c.p.code[i].width }

// input materializes a uniform the first time the program references it.
func (c *compiler) input(name string) (int, bool, error) {
	if i, ok := c.inputs[name]; ok {
		return i, true, nil
	}
	u, ok := uniformIndex[name]
	if !ok {
		return 0, false, nil
	}
	i, err := c.add(instruction{op: opInput, input: u, width: uniforms[u].Width, varying: uniforms[u].varying})
	if err == nil {
		c.inputs[name] = i
	}
	return i, true, err
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
		// Equal literals share one pooled operation.
		if i, ok := c.literals[v]; ok {
			return i, nil
		}
		i, err := c.add(instruction{op: opLiteral, value: Scalar(v), width: 1})
		if err == nil {
			c.literals[v] = i
		}
		return i, err
	case *ast.Ident:
		if i, ok := c.vars[x.Name]; ok {
			return i, nil
		}
		if i, ok, err := c.input(x.Name); ok {
			return i, err
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
			return c.add(instruction{op: opNeg, nargs: 1, args: [4]int{i}, width: c.width(i)})
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
		alphabet := "xyzw"
		if strings.ContainsAny(sw, "rgba") {
			alphabet = "rgba"
		}
		ins := instruction{op: opSwizzle, nargs: 1, args: [4]int{a}, width: len(sw)}
		for k, ch := range sw {
			j := strings.IndexRune(alphabet, ch)
			if j < 0 || j >= c.width(a) {
				return 0, fmt.Errorf("invalid swizzle %s", sw)
			}
			ins.swizzle[k] = j
		}
		return c.add(ins)
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
func (c *compiler) call(name string, args []int) (int, error) {
	ins := instruction{nargs: len(args)}
	copy(ins.args[:], args)
	if w := typeWidth(name); w > 0 {
		width := 0
		for _, a := range args {
			width += c.width(a)
		}
		if width != w && !(len(args) == 1 && width == 1) {
			return 0, fmt.Errorf("%s constructor needs %d components", name, w)
		}
		ins.op, ins.width = opConstruct, w
		return c.add(ins)
	}
	op, isOperator := operators[name]
	arity := 2
	if !isOperator {
		f, ok := functions[name]
		if !ok {
			return 0, fmt.Errorf("unsupported function or argument count: %s", name)
		}
		op, arity = f.op, f.arity
	}
	if len(args) != arity {
		return 0, fmt.Errorf("unsupported function or argument count: %s", name)
	}
	width := 0
	for _, a := range args {
		w := c.width(a)
		if width > 1 && w > 1 && width != w {
			return 0, fmt.Errorf("vector widths differ in %s", name)
		}
		width = max(width, w)
	}
	if (op == opDot || op == opDistance) && c.width(args[0]) != c.width(args[1]) {
		return 0, fmt.Errorf("%s needs equal vector widths", name)
	}
	if op == opLength || op == opDot || op == opDistance {
		width = 1
	}
	ins.op, ins.width = op, width
	return c.add(ins)
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
	c := compiler{vars: map[string]int{}, inputs: map[string]int{}, literals: map[float64]int{}}
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
		index, ok := uniformIndex[name]
		if !ok || name == "uv" || typeWidth(typ) != uniforms[index].Width || declared[name] {
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
			if _, reserved := uniformIndex[name]; reserved {
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
			if c.width(index) != 4 {
				return nil, fmt.Errorf("main must return float4")
			}
			c.p.result = index
			returned = true
			break
		}
		if c.width(index) != width {
			return nil, fmt.Errorf("initializer width differs for %s; use an explicit constructor", name)
		}
		c.vars[name] = index
	}
	if !returned || !take("}") || pos != len(tokens) {
		return nil, fmt.Errorf("expected one final return and closing brace")
	}
	c.p.schedule()
	return &c.p, nil
}

// schedule keeps only instructions the result depends on, split into
// per-frame uniform work and per-cell varying work.
func (p *Program) schedule() {
	live := make([]bool, len(p.code))
	live[p.result] = true
	for n := len(p.code) - 1; n >= 0; n-- {
		if live[n] {
			for j := 0; j < p.code[n].nargs; j++ {
				live[p.code[n].args[j]] = true
			}
		}
	}
	for n, ok := range live {
		if !ok {
			continue
		}
		p.all = append(p.all, n)
		if p.code[n].varying {
			p.varying = append(p.varying, n)
		} else {
			p.uniform = append(p.uniform, n)
		}
	}
}

// Operations is the number of compiled operations, counted against MaxOps.
func (p *Program) Operations() int { return len(p.code) }

// VaryingOperations is the per-cell cost after uniform work is hoisted per frame.
func (p *Program) VaryingOperations() int { return len(p.varying) }
