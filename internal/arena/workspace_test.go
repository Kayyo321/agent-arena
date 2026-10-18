package arena

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func committedProject(t *testing.T) *Project {
	t.Helper()
	return committedProjectIn(t, t.TempDir())
}

func committedProjectIn(t *testing.T, dir string) *Project {
	t.Helper()
	p := testProjectIn(t, dir)
	if e := os.WriteFile(filepath.Join(p.Root, ".gitignore"), []byte(".arena/.state/\n.arena/locks/\n.arena-root\n"), 0644); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(p.Root, "base.txt"), []byte("baseline\n"), 0644); e != nil {
		t.Fatal(e)
	}
	if _, e := Git(p.Root, "add", "."); e != nil {
		t.Fatal(e)
	}
	if _, e := Git(p.Root, "-c", "user.name=Test", "-c", "user.email=test@localhost", "commit", "-qm", "baseline"); e != nil {
		t.Fatal(e)
	}
	return p
}
func testRun(t *testing.T, p *Project) Run {
	t.Helper()
	w := NewWorkflow("test", "Test")
	w.HeadAgent = "head"
	w.Agents = []Agent{{Id: "head", Level: Int(0), Profile: ".agents/test/head.agent.json", Runtime: Runtime{Provider: "codex", Model: "explicit-test-model", ReasoningEffort: "high"}}}
	profiles := map[string]Profile{"head": NewProfile("head", "research", "Research", "Use evidence", []string{"read"})}
	if _, e := p.Save(w, profiles, ""); e != nil {
		t.Fatal(e)
	}
	s, e := p.Settings()
	if e != nil {
		t.Fatal(e)
	}
	head, e := Git(p.Root, "rev-parse", "HEAD")
	if e != nil {
		t.Fatal(e)
	}
	r, e := p.CreateRun(w, profiles, s, "test", head)
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func TestTwoIsolatedWritersIntegrationAndReversibleRollback(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	if _, e := Git(p.Root, "add", ".arena"); e != nil {
		t.Fatal(e)
	}
	if _, e := Git(p.Root, "-c", "user.name=Test", "-c", "user.email=test@localhost", "commit", "-qm", "workflow"); e != nil {
		t.Fatal(e)
	}
	one, e := p.Workspace(r, "one", "one", true)
	if e != nil {
		t.Fatal(e)
	}
	two, e := p.Workspace(r, "two", "two", true)
	if e != nil {
		t.Fatal(e)
	}
	if one.Path == two.Path || one.Branch == two.Branch {
		t.Fatal("writers share isolation")
	}
	if e := os.WriteFile(filepath.Join(one.Path, "one.txt"), []byte("one\n"), 0644); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(two.Path, "two.txt"), []byte("two\n"), 0644); e != nil {
		t.Fatal(e)
	}
	if _, e := p.FinishWorkspace(r, one); e != nil {
		t.Fatal(e)
	}
	if _, e := p.FinishWorkspace(r, two); e != nil {
		t.Fatal(e)
	}
	changes, e := p.Changes(r.ID)
	if e != nil || len(changes) != 2 {
		t.Fatalf("changes %v %v", changes, e)
	}
	var change Change
	for _, c := range changes {
		if c.AgentID == "one" {
			change = c
		}
	}
	integrated, e := p.Integrate(r.ID, change.ID, true)
	if e != nil {
		t.Fatal(e)
	}
	if integrated.State != "integrated" {
		t.Fatal(integrated.State)
	}
	if e := os.WriteFile(filepath.Join(p.Root, "later.txt"), []byte("later\n"), 0644); e != nil {
		t.Fatal(e)
	}
	if _, e := Git(p.Root, "add", "later.txt"); e != nil {
		t.Fatal(e)
	}
	if _, e := Git(p.Root, "-c", "user.name=Test", "-c", "user.email=test@localhost", "commit", "-qm", "later work"); e != nil {
		t.Fatal(e)
	}
	rollback, e := p.Rollback(r.ID, change.ID, true)
	if e != nil {
		t.Fatal(e)
	}
	if rollback.State != "integrated" {
		t.Fatal("rollback not integrated")
	}
	if _, e := os.Stat(filepath.Join(p.Root, "one.txt")); !os.IsNotExist(e) {
		t.Fatal("selected change was not reverted")
	}
	if b, e := os.ReadFile(filepath.Join(p.Root, "later.txt")); e != nil || string(b) != "later\n" {
		t.Fatal("later work lost")
	}
	if _, e := p.Rollback(r.ID, rollback.ID, true); e != nil {
		t.Fatal(e)
	}
	if _, e := os.Stat(filepath.Join(p.Root, "one.txt")); e != nil {
		t.Fatal("rollback could not be reverted")
	}
	if _, e := os.Stat(filepath.Join(two.Path, "two.txt")); e != nil {
		t.Fatal("unaccepted worker proposal lost")
	}
}
func TestCheckpointPreservesUserIndexAndUntrackedFiles(t *testing.T) {
	p := committedProject(t)
	if e := os.WriteFile(filepath.Join(p.Root, "base.txt"), []byte("staged\n"), 0644); e != nil {
		t.Fatal(e)
	}
	if _, e := Git(p.Root, "add", "base.txt"); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(p.Root, "base.txt"), []byte("working\n"), 0644); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(p.Root, "untracked.txt"), []byte("untracked\n"), 0644); e != nil {
		t.Fatal(e)
	}
	before, e := Git(p.Root, "diff", "--cached")
	if e != nil {
		t.Fatal(e)
	}
	c, e := p.Checkpoint(p.Root, "dirty baseline")
	if e != nil {
		t.Fatal(e)
	}
	after, e := Git(p.Root, "diff", "--cached")
	if e != nil {
		t.Fatal(e)
	}
	if before != after {
		t.Fatal("checkpoint changed the user index")
	}
	b, e := Git(p.Root, "show", c.Commit+":base.txt")
	if e != nil || b != "working" {
		t.Fatalf("working content not captured: %s %v", b, e)
	}
	b, e = Git(p.Root, "show", c.Commit+":untracked.txt")
	if e != nil || !strings.Contains(b, "untracked") {
		t.Fatal("untracked content not captured")
	}
	if !strings.Contains(c.StagedDiff, "staged") {
		t.Fatal("staged state not retained")
	}
}

