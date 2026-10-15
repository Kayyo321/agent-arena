package arena

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

const tinyPNG = "iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAIAAAD91JpzAAAAEUlEQVR4nGP4z8AARGDiPwMAHfAD/aAzCYkAAAAASUVORK5CYII="

func imageAttachment() Attachment {
	b, _ := base64.StdEncoding.DecodeString(tinyPNG)
	return attachmentFromBytes("picture.png", b)
}

func TestAttachmentSnapshotIntegrityAndIsolation(t *testing.T) {
	p := committedProject(t)
	original := filepath.Join(t.TempDir(), "notes with spaces.txt")
	if err := os.WriteFile(original, []byte("original content"), 0600); err != nil {
		t.Fatal(err)
	}
	a, err := readAttachment(original)
	if err != nil {
		t.Fatal(err)
	}
	saved, err := p.saveAttachments("run-fixture", []Attachment{a, imageAttachment()})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(original, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := p.loadAttachments("run-fixture", saved)
	if err != nil || string(loaded[0].data) != "original content" {
		t.Fatalf("snapshot changed: %v", err)
	}
	in := Invocation{Prompt: "inspect", Attachments: loaded, AttachmentDir: p.RunDir("run-fixture")}
	input := codexInput(in)
	if len(input) != 2 || input[1]["url"] != "data:image/png;base64,"+tinyPNG || !strings.Contains(str(input[0]["text"]), p.RunDir("run-fixture")) || strings.Contains(str(input[0]["text"]), original) {
		t.Fatal("attachments not delivered as snapshots/native images")
	}
	if _, err := os.Stat(filepath.Join(p.RunDir("another-run"), saved[0].File)); !os.IsNotExist(err) {
		t.Fatal("attachment leaked into another run")
	}
	if err := os.WriteFile(filepath.Join(p.RunDir("run-fixture"), saved[0].File), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.loadAttachments("run-fixture", saved); err == nil {
		t.Fatal("accepted corrupt snapshot")
	}
	saved[0].File = "../outside"
	if _, err := p.loadAttachments("run-fixture", saved); err == nil {
		t.Fatal("accepted escaping path")
	}
}

func TestAttachmentCommandsAndDraftPreservation(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "a b.txt")
	if err := os.WriteFile(path, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	m := LiveModel{Project: &Project{Root: root}, Run: Run{Settings: DefaultSettings(Runtime{})}, Width: 80, Height: 24}
	next, _ := m.submit(`/attach "a b.txt"`)
	m = next.(LiveModel)
	if m.Error != "" || len(m.Attachments) != 1 || !strings.Contains(m.View().Content, "a b.txt") {
		t.Fatalf("attach failed: %s", m.Error)
	}
	m = inputText(m, "inspect this")
	m = inputKey(m, tea.KeyEnter, 0)
	if m.Prompt != "inspect this" || len(m.Attachments) != 1 || m.Error == "" {
		t.Fatal("failed send lost draft")
	}
	next, _ = m.submit("/detach 1")
	m = next.(LiveModel)
	if len(m.Attachments) != 0 {
		t.Fatal("detach failed")
	}
	m.setPrompt("", 0)
	m = inputText(m, strings.ReplaceAll(path, " ", `\ `))
	if len(m.Attachments) != 1 || m.Prompt != "" {
		t.Fatal("drop path was sent as text")
	}
	m = inputText(m, "ordinary\nmultiline paste")
	if m.Prompt != "ordinary\nmultiline paste" {
		t.Fatal("prose became an attachment")
	}
	if err := m.attachPaths([]string{path, filepath.Join(root, "missing")}); err == nil || len(m.Attachments) != 1 {
		t.Fatal("partial attach was not atomic")
	}
}

func TestAttachmentLimitsAndProviderRefusal(t *testing.T) {
	a := imageAttachment()
	for _, mode := range []string{"redacted-local", "ephemeral"} {
		if attachmentPolicy([]Attachment{a}, mode, []Runtime{{Provider: "codex"}}) == nil {
			t.Fatal("retention bypass")
		}
	}
	if attachmentPolicy([]Attachment{a}, "full-local", []Runtime{{Provider: "local"}}) == nil {
		t.Fatal("silently dropped unsupported image")
	}
	if attachmentPolicy([]Attachment{a}, "full-local", []Runtime{{Provider: "codex"}}) != nil {
		t.Fatal("Codex image refused")
	}
	if attachmentLimits(make([]Attachment, 9)) == nil {
		t.Fatal("count limit")
	}
	if attachmentLimits([]Attachment{{Size: maxAttachmentTotal + 1}}) == nil {
		t.Fatal("byte limit")
	}
	if _, err := readAttachment(t.TempDir()); err == nil {
		t.Fatal("directory accepted")
	}
	path := filepath.Join(t.TempDir(), "huge")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	err = f.Truncate(maxAttachmentBytes + 1)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := readAttachment(path); err == nil {
		t.Fatal("oversized file accepted")
	}
}

func TestAttachmentMediatedReadAndRunHandoff(t *testing.T) {
	p := committedProject(t)
	r := teamRun(t, p)
	a := attachmentFromBytes("notes.txt", []byte("attachment evidence"))
	var err error
	r.Attachments, err = p.saveAttachments(r.ID, []Attachment{a})
	if err != nil {
		t.Fatal(err)
	}
	called := false
	fake := testInvoker(func(ctx context.Context, in Invocation) (map[string]any, error) {
		called = true
		if len(in.Attachments) != 1 || !strings.Contains(in.Prompt, `"attachments"`) {
			return nil, fmt.Errorf("missing envelope attachments")
		}
		out, err := performTool(ctx, in, "read_attachment", map[string]any{"path": a.File})
		if err != nil || out.(map[string]any)["text"] != "attachment evidence" {
			return nil, fmt.Errorf("attachment tool: %v", err)
		}
		if _, err := performTool(ctx, in, "read_attachment", map[string]any{"path": "../secret"}); err == nil {
			return nil, fmt.Errorf("unscoped attachment read")
		}
		if in.Schema["properties"].(map[string]any)["verdict"] != nil {
			return map[string]any{"verdict": "Approved", "acceptTeamVerdict": true, "summary": "fixture", "evidence": []any{}, "rationale": "fixture evidence", "nextAction": "inspect"}, nil
		}
		return ordinaryResult(), nil
	})
	if err := NewEngine(p, r, fake).Execute(context.Background(), false, false); err != nil {
		t.Fatal(err)
	}
	if !called {
		t.Fatal("no invocation")
	}
}

func TestInterviewRetainsAttachmentsAcrossTurns(t *testing.T) {
	m := interviewModel(t)
	m.Attachments = []Attachment{imageAttachment()}
	m.Interview.invoker = testInvoker(func(ctx context.Context, in Invocation) (map[string]any, error) {
		if len(in.Attachments) != 1 || len(codexInput(in)) != 2 {
			return nil, fmt.Errorf("image lost")
		}
		return map[string]any{"question": "What next?", "complete": false, "builderRequestJson": ""}, nil
	})
	for _, text := range []string{"inspect image", "describe the image again"} {
		next, cmd := m.submit(text)
		m = pumpInterview(t, next.(LiveModel), cmd, "")
		if m.Error != "" {
			t.Fatal(m.Error)
		}
	}
	events, err := m.Project.Events("project")
	if err != nil {
		t.Fatal(err)
	}
	found := 0
	for _, event := range events {
		if event.Type != "message.sent" || event.Data["kind"] != "task" {
			continue
		}
		packet, err := m.Project.ReadPacket("project", str(event.Data["messageId"]))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(JSON(packet)), `"attachments"`) {
			t.Fatal("attachment missing from task packet")
		}
		found++
	}
	if found != 2 {
		t.Fatalf("expected two retained task packets, got %d", found)
	}
}

func TestMultilineEditingAndMinimalFooter(t *testing.T) {
	m := LiveModel{Width: 80, Height: 24}
	m = inputText(m, "first")
	m = inputKey(m, tea.KeyEnter, tea.ModShift)
	m = inputText(m, "second")
	if m.Prompt != "first\nsecond" || m.Starting {
		t.Fatal("Shift+Return sent message")
	}
	m = inputKey(m, tea.KeyHome, 0)
	m = inputKey(m, tea.KeyUp, 0)
	m = inputText(m, "X")
	if m.Prompt != "Xfirst\nsecond" {
		t.Fatal("up did not edit prior line: ", m.Prompt)
	}
	m = inputKey(m, tea.KeyDown, 0)
	m = inputKey(m, tea.KeyEnd, 0)
	m = inputText(m, "!")
	if m.Prompt != "Xfirst\nsecond!" {
		t.Fatal("line end editing: ", m.Prompt)
	}
	m.rememberPrompt("history")
	m = inputKey(m, 'p', tea.ModCtrl)
	if m.Prompt != "history" {
		t.Fatal("explicit history")
	}
	m = inputKey(m, 'n', tea.ModCtrl)
	if m.Prompt != "Xfirst\nsecond!" {
		t.Fatal("history draft lost")
	}
	for _, width := range []int{8, 80, 140} {
		rule := promptRule(width, false)
		if !strings.Contains(rule, "/help") || strings.Contains(rule, "history") || strings.Contains(rule, "scroll") {
			t.Fatal("footer not minimal: ", rule)
		}
	}
}

func TestAttachedRunPersistsBeforeExecutionAndRecoversFailedDraft(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	s := newUISession(context.Background(), p)
	defer s.close()
	s.prepare = func(context.Context, *Project, string, string) (Run, []Readiness, error) { return r, nil, nil }
	executed := make(chan error, 1)
	s.execute = func(_ context.Context, p *Project, r Run) error {
		saved, err := p.ReadRun(r.ID)
		if err == nil && len(saved.Attachments) != 1 {
			err = fmt.Errorf("manifest missing snapshot")
		}
		if err == nil {
			_, err = p.loadAttachments(r.ID, saved.Attachments)
		}
		executed <- err
		return err
	}
	m := LiveModel{Project: p, Run: Run{Workflow: r.Workflow, Settings: r.Settings}, Session: s, State: RunState{Status: "idle"}, Attachments: []Attachment{imageAttachment()}}
	next, cmd := m.submit("inspect image")
	m = next.(LiveModel)
	if cmd == nil || !m.Starting || len(m.Attachments) != 0 {
		t.Fatal("did not dispatch attachment")
	}
	started := cmd().(uiRunStarted)
	if started.Err != nil {
		t.Fatal(started.Err)
	}
	if err := <-executed; err != nil {
		t.Fatal(err)
	}
	next, _ = m.Update(started)
	m = next.(LiveModel)
	if len(m.SendingAttachments) != 0 {
		t.Fatal("sent draft retained in composer")
	}
	// A new draft typed while preparation fails must not erase either draft.
	m = LiveModel{SendingPrompt: "failed task", SendingAttachments: []Attachment{imageAttachment()}, Attachments: []Attachment{attachmentFromBytes("new.txt", []byte("new"))}}
	m = inputText(m, "new draft")
	next, _ = m.Update(uiRunStarted{Err: fmt.Errorf("fixture readiness failure")})
	m = next.(LiveModel)
	if !strings.Contains(m.Prompt, "failed task") || !strings.Contains(m.Prompt, "new draft") || len(m.Attachments) != 2 {
		t.Fatal("failed preparation lost a draft")
	}
}

func TestAttachmentEnvelopeRejectsMalformedMetadata(t *testing.T) {
	a := imageAttachment()
	payload := taskPayload("inspect", ".", "fixture", []string{}, false)
	payload["attachments"] = []Attachment{a}
	packet := NewPacket("run-fixture", "task", "arena", "arena", "head", "codex", "", payload, Runtime{Provider: "codex"})
	if err := ValidatePacket(packet, Workflow{}); err != nil {
		t.Fatal(err)
	}
	a.SHA256 = "not-a-hash"
	payload["attachments"] = []Attachment{a}
	if err := ValidatePacket(packet, Workflow{}); err == nil {
		t.Fatal("malformed attachment envelope accepted")
	}
}
