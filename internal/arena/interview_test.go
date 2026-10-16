package arena

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

func interviewModel(t *testing.T) LiveModel {
	t.Helper()
	p := testProject(t)
	s, err := p.Settings()
	if err != nil {
		t.Fatal(err)
	}
	session := newUISession(context.Background(), p)
	chat := newInterviewSession(session.ctx, p, s)
	chat.probe = func(context.Context, string, Runtime) Readiness {
		return Readiness{CanStart: true, State: "fixture only"}
	}
	chat.discover = func(context.Context, string, Runtime) ([]ModelOption, error) {
		return []ModelOption{{ID: "explicit-test-model", Efforts: []string{"low", "high"}}}, nil
	}
	session.interviews = append(session.interviews, chat)
	t.Cleanup(session.close)
	return LiveModel{Project: p, Run: Run{Workflow: NewWorkflow("setup", "Setup"), Settings: s}, State: RunState{Status: "setup"}, ChatMode: true, Interview: chat, Session: session, Context: session.ctx, Width: 120, Height: 32}
}

func pumpInterview(t *testing.T, m LiveModel, cmd tea.Cmd, answer string) LiveModel {
	t.Helper()
	for m.ChatBusy {
		if cmd == nil {
			t.Fatal("interview stopped listening while busy")
		}
		result := make(chan tea.Msg, 1)
		go func() { result <- cmd() }()
		var msg tea.Msg
		select {
		case msg = <-result:
		case <-time.After(5 * time.Second):
			t.Fatal("interview stalled")
		}
		next, following := m.Update(msg)
		m, cmd = next.(LiveModel), following
		if m.PendingReply != nil {
			next, _ = m.submit(answer)
			m = next.(LiveModel)
		}
	}
	return m
}

func TestArenaInterviewQuestionsPreviewAndExplicitSave(t *testing.T) {
	m := interviewModel(t)
	calls := 0
	m.Interview.invoker = testInvoker(func(ctx context.Context, in Invocation) (map[string]any, error) {
		calls++
		if in.Write || in.Commands || in.Runtime.Model != "explicit-test-model" || in.Runtime.ReasoningEffort != "high" || !strings.Contains(in.Prompt, "Task") && !strings.Contains(in.Prompt, "Arena envelope") {
			return nil, fmt.Errorf("setup invocation lost control runtime/envelope or allowed writes")
		}
		if in.Delivered == nil || in.Schema == nil {
			return nil, fmt.Errorf("missing structured handoff")
		}
		if err := in.Delivered(); err != nil {
			return nil, err
		}
		if calls == 1 {
			return map[string]any{"question": "Which worker model and effort should the head use?", "complete": false, "builderRequestJson": ""}, nil
		}
		w := NewWorkflow("from-chat", "From chat")
		w.HeadAgent = "head"
		w.Agents = []Agent{{Id: "head", Level: Int(0), Profile: ".agents/from-chat/head.agent.json", Runtime: in.Runtime}}
		draft := WorkflowDraft{Workflow: w, Profiles: map[string]Profile{"head": NewProfile("head", "research", "Inspect the project", "Use evidence", []string{"read"})}}
		return map[string]any{"question": "Review the proposed read-only workflow.", "complete": true, "builderRequestJson": string(JSON(draft))}, nil
	})
	next, cmd := m.submit("Build a project")
	m = pumpInterview(t, next.(LiveModel), cmd, "")
	if m.Error != "" || m.Draft != nil || !strings.Contains(m.Chat[len(m.Chat)-1].Text, "Which worker") {
		t.Fatalf("question not rendered: %+v", m.Chat)
	}
	next, cmd = m.submit("Use the same explicitly selected model and high effort for a read-only head")
	m = pumpInterview(t, next.(LiveModel), cmd, "")
	if m.Error != "" || m.Draft == nil {
		t.Fatalf("draft missing: %s", m.Error)
	}
	if _, err := os.Stat(m.Project.Path("from-chat.arena.json")); !os.IsNotExist(err) {
		t.Fatal("draft saved before explicit approval")
	}
	if !strings.Contains(m.View().Content, "/save") {
		t.Fatal("draft save action is invisible")
	}
	next, cmd = m.submit("/save")
	m = next.(LiveModel)
	next, _ = m.Update(cmd())
	m = next.(LiveModel)
	if m.Error != "" || m.ChatMode || m.Run.Workflow.Id != "from-chat" || m.Run.ID != "" {
		t.Fatalf("save did not enter idle graph: %s", m.Error)
	}
	if _, _, err := m.Project.Load("from-chat"); err != nil {
		t.Fatal(err)
	}
	assertNoLaunchRun(t, m.Project)
	if events, err := m.Project.Events("project"); err != nil || len(events) < 5 {
		t.Fatalf("missing audited setup handoffs: %d %v", len(events), err)
	}
}

