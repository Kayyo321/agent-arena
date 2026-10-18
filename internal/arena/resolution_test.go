package arena

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// resolutionRun returns a committed project and a run whose workflow files are
// committed, so the checkout starts clean. The run is left in "created".
func resolutionRun(t *testing.T) (*Project, Run) {
	t.Helper()
	p := committedProject(t)
	r := testRun(t, p)
	if _, e := Git(p.Root, "add", ".arena"); e != nil {
		t.Fatal(e)
	}
	if _, e := Git(p.Root, "-c", "user.name=Test", "-c", "user.email=test@localhost", "commit", "-qm", "workflow"); e != nil {
		t.Fatal(e)
	}
	return p, r
}

func startRun(t *testing.T, p *Project, r Run) {
	t.Helper()
	if _, e := p.Append(r.ID, r.Workflow.Id, "", "workflow.started", map[string]any{}); e != nil {
		t.Fatal(e)
	}
}

func succeedRun(t *testing.T, p *Project, r Run) {
	t.Helper()
	startRun(t, p, r)
	if _, e := p.Append(r.ID, r.Workflow.Id, "", "workflow.succeeded", map[string]any{"nodeCount": 1}); e != nil {
		t.Fatal(e)
	}
}

// propose writes files in a new writer's worktree and records its change.
func propose(t *testing.T, p *Project, r Run, agent string, files map[string]string, deps ...string) Change {
	t.Helper()
	w, e := p.Workspace(r, agent, agent, true, deps...)
	if e != nil {
		t.Fatal(e)
	}
	for name, content := range files {
		if e := os.WriteFile(filepath.Join(w.Path, name), []byte(content), 0644); e != nil {
			t.Fatal(e)
		}
	}
	event, e := p.FinishWorkspace(r, w)
	if e != nil || event == nil {
		t.Fatalf("no proposal for %s: %v", agent, e)
	}
	c, e := p.Change(r.ID, str(event.Data["changeId"]))
	if e != nil {
		t.Fatal(e)
	}
	return c
}

func resolutionGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, e := Git(dir, args...)
	if e != nil {
		t.Fatal(e)
	}
	return out
}

func changeState(t *testing.T, p *Project, r Run, id string) string {
	t.Helper()
	c, e := p.Change(r.ID, id)
	if e != nil {
		t.Fatal(e)
	}
	return c.State
}

func resolutionEvents(t *testing.T, p *Project, r Run) []Event {
	t.Helper()
	events, e := p.Events(r.ID)
	if e != nil {
		t.Fatal(e)
	}
	out := []Event{}
	for _, ev := range events {
		if strings.HasPrefix(ev.Type, "resolution.") {
			out = append(out, ev)
		}
	}
	return out
}

func itemStatuses(res Resolution) map[string]string {
	out := map[string]string{}
	for _, item := range res.Items {
		out[item.ChangeID] = item.Status
	}
	return out
}

