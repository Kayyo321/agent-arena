package arena

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// proposedChange makes a run with one writer that adds file with content, and
// returns the change it proposed. The workflow files are committed so the
// checkout is clean.
func proposedChange(t *testing.T, p *Project, r Run, agent, file, content string) Change {
	t.Helper()
	w, e := p.Workspace(r, agent, agent, true)
	if e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(w.Path, file), []byte(content), 0644); e != nil {
		t.Fatal(e)
	}
	event, e := p.FinishWorkspace(r, w)
	if e != nil || event == nil {
		t.Fatalf("finish: %v %v", event, e)
	}
	c, e := p.Change(r.ID, str(event.Data["changeId"]))
	if e != nil {
		t.Fatal(e)
	}
	return c
}

func commitFile(t *testing.T, p *Project, name, content string) {
	t.Helper()
	if e := os.WriteFile(filepath.Join(p.Root, name), []byte(content), 0644); e != nil {
		t.Fatal(e)
	}
	if _, e := Git(p.Root, "add", name); e != nil {
		t.Fatal(e)
	}
	if _, e := Git(p.Root, "-c", "user.name=Test", "-c", "user.email=test@localhost", "commit", "-qm", "work on "+name); e != nil {
		t.Fatal(e)
	}
}

func requestFixture(t *testing.T) (*Project, Run, *controlClient) {
	t.Helper()
	p := committedProject(t)
	r := testRun(t, p)
	if _, e := Git(p.Root, "add", ".arena"); e != nil {
		t.Fatal(e)
	}
	if _, e := Git(p.Root, "-c", "user.name=Test", "-c", "user.email=test@localhost", "commit", "-qm", "workflow"); e != nil {
		t.Fatal(e)
	}
	c := startControl(t, newControlServer(p))
	t.Cleanup(func() { c.close() })
	return p, r, c
}

type changeRequestResult struct {
	DecisionID string `json:"decisionId"`
	State      string `json:"state"`
	Reused     bool   `json:"reused"`
	Action     string `json:"action"`
	Preview    map[string]any
}

func request(t *testing.T, c *controlClient, tool, run, change string) changeRequestResult {
	t.Helper()
	var out changeRequestResult
	if e := json.Unmarshal([]byte(c.mustCall(tool, map[string]any{"runId": run, "changeId": change})), &out); e != nil {
		t.Fatal(e)
	}
	return out
}

func eventsOf(t *testing.T, p *Project, run, kind string) []Event {
	t.Helper()
	events, e := p.Events(run)
	if e != nil {
		t.Fatal(e)
	}
	out := []Event{}
	for _, v := range events {
		if v.Type == kind {
			out = append(out, v)
		}
	}
	return out
}

func mustFail(t *testing.T, c *controlClient, tool, run, change, want string) {
	t.Helper()
	text, isError := c.call(tool, map[string]any{"runId": run, "changeId": change})
	if !isError || !strings.Contains(text, want) {
		t.Fatalf("%s: want an error containing %q, got %v %q", tool, want, isError, text)
	}
}

// requireUnverifiedApproval checks the provenance recorded for an approval: the
// entry point it came through, and that no identity is claimed as verified.
func requireUnverifiedApproval(t *testing.T, data map[string]any, channel string) {
	t.Helper()
	if claimed, has := data["approvedBy"]; has {
		t.Fatalf("event still attributes the approval to %v: %+v", claimed, data)
	}
	approval, ok := data["approval"].(map[string]any)
	if !ok || approval["channel"] != channel || approval["identityVerified"] != false || approval["verification"] != "none" {
		t.Fatalf("approval record = %+v, want channel %q and no verified identity", data["approval"], channel)
	}
}

