package shader

import _ "embed"

//go:embed dots.hlsl
var DefaultSource string

var Default = func() *Program {
	p, err := Compile(DefaultSource)
	if err != nil {
		panic(err)
	}
	return p
}()