func TestResolutionDependencyChainAppliesInOrder(t *testing.T) {
	p, r := resolutionRun(t)
	a := propose(t, p, r, "a", map[string]string{"a.txt": "a\n"})
	b := propose(t, p, r, "b", map[string]string{"b.txt": "b\n"}, a.ID)
	// Proposed second but listed first on disk order must not matter.
	succeedRun(t, p, r)

	res, e := p.PrepareResolution(r.ID, nil)
	if e != nil || !res.Ready || len(res.Items) != 2 {
		t.Fatalf("prepare: %+v %v", res, e)
	}
	if res.Items[0].ChangeID != a.ID || res.Items[1].ChangeID != b.ID || res.Items[0].Order != 1 || res.Items[1].Order != 2 {
		t.Fatalf("order: %+v", res.Items)
	}
	if got := res.Items[0].Paths; len(got) != 1 || got[0] != "a.txt" {
		t.Fatalf("paths: %v", got)
	}
	applied, e := p.ApplyResolution(r.ID, "tester", nil)
	if e != nil {
		t.Fatal(e)
	}
	if !applied.Ready {
		t.Fatalf("apply result: %+v", applied)
	}
	for _, id := range []string{a.ID, b.ID} {
		if s := changeState(t, p, r, id); s != "integrated" {
			t.Fatalf("%s is %s", id, s)
		}
	}
	for _, name := range []string{"a.txt", "b.txt"} {
		if _, e := os.Stat(filepath.Join(p.Root, name)); e != nil {
			t.Fatalf("%s not in checkout", name)
		}
	}
	if resolutionGit(t, p.Root, "status", "--porcelain") != "" {
		t.Fatal("checkout dirty after apply")
	}
	ca, _ := p.Change(r.ID, a.ID)
	cb, _ := p.Change(r.ID, b.ID)
	if resolutionGit(t, p.Root, "rev-parse", "HEAD") != cb.IntegrationCommit || resolutionGit(t, p.Root, "rev-parse", "HEAD~1") != ca.IntegrationCommit {
		t.Fatal("HEAD is not one commit per change in order")
	}
	events := resolutionEvents(t, p, r)
	if len(events) != 1 || events[0].Type != "resolution.applied" {
		t.Fatalf("events: %+v", events)
	}
	ids := stringSlice(events[0].Data["changeIds"])
	if len(ids) != 2 || ids[0] != a.ID || ids[1] != b.ID || str(events[0].Data["head"]) != cb.IntegrationCommit {
		t.Fatalf("applied data: %+v", events[0].Data)
	}
	// A second apply has nothing left to do and must not silently succeed.
	if again, e := p.ApplyResolution(r.ID, "tester", nil); e == nil || again.Ready {
		t.Fatal("second apply should refuse: nothing proposed")
	}
}

func TestResolutionSameFileConflictRefusesAndLeavesCheckoutUntouched(t *testing.T) {
	p, r := resolutionRun(t)
	one := propose(t, p, r, "one", map[string]string{"base.txt": "one\n"})
	two := propose(t, p, r, "two", map[string]string{"base.txt": "two\n"})
	succeedRun(t, p, r)
	head := resolutionGit(t, p.Root, "rev-parse", "HEAD")

	res, e := p.PrepareResolution(r.ID, nil)
	if e != nil {
		t.Fatal(e)
	}
	got := itemStatuses(res)
	if res.Ready || got[one.ID] != "ready" || got[two.ID] != "conflict" || res.Reason == "" {
		t.Fatalf("prepare: %+v", res)
	}
	if res.Items[1].Reason == "" {
		t.Fatal("conflict has no reason")
	}
	applied, e := p.ApplyResolution(r.ID, "tester", nil)
	if e == nil || applied.Ready {
		t.Fatal("apply must refuse")
	}
	if !strings.Contains(e.Error(), two.ID) {
		t.Fatalf("error does not name the blocking change: %v", e)
	}
	if resolutionGit(t, p.Root, "rev-parse", "HEAD") != head || resolutionGit(t, p.Root, "status", "--porcelain") != "" {
		t.Fatal("checkout changed")
	}
	if b, _ := os.ReadFile(filepath.Join(p.Root, "base.txt")); string(b) != "baseline\n" {
		t.Fatalf("base.txt changed: %q", b)
	}
	for _, id := range []string{one.ID, two.ID} {
		if s := changeState(t, p, r, id); s != "proposed" {
			t.Fatalf("%s is %s", id, s)
		}
	}
	if len(resolutionEvents(t, p, r)) != 0 {
		t.Fatal("refused apply journaled a resolution event")
	}
	// Skipping the loser lets the clean change land.
	if _, e := p.ApplyResolution(r.ID, "tester", []string{two.ID}); e != nil {
		t.Fatal(e)
	}
	if changeState(t, p, r, one.ID) != "integrated" || changeState(t, p, r, two.ID) != "proposed" {
		t.Fatal("skip did not isolate the conflicting change")
	}
}

func TestResolutionDirtyCheckoutIsNotReadyAndWritesNothing(t *testing.T) {
	p, r := resolutionRun(t)
	a := propose(t, p, r, "a", map[string]string{"a.txt": "a\n"})
	succeedRun(t, p, r)
	if e := os.WriteFile(filepath.Join(p.Root, "scratch.txt"), []byte("mine\n"), 0644); e != nil {
		t.Fatal(e)
	}
	head := resolutionGit(t, p.Root, "rev-parse", "HEAD")
	status := resolutionGit(t, p.Root, "status", "--porcelain")

	res, e := p.PrepareResolution(r.ID, nil)
	if e != nil || res.Ready || !strings.Contains(res.Reason, "uncommitted") {
		t.Fatalf("prepare: %+v %v", res, e)
	}
	if _, e := p.ApplyResolution(r.ID, "tester", nil); e == nil {
		t.Fatal("apply must refuse a dirty checkout")
	}
	if resolutionGit(t, p.Root, "rev-parse", "HEAD") != head || resolutionGit(t, p.Root, "status", "--porcelain") != status {
		t.Fatal("checkout changed")
	}
	if changeState(t, p, r, a.ID) != "proposed" || len(resolutionEvents(t, p, r)) != 0 {
		t.Fatal("dirty refusal wrote state")
	}
}

