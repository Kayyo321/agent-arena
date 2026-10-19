package arena

import (
	"fmt"
	"os"

	assets "github.com/Kayyo321/agent-arena"
)

var skillPaths = map[string]string{"codex": ".agents/skills/arena-orchestrator/SKILL.md", "claude": ".claude/skills/arena-orchestrator/SKILL.md", "gemini": ".agents/skills/arena-orchestrator/SKILL.md", "gemini-native": ".gemini/skills/arena-orchestrator/SKILL.md", "copilot": ".github/skills/arena-orchestrator/SKILL.md", "opencode": ".opencode/skills/arena-orchestrator/SKILL.md"}

func providerSkillState(root, provider string) string {
	// Providers with no host skill file of their own: Arena puts the canonical skill
	// into the prompt of its control calls. OpenCode is one of them, although
	// skillPaths lists an opencode install target for a user who runs Arena from
	// OpenCode: its workers run with OPENCODE_DISABLE_PROJECT_CONFIG=1 and the skill
	// permission denied (openCodePermissions), so a host copy never reaches them.
	if provider == "gemini" || provider == "local" || provider == "opencode" {
		return "canonical skill embedded for Arena control calls"
	}
	target := map[string]string{"codex": "codex", "claude-code": "claude", "antigravity": "gemini"}[provider]
	if target == "" {
		return "unknown host"
	}
	home, _ := os.UserHomeDir()
	for _, location := range []struct{ base, label string }{{root, "project"}, {home, "user"}} {
		if location.base == "" {
			continue
		}
		path, err := SafePath(location.base, skillPaths[target])
		if err != nil {
			continue
		}
		if b, err := os.ReadFile(path); err == nil {
			if Hash(b) == Hash(assets.Skill()) {
				return location.label + ": canonical skill present; host discovery unverified"
			}
			return location.label + ": modified/companion skill present; host discovery unverified"
		}
	}
	return "host copy missing; canonical skill embedded for Arena control calls"
}

func (p *Project) InstallSkill(agent string, user, replace bool) (string, error) {
	rel, ok := skillPaths[agent]
	if !ok {
		return "", fmt.Errorf("unknown skill target; use arena skill targets")
	}
	base := p.Root
	if user {
		var e error
		base, e = os.UserHomeDir()
		if e != nil {
			return "", e
		}
	}
	if user {
		rel = userSkillPath(agent, rel)
	}
	path, e := SafePath(base, rel)
	if e != nil {
		return "", e
	}
	old, e := os.ReadFile(path)
	if e != nil && !os.IsNotExist(e) {
		return "", e
	}
	source := assets.Skill()
	if old != nil && Hash(old) != Hash(source) {
		if !replace {
			return "", fmt.Errorf("%s has different content; inspect before using --replace", path)
		}
		if e := Atomic(p.Path(".state", "skill-backups", ID("skill-")+".md"), old, 0600); e != nil {
			return "", e
		}
	}
	if e := Atomic(path, source, 0644); e != nil {
		return "", e
	}
	if user {
		// Update and uninstall use the recorded hash to tell an untouched copy from an edited one.
		if e := recordUserSkill(path, source); e != nil {
			return "", fmt.Errorf("installed %s but could not record it in the user skill manifest: %w", path, e)
		}
	}
	return path, nil
}
func userSkillPath(agent, project string) string {
	switch agent {
	case "copilot":
		return ".copilot/skills/arena-orchestrator/SKILL.md"
	case "opencode":
		return ".config/opencode/skills/arena-orchestrator/SKILL.md"
	}
	return project
}
