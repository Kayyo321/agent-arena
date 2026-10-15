// Package assets contains the single source of installed skill and schema assets.
package assets

import "embed"

//go:embed .agents/skills/arena-orchestrator/SKILL.md schemas/*.json providers/*.json
var Files embed.FS

func Skill() []byte {
	b, err := Files.ReadFile(".agents/skills/arena-orchestrator/SKILL.md")
	if err != nil {
		panic(err)
	}
	return b
}