func TestResolutionSupersededPrerequisiteBlocksDependent(t *testing.T) {
	p, r := resolutionRun(t)
	a := propose(t, p, r, "a", map[string]string{"a.txt": "a\n"})
	b := propose(t, p, r, "b", map[string]string{"b.txt": "b\n"}, a.ID)
	c := propose(t, p, r, "c", map[string]string{"c.txt": "c\n"})
	// Mark the prerequisite superseded, as a later proposal from its writer would.
	a.State = "superseded"
	if e := p.saveChange(a); e != nil {
		t.Fatal(e)
	}
	succeedRun(t, p, r)

	res, e := p.PrepareResolution(r.ID, nil)
	if e != nil {
		t.Fatal(e)
	}
	got := itemStatuses(res)
	if res.Ready || got[b.ID] != "blocked" || got[c.ID] != "ready" || len(res.Items) != 2 {
		t.Fatalf("prepare: %+v", res)
	}
	for _, item := range res.Items {
		if item.ChangeID == b.ID && !strings.Contains(item.Reason, a.ID) {
			t.Fatalf("reason does not name the prerequisite: %q", item.Reason)
		}
	}
	if _, e := p.ApplyResolution(r.ID, "tester", nil); e == nil {
		t.Fatal("apply must refuse")
	}
	if changeState(t, p, r, b.ID) != "proposed" || changeState(t, p, r, c.ID) != "proposed" {
		t.Fatal("something was applied")
	}
	if _, e := os.Stat(filepath.Join(p.Root, "b.txt")); !os.IsNotExist(e) {
		t.Fatal("blocked change reached the checkout")
	}
}

func TestResolutionConflictBlocksDependentsAndKeepsWalking(t *testing.T) {
	p, r := resolutionRun(t)
	one := propose(t, p, r, "one", map[string]string{"base.txt": "one\n"})
	two := propose(t, p, r, "two", map[string]string{"base.txt": "two\n"})
	dep := propose(t, p, r, "dep", map[string]string{"dep.txt": "d\n"}, two.ID)
	free := propose(t, p, r, "free", map[string]string{"free.txt": "f\n"})
	succeedRun(t, p, r)

	res, e := p.PrepareResolution(r.ID, nil)
	if e != nil {
		t.Fatal(e)
	}
	got := itemStatuses(res)
	want := map[string]string{one.ID: "ready", two.ID: "conflict", dep.ID: "blocked", free.ID: "ready"}
	for id, status := range want {
		if got[id] != status {
			t.Fatalf("%s is %s, want %s: %+v", id, got[id], status, res.Items)
		}
	}
}

func TestResolutionSkipRemovesItemAndBlocksItsDependents(t *testing.T) {
	p, r := resolutionRun(t)
	a := propose(t, p, r, "a", map[string]string{"a.txt": "a\n"})
	b := propose(t, p, r, "b", map[string]string{"b.txt": "b\n"}, a.ID)
	c := propose(t, p, r, "c", map[string]string{"c.txt": "c\n"})
	succeedRun(t, p, r)

	res, e := p.PrepareResolution(r.ID, []string{c.ID})
	if e != nil || !res.Ready || len(res.Items) != 2 {
		t.Fatalf("skip independent: %+v %v", res, e)
	}
	for _, item := range res.Items {
		if item.ChangeID == c.ID {
			t.Fatal("skipped change still listed")
		}
	}
	res, e = p.PrepareResolution(r.ID, []string{a.ID})
	if e != nil {
		t.Fatal(e)
	}
	got := itemStatuses(res)
	if res.Ready || len(res.Items) != 2 || got[b.ID] != "blocked" || got[c.ID] != "ready" {
		t.Fatalf("skip prerequisite: %+v", res)
	}
	if _, e := p.PrepareResolution(r.ID, []string{"change-doesnotexist"}); e == nil {
		t.Fatal("unknown skip accepted")
	}
	a.State = "superseded"
	if e := p.saveChange(a); e != nil {
		t.Fatal(e)
	}
	if _, e := p.PrepareResolution(r.ID, []string{a.ID}); e == nil {
		t.Fatal("skip of a non-proposed change accepted")
	}
}

