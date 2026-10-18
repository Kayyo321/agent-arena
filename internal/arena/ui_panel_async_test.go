package arena

import (
	"errors"
	"testing"
)

func TestResolutionPanelMsgSetsPanel(t *testing.T) {
	m := LiveModel{Error: "old"}
	next, _ := m.Update(resolutionPanelMsg{lines: []string{"a", "b"}})
	got := next.(LiveModel)
	if got.Panel != "Resolution" || len(got.PanelText) != 2 || got.PanelText[0] != "a" {
		t.Fatalf("panel %q %v", got.Panel, got.PanelText)
	}
}

func TestResolutionPanelMsgErrorSetsError(t *testing.T) {
	m := LiveModel{}
	next, _ := m.Update(resolutionPanelMsg{err: errors.New("boom")})
	got := next.(LiveModel)
	if got.Error != "boom" || got.Panel != "" {
		t.Fatalf("error %q panel %q", got.Error, got.Panel)
	}
}

func TestIntegratePanelIsAsync(t *testing.T) {
	m := LiveModel{Run: Run{ID: "run1"}, Project: &Project{}, Error: "stale"}
	next, cmd := m.submitInput("/integrate")
	got := next.(LiveModel)
	if cmd == nil {
		t.Fatal("expected a command; Git work must not run synchronously")
	}
	if got.Panel != "" || got.Error != "" {
		t.Fatalf("panel %q error %q set synchronously", got.Panel, got.Error)
	}
}
