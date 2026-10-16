package arena

import (
	"bytes"
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// purgeFixture has two runs in one project. The first has two writers: one
// change integrated, the other proposed or integrated too. The second run is
// not touched by anything done to the first.
type purgeFixture struct {
	p          *Project
	run, other Run
	integrated Change
	proposed   Change
	otherTree  string
	userFile   string
}

func newPurgeFixture(t *testing.T, integrateAll bool) purgeFixture {
	t.Helper()
	p := committedProject(t)
	f := purgeFixture{p: p}
	f.run = testRun(t, p)
	if _, err := Git(p.Root, "add", ".arena"); err != nil {
		t.Fatal(err)
	}
	if _, err := Git(p.Root, "-c", "user.name=Test", "-c", "user.email=test@localhost", "commit", "-qm", "workflow"); err != nil {
		t.Fatal(err)
	}
	var err error
	if f.other, err = p.CreateRun(f.run.Workflow, f.run.Profiles, f.run.Settings, "second run", f.run.BaseCommit); err != nil {
		t.Fatal(err)
	}
	write := func(r Run, agent, name string) (Workspace, Change) {
		w, err := p.Workspace(r, agent, agent, true)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(w.Path, name), []byte(name+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if _, err := p.FinishWorkspace(r, w); err != nil {
			t.Fatal(err)
		}
		changes, _ := p.Changes(r.ID)
		for _, c := range changes {
			if c.AgentID == agent {
				return w, c
			}
		}
		t.Fatal("no change for " + agent)
		return w, Change{}
	}
	_, f.integrated = write(f.run, "one", "one.txt")
	if _, err := p.Integrate(f.run.ID, f.integrated.ID, true); err != nil {
		t.Fatal(err)
	}
	_, f.proposed = write(f.run, "two", "two.txt")
	if integrateAll {
		if _, err := p.Integrate(f.run.ID, f.proposed.ID, true); err != nil {
			t.Fatal(err)
		}
	}
	w, _ := write(f.other, "three", "three.txt")
	f.otherTree = w.Path
	f.userFile = filepath.Join(p.Root, "mine.txt")
	return f
}

// userWork leaves an uncommitted file in the user's checkout, which no purge
// may touch. It comes last because integration refuses a dirty target.
func (f purgeFixture) userWork(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(f.userFile, []byte("uncommitted user work\n"), 0644); err != nil {
		t.Fatal(err)
	}
}

func (f purgeFixture) retire(t *testing.T) {
	t.Helper()
	if err := f.p.PruneRun(f.run.ID, true); err != nil {
		t.Fatal(err)
	}
}

func gitHas(p *Project, ref string) bool {
	_, err := Git(p.Root, "show-ref", "--verify", "--quiet", ref)
	return err == nil
}

func purgeCLI(p *Project, args ...string) (string, error) {
	var out bytes.Buffer
	err := NewCLI(strings.NewReader(""), &out, io.Discard).Run(context.Background(), append(append([]string{"history"}, args...), "--project", p.Root))
	return out.String(), err
}

func runCheckpoints(t *testing.T, p *Project, r Run) []string {
	t.Helper()
	ids := []string{}
	changes, _ := p.Changes(r.ID)
	for _, c := range changes {
		if c.Checkpoint != "" {
			ids = append(ids, c.Checkpoint)
		}
	}
	var baseline Checkpoint
	if err := ReadJSON(filepath.Join(p.RunDir(r.ID), "baseline.json"), &baseline); err != nil {
		t.Fatal(err)
	}
	return append(ids, baseline.ID)
}

func TestPurgeRemovesOnlyTheRetiredRunsOwnState(t *testing.T) {
	f := newPurgeFixture(t, true)
	p := f.p
	// A rollback leaves a recovery worktree and branch inside the run.
	rollback, err := p.Rollback(f.run.ID, f.integrated.ID, true)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(p.RunDir(f.run.ID), "recoveries", rollback.ID)); err != nil {
		t.Fatalf("fixture lacks recovery worktree: %v", err)
	}
	f.userWork(t)
	mine, theirs := runCheckpoints(t, p, f.run), runCheckpoints(t, p, f.other)
	for _, id := range mine {
		if !gitHas(p, "refs/arena/checkpoints/"+id) {
			t.Fatalf("fixture lacks ref %s", id)
		}
	}
	f.retire(t)
	plan, err := p.PlanPurge(f.run.ID)
	if err != nil || len(plan.Unintegrated) != 0 || len(plan.Worktrees) < 3 || len(plan.Branches) < 3 || len(plan.Checkpoints) < 3 {
		t.Fatalf("plan %+v %v", plan, err)
	}
	for _, path := range plan.Worktrees {
		if !underRun(p.RunDir(f.run.ID), path) {
			t.Fatalf("plan names %s outside the run's state", path)
		}
	}
	// Without --yes nothing is deleted.
	if _, err := purgeCLI(p, f.run.ID, "purge"); err == nil || !strings.Contains(err.Error(), "--yes") {
		t.Fatalf("purge without --yes: %v", err)
	}
	retired, _ := p.retiredDirs(f.run.ID)
	if len(retired) != 1 {
		t.Fatalf("retired dirs %v", retired)
	}
	headBefore, _ := Git(p.Root, "rev-parse", "HEAD")
	out, err := purgeCLI(p, f.run.ID, "purge", "--yes")
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}

	// Gone: the retired history, registrations, branches, refs, records.
	if _, err := os.Stat(retired[0]); !os.IsNotExist(err) {
		t.Fatal("retired history remains")
	}
	if list, _ := Git(p.Root, "worktree", "list", "--porcelain"); strings.Contains(list, f.run.ID) {
		t.Fatalf("worktree registrations remain:\n%s", list)
	}
	if left, _ := Git(p.Root, "branch", "--list", "codex/arena/*"); strings.Contains(left, f.run.ID) || strings.Contains(left, f.integrated.ID) || strings.Contains(left, rollback.ID) {
		t.Fatalf("branches remain:\n%s", left)
	}
	for _, id := range mine {
		if gitHas(p, "refs/arena/checkpoints/"+id) {
			t.Fatalf("checkpoint ref %s remains", id)
		}
		if _, err := os.Stat(p.Path(".state", "checkpoints", id+".json")); !os.IsNotExist(err) {
			t.Fatalf("checkpoint record %s remains", id)
		}
	}
	// Untouched: the user's checkout and history, and the other run.
	if b, err := os.ReadFile(f.userFile); err != nil || string(b) != "uncommitted user work\n" {
		t.Fatal("user's uncommitted work changed")
	}
	if head, _ := Git(p.Root, "rev-parse", "HEAD"); head != headBefore {
		t.Fatal("user's HEAD moved")
	}
	if _, err := os.Stat(filepath.Join(p.Root, "base.txt")); err != nil {
		t.Fatal("checkout lost a tracked file")
	}
	if _, err := os.Stat(filepath.Join(f.otherTree, "three.txt")); err != nil {
		t.Fatal("other run's worktree touched")
	}
	if list, _ := Git(p.Root, "worktree", "list", "--porcelain"); !strings.Contains(list, f.otherTree) {
		t.Fatal("other run's worktree unregistered")
	}
	if left, _ := Git(p.Root, "branch", "--list", "codex/arena/"+f.other.ID+"/*"); left == "" {
		t.Fatal("other run's branch deleted")
	}
	for _, id := range theirs {
		if !gitHas(p, "refs/arena/checkpoints/"+id) {
			t.Fatalf("other run's checkpoint %s deleted", id)
		}
	}
	if events, err := p.Events(f.other.ID); err != nil || len(events) == 0 {
		t.Fatalf("other run's history: %v", err)
	}

	// The tombstone: intent first, then what was removed.
	events, err := p.Events("project")
	if err != nil {
		t.Fatalf("project journal no longer verifies: %v", err)
	}
	var started, purged *Event
	for i := range events {
		switch events[i].Type {
		case "history.purge.started":
			started = &events[i]
		case "history.purged":
			purged = &events[i]
		}
	}
	if started == nil || purged == nil || purged.Sequence != started.Sequence+1 || purged.Data["complete"] != true || purged.At == "" {
		t.Fatalf("tombstone missing: %+v %+v", started, purged)
	}
	recorded := string(JSON(purged.Data))
	for _, want := range []string{f.run.ID, mine[0], rollback.ID, plan.LastEventHash} {
		if !strings.Contains(recorded, want) {
			t.Fatalf("tombstone omits %s:\n%s", want, recorded)
		}
	}
	if strings.Contains(recorded, f.other.ID) || strings.Contains(recorded, theirs[0]) {
		t.Fatal("tombstone names another run")
	}
	if _, err := purgeCLI(p, f.run.ID, "purge", "--yes"); err == nil {
		t.Fatal("purged twice")
	}
}