func TestNativeProviderQuestionStaysInsideArena(t *testing.T) {
	m := interviewModel(t)
	m.Interview.invoker = testInvoker(func(ctx context.Context, in Invocation) (map[string]any, error) {
		answer, err := in.Question(ctx, "Which project name?")
		if err != nil || answer != "my-project" {
			return nil, fmt.Errorf("question reply: %q %v", answer, err)
		}
		return map[string]any{"question": "Which worker role?", "complete": false}, nil
	})
	next, cmd := m.submit("Set up a project")
	m = pumpInterview(t, next.(LiveModel), cmd, "my-project")
	if m.Error != "" || !strings.Contains(m.Transcript, "my-project") {
		t.Fatalf("question failed: %s", m.Error)
	}
}

func TestInterviewShutdownCancelsAttachedInvocation(t *testing.T) {
	m := interviewModel(t)
	started := make(chan struct{})
	m.Interview.invoker = testInvoker(func(ctx context.Context, in Invocation) (map[string]any, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	})
	_, cmd := m.submit("Start the fixture interview")
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("fixture did not start")
	}
	m.Session.close()
	// Even an unread command completes after the owning TUI shuts down.
	_ = cmd()
	lock, err := m.Project.Lock("orchestrator")
	if err != nil {
		t.Fatal("orchestrator lock leaked after quit")
	}
	_ = lock.Close()
}