func TestControlIntegrateOnlyRequestsAndTheHumanApprovalExecutesOnce(t *testing.T) {
	p, r, c := requestFixture(t)
	change := proposedChange(t, p, r, "one", "one.txt", "one\n")

	first := request(t, c, "arena_integrate", r.ID, change.ID)
	if first.State != "pending" || first.Reused || first.DecisionID == "" || first.Preview["currentHead"] == nil {
		t.Fatalf("request: %+v", first)
	}
	if again := request(t, c, "arena_integrate", r.ID, change.ID); again.DecisionID != first.DecisionID || !again.Reused {
		t.Fatalf("second request did not reuse the open decision: %+v", again)
	}
	open, e := p.Decisions(r.ID)
	if e != nil || len(open) != 1 || open[0].Kind != decisionIntegrate || open[0].AgentID != controlActor || strings.Join(open[0].Choices, ",") != "approve,reject" {
		t.Fatalf("decisions: %+v %v", open, e)
	}
	if after, _ := p.Change(r.ID, change.ID); after.State != "proposed" {
		t.Fatalf("requesting integrated the change: %s", after.State)
	}
	if _, e := os.Stat(filepath.Join(p.Root, "one.txt")); !os.IsNotExist(e) {
		t.Fatal("requesting changed the checkout")
	}
	if len(eventsOf(t, p, r.ID, "decision.answered")) != 0 || len(eventsOf(t, p, r.ID, "change.integrated")) != 0 {
		t.Fatal("request answered or integrated")
	}

	// The CLI refuses to approve without a terminal, so an agent's command
	// runner cannot approve its own request.
	refused := NewCLI(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}).Run(context.Background(), []string{"decision", "answer", r.ID, first.DecisionID, "approve", "--project", p.Root})
	if refused == nil || !strings.Contains(refused.Error(), "needs a person at a terminal") {
		t.Fatalf("approval without a terminal: %v", refused)
	}
	if len(eventsOf(t, p, r.ID, "decision.answered")) != 0 {
		t.Fatal("a refused approval answered the decision")
	}
	outcome, e := p.AnswerDecision(r.ID, first.DecisionID, "approve", Approval{Channel: ApprovalTUI})
	if e != nil || outcome["resultState"] != "integrated" {
		t.Fatalf("approval: %v %+v", e, outcome)
	}
	if b, e := os.ReadFile(filepath.Join(p.Root, "one.txt")); e != nil || string(b) != "one\n" {
		t.Fatalf("not integrated: %v", e)
	}
	executed := eventsOf(t, p, r.ID, "decision.executed")
	if len(executed) != 1 || executed[0].Data["requestedBy"] != controlActor || executed[0].Data["changeId"] != change.ID {
		t.Fatalf("journal: %+v", executed)
	}
	requireUnverifiedApproval(t, executed[0].Data, ApprovalTUI)
	if answered := eventsOf(t, p, r.ID, "decision.answered"); len(answered) != 1 {
		t.Fatalf("answered %d times", len(answered))
	} else {
		requireUnverifiedApproval(t, answered[0].Data, ApprovalTUI)
	}
	if integrated := eventsOf(t, p, r.ID, "change.integrated"); len(integrated) != 1 {
		t.Fatalf("integrated %d times", len(integrated))
	} else {
		requireUnverifiedApproval(t, integrated[0].Data, ApprovalTUI)
	}

	if e := p.Answer(r.ID, first.DecisionID, "approve"); e == nil || !strings.Contains(e.Error(), "already answered") {
		t.Fatalf("second approval: %v", e)
	}
	if len(eventsOf(t, p, r.ID, "change.integrated")) != 1 || len(eventsOf(t, p, r.ID, "decision.executed")) != 1 {
		t.Fatal("second approval executed again")
	}
	mustFail(t, c, "arena_integrate", r.ID, change.ID, "change is integrated")
}

func TestControlRejectedRequestDoesNothing(t *testing.T) {
	p, r, c := requestFixture(t)
	change := proposedChange(t, p, r, "one", "one.txt", "one\n")
	d := request(t, c, "arena_integrate", r.ID, change.ID)
	if e := p.Answer(r.ID, d.DecisionID, "reject"); e != nil {
		t.Fatal(e)
	}
	if after, _ := p.Change(r.ID, change.ID); after.State != "proposed" {
		t.Fatalf("state %s", after.State)
	}
	if len(eventsOf(t, p, r.ID, "decision.executed")) != 0 {
		t.Fatal("rejected request executed")
	}
	if e := p.Answer(r.ID, d.DecisionID, "approve"); e == nil {
		t.Fatal("a rejected request was approved afterwards")
	}
	// A fresh request after a rejection is a new decision.
	if again := request(t, c, "arena_integrate", r.ID, change.ID); again.DecisionID == d.DecisionID || again.Reused {
		t.Fatalf("%+v", again)
	}
	if e := p.Answer(r.ID, d.DecisionID, "maybe"); e == nil {
		t.Fatal("an answer outside the choices was accepted")
	}
}