func TestResolutionNotReadyWhileRunIsActive(t *testing.T) {
	p, r := resolutionRun(t)
	a := propose(t, p, r, "a", map[string]string{"a.txt": "a\n"})
	check := func(label string) {
		t.Helper()
		res, e := p.PrepareResolution(r.ID, nil)
		if e != nil || res.Ready || res.Reason == "" || len(res.Items) != 0 {
			t.Fatalf("%s: %+v %v", label, res, e)
		}
		if _, e := p.ApplyResolution(r.ID, "tester", nil); e == nil {
			t.Fatalf("%s: apply must refuse", label)
		}
		if changeState(t, p, r, a.ID) != "proposed" {
			t.Fatalf("%s: change applied", label)
		}
	}
	check("created")
	startRun(t, p, r)
	check("running")
	if e := p.Transition(r.ID, "paused"); e != nil {
		t.Fatal(e)
	}
	check("paused")
	if _, e := p.Append(r.ID, r.Workflow.Id, "", "workflow.interrupted", map[string]any{}); e != nil {
		t.Fatal(e)
	}
	check("interrupted")
	if _, e := p.Append(r.ID, r.Workflow.Id, "", "workflow.resumed", map[string]any{}); e != nil {
		t.Fatal(e)
	}
	if _, e := p.Append(r.ID, r.Workflow.Id, "", "workflow.succeeded", map[string]any{"nodeCount": 1}); e != nil {
		t.Fatal(e)
	}
	if res, e := p.PrepareResolution(r.ID, nil); e != nil || !res.Ready {
		t.Fatalf("succeeded: %+v %v", res, e)
	}
}

func TestResolutionPrepareWritesNoRefsBranchesOrIndex(t *testing.T) {
	p, r := resolutionRun(t)
	propose(t, p, r, "a", map[string]string{"a.txt": "a\n"})
	propose(t, p, r, "b", map[string]string{"base.txt": "b\n"})
	propose(t, p, r, "c", map[string]string{"base.txt": "c\n"})
	succeedRun(t, p, r)
	refs := resolutionGit(t, p.Root, "for-each-ref")
	branches := resolutionGit(t, p.Root, "branch", "--list")
	worktrees := resolutionGit(t, p.Root, "worktree", "list", "--porcelain")
	cached := resolutionGit(t, p.Root, "diff", "--cached", "--raw")
	status := resolutionGit(t, p.Root, "status", "--porcelain")
	events, e := p.Events(r.ID)
	if e != nil {
		t.Fatal(e)
	}

	for range 2 {
		if _, e := p.PrepareResolution(r.ID, nil); e != nil {
			t.Fatal(e)
		}
	}
	if resolutionGit(t, p.Root, "for-each-ref") != refs {
		t.Fatal("prepare changed refs")
	}
	if resolutionGit(t, p.Root, "branch", "--list") != branches {
		t.Fatal("prepare changed branches")
	}
	if resolutionGit(t, p.Root, "worktree", "list", "--porcelain") != worktrees {
		t.Fatal("prepare changed worktrees")
	}
	if resolutionGit(t, p.Root, "diff", "--cached", "--raw") != cached || resolutionGit(t, p.Root, "status", "--porcelain") != status {
		t.Fatal("prepare touched the checkout index or tree")
	}
	after, e := p.Events(r.ID)
	if e != nil || len(after) != len(events) {
		t.Fatal("prepare wrote journal events")
	}
	left, _ := filepath.Glob(p.Path(".state", "indices", "resolution-*"))
	if len(left) != 0 {
		t.Fatalf("temporary index left behind: %v", left)
	}
}