func TestPurgeRefusesUnintegratedWorkUntilDiscarded(t *testing.T) {
	f := newPurgeFixture(t, false)
	f.userWork(t)
	p := f.p
	// Not retired yet: refused.
	if _, err := purgeCLI(p, f.run.ID, "purge", "--yes"); err == nil || !strings.Contains(err.Error(), "not retired") {
		t.Fatalf("purge of a live run: %v", err)
	}
	f.retire(t)
	out, err := purgeCLI(p, f.run.ID, "purge", "--yes")
	if err == nil || !strings.Contains(err.Error(), "--discard-unintegrated") || !strings.Contains(out, f.proposed.ID) || !strings.Contains(out, "proposed") {
		t.Fatalf("expected the unintegrated change to be listed and refused: %v\n%s", err, out)
	}
	if strings.Contains(out, "change "+f.integrated.ID) {
		t.Fatalf("integrated change listed as unintegrated:\n%s", out)
	}
	retired, _ := p.retiredDirs(f.run.ID)
	if len(retired) != 1 || !gitHas(p, "refs/arena/checkpoints/"+f.proposed.Checkpoint) {
		t.Fatal("a refused purge deleted something")
	}
	events, _ := p.Events("project")
	for _, e := range events {
		if strings.HasPrefix(e.Type, "history.purge") {
			t.Fatalf("a refused purge was journaled as %s", e.Type)
		}
	}
	// --discard-unintegrated alone, without --yes, deletes nothing.
	if _, err := purgeCLI(p, f.run.ID, "purge", "--discard-unintegrated"); err == nil {
		t.Fatal("purged without --yes")
	}
	if _, err := os.Stat(retired[0]); err != nil {
		t.Fatal("deleted without --yes")
	}
	if _, err := purgeCLI(p, f.run.ID, "purge", "--yes", "--discard-unintegrated"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(retired[0]); !os.IsNotExist(err) || gitHas(p, "refs/arena/checkpoints/"+f.proposed.Checkpoint) {
		t.Fatal("discard did not purge")
	}
	events, _ = p.Events("project")
	if last := events[len(events)-1]; last.Type != "history.purged" || last.Data["discardedUnintegrated"] != true || !strings.Contains(string(JSON(last.Data)), f.proposed.ID) {
		t.Fatalf("tombstone does not record the discard: %+v", last)
	}
	// The integrated change's work is in the user's history, not lost.
	if _, err := os.Stat(filepath.Join(p.Root, "one.txt")); err != nil {
		t.Fatal("integrated work lost")
	}
}

func TestPurgeBlocksOnUncommittedWorktreeFiles(t *testing.T) {
	f := newPurgeFixture(t, true)
	f.userWork(t)
	p := f.p
	workspaces, _ := p.Workspaces(f.run.ID)
	if err := os.WriteFile(filepath.Join(workspaces[0].Path, "scratch.txt"), []byte("agent notes\n"), 0644); err != nil {
		t.Fatal(err)
	}
	f.retire(t)
	out, err := purgeCLI(p, f.run.ID, "purge", "--yes")
	if err == nil || !strings.Contains(out, "scratch.txt") {
		t.Fatalf("uncommitted worktree file not reported: %v\n%s", err, out)
	}
	if retired, _ := p.retiredDirs(f.run.ID); len(retired) != 1 {
		t.Fatal("deleted despite uncommitted files")
	}
}

// A retired run's records name paths and branches; a purge follows only the
// ones inside Arena's own state and naming.
func TestPurgeIgnoresRecordsThatPointOutsideArenaState(t *testing.T) {
	f := newPurgeFixture(t, true)
	f.userWork(t)
	p := f.p
	f.retire(t)
	retired, _ := p.retiredDirs(f.run.ID)
	manifests, _ := filepath.Glob(filepath.Join(retired[0], "workspaces", "*.json"))
	for _, name := range manifests {
		var w Workspace
		if err := ReadJSON(name, &w); err != nil {
			t.Fatal(err)
		}
		w.Path, w.Branch = f.otherTree, "master"
		if err := Atomic(name, JSON(w), 0600); err != nil {
			t.Fatal(err)
		}
	}
	changes, _ := filepath.Glob(filepath.Join(retired[0], "changes", "*.json"))
	for _, name := range changes {
		var c Change
		if err := ReadJSON(name, &c); err != nil {
			t.Fatal(err)
		}
		c.Workspace, c.IntegrationWorkspace, c.Checkpoint = p.Root, filepath.Dir(p.Root), "../../../escape"
		if err := Atomic(name, JSON(c), 0600); err != nil {
			t.Fatal(err)
		}
	}
	plan, err := p.PlanPurge(f.run.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Worktrees) != 0 {
		t.Fatalf("plan follows records outside Arena state: %+v", plan)
	}
	for _, id := range plan.Checkpoints {
		if !strings.HasPrefix(id, "checkpoint-") || strings.Contains(id, "/") {
			t.Fatalf("plan deletes checkpoint %q", id)
		}
	}
	for _, b := range plan.Branches {
		if !strings.HasPrefix(b, "codex/arena/") {
			t.Fatalf("plan deletes branch %s", b)
		}
	}
	branch, _ := Git(p.Root, "rev-parse", "--abbrev-ref", "HEAD")
	// The tampered records hide the integration worktrees, so git refuses to
	// delete their still-checked-out branches: the purge reports itself
	// incomplete and keeps the retired history for a retry.
	if _, err := purgeCLI(p, f.run.ID, "purge", "--yes", "--discard-unintegrated"); err == nil || !strings.Contains(err.Error(), "purge incomplete") {
		t.Fatalf("expected an incomplete purge: %v", err)
	}
	if left, _ := p.retiredDirs(f.run.ID); len(left) != 1 {
		t.Fatal("incomplete purge deleted the retired history")
	}
	events, _ := p.Events("project")
	if last := events[len(events)-1]; last.Type != "history.purged" || last.Data["complete"] != false {
		t.Fatalf("incomplete purge not recorded: %+v", last)
	}
	if after, _ := Git(p.Root, "rev-parse", "--abbrev-ref", "HEAD"); after != branch {
		t.Fatal("user's branch changed")
	}
	if _, err := os.Stat(filepath.Join(p.Root, "base.txt")); err != nil {
		t.Fatal("user's checkout deleted")
	}
	if _, err := os.Stat(filepath.Join(f.otherTree, "three.txt")); err != nil {
		t.Fatal("other run's worktree deleted")
	}
	if list, _ := Git(p.Root, "worktree", "list", "--porcelain"); !strings.Contains(list, f.otherTree) {
		t.Fatal("other run's worktree unregistered")
	}
	if _, err := os.Stat(f.userFile); err != nil {
		t.Fatal("user's file deleted")
	}
}

func TestPurgeNeverMatchesAnotherRunsRetiredHistory(t *testing.T) {
	p := committedProject(t)
	root := p.Path(".state", "retired")
	for _, name := range []string{"run-aaaa-20260101T000000", "run-aaaa-extra-20260101T000000", "run-aaaab-20260101T000000", "run-aaaa-20260101T000000.tmp"} {
		if err := os.MkdirAll(filepath.Join(root, name), 0700); err != nil {
			t.Fatal(err)
		}
	}
	dirs, err := p.retiredDirs("run-aaaa")
	if err != nil || len(dirs) != 1 || filepath.Base(dirs[0]) != "run-aaaa-20260101T000000" {
		t.Fatalf("retired dirs %v %v", dirs, err)
	}
	if _, err := p.PlanPurge("project"); err == nil {
		t.Fatal("planned a purge of the project journal")
	}
}