func TestControlStaleIntegrateIsRefusedAndRequestAgainMakesAFreshDecision(t *testing.T) {
	p, r, c := requestFixture(t)
	change := proposedChange(t, p, r, "one", "one.txt", "one\n")
	d := request(t, c, "arena_integrate", r.ID, change.ID)
	commitFile(t, p, "elsewhere.txt", "x\n")

	// The open request is stale: asking again replaces it.
	fresh := request(t, c, "arena_integrate", r.ID, change.ID)
	if fresh.DecisionID == d.DecisionID || fresh.Reused {
		t.Fatalf("stale decision reused: %+v", fresh)
	}
	if open, _ := p.Decisions(r.ID); len(open) != 1 || open[0].ID != fresh.DecisionID {
		t.Fatalf("open decisions %+v", open)
	}
	if len(eventsOf(t, p, r.ID, "decision.superseded")) != 1 {
		t.Fatal("superseding was not journaled")
	}
	if e := p.Answer(r.ID, d.DecisionID, "approve"); e == nil {
		t.Fatal("a superseded decision was approvable")
	}

	// HEAD moves again after the request: approving is refused, nothing runs.
	commitFile(t, p, "elsewhere2.txt", "y\n")
	e := p.Answer(r.ID, fresh.DecisionID, "approve")
	if e == nil || !strings.Contains(e.Error(), "stale request") {
		t.Fatalf("approve: %v", e)
	}
	if after, _ := p.Change(r.ID, change.ID); after.State != "proposed" {
		t.Fatalf("stale approval changed state to %s", after.State)
	}
	refused := eventsOf(t, p, r.ID, "decision.refused")
	if len(refused) != 1 || refused[0].Data["requestedBy"] != controlActor {
		t.Fatalf("refusal journal: %+v", refused)
	}
	requireUnverifiedApproval(t, refused[0].Data, ApprovalTUI)
	if len(eventsOf(t, p, r.ID, "decision.executed")) != 0 {
		t.Fatal("stale request executed")
	}
	if e := p.Answer(r.ID, fresh.DecisionID, "approve"); e == nil || !strings.Contains(e.Error(), "already answered") {
		t.Fatalf("retry of the refused decision: %v", e)
	}
	again := request(t, c, "arena_integrate", r.ID, change.ID)
	if again.DecisionID == fresh.DecisionID {
		t.Fatal("answered decision reused")
	}
	if _, e := p.AnswerDecision(r.ID, again.DecisionID, "approve", Approval{Channel: ApprovalTUI}); e != nil {
		t.Fatal(e)
	}
}

func TestControlIntegrateRefusesConflictedAndDirtyAndUnmetPrerequisites(t *testing.T) {
	p, r, c := requestFixture(t)
	change := proposedChange(t, p, r, "one", "base.txt", "from the writer\n")
	commitFile(t, p, "base.txt", "from the user\n")
	mustFail(t, c, "arena_integrate", r.ID, change.ID, "conflicts with the current checkout")
	if open, _ := p.Decisions(r.ID); len(open) != 0 {
		t.Fatalf("a conflicted change opened %+v", open)
	}
	if after, _ := p.Change(r.ID, change.ID); after.State != "proposed" {
		t.Fatalf("pre-check changed the change to %s", after.State)
	}
	if b, _ := os.ReadFile(filepath.Join(p.Root, "base.txt")); string(b) != "from the user\n" {
		t.Fatal("pre-check touched the checkout")
	}

	// A change already marked conflicted by a failed integration.
	change.State = "conflicted"
	if e := p.saveChange(change); e != nil {
		t.Fatal(e)
	}
	mustFail(t, c, "arena_integrate", r.ID, change.ID, "change is conflicted")

	two := proposedChange(t, p, r, "two", "two.txt", "two\n")
	two.DependsOn = []string{change.ID}
	if e := p.saveChange(two); e != nil {
		t.Fatal(e)
	}
	mustFail(t, c, "arena_integrate", r.ID, two.ID, "integrate prerequisite")

	two.DependsOn = nil
	if e := p.saveChange(two); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(p.Root, "base.txt"), []byte("uncommitted\n"), 0644); e != nil {
		t.Fatal(e)
	}
	mustFail(t, c, "arena_integrate", r.ID, two.ID, "user changes")
}