func TestResolutionApplyStopsAndJournalsAppliedPrefix(t *testing.T) {
	p, r := resolutionRun(t)
	a := propose(t, p, r, "a", map[string]string{"a.txt": "a\n"})
	b := propose(t, p, r, "b", map[string]string{"b.txt": "b\n"})
	c := propose(t, p, r, "c", map[string]string{"c.txt": "c\n"})
	succeedRun(t, p, r)
	// Prepare accepts all three, but integrate cannot create b's worktree
	// because its directory is occupied, as if the target changed between
	// preview and apply.
	occupied := filepath.Join(p.RunDir(r.ID), "integrations", b.ID)
	if e := os.MkdirAll(occupied, 0700); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(occupied, "keep"), []byte("x"), 0600); e != nil {
		t.Fatal(e)
	}

	res, e := p.PrepareResolution(r.ID, nil)
	if e != nil || !res.Ready {
		t.Fatalf("prepare: %+v %v", res, e)
	}
	if _, e := p.ApplyResolution(r.ID, "tester", nil); e == nil {
		t.Fatal("apply should fail at b")
	}
	if changeState(t, p, r, a.ID) != "integrated" || changeState(t, p, r, b.ID) != "proposed" || changeState(t, p, r, c.ID) != "proposed" {
		t.Fatal("wrong states after a mid-way failure")
	}
	if _, e := os.Stat(filepath.Join(p.Root, "c.txt")); !os.IsNotExist(e) {
		t.Fatal("apply continued past the failure")
	}
	events := resolutionEvents(t, p, r)
	if len(events) != 1 || events[0].Type != "resolution.stopped" {
		t.Fatalf("events: %+v", events)
	}
	d := events[0].Data
	applied := stringSlice(d["applied"])
	if len(applied) != 1 || applied[0] != a.ID || str(d["failedChange"]) != b.ID || str(d["reason"]) == "" {
		t.Fatalf("stopped data: %+v", d)
	}
}

func TestResolutionEngineHookJournalsReadyCounts(t *testing.T) {
	p, r := resolutionRun(t)
	one := propose(t, p, r, "one", map[string]string{"base.txt": "one\n"})
	propose(t, p, r, "two", map[string]string{"base.txt": "two\n"})
	free := propose(t, p, r, "free", map[string]string{"free.txt": "f\n"})
	succeedRun(t, p, r)

	e := NewEngine(p, r, nil)
	e.journalResolution()
	events := resolutionEvents(t, p, r)
	if len(events) != 1 || events[0].Type != "resolution.ready" {
		t.Fatalf("events: %+v", events)
	}
	d := events[0].Data
	ids := stringSlice(d["changeIds"])
	if resolutionInt(d["ready"]) != 2 || resolutionInt(d["blocked"]) != 0 || resolutionInt(d["conflict"]) != 1 || len(ids) != 2 || ids[0] != one.ID || ids[1] != free.ID {
		t.Fatalf("ready data: %+v", d)
	}
}

func TestResolutionEngineHookSkipsRunsWithoutProposals(t *testing.T) {
	p, r := resolutionRun(t)
	succeedRun(t, p, r)
	NewEngine(p, r, nil).journalResolution()
	if events := resolutionEvents(t, p, r); len(events) != 0 {
		t.Fatalf("events: %+v", events)
	}
}

func TestResolutionEngineHookJournalsErrorWithoutFailingRun(t *testing.T) {
	p, r := resolutionRun(t)
	a := propose(t, p, r, "a", map[string]string{"a.txt": "a\n"})
	b := propose(t, p, r, "b", map[string]string{"b.txt": "b\n"})
	succeedRun(t, p, r)
	// A dependency cycle makes preparation fail.
	a.DependsOn, b.DependsOn = []string{b.ID}, []string{a.ID}
	if e := p.saveChange(a); e != nil {
		t.Fatal(e)
	}
	if e := p.saveChange(b); e != nil {
		t.Fatal(e)
	}
	NewEngine(p, r, nil).journalResolution()
	events := resolutionEvents(t, p, r)
	if len(events) != 1 || events[0].Type != "resolution.error" || str(events[0].Data["reason"]) == "" {
		t.Fatalf("events: %+v", events)
	}
	if s, e := p.State(r.ID); e != nil || s.Status != "succeeded" {
		t.Fatalf("run state %+v %v", s, e)
	}
}

func resolutionInt(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case float64:
		return int(x)
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	}
	return -1
}