func TestCheckpointRefusesCredentialPathsAndUntrackedSecretContent(t *testing.T) {
	for _, filename := range []string{".env", "new.txt"} {
		t.Run(filename, func(t *testing.T) {
			p := committedProject(t)
			if err := os.WriteFile(filepath.Join(p.Root, filename), []byte("api_key=fixture-test-value\n"), 0600); err != nil {
				t.Fatal(err)
			}
			before, err := Git(p.Root, "show-ref")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := p.Checkpoint(p.Root, "must refuse"); err == nil {
				t.Fatal("secret-like data entered a checkpoint")
			}
			after, err := Git(p.Root, "show-ref")
			if err != nil || before != after {
				t.Fatal("refusal created a recovery ref")
			}
		})
	}
}

func TestDownstreamSnapshotContainsPrerequisiteProposal(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	writer, e := p.Workspace(r, "writer", "writer", true)
	if e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(writer.Path, "proposal.txt"), []byte("review this actual change\n"), 0644); e != nil {
		t.Fatal(e)
	}
	event, e := p.FinishWorkspace(r, writer)
	if e != nil || event == nil {
		t.Fatalf("proposal %v %v", event, e)
	}
	changeID := str(event.Data["changeId"])
	reader, e := p.Workspace(r, "reviewer", "reviewer", false, changeID)
	if e != nil {
		t.Fatal(e)
	}
	content, e := os.ReadFile(filepath.Join(reader.Path, "proposal.txt"))
	if e != nil || string(content) != "review this actual change\n" {
		t.Fatalf("reviewer did not receive actual code: %s %v", content, e)
	}
	head, e := Git(reader.Path, "rev-parse", "HEAD")
	if e != nil || head != reader.Base {
		t.Fatal("reader snapshot is not pinned to its declared base")
	}
	if _, e := os.Stat(filepath.Join(p.Root, "proposal.txt")); !os.IsNotExist(e) {
		t.Fatal("proposal modified user checkout")
	}
}
func TestCheckpointRestoreRecreatesStagingWithoutChangingUserCheckout(t *testing.T) {
	p := committedProject(t)
	path := filepath.Join(p.Root, "base.txt")
	if e := os.WriteFile(path, []byte("staged\n"), 0644); e != nil {
		t.Fatal(e)
	}
	if _, e := Git(p.Root, "add", "base.txt"); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(path, []byte("unstaged\n"), 0644); e != nil {
		t.Fatal(e)
	}
	if e := os.WriteFile(filepath.Join(p.Root, "untracked.txt"), []byte("new\n"), 0644); e != nil {
		t.Fatal(e)
	}
	checkpoint, e := p.Checkpoint(p.Root, "restore test")
	if e != nil {
		t.Fatal(e)
	}
	recovery, e := p.RestoreCheckpoint(checkpoint.ID)
	if e != nil {
		t.Fatal(e)
	}
	staged, e := Git(recovery, "diff", "--cached", "--binary")
	if e != nil || staged != checkpoint.StagedDiff {
		t.Fatalf("staged content differs: %v", e)
	}
	content, e := os.ReadFile(filepath.Join(recovery, "base.txt"))
	if e != nil || string(content) != "unstaged\n" {
		t.Fatal("working content differs")
	}
	if _, e := os.Stat(filepath.Join(recovery, "untracked.txt")); e != nil {
		t.Fatal("untracked file lost")
	}
	current, e := os.ReadFile(path)
	if e != nil || string(current) != "unstaged\n" {
		t.Fatal("user checkout changed")
	}
}