func TestControlRollbackRequestApprovalAndAlreadyRolledBack(t *testing.T) {
	p, r, c := requestFixture(t)
	change := proposedChange(t, p, r, "one", "one.txt", "one\n")
	if _, e := p.Integrate(r.ID, change.ID, true); e != nil {
		t.Fatal(e)
	}
	commitFile(t, p, "later.txt", "later\n")

	d := request(t, c, "arena_rollback", r.ID, change.ID)
	if d.State != "pending" || d.Action != "rollback" || d.Preview["behavior"] == nil {
		t.Fatalf("request: %+v", d)
	}
	if again := request(t, c, "arena_rollback", r.ID, change.ID); again.DecisionID != d.DecisionID || !again.Reused {
		t.Fatalf("not reused: %+v", again)
	}
	if _, e := os.Stat(filepath.Join(p.Root, "one.txt")); e != nil {
		t.Fatal("requesting rolled back")
	}
	if len(eventsOf(t, p, r.ID, "rollback.created")) != 0 {
		t.Fatal("request created a rollback")
	}

	if _, e := p.AnswerDecision(r.ID, d.DecisionID, "approve", Approval{Channel: ApprovalTUI}); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(p.Root, "one.txt")); !os.IsNotExist(e) {
		t.Fatal("not rolled back")
	}
	if b, _ := os.ReadFile(filepath.Join(p.Root, "later.txt")); string(b) != "later\n" {
		t.Fatal("later work lost")
	}
	executed := eventsOf(t, p, r.ID, "decision.executed")
	if len(executed) != 1 || executed[0].Data["action"] != "rollback" || executed[0].Data["requestedBy"] != controlActor {
		t.Fatalf("journal: %+v", executed)
	}
	requireUnverifiedApproval(t, executed[0].Data, ApprovalTUI)
	for _, kind := range []string{"rollback.created", "rollback.completed"} {
		if events := eventsOf(t, p, r.ID, kind); len(events) != 1 {
			t.Fatalf("%s journaled %d times", kind, len(events))
		} else {
			requireUnverifiedApproval(t, events[0].Data, ApprovalTUI)
		}
	}
	if e := p.Answer(r.ID, d.DecisionID, "approve"); e == nil || !strings.Contains(e.Error(), "already answered") {
		t.Fatalf("second approval: %v", e)
	}
	if len(eventsOf(t, p, r.ID, "rollback.completed")) != 1 {
		t.Fatal("rolled back more than once")
	}
	mustFail(t, c, "arena_rollback", r.ID, change.ID, "already rolled back")
	// The CLI and a direct call refuse as well, so approval cannot double-revert.
	if _, e := p.Rollback(r.ID, change.ID, true); e == nil || !strings.Contains(e.Error(), "already rolled back") {
		t.Fatalf("direct rollback: %v", e)
	}
	// Rolling back the rollback is still allowed: it restores the change.
	rollbacks, _ := p.Changes(r.ID)
	for _, rb := range rollbacks {
		if rb.Reverts == change.ID {
			request(t, c, "arena_rollback", r.ID, rb.ID)
		}
	}
}

func TestControlRollbackApprovalIsRefusedWhenHeadMoved(t *testing.T) {
	p, r, c := requestFixture(t)
	change := proposedChange(t, p, r, "one", "one.txt", "one\n")
	if _, e := p.Integrate(r.ID, change.ID, true); e != nil {
		t.Fatal(e)
	}
	d := request(t, c, "arena_rollback", r.ID, change.ID)
	commitFile(t, p, "later.txt", "later\n")
	if e := p.Answer(r.ID, d.DecisionID, "approve"); e == nil || !strings.Contains(e.Error(), "stale request") {
		t.Fatalf("approve: %v", e)
	}
	if _, e := os.Stat(filepath.Join(p.Root, "one.txt")); e != nil {
		t.Fatal("stale rollback executed")
	}
	if len(eventsOf(t, p, r.ID, "rollback.created")) != 0 {
		t.Fatal("stale rollback created a recovery")
	}
}

func TestControlRequestsRefuseRetiredRuns(t *testing.T) {
	p, r, c := requestFixture(t)
	change := proposedChange(t, p, r, "one", "one.txt", "one\n")
	if e := p.PruneRun(r.ID, true); e != nil {
		t.Fatal(e)
	}
	mustFail(t, c, "arena_integrate", r.ID, change.ID, "retired")
	mustFail(t, c, "arena_rollback", r.ID, change.ID, "retired")
	mustFail(t, c, "arena_integrate", "run-nope", change.ID, "no history")
}