func TestSlashAgentContextPopupAndTextInput(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	packet := NewPacket(r.ID, "task", "arena", "arena", "head", "codex", "", taskPayload("Actual assigned task", p.Root, r.BaseCommit, []string{"actual handoff context"}, false), r.Workflow.Agents[0].Runtime)
	if _, err := p.Send(r, packet); err != nil {
		t.Fatal(err)
	}
	events, err := p.Events(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	m := LiveModel{Project: p, Run: r, Events: events, State: RunState{Status: "running", Agents: map[string]string{"head": "running"}}, Width: 120, Height: 32}
	next, cmd := m.submit("/context head")
	m = next.(LiveModel)
	next, _ = m.Update(cmd())
	m = next.(LiveModel)
	text := strings.Join(m.PopupLines, "\n")
	if !strings.Contains(text, "actual handoff context") || !strings.Contains(text, "ACTUAL TASK ENVELOPE") || !strings.Contains(text, "AVAILABLE WORKFLOW CONTEXT") {
		t.Fatal("context does not distinguish actual and available inputs")
	}
	for _, size := range [][2]int{{140, 40}, {60, 18}, {30, 8}, {8, 4}} {
		m.Width, m.Height = size[0], size[1]
		lines := strings.Split(m.View().Content, "\n")
		if len(lines) > size[1] {
			t.Fatal("popup exceeds terminal height")
		}
		for _, line := range lines {
			if ansi.StringWidth(line) > size[0] {
				t.Fatal("popup exceeds terminal width")
			}
		}
	}
	next, _ = m.Update(launchKey(tea.KeyEscape))
	m = next.(LiveModel)
	if m.PopupAgent != "" {
		t.Fatal("Esc did not close mini-window")
	}
	m.ChatMode, m.Offset = true, 1<<30
	for i := 0; i < 100; i++ {
		m.Chat = append(m.Chat, chatLine{"you", "previous context line"})
	}
	next, _ = m.Update(launchKey(tea.KeyPgUp))
	m = next.(LiveModel)
	if m.Offset >= 1<<29 {
		t.Fatal("follow-tail prevented scrolling back through chat")
	}
	m.ChatMode = false
	for _, char := range "test generate questions" {
		next, _ = m.Update(tea.KeyPressMsg{Code: char, Text: string(char)})
		m = next.(LiveModel)
	}
	if m.Prompt != "test generate questions" {
		t.Fatalf("shortcuts consumed prompt text: %q", m.Prompt)
	}
	m.Prompt = "/res"
	m.completePrompt()
	if m.Prompt != "/resume " {
		t.Fatal("slash completion missed grouped command")
	}
	if _, err := m.resolveAgent("missing"); err == nil {
		t.Fatal("unknown agent silently substituted")
	}
}

func TestActivityProjectionExcludesPrivateReasoning(t *testing.T) {
	if codexPublicActivity(map[string]any{"type": "reasoning", "text": "private fixture reasoning", "summary": "private fixture"}, "item/completed") != nil {
		t.Fatal("private reasoning entered activity diagnostics")
	}
	if text := visibleActivity(Event{Type: "reasoning", Data: map[string]any{"text": "private fixture"}}); text != "" {
		t.Fatal("private reasoning entered mini-window")
	}
	data := codexPublicActivity(map[string]any{"type": "commandExecution", "command": "go test ./...", "status": "completed", "exitCode": 0, "rawThought": "private fixture"}, "item/completed")
	text := string(SafeJSON(data))
	if !strings.Contains(text, "go test") || strings.Contains(text, "private fixture") {
		t.Fatal("activity projection lost tools or retained private data")
	}
}

func TestMediatedProviderToolsEmitPublicActivityWithoutArguments(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(dir+"/file.txt", []byte("fixture content"), 0600); err != nil {
		t.Fatal(err)
	}
	var events []Event
	turns := 0
	_, err := toolLoop(context.Background(), Invocation{Dir: dir, Diagnostic: func(kind string, data map[string]any) error {
		events = append(events, Event{Type: kind, Data: data})
		return nil
	}}, func(context.Context, []chatMessage) (string, error) {
		turns++
		if turns == 1 {
			return `{"action":"read_file","arguments":{"path":"file.txt"}}`, nil
		}
		return `{"action":"complete","result":{"summary":"read fixture"}}`, nil
	})
	if err != nil || len(events) != 2 || !strings.Contains(visibleActivity(events[0]), "read_file") || strings.Contains(string(SafeJSON(events)), "fixture content") {
		t.Fatalf("mediated activity: %v %v", events, err)
	}
}

func TestInvalidDraftIsRepairedByOrchestratorBeforeUserSeesIt(t *testing.T) {
	m := interviewModel(t)
	calls := 0
	m.Interview.invoker = testInvoker(func(ctx context.Context, in Invocation) (map[string]any, error) {
		calls++
		if err := in.Delivered(); err != nil {
			return nil, err
		}
		if !strings.Contains(in.Prompt, "explicit-test-model") || !strings.Contains(in.Prompt, "Project settings") {
			return nil, fmt.Errorf("catalog or settings missing from setup prompt")
		}
		w := NewWorkflow("repaired", "Repaired")
		w.HeadAgent = "head"
		w.Agents = []Agent{{Id: "head", Level: Int(0), Profile: ".agents/repaired/head.agent.json", Runtime: in.Runtime}, {Id: "worker", Level: Int(1), Profile: ".agents/repaired/worker.agent.json", Runtime: in.Runtime}}
		if calls == 2 {
			if !strings.Contains(in.Prompt, "path node worker is unreachable") {
				return nil, fmt.Errorf("repair prompt lacks validation error")
			}
			w.Relationships = []Relationship{{Type: "delegates", From: "head", To: "worker"}}
		}
		profiles := map[string]Profile{"head": NewProfile("head", "lead", "Plan", "Use evidence", []string{"read"}), "worker": NewProfile("worker", "builder", "Build", "Use evidence", []string{"read"})}
		return map[string]any{"question": "Draft ready.", "complete": true, "builderRequestJson": string(JSON(WorkflowDraft{Workflow: w, Profiles: profiles}))}, nil
	})
	next, cmd := m.submit("Build it; you pick everything")
	m = pumpInterview(t, next.(LiveModel), cmd, "")
	if m.Error != "" || m.Draft == nil || calls != 2 {
		t.Fatalf("draft not repaired: calls=%d err=%s", calls, m.Error)
	}
	repaired := false
	for _, event := range m.ControlEvents {
		repaired = repaired || event.Type == "setup.repair"
	}
	if !repaired {
		t.Fatal("repair attempt not audited")
	}
}
