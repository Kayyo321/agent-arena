package arena

import (
	"fmt"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func inputKey(m LiveModel, code rune, mod tea.KeyMod) LiveModel {
	next, _ := m.Update(tea.KeyPressMsg{Code: code, Mod: mod})
	return next.(LiveModel)
}

func inputText(m LiveModel, text string) LiveModel {
	next, _ := m.Update(tea.PasteMsg{Content: text})
	return next.(LiveModel)
}

func TestPromptEditsAtCursorWithoutSplittingGraphemes(t *testing.T) {
	m := inputText(LiveModel{}, "Ae\u0301👩🏽‍💻界Z")
	m = inputKey(m, tea.KeyLeft, 0)
	m = inputKey(m, tea.KeyLeft, 0)
	m = inputKey(m, tea.KeyBackspace, 0)
	if m.Prompt != "Ae\u0301界Z" {
		t.Fatalf("emoji deletion: %q", m.Prompt)
	}
	m = inputKey(m, tea.KeyLeft, 0)
	m = inputKey(m, tea.KeyDelete, 0)
	m = inputText(m, "B")
	if m.Prompt != "AB界Z" {
		t.Fatalf("middle insertion/deletion: %q", m.Prompt)
	}
	m = inputKey(m, tea.KeyHome, 0)
	m = inputKey(m, tea.KeyBackspace, 0)
	m = inputText(m, "start ")
	m = inputKey(m, tea.KeyEnd, 0)
	m = inputKey(m, tea.KeyDelete, 0)
	m = inputText(m, " end")
	if m.Prompt != "start AB界Z end" {
		t.Fatalf("boundaries: %q", m.Prompt)
	}
}

func TestPromptHistoryRestoresDraftAndDoesNotExecuteRecall(t *testing.T) {
	m := LiveModel{}
	for _, text := range []string{"first message", "second message"} {
		m.rememberPrompt(text)
	}
	m = inputText(m, "unfinished draft")
	m = inputKey(m, tea.KeyLeft, 0)
	draftCursor := m.input.cursor
	m = inputKey(m, tea.KeyUp, 0)
	if m.Prompt != "second message" {
		t.Fatal(m.Prompt)
	}
	m = inputKey(m, tea.KeyUp, 0)
	m = inputKey(m, tea.KeyUp, 0)
	if m.Prompt != "first message" {
		t.Fatal(m.Prompt)
	}
	m = inputText(m, " edited")
	m = inputKey(m, tea.KeyDown, 0)
	m = inputKey(m, tea.KeyDown, 0)
	if m.Prompt != "unfinished draft" || m.input.cursor != draftCursor {
		t.Fatal("draft or caret lost")
	}
	if m.Starting || m.Executing || m.input.history[0] != "first message" {
		t.Fatal("recall changed execution or saved history")
	}
}

func TestBusyPromptKeepsDraftAndHistory(t *testing.T) {
	m := LiveModel{ChatMode: true, ChatBusy: true}
	m = inputText(m, "wait for me")
	m = inputKey(m, tea.KeyEnter, 0)
	if m.Prompt != "wait for me" || len(m.input.history) != 0 {
		t.Fatal("busy input was lost or recorded as sent")
	}
}

func TestPromptWordEditingCompletionAndPaste(t *testing.T) {
	m := inputText(LiveModel{}, "one two three")
	m = inputKey(m, tea.KeyLeft, tea.ModAlt)
	m = inputKey(m, 'w', tea.ModCtrl)
	if m.Prompt != "one three" {
		t.Fatal(m.Prompt)
	}
	m = inputKey(m, 'u', tea.ModCtrl)
	if m.Prompt != "three" {
		t.Fatal(m.Prompt)
	}
	m = inputKey(m, 'k', tea.ModCtrl)
	m = inputText(m, "/res")
	m = inputKey(m, tea.KeyTab, 0)
	m = inputText(m, "now")
	if m.Prompt != "/resume now" {
		t.Fatal("completion caret: ", m.Prompt)
	}
	m = inputKey(m, tea.KeyHome, 0)
	m = inputText(m, "a\r\nb\t\x1b[31mc\x00")
	if !strings.HasPrefix(m.Prompt, "a\nb    c/resume") || strings.ContainsAny(m.Prompt, "\r\x1b\x00") {
		t.Fatal("paste was not sanitized: ", m.Prompt)
	}
	m = inputKey(m, tea.KeyEnter, tea.ModAlt)
	if len(strings.Split(m.Prompt, "\n")) != 3 {
		t.Fatal("newline submitted instead of inserted")
	}
}

func TestComposerAndPopupKeepCursorVisibleAfterResize(t *testing.T) {
	w, profiles, settings := fixture(t)
	m := LiveModel{Run: Run{Workflow: w, Profiles: profiles, Settings: settings}}
	m = inputText(m, strings.Repeat("界e\u0301👩🏽‍💻 long message\n", 20))
	for _, size := range [][2]int{{140, 40}, {80, 24}, {30, 8}, {8, 4}} {
		for _, popup := range []string{"", "arena-orchestrator"} {
			m.Width, m.Height, m.PopupAgent = size[0], size[1], popup
			for _, key := range []rune{tea.KeyEnd, tea.KeyHome, tea.KeyRight} {
				m = inputKey(m, key, 0)
				view := m.View()
				lines := strings.Split(view.Content, "\n")
				if len(lines) > size[1] {
					t.Fatalf("%v: too many rows", size)
				}
				for _, line := range lines {
					if ansi.StringWidth(line) > size[0] {
						t.Fatalf("%v: row overflow", size)
					}
				}
				if view.Cursor == nil || view.Cursor.X < 0 || view.Cursor.X >= size[0] || view.Cursor.Y < 0 || view.Cursor.Y >= size[1] {
					t.Fatalf("%v: cursor out of bounds: %+v", size, view.Cursor)
				}
				if view.Cursor.Y < size[1]-5 {
					t.Fatal("popup covered composer cursor")
				}
			}
		}
	}
}

func TestHistoryBoundedAndPanelNavigationSeparate(t *testing.T) {
	m := LiveModel{Run: Run{Settings: DefaultSettings(Runtime{})}}
	for i := range 120 {
		m.rememberPrompt(fmt.Sprintf("message %d", i))
	}
	m.rememberPrompt("message 119")
	if len(m.input.history) != 100 || m.input.history[0] != "message 20" {
		t.Fatal("history cap/deduplication")
	}
	m.Detail = true
	m.Events = []Event{{Type: "first"}, {Type: "second"}}
	m = inputKey(m, tea.KeyDown, tea.ModAlt)
	if m.Selected != 1 || m.Prompt != "" {
		t.Fatal("Alt+Down did not select event")
	}
	m = inputKey(m, tea.KeyUp, 0)
	if m.Prompt != "message 119" || m.Selected != 1 {
		t.Fatal("history moved event selection")
	}
}

func TestModernBordersAndASCIICompatibility(t *testing.T) {
	t.Setenv("TERM", "xterm-256color")
	t.Setenv("LC_ALL", "en_US.UTF-8")
	t.Setenv("NO_COLOR", "1")
	w, p, s := fixture(t)
	m := LiveModel{Run: Run{Workflow: w, Profiles: p, Settings: s}, Width: 140, Height: 40}
	view := m.View().Content
	if !strings.Contains(view, "╭─") || strings.Contains(view, "+---") || strings.Contains(view, "\x1b") {
		t.Fatal("rounded monochrome rendering failed")
	}
	t.Setenv("LC_ALL", "C")
	view = m.View().Content
	if strings.ContainsAny(view, "╭╮╰╯│─") || !strings.Contains(view, "+---") {
		t.Fatal("ASCII border fallback failed")
	}
}

func TestDetailShortcutsPreserveComposerAndShowSelectedEvent(t *testing.T) {
	m := inputText(LiveModel{Run: Run{Settings: DefaultSettings(Runtime{})}, ChatMode: true, Width: 60, Height: 12}, "unfinished task")
	for i := range 12 {
		m.Events = append(m.Events, Event{Sequence: i + 1, Type: "event", Data: map[string]any{"detail": strings.Repeat("line\n", 30)}})
	}
	m = inputKey(m, 'e', tea.ModCtrl)
	if m.ChatMode || !m.Detail || m.Prompt != "unfinished task" {
		t.Fatal("detail shortcut lost input or stayed in chat")
	}
	for range 10 {
		m = inputKey(m, tea.KeyDown, tea.ModAlt)
	}
	if !strings.Contains(m.View().Content, "> 0011") {
		t.Fatal("selected event was scrolled out of view")
	}
	m = inputKey(m, 'g', tea.ModCtrl)
	if !m.GitDetail || m.Detail || m.Prompt != "unfinished task" {
		t.Fatal("views did not switch cleanly")
	}
}

func TestGraphUnicodeLabelsKeepTerminalCellGeometry(t *testing.T) {
	t.Setenv("LC_ALL", "en_US.UTF-8")
	t.Setenv("TERM", "xterm-256color")
	w, _, _ := fixture(t)
	w.Agents[0].Runtime.Model = "模型 👩🏽‍💻 e\u0301"
	w.Teams[0].Name = "検証チーム"
	for _, color := range []bool{false, true} {
		for _, line := range wideGraph(w, RunState{}, nil, 120, color) {
			if ansi.StringWidth(line) > 120 {
				t.Fatal("wide label overflowed graph: ", line)
			}
		}
	}
}

func TestMouseWheelScrollsWithoutRecallingHistory(t *testing.T) {
	m := LiveModel{Run: Run{Settings: DefaultSettings(Runtime{})}, Width: 40, Height: 10, ChatMode: true}
	for i := 0; i < 30; i++ {
		m.Chat = append(m.Chat, chatLine{"Arena", fmt.Sprintf("line %d", i)})
	}
	m.rememberPrompt("sent message")
	m.Offset = 1 << 30
	next, _ := m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelUp})
	m = next.(LiveModel)
	if m.Prompt != "" {
		t.Fatalf("wheel recalled history: %q", m.Prompt)
	}
	end := m.scrollEnd(false)
	if m.Offset != end-3 {
		t.Fatalf("wheel up offset %d, end %d", m.Offset, end)
	}
	next, _ = m.Update(tea.MouseWheelMsg{Button: tea.MouseWheelDown})
	if next.(LiveModel).Offset != end {
		t.Fatalf("wheel down offset %d", next.(LiveModel).Offset)
	}
	if m.View().MouseMode != tea.MouseModeCellMotion {
		t.Fatal("live view must capture the wheel")
	}
}
