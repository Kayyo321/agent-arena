package arena

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// userSkillManifest records the hash of every user-level skill copy that
// `arena skill install --user` wrote, so update and uninstall can tell an
// untouched copy from one the user edited.
type userSkillManifest struct {
	SchemaVersion int              `json:"schemaVersion"`
	Skills        []userSkillEntry `json:"skills"`
}

type userSkillEntry struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// stateDir is $XDG_STATE_HOME/arena, or ~/.local/state/arena. A relative
// XDG_STATE_HOME is ignored, as the XDG spec requires, so state never lands in
// whatever directory the command ran from.
func stateDir() (string, error) {
	if x := os.Getenv("XDG_STATE_HOME"); filepath.IsAbs(x) {
		return filepath.Join(x, "arena"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "state", "arena"), nil
}

func userSkillManifestPath() (string, error) {
	dir, err := stateDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "user-skills.json"), nil
}

// loadUserSkills returns an empty manifest when the file does not exist yet.
func loadUserSkills() (userSkillManifest, error) {
	m := userSkillManifest{SchemaVersion: 1}
	path, err := userSkillManifestPath()
	if err != nil {
		return m, err
	}
	if err := ReadJSON(path, &m); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return userSkillManifest{SchemaVersion: 1}, nil
		}
		return m, fmt.Errorf("read %s: %w", path, err)
	}
	return m, nil
}

func saveUserSkills(m userSkillManifest) error {
	path, err := userSkillManifestPath()
	if err != nil {
		return err
	}
	m.SchemaVersion = 1
	if m.Skills == nil {
		m.Skills = []userSkillEntry{}
	}
	return Atomic(path, JSON(m), 0644)
}

// recordUserSkill upserts path with the hash of the content just written.
func recordUserSkill(path string, content []byte) error {
	m, err := loadUserSkills()
	if err != nil {
		return err
	}
	entry := userSkillEntry{Path: path, SHA256: Hash(content)}
	for i := range m.Skills {
		if m.Skills[i].Path == path {
			m.Skills[i] = entry
			return saveUserSkills(m)
		}
	}
	m.Skills = append(m.Skills, entry)
	return saveUserSkills(m)
}

// forgetUserSkill drops path from the manifest. It is a no-op when the path was
// never tracked, and it does not create the file.
func forgetUserSkill(path string) error {
	m, err := loadUserSkills()
	if err != nil {
		return err
	}
	kept := m.Skills[:0]
	for _, entry := range m.Skills {
		if entry.Path != path {
			kept = append(kept, entry)
		}
	}
	if len(kept) == len(m.Skills) {
		return nil
	}
	m.Skills = kept
	return saveUserSkills(m)
}
