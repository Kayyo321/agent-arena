package arena

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// auditRun saves a one-agent arena under the given settings and creates a run.
func auditRun(t *testing.T, s Settings) (*Project, Run) {
	t.Helper()
	p := committedProject(t)
	if _, err := p.Commit("audit settings", map[string][]byte{"settings.arena.json": JSON(s)}); err != nil {
		t.Fatal(err)
	}
	w := NewWorkflow("audit", "Audit fixture")
	w.HeadAgent = "head"
	w.Agents = []Agent{{Id: "head", Level: Int(0), Profile: ".agents/audit/head.agent.json", Runtime: s.DefaultModel}}
	profiles := map[string]Profile{"head": NewProfile("head", "research", "Inspect evidence", "Report findings", []string{"read"})}
	if _, err := p.Save(w, profiles, ""); err != nil {
		t.Fatal(err)
	}
	head, err := Git(p.Root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	r, err := p.CreateRun(w, profiles, s, "Audit task", head)
	if err != nil {
		t.Fatal(err)
	}
	return p, r
}

func TestAgentCannotChooseCoworkerRequestPath(t *testing.T) {
	s := DefaultSettings(Runtime{Provider: "codex", Model: "explicit-test-model", ReasoningEffort: "high"})
	s.ApprovalPolicies.ArenaSubagentModificationInquiry = "auto-refuse"
	p, r := auditRun(t, s)
	before, err := os.ReadFile(p.Path("settings.arena.json"))
	if err != nil {
		t.Fatal(err)
	}
	payload := map[string]any{"workflowId": "audit", "role": "research", "objective": "Inspect", "runtime": map[string]any{"provider": "codex", "model": "explicit-test-model", "reasoningEffort": "high"}, "capabilities": []any{"read"}, "workspaceMode": "read-only", "files": []any{}, "reason": "help", "id": "../../settings.arena", "state": "approved"}
	result, err := p.AgentPacket(context.Background(), r, "head", map[string]any{"to": "arena-orchestrator", "kind": "coworker-request", "payload": payload})
	if err != nil {
		t.Fatal(err)
	}
	q := result.(CoworkerRequest)
	if !strings.HasPrefix(q.ID, "request-") || q.State != "refused" {
		t.Fatalf("agent chose request metadata: %+v", q)
	}
	after, _ := os.ReadFile(p.Path("settings.arena.json"))
	if !bytes.Equal(before, after) {
		t.Fatal("coworker request overwrote project settings")
	}
	if _, err := os.Stat(p.Path(".state", "coworker-requests", q.ID+".json")); err != nil {
		t.Fatal(err)
	}
	// A stored request with a tampered ID never names a path either.
	if _, err := p.RequestCoworker(context.Background(), CoworkerRequest{ID: "../../settings.arena", WorkflowID: "audit", From: "head"}, true, nil); err == nil {
		t.Fatal("resolved a request with a traversal ID")
	}
}

func TestMalformedContextProposalIsRefusedWithoutPanic(t *testing.T) {
	p, r := auditRun(t, DefaultSettings(Runtime{Provider: "codex", Model: "explicit-test-model", ReasoningEffort: "high"}))
	_, err := p.AgentPacket(context.Background(), r, "head", map[string]any{"to": "context-manager", "kind": "context-proposal", "payload": map[string]any{"workflowId": "audit", "operation": 123}})
	if err == nil || !strings.Contains(err.Error(), "invalid context-proposal payload") {
		t.Fatalf("malformed proposal: %v", err)
	}
}

// Run with -race (make check): two writers on one buffer used to race.
func TestShellCommandInterleavesStdoutAndStderrSafely(t *testing.T) {
	p := testProject(t)
	m := LiveModel{Project: p}
	result := m.runShell(`i=0; while [ $i -lt 300 ]; do echo out$i; echo err$i >&2; i=$((i+1)); done`)().(shellResult)
	if result.Err != nil || !strings.Contains(result.Output, "out299") || !strings.Contains(result.Output, "err299") {
		t.Fatalf("shell output: %v %q", result.Err, result.Output[max(0, len(result.Output)-40):])
	}
}

func TestGitOutputIsExactOrFailsLoudly(t *testing.T) {
	p := committedProject(t)
	big := strings.Repeat("arena line that makes a large diff\n", 90000) // ~3 MiB
	if err := os.WriteFile(filepath.Join(p.Root, "big.txt"), []byte(big), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := Git(p.Root, "add", "big.txt"); err != nil {
		t.Fatal(err)
	}
	diff, err := Git(p.Root, "diff", "--cached")
	if err != nil {
		t.Fatal(err)
	}
	if len(diff) < len(big) || !strings.HasSuffix(diff, "+arena line that makes a large diff") {
		t.Fatalf("diff truncated to %d bytes", len(diff))
	}
	b := exactBuffer{limit: 4}
	if _, err := b.Write([]byte("12345")); err == nil || !b.overflow {
		t.Fatal("overflow accepted silently")
	}
}