func TestControlRequestsRefuseImportedRuns(t *testing.T) {
	p, r, c := requestFixture(t)
	change := proposedChange(t, p, r, "one", "one.txt", "one\n")
	// Audit import keeps a run only under .state/imported/RUN.
	if e := os.MkdirAll(p.Path(".state", "imported"), 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.Rename(p.RunDir(r.ID), p.Path(".state", "imported", r.ID)); e != nil {
		t.Fatal(e)
	}
	mustFail(t, c, "arena_integrate", r.ID, change.ID, "imported")
	mustFail(t, c, "arena_rollback", r.ID, change.ID, "imported")
}

func TestControlRequestsValidateArguments(t *testing.T) {
	_, r, c := requestFixture(t)
	for tool, args := range map[string]map[string]any{
		"arena_integrate": {"runId": r.ID},
		"arena_rollback":  {"changeId": "change-1"},
	} {
		if text, isError := c.call(tool, args); !isError || !strings.Contains(text, "required") {
			t.Fatalf("%s: %q", tool, text)
		}
	}
	for _, tool := range []string{"arena_integrate", "arena_rollback"} {
		if text, isError := c.call(tool, map[string]any{"runId": r.ID, "changeId": "change-1", "approve": true}); !isError || !strings.Contains(text, "unknown argument") {
			t.Fatalf("%s accepted an extra argument: %q", tool, text)
		}
		if _, isError := c.call(tool, map[string]any{"runId": r.ID, "changeId": "../x"}); !isError {
			t.Fatalf("%s accepted a bad ID", tool)
		}
	}
}

func TestControlServerHasNoWayToAnswerDecisions(t *testing.T) {
	p, r, c := requestFixture(t)
	change := proposedChange(t, p, r, "one", "one.txt", "one\n")
	d := request(t, c, "arena_integrate", r.ID, change.ID)
	for _, tool := range []string{"arena_answer", "arena_decision", "arena_decision_answer", "arena_answer_decision", "arena_approve", "arena_ask_user"} {
		if text, isError := c.call(tool, map[string]any{"runId": r.ID, "decisionId": d.DecisionID, "answer": "approve"}); !isError || !strings.Contains(text, "unknown Arena tool") {
			t.Fatalf("%s: %q", tool, text)
		}
	}
	// The request tools take no answer; an "approve" smuggled in is rejected.
	if _, isError := c.call("arena_integrate", map[string]any{"runId": r.ID, "changeId": change.ID, "answer": "approve"}); !isError {
		t.Fatal("answer accepted as an argument")
	}
	if open, _ := p.Decisions(r.ID); len(open) != 1 || open[0].Answer != "" {
		t.Fatalf("decision no longer open: %+v", open)
	}
	if len(eventsOf(t, p, r.ID, "decision.answered")) != 0 {
		t.Fatal("the control server answered")
	}
}

func TestChangeRequestDecisionIsCompactedOnceAnsweredWithoutFullHistory(t *testing.T) {
	p, r, c := requestFixture(t)
	r.Retention = "redacted-local"
	if e := Atomic(filepath.Join(p.RunDir(r.ID), "run.json"), JSON(r), 0600); e != nil {
		t.Fatal(e)
	}
	if got, e := p.ReadRun(r.ID); e != nil || got.Retention != "redacted-local" {
		t.Fatalf("fixture retention: %q %v", got.Retention, e)
	}
	change := proposedChange(t, p, r, "one", "one.txt", "one\n")
	d := request(t, c, "arena_integrate", r.ID, change.ID)
	if _, e := p.AnswerDecision(r.ID, d.DecisionID, "approve", Approval{Channel: ApprovalTUI}); e != nil {
		t.Fatal(e)
	}
	var stored Decision
	if e := ReadJSON(filepath.Join(p.RunDir(r.ID), "decisions", d.DecisionID+".json"), &stored); e != nil || stored.ClosedAt == "" || stored.Context != nil {
		t.Fatalf("not compacted: %+v %v", stored, e)
	}
}

// Every way of approving records the entry point it came through, and none
// records a verified identity: from Arena's side a person at a keyboard and a
// process driving the same command line look the same.
func TestApprovalsRecordTheirChannelNotAVerifiedIdentity(t *testing.T) {
	p, r, c := requestFixture(t)
	cli := func(args ...string) error {
		return NewCLI(strings.NewReader(""), &bytes.Buffer{}, &bytes.Buffer{}).Run(context.Background(), append(args, "--project", p.Root))
	}
	one := proposedChange(t, p, r, "one", "one.txt", "one\n")
	if e := cli("integrate", r.ID, one.ID, "--yes"); e != nil {
		t.Fatal(e)
	}
	integrated := eventsOf(t, p, r.ID, "change.integrated")
	if len(integrated) != 1 {
		t.Fatalf("integrated %d times", len(integrated))
	}
	requireUnverifiedApproval(t, integrated[0].Data, ApprovalCLINoTerminal)

	// A reject needs no terminal; it is recorded as coming from a pipe.
	d := request(t, c, "arena_rollback", r.ID, one.ID)
	if e := cli("decision", "answer", r.ID, d.DecisionID, "reject"); e != nil {
		t.Fatal(e)
	}
	answered := eventsOf(t, p, r.ID, "decision.answered")
	if len(answered) != 1 {
		t.Fatalf("answered %d times", len(answered))
	}
	requireUnverifiedApproval(t, answered[0].Data, ApprovalCLINoTerminal)

	if e := cli("rollback", r.ID, one.ID, "--yes"); e != nil {
		t.Fatal(e)
	}
	for _, kind := range []string{"rollback.created", "rollback.completed"} {
		events := eventsOf(t, p, r.ID, kind)
		if len(events) != 1 {
			t.Fatalf("%s journaled %d times", kind, len(events))
		}
		requireUnverifiedApproval(t, events[0].Data, ApprovalCLINoTerminal)
	}

	two := proposedChange(t, p, r, "two", "two.txt", "two\n")
	if _, e := p.Integrate(r.ID, two.ID, true); e != nil {
		t.Fatal(e)
	}
	integrated = eventsOf(t, p, r.ID, "change.integrated")
	if len(integrated) != 2 {
		t.Fatalf("integrated %d times", len(integrated))
	}
	requireUnverifiedApproval(t, integrated[1].Data, ApprovalLibrary)
}

// The operation that runs is the one that was reviewed: a patch that changes
// after the request is opened is refused, not executed under the old approval.
func TestControlApprovalRefusesAPatchChangedSinceTheRequest(t *testing.T) {
	p, r, c := requestFixture(t)
	change := proposedChange(t, p, r, "one", "one.txt", "one\n")
	d := request(t, c, "arena_integrate", r.ID, change.ID)
	if d.Preview["patchSha256"] == nil || d.Preview["patchSha256"] == "" {
		t.Fatalf("preview carries no patch digest: %+v", d.Preview)
	}
	patch := filepath.Join(p.RunDir(r.ID), "changes", change.ID+".patch")
	body, e := os.ReadFile(patch)
	if e != nil {
		t.Fatal(e)
	}
	altered := strings.Replace(string(body), "+one", "+ONE", 1)
	if altered == string(body) {
		t.Fatalf("test patch has no line to alter:\n%s", body)
	}
	if e := os.WriteFile(patch, []byte(altered), 0600); e != nil {
		t.Fatal(e)
	}
	e = p.Answer(r.ID, d.DecisionID, "approve")
	if e == nil || !strings.Contains(e.Error(), "patchSha256 changed since the request was made") {
		t.Fatalf("approval of an altered patch: %v", e)
	}
	if _, e := os.Stat(filepath.Join(p.Root, "one.txt")); !os.IsNotExist(e) {
		t.Fatal("the altered patch was integrated")
	}
	if after, _ := p.Change(r.ID, change.ID); after.State != "proposed" {
		t.Fatalf("change is %s after a refused approval", after.State)
	}
	if len(eventsOf(t, p, r.ID, "decision.refused")) != 1 || len(eventsOf(t, p, r.ID, "change.integrated")) != 0 {
		t.Fatal("refusal not journaled, or the change was integrated")
	}
}

func TestControlRollbackApprovalRefusesAChangedIntegrationCommit(t *testing.T) {
	p, r, c := requestFixture(t)
	change := proposedChange(t, p, r, "one", "one.txt", "one\n")
	if _, e := p.Integrate(r.ID, change.ID, true); e != nil {
		t.Fatal(e)
	}
	commitFile(t, p, "later.txt", "later\n")
	d := request(t, c, "arena_rollback", r.ID, change.ID)
	head, e := Git(p.Root, "rev-parse", "HEAD")
	if e != nil {
		t.Fatal(e)
	}
	integrated, _ := p.Change(r.ID, change.ID)
	integrated.IntegrationCommit = head
	if e := p.saveChange(integrated); e != nil {
		t.Fatal(e)
	}
	e = p.Answer(r.ID, d.DecisionID, "approve")
	if e == nil || !strings.Contains(e.Error(), "integrationCommit changed since the request was made") {
		t.Fatalf("approval after the integration commit changed: %v", e)
	}
	if len(eventsOf(t, p, r.ID, "rollback.created")) != 0 {
		t.Fatal("a rollback was created for a different commit")
	}
}
