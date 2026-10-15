package arena

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"unicode"
)

// Parse quoted or shell-escaped terminal drop paths without invoking a shell.
func attachmentPaths(text string) ([]string, error) {
	var paths []string
	var word strings.Builder
	var quote rune
	escaped := false
	for _, r := range strings.TrimSpace(text) {
		switch {
		case escaped:
			word.WriteRune(r)
			escaped = false
		case r == '\\' && quote != '\'':
			escaped = true
		case quote != 0:
			if r == quote {
				quote = 0
			} else {
				word.WriteRune(r)
			}
		case r == '\'' || r == '"':
			quote = r
		case unicode.IsSpace(r):
			if word.Len() > 0 {
				paths = append(paths, word.String())
				word.Reset()
			}
		default:
			word.WriteRune(r)
		}
	}
	if escaped || quote != 0 {
		return nil, fmt.Errorf("unfinished quote or escape in attachment path")
	}
	if word.Len() > 0 {
		paths = append(paths, word.String())
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("usage: /attach PATH [PATH ...]; quote paths containing spaces")
	}
	for i, path := range paths {
		if strings.HasPrefix(path, "file://") {
			u, err := url.Parse(path)
			if err != nil || u.Host != "" && u.Host != "localhost" {
				return nil, fmt.Errorf("only local file URLs can be attached")
			}
			paths[i] = u.Path
		}
	}
	return paths, nil
}

func (m LiveModel) attachmentPath(path string) string {
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	if !filepath.IsAbs(path) && m.Project != nil {
		path = filepath.Join(m.Project.Root, path)
	}
	return path
}

func (m *LiveModel) attachPaths(paths []string) error {
	items := append([]Attachment(nil), m.Attachments...)
	for _, path := range paths {
		a, err := readAttachment(m.attachmentPath(path))
		if err != nil {
			return fmt.Errorf("attach %q: %w", path, err)
		}
		duplicate := false
		for _, old := range items {
			if old.SHA256 == a.SHA256 && old.Name == a.Name {
				duplicate = true
			}
		}
		if !duplicate {
			items = append(items, a)
		}
		if err := attachmentLimits(items); err != nil {
			return err
		}
	}
	m.Attachments = items
	return nil
}

func (m *LiveModel) attachmentCommand(name, arg string) {
	switch name {
	case "attach":
		paths, err := attachmentPaths(arg)
		if err == nil {
			err = m.attachPaths(paths)
		}
		if err != nil {
			m.Error = err.Error()
		}
	case "attachments":
		m.Panel, m.PanelText = "Attachments", []string{"Next message · /detach NUMBER or /detach all · snapshots retained with full-local history"}
		for i, a := range m.Attachments {
			m.PanelText = append(m.PanelText, fmt.Sprintf("%d. %s · %s · %d bytes", i+1, a.Name, a.MediaType, a.Size))
		}
		if len(m.Attachments) == 0 {
			m.PanelText = append(m.PanelText, "No attachments. Use /attach PATH or drop files into an empty composer.")
		}
	case "detach":
		if arg == "all" {
			m.Attachments = nil
			return
		}
		n, err := strconv.Atoi(arg)
		if err != nil || n < 1 || n > len(m.Attachments) {
			m.Error = "usage: /detach NUMBER or /detach all; /attachments lists numbers"
			return
		}
		m.Attachments = append(append([]Attachment(nil), m.Attachments[:n-1]...), m.Attachments[n:]...)
	}
}

func (m *LiveModel) pasteAttachments(text string) bool {
	// Only a whole drop into an empty composer is interpreted as files.
	// Regular pasted prose and paths inside a message remain text.
	if m.Prompt != "" {
		return false
	}
	paths, err := attachmentPaths(text)
	if err != nil {
		return false
	}
	for _, path := range paths {
		if !filepath.IsAbs(path) && !strings.HasPrefix(path, "~/") {
			return false
		}
		if info, err := os.Stat(m.attachmentPath(path)); err != nil || !info.Mode().IsRegular() {
			return false
		}
	}
	if err := m.attachPaths(paths); err != nil {
		m.Error = err.Error()
	}
	return true
}

func (m LiveModel) attachmentSummary() string {
	names := []string{}
	for i, a := range m.Attachments {
		names = append(names, fmt.Sprintf("[%d] %s", i+1, a.Name))
	}
	return strings.Join(names, " · ")
}
