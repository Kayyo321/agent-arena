package arena

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type Checkpoint struct {
	ID          string   `json:"id"`
	Commit      string   `json:"commit"`
	Head        string   `json:"head"`
	Tree        string   `json:"tree"`
	CreatedAt   string   `json:"createdAt"`
	Directory   string   `json:"directory"`
	Reason      string   `json:"reason"`
	Status      string   `json:"status"`
	StagedDiff  string   `json:"stagedDiff"`
	WorkingDiff string   `json:"workingDiff"`
	Untracked   []string `json:"untracked"`
}
type Workspace struct {
	Dependencies []string `json:"dependencies,omitempty"`
	State        string   `json:"state,omitempty"`
	Version      int      `json:"version"`
	AgentID      string   `json:"agentId"`
	TaskID       string   `json:"taskId"`
	Path         string   `json:"path"`
	Branch       string   `json:"branch"`
	GitDir       string   `json:"gitDir,omitempty"`
	Rejected     string   `json:"rejected,omitempty"`
	Base         string   `json:"base"`
	Write        bool     `json:"write"`
	LastTree     string   `json:"lastTree,omitempty"`
	LastChange   string   `json:"lastChange,omitempty"`
}
type Change struct {
	DependsOn            []string `json:"dependsOn,omitempty"`
	Version              int      `json:"version"`
	ID                   string   `json:"id"`
	RunID                string   `json:"runId"`
	AgentID              string   `json:"agentId"`
	Workspace            string   `json:"workspace"`
	Base                 string   `json:"base"`
	Proposal             string   `json:"proposal"`
	Paths                []string `json:"paths"`
	State                string   `json:"state"`
	CreatedAt            string   `json:"createdAt"`
	IntegrationCommit    string   `json:"integrationCommit,omitempty"`
	IntegrationWorkspace string   `json:"integrationWorkspace,omitempty"`
	Checkpoint           string   `json:"checkpoint,omitempty"`
	Supersedes           string   `json:"supersedes,omitempty"`
	Reverts              string   `json:"reverts,omitempty"`
}

func Git(dir string, args ...string) (string, error) { return gitEnv(dir, nil, args...) }
func gitEnv(dir string, env []string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60e9)
	defer cancel()
	cmd := exec.CommandContext(ctx, "git", args...)
	cmd.Dir = dir
	if env != nil {
		cmd.Env = append(os.Environ(), env...)
	}
	// Stdout carries machine-readable results (path lists, binary patches), so
	// it is kept exactly and fails loudly past maxGitOutput instead of being
	// cut short. Stderr is diagnostics only and stays capped.
	stdout := exactBuffer{limit: maxGitOutput}
	var stderr cappedBuffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err := cmd.Run()
	if stdout.overflow {
		return "", fmt.Errorf("git %s: output exceeds %d MiB; Arena does not record truncated Git results", strings.Join(args, " "), maxGitOutput>>20)
	}
	if err != nil {
		return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), Redact(strings.TrimSpace(stderr.String()+"\n"+string(stdout.data[:min(len(stdout.data), 1<<20)]))))
	}
	return strings.TrimSuffix(string(stdout.data), "\n"), nil
}

const maxGitOutput = 256 << 20

// exactBuffer keeps every byte up to limit and records an overflow beyond it.
type exactBuffer struct {
	data     []byte
	limit    int
	overflow bool
}

func (b *exactBuffer) Write(p []byte) (int, error) {
	if len(b.data)+len(p) > b.limit {
		b.overflow = true
		return 0, fmt.Errorf("git output exceeds %d bytes", b.limit)
	}
	b.data = append(b.data, p...)
	return len(p), nil
}
func (p *Project) Checkpoint(dir, reason string) (Checkpoint, error) {
	g, e := p.repo(dir)
	if e != nil {
		return Checkpoint{Directory: dir, Reason: reason}, e
	}
	return p.checkpointRepo(g, reason)
}
func (p *Project) checkpointRepo(g gitRepo, reason string) (Checkpoint, error) {
	l, e := p.WaitLock("git")
	if e != nil {
		return Checkpoint{}, e
	}
	defer l.Close()
	return p.checkpoint(g, reason)
}
func (p *Project) checkpoint(g gitRepo, reason string) (Checkpoint, error) {
	dir := g.dir
	c := Checkpoint{ID: ID("checkpoint-"), Directory: dir, Reason: reason, CreatedAt: Now(), Untracked: []string{}}
	if e := flatRepository(g); e != nil {
		return c, e
	}
	var e error
	c.Head, e = g.git("rev-parse", "HEAD")
	if e != nil {
		return c, fmt.Errorf("commit the initial repository state before running Arena: %w", e)
	}
	c.Status, e = g.git("status", "--porcelain=v1", "--untracked-files=all")
	if e != nil {
		return c, e
	}
	tracked, e := g.git("ls-tree", "-r", "--name-only", "-z", c.Head)
	if e != nil {
		return c, e
	}
	candidates, e := g.git("ls-files", "--cached", "--others", "--exclude-standard", "-z")
	if e != nil {
		return c, e
	}
	for _, path := range strings.Split(tracked+"\x00"+candidates, "\x00") {
		if credentialPath(path) {
			return c, fmt.Errorf("credential path %q must be excluded from the repository/checkpoint", path)
		}
	}
	c.StagedDiff, e = g.git("diff", "--cached", "--binary", "--no-ext-diff", "--no-textconv")
	if e != nil {
		return c, e
	}
	c.WorkingDiff, e = g.git("diff", "--binary", "--no-ext-diff", "--no-textconv")
	if e != nil {
		return c, e
	}
	untracked, e := g.git("ls-files", "--others", "--exclude-standard", "-z")
	if e != nil {
		return c, e
	}
	for _, path := range strings.Split(untracked, "\x00") {
		if path != "" {
			c.Untracked = append(c.Untracked, path)
		}
	}
	if Redact(c.StagedDiff) != c.StagedDiff || Redact(c.WorkingDiff) != c.WorkingDiff {
		return c, fmt.Errorf("checkpoint contains detected secret-like changes; remove them before launching workers")
	}
	index := p.Path(".state", "indices", c.ID)
	if e := os.MkdirAll(filepath.Dir(index), 0700); e != nil {
		return c, e
	}
	defer os.Remove(index)
	env := []string{"GIT_INDEX_FILE=" + index, "GIT_AUTHOR_NAME=Agent Arena", "GIT_AUTHOR_EMAIL=arena@localhost", "GIT_COMMITTER_NAME=Agent Arena", "GIT_COMMITTER_EMAIL=arena@localhost"}
	if _, e = g.gitEnv(env, "read-tree", c.Head); e != nil {
		return c, e
	}
	paths := map[string]bool{}
	for _, path := range strings.Split(tracked+"\x00"+candidates, "\x00") {
		if path == "" || path == ".arena-root" || path == ".arena/.state" || strings.HasPrefix(path, ".arena/.state/") || path == ".arena/locks" || strings.HasPrefix(path, ".arena/locks/") {
			continue
		}
		paths[path] = true
	}
	for _, path := range c.Untracked {
		if !paths[path] {
			continue
		}
		full := filepath.Join(dir, path)
		if info, err := os.Lstat(full); err == nil && info.Mode().IsRegular() {
			if err := checkSecretContent(full); err != nil {
				return c, err
			}
		}
	}
	// read-tree starts from HEAD, which may accidentally track Arena runtime data.
	// Remove that data from the alternate index without modifying the caller index.
	if _, e = g.gitEnv(env, "rm", "--cached", "-r", "-f", "--ignore-unmatch", "--", ".arena/.state", ".arena/locks", ".arena-root"); e != nil {
		return c, e
	}
	listPath := index + ".paths"
	defer os.Remove(listPath)
	if e := os.WriteFile(listPath, []byte(strings.Join(sortedKeys(paths), "\x00")+"\x00"), 0600); e != nil {
		return c, e
	}
	if len(paths) > 0 {
		if _, e = g.gitEnv(append(env, "GIT_LITERAL_PATHSPECS=1"), "add", "-A", "--pathspec-from-file="+listPath, "--pathspec-file-nul"); e != nil {
			return c, e
		}
	}
	c.Tree, e = g.gitEnv(env, "write-tree")
	if e != nil {
		return c, e
	}
	c.Commit, e = g.gitEnv(env, "commit-tree", c.Tree, "-p", c.Head, "-m", "Arena checkpoint: "+reason)
	if e != nil {
		return c, e
	}
	if _, e = g.git("update-ref", "refs/arena/checkpoints/"+c.ID, c.Commit); e != nil {
		return c, e
	}
	if e := Atomic(p.Path(".state", "checkpoints", c.ID+".json"), SafeJSON(c), 0600); e != nil {
		return c, e
	}
	return c, nil
}

func credentialPath(path string) bool {
	parts := strings.Split(filepath.ToSlash(path), "/")
	for i, part := range parts {
		if part == ".ssh" || part == ".aws" || part == ".env" || strings.HasPrefix(part, ".env.") && part != ".env.example" && part != ".env.sample" {
			return true
		}
		if i > 0 && (parts[i-1] == ".codex" && strings.HasPrefix(part, "auth.json") || parts[i-1] == ".claude" && part == ".credentials.json" || parts[i-1] == ".gemini" && part == "oauth_creds.json") {
			return true
		}
	}
	return false
}
func checkSecretContent(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	buffer := make([]byte, 64<<10)
	tail := ""
	for {
		n, err := f.Read(buffer)
		text := tail + string(buffer[:n])
		if Redact(text) != text {
			return fmt.Errorf("untracked checkpoint content contains detected secret-like data; exclude or remove it before launching workers")
		}
		tail = text[max(0, len(text)-4096):]
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

// Nested repositories have independent indexes/objects. Until they have their own
// recovery contract, refuse them instead of silently producing a partial snapshot.
func flatRepository(g gitRepo) error {
	for _, args := range [][]string{{"ls-files", "--stage", "-z"}, {"ls-tree", "-r", "-z", "HEAD"}} {
		entries, err := g.git(args...)
		if err != nil {
			return err
		}
		for _, entry := range strings.Split(entries, "\x00") {
			if strings.HasPrefix(entry, "160000 ") {
				return fmt.Errorf("submodules/nested Git repositories require independent recovery and are not supported by Arena checkpoints")
			}
		}
	}
	untracked, err := g.git("ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return err
	}
	for _, path := range strings.Split(untracked, "\x00") {
		if strings.HasSuffix(path, "/") {
			if _, err := os.Lstat(filepath.Join(g.dir, path, ".git")); err == nil {
				return fmt.Errorf("nested Git repository %q is not supported by Arena checkpoints", path)
			}
		}
	}
	return nil
}
func (p *Project) PrepareBaseline(r Run) (Checkpoint, error) {
	l, e := p.WaitLock("baseline-" + r.ID)
	if e != nil {
		return Checkpoint{}, e
	}
	defer l.Close()
	path := filepath.Join(p.RunDir(r.ID), "baseline.json")
	var baseline Checkpoint
	if e := ReadJSON(path, &baseline); e == nil {
		return baseline, nil
	} else if !os.IsNotExist(e) {
		return baseline, e
	}
	baseline, e = p.Checkpoint(p.Root, "baseline for "+r.ID)
	if e != nil {
		return baseline, e
	}
	if e := Atomic(path, SafeJSON(baseline), 0600); e != nil {
		return baseline, e
	}
	_, e = p.Append(r.ID, r.Workflow.Id, "", "checkpoint.created", map[string]any{"checkpointId": baseline.ID, "commit": baseline.Commit, "includesUncommitted": baseline.Status != ""})
	return baseline, e
}
func (p *Project) Workspace(r Run, agent, task string, write bool, dependencies ...string) (Workspace, error) {
	var w Workspace
	if e := validID(task); e != nil {
		return w, e
	}
	l, e := p.WaitLock("workspaces-" + r.ID)
	if e != nil {
		return w, e
	}
	defer l.Close()
	manifest := filepath.Join(p.RunDir(r.ID), "workspaces", task+".json")
	if e := ReadJSON(manifest, &w); e == nil {
		if w.Write != write || w.AgentID != agent {
			return w, fmt.Errorf("workspace capability/owner changed")
		}
		if w.Rejected != "" {
			return w, fmt.Errorf("workspace was rejected and is preserved for inspection: %s", w.Rejected)
		}
		if _, e := os.Stat(w.Path); e != nil {
			return w, fmt.Errorf("workspace missing; recover it before retrying")
		}
		return w, nil
	} else if !os.IsNotExist(e) {
		return w, e
	}
	baseline, e := p.PrepareBaseline(r)
	if e != nil {
		return w, e
	}
	w = Workspace{Version: 1, AgentID: agent, TaskID: task, Path: filepath.Join(p.RunDir(r.ID), "trees", task), Base: baseline.Commit, Write: write}
	if e := os.MkdirAll(filepath.Dir(w.Path), 0700); e != nil {
		return w, e
	}
	gitLock, e := p.WaitLock("git")
	if e != nil {
		return w, e
	}
	defer gitLock.Close()
	if len(dependencies) > 0 {
		base, err := p.composeBase(r, w.Base, dependencies)
		if err != nil {
			return w, err
		}
		w.Base = base
		w.Dependencies = dependencies
	}
	args := []string{"worktree", "add", "--detach", w.Path, w.Base}
	if write {
		w.Branch = "codex/arena/" + r.ID + "/" + task
		args = []string{"worktree", "add", "-b", w.Branch, w.Path, w.Base}
	}
	if _, e = Git(p.Root, args...); e != nil {
		return w, e
	}
	// Record the gitdir pointer before any worker runs so later host Git calls
	// can refuse a worktree whose .git was replaced.
	if w.GitDir, e = readGitPointer(w.Path); e != nil {
		return w, e
	}
	if _, e := p.workerRepo(w.Path, w.GitDir); e != nil {
		return w, e
	}
	if e := Atomic(filepath.Join(w.Path, ".arena-root"), []byte(p.Root+"\n"), 0600); e != nil {
		return w, e
	}
	if e := Atomic(manifest, JSON(w), 0600); e != nil {
		return w, e
	}
	_, e = p.Append(r.ID, r.Workflow.Id, agent, "worktree.created", map[string]any{"workspace": w})
	return w, e
}
func (p *Project) FinishWorkspace(r Run, w Workspace) (*Event, error) {
	if !w.Write {
		return nil, nil
	}
	l, e := p.WaitLock("workspaces-" + r.ID)
	if e != nil {
		return nil, e
	}
	defer l.Close()
	manifest := filepath.Join(p.RunDir(r.ID), "workspaces", w.TaskID+".json")
	if e := ReadJSON(manifest, &w); e != nil {
		return nil, e
	}
	if w.Rejected != "" {
		return nil, fmt.Errorf("workspace was rejected and is preserved for inspection: %s", w.Rejected)
	}
	repo, e := p.workerRepo(w.Path, w.GitDir)
	if e != nil {
		return nil, p.rejectWorkspace(r, manifest, w, e)
	}
	checkpoint, e := p.checkpointRepo(repo, "proposal from "+w.AgentID)
	if e != nil {
		return nil, e
	}
	if checkpoint.Tree == w.LastTree {
		return nil, nil
	}
	patch, e := repo.git("diff", "--binary", "--no-ext-diff", "--no-textconv", w.Base, checkpoint.Commit)
	if e != nil {
		return nil, e
	}
	if patch == "" {
		w.LastTree = checkpoint.Tree
		return nil, Atomic(manifest, JSON(w), 0600)
	}
	paths, e := repo.git("diff", "--name-only", "-z", w.Base, checkpoint.Commit)
	if e != nil {
		return nil, e
	}
	changed := []string{}
	for _, path := range strings.Split(paths, "\x00") {
		if path != "" {
			if protectedPath(path) {
				return nil, fmt.Errorf("worker changed protected path %s", path)
			}
			changed = append(changed, path)
		}
	}
	change := Change{DependsOn: w.Dependencies, Version: 1, ID: ID("change-"), RunID: r.ID, AgentID: w.AgentID, Workspace: w.Path, Base: w.Base, Proposal: checkpoint.Commit, Paths: changed, State: "proposed", CreatedAt: Now(), Checkpoint: checkpoint.ID, Supersedes: w.LastChange}
	if e := Atomic(filepath.Join(p.RunDir(r.ID), "changes", change.ID+".patch"), []byte(patch+"\n"), 0600); e != nil {
		return nil, e
	}
	if e := p.saveChange(change); e != nil {
		return nil, e
	}
	if w.LastChange != "" {
		previous, e := p.Change(r.ID, w.LastChange)
		if e != nil {
			return nil, e
		}
		if previous.State == "proposed" {
			previous.State = "superseded"
			if e := p.saveChange(previous); e != nil {
				return nil, e
			}
		}
	}
	w.LastTree = checkpoint.Tree
	w.LastChange = change.ID
	if e := Atomic(manifest, JSON(w), 0600); e != nil {
		return nil, e
	}
	if _, e := p.Append(r.ID, r.Workflow.Id, w.AgentID, "change.proposed", map[string]any{"change": change}); e != nil {
		return nil, e
	}
	event, e := p.Append(r.ID, r.Workflow.Id, w.AgentID, "change.touches-paths", map[string]any{"changeId": change.ID, "paths": changed})
	return &event, e
}

// rejectWorkspace fails a worktree closed: it is never checkpointed or
// proposed again, and stays on disk for the user to inspect.
func (p *Project) rejectWorkspace(r Run, manifest string, w Workspace, cause error) error {
	w.Rejected = Redact(cause.Error())
	if e := Atomic(manifest, JSON(w), 0600); e != nil {
		return errors.Join(cause, e)
	}
	if _, e := p.Append(r.ID, r.Workflow.Id, w.AgentID, "workspace.rejected", map[string]any{"taskId": w.TaskID, "workspace": w.Path, "reason": w.Rejected, "preserved": true}); e != nil {
		return errors.Join(cause, e)
	}
	return cause
}
func (p *Project) saveChange(c Change) error {
	return Atomic(filepath.Join(p.RunDir(c.RunID), "changes", c.ID+".json"), JSON(c), 0600)
}
func (p *Project) Change(run, id string) (Change, error) {
	var c Change
	if e := validID(run); e != nil {
		return c, e
	}
	if e := validID(id); e != nil {
		return c, e
	}
	e := ReadJSON(filepath.Join(p.RunDir(run), "changes", id+".json"), &c)
	if e == nil && (c.RunID != run || c.ID != id || c.Version != 1) {
		e = fmt.Errorf("unsupported or mismatched change")
	}
	return c, e
}
func (p *Project) Changes(run string) ([]Change, error) {
	if e := validID(run); e != nil {
		return nil, e
	}
	files, e := filepath.Glob(filepath.Join(p.RunDir(run), "changes", "*.json"))
	if e != nil {
		return nil, e
	}
	out := []Change{}
	for _, path := range files {
		var c Change
		if e := ReadJSON(path, &c); e != nil {
			return nil, e
		}
		out = append(out, c)
	}
	return out, nil
}
func (p *Project) Workspaces(run string) ([]Workspace, error) {
	if e := validID(run); e != nil {
		return nil, e
	}
	files, e := filepath.Glob(filepath.Join(p.RunDir(run), "workspaces", "*.json"))
	if e != nil {
		return nil, e
	}
	out := []Workspace{}
	for _, path := range files {
		var w Workspace
		if e := ReadJSON(path, &w); e != nil {
			return nil, e
		}
		w.State = "read-only"
		if w.Write {
			w.State = "isolated"
			if w.LastChange != "" {
				change, err := p.Change(run, w.LastChange)
				if err != nil {
					return nil, err
				}
				w.State = change.State
			}
		}
		if _, err := os.Stat(w.Path); err != nil {
			w.State = "missing"
		} else if w.Rejected != "" {
			w.State = "rejected"
		}
		out = append(out, w)
	}
	return out, nil
}
func (p *Project) Integrate(run, id string, approved bool) (Change, error) {
	if !approved {
		return Change{}, fmt.Errorf("integration requires explicit human approval")
	}
	return p.IntegrateBy(run, id, Approval{Channel: ApprovalLibrary})
}

// IntegrateBy integrates a change on an approval received through the given
// entry point; the journal records the channel, never a verified identity.
func (p *Project) IntegrateBy(run, id string, by Approval) (Change, error) {
	return p.integrate(run, id, "", by)
}

// integrate applies a proposed change. policy names an automatic integration
// policy that approved it (empty for a human entry point); by records the
// entry point otherwise.
func (p *Project) integrate(run, id, policy string, by Approval) (Change, error) {
	change, e := p.Change(run, id)
	if e != nil {
		return change, e
	}

	if change.State != "proposed" {
		return change, fmt.Errorf("change is %s", change.State)
	}
	r, e := p.ReadRun(run)
	if e != nil {
		return change, e
	}
	for _, dependency := range change.DependsOn {
		required, err := p.Change(run, dependency)
		if err != nil {
			return change, err
		}
		if required.State != "integrated" {
			return change, fmt.Errorf("integrate prerequisite %s before %s", dependency, id)
		}
	}
	lock, e := p.WaitLock("git")
	if e != nil {
		return change, e
	}
	defer lock.Close()
	status, e := Git(p.Root, "status", "--porcelain=v1")
	if e != nil {
		return change, e
	}
	if status != "" {
		return change, fmt.Errorf("integration target has user changes; commit them or review in the preserved isolated integration workspace")
	}
	head, e := Git(p.Root, "rev-parse", "HEAD")
	if e != nil {
		return change, e
	}
	checkpoint, e := p.checkpoint(gitRepo{dir: p.Root}, "before integrating "+id)
	if e != nil {
		return change, e
	}
	change.Checkpoint = checkpoint.ID
	change.IntegrationWorkspace = filepath.Join(p.RunDir(run), "integrations", id)
	if e := os.MkdirAll(filepath.Dir(change.IntegrationWorkspace), 0700); e != nil {
		return change, e
	}
	branch := "codex/arena/integrate/" + id
	if _, e = Git(p.Root, "worktree", "add", "-b", branch, change.IntegrationWorkspace, head); e != nil {
		return change, e
	}
	patch := filepath.Join(p.RunDir(run), "changes", id+".patch")
	if _, e = Git(change.IntegrationWorkspace, "apply", "--check", patch); e != nil {
		change.State = "conflicted"
		_ = p.saveChange(change)
		_, _ = p.Append(run, r.Workflow.Id, change.AgentID, "change.conflicted", map[string]any{"change": change, "error": e.Error()})
		return change, e
	}
	if _, e = Git(change.IntegrationWorkspace, "apply", "--index", patch); e != nil {
		return change, e
	}
	if _, e = Git(change.IntegrationWorkspace, "-c", "user.name=Agent Arena", "-c", "user.email=arena@localhost", "commit", "-m", "Arena: integrate "+id); e != nil {
		return change, e
	}
	commit, e := Git(change.IntegrationWorkspace, "rev-parse", "HEAD")
	if e != nil {
		return change, e
	}
	current, e := Git(p.Root, "rev-parse", "HEAD")
	if e != nil {
		return change, e
	}
	status, e = Git(p.Root, "status", "--porcelain=v1")
	if e != nil {
		return change, e
	}
	if current != head || status != "" {
		change.State = "integration-prepared"
		change.IntegrationCommit = commit
		_ = p.saveChange(change)
		return change, fmt.Errorf("target changed during review; integration commit preserved at %s", change.IntegrationWorkspace)
	}
	if _, e = Git(p.Root, "merge", "--ff-only", commit); e != nil {
		return change, e
	}
	change.IntegrationCommit = commit
	change.State = "integrated"
	if e := p.saveChange(change); e != nil {
		return change, e
	}
	data := map[string]any{"change": change, "previousHead": head}
	if policy != "" {
		data["approvedBy"] = policy
	}
	if by.Channel != "" {
		data["approval"] = by.Record()
	}
	_, e = p.Append(run, r.Workflow.Id, change.AgentID, "change.integrated", data)
	return change, e
}
func (p *Project) Rollback(run, id string, approved bool) (Change, error) {
	if !approved {
		return p.Change(run, id)
	}
	return p.RollbackBy(run, id, Approval{Channel: ApprovalLibrary})
}

// RollbackBy rolls a change back on an approval received through the given
// entry point; the journal records the channel, never a verified identity.
func (p *Project) RollbackBy(run, id string, by Approval) (Change, error) {
	original, e := p.Change(run, id)
	if e != nil {
		return original, e
	}
	r, e := p.ReadRun(run)
	if e != nil {
		return original, e
	}
	lock, e := p.WaitLock("git")
	if e != nil {
		return original, e
	}
	defer lock.Close()
	if e := p.rollbackBlocker(run, original); e != nil {
		return original, e
	}
	head, e := Git(p.Root, "rev-parse", "HEAD")
	if e != nil {
		return original, e
	}
	checkpoint, e := p.checkpoint(gitRepo{dir: p.Root}, "before rollback "+id)
	if e != nil {
		return original, e
	}
	recovery := Change{Version: 1, ID: ID("rollback-"), RunID: run, AgentID: "user", Base: head, State: "recovery", CreatedAt: Now(), Checkpoint: checkpoint.ID, Reverts: id, Paths: original.Paths}
	recovery.Workspace = filepath.Join(p.RunDir(run), "recoveries", recovery.ID)
	if e := os.MkdirAll(filepath.Dir(recovery.Workspace), 0700); e != nil {
		return recovery, e
	}
	start := original.Proposal
	if original.State == "integrated" {
		start = head
	}
	if _, e = Git(p.Root, "worktree", "add", "-b", "codex/arena/recovery/"+recovery.ID, recovery.Workspace, start); e != nil {
		return recovery, e
	}
	if _, e := p.Append(run, r.Workflow.Id, "user", "rollback.created", map[string]any{"original": id, "recovery": recovery, "approval": by.Record()}); e != nil {
		return recovery, e
	}
	if original.State == "integrated" {
		if _, e = Git(recovery.Workspace, "-c", "user.name=Agent Arena", "-c", "user.email=arena@localhost", "revert", "--no-edit", original.IntegrationCommit); e != nil {
			recovery.State = "conflicted"
			_ = p.saveChange(recovery)
			return recovery, fmt.Errorf("rollback conflicts with later work; preserved at %s", recovery.Workspace)
		}
		recovery.Proposal, e = Git(recovery.Workspace, "rev-parse", "HEAD")
		if e != nil {
			return recovery, e
		}
		status, e := Git(p.Root, "status", "--porcelain=v1")
		if e != nil {
			return recovery, e
		}
		current, e := Git(p.Root, "rev-parse", "HEAD")
		if e != nil {
			return recovery, e
		}
		if status == "" && current == head {
			if _, e = Git(p.Root, "merge", "--ff-only", recovery.Proposal); e != nil {
				return recovery, e
			}
			recovery.State = "integrated"
			recovery.IntegrationCommit = recovery.Proposal
		} else {
			recovery.State = "recovery"
		}
	} else {
		recovery.Proposal = original.Proposal
	}
	if e := p.saveChange(recovery); e != nil {
		return recovery, e
	}
	_, e = p.Append(run, r.Workflow.Id, "user", "rollback.completed", map[string]any{"recovery": recovery, "preservedLaterWork": true, "approval": by.Record()})
	return recovery, e
}

func (p *Project) RestoreCheckpoint(id string) (string, error) {
	if e := validID(id); e != nil {
		return "", e
	}
	var checkpoint Checkpoint
	if e := ReadJSON(p.Path(".state", "checkpoints", id+".json"), &checkpoint); e != nil {
		return "", e
	}
	lock, e := p.WaitLock("git")
	if e != nil {
		return "", e
	}
	defer lock.Close()
	recoveryID := ID("recovery-")
	dir := p.Path(".state", "recoveries", recoveryID)
	if e := os.MkdirAll(filepath.Dir(dir), 0700); e != nil {
		return "", e
	}
	if _, e := Git(p.Root, "worktree", "add", "-b", "codex/arena/recovery/"+recoveryID, dir, checkpoint.Head); e != nil {
		return "", e
	}
	patch, e := Git(p.Root, "diff", "--binary", checkpoint.Head, checkpoint.Commit)
	if e != nil {
		return dir, e
	}
	if patch != "" {
		path := p.Path(".state", "recoveries", recoveryID+".working.patch")
		if e := Atomic(path, []byte(patch+"\n"), 0600); e != nil {
			return dir, e
		}
		if _, e := Git(dir, "apply", path); e != nil {
			return dir, e
		}
	}
	if checkpoint.StagedDiff != "" {
		path := p.Path(".state", "recoveries", recoveryID+".staged.patch")
		if e := Atomic(path, []byte(checkpoint.StagedDiff+"\n"), 0600); e != nil {
			return dir, e
		}
		if _, e := Git(dir, "apply", "--cached", path); e != nil {
			return dir, e
		}
	}
	return dir, p.Audit("", "checkpoint.restored", map[string]any{"checkpointId": id, "recoveryWorkspace": dir, "userCheckoutUnchanged": true})
}
func (p *Project) RollbackPreview(run, id string) (map[string]any, error) {
	if _, e := p.requireLiveRun(run); e != nil {
		return nil, e
	}
	change, e := p.Change(run, id)
	if e != nil {
		return nil, e
	}
	if e := p.rollbackBlocker(run, change); e != nil {
		return nil, e
	}
	head, e := Git(p.Root, "rev-parse", "HEAD")
	if e != nil {
		return nil, e
	}
	later := ""
	if change.IntegrationCommit != "" {
		args := []string{"log", "--oneline", change.IntegrationCommit + ".." + head, "--"}
		args = append(args, change.Paths...)
		later, e = Git(p.Root, args...)
		if e != nil {
			return nil, e
		}
	}
	return map[string]any{"change": change, "currentHead": head, "integrationCommit": change.IntegrationCommit, "laterChangesToAffectedPaths": later, "behavior": "creates a revert commit or an isolated recovery worktree; preserves later work"}, nil
}

// composeBase builds a review/task snapshot in an alternate index. The user's
// checkout and index are never used as a staging area for another worker's patch.
func (p *Project) composeBase(r Run, base string, dependencies []string) (string, error) {
	ordered := []Change{}
	seen := map[string]bool{}
	visiting := map[string]bool{}
	var visit func(string) error
	visit = func(id string) error {
		if seen[id] {
			return nil
		}
		if visiting[id] {
			return fmt.Errorf("change dependency cycle")
		}
		visiting[id] = true
		change, err := p.Change(r.ID, id)
		if err != nil {
			return err
		}
		for _, dependency := range change.DependsOn {
			if err := visit(dependency); err != nil {
				return err
			}
		}
		visiting[id] = false
		seen[id] = true
		ordered = append(ordered, change)
		return nil
	}
	for _, id := range dependencies {
		if err := visit(id); err != nil {
			return "", err
		}
	}
	id := ID("checkpoint-")
	index := p.Path(".state", "indices", id)
	if err := os.MkdirAll(filepath.Dir(index), 0700); err != nil {
		return "", err
	}
	env := []string{"GIT_INDEX_FILE=" + index, "GIT_AUTHOR_NAME=Agent Arena", "GIT_AUTHOR_EMAIL=arena@localhost", "GIT_COMMITTER_NAME=Agent Arena", "GIT_COMMITTER_EMAIL=arena@localhost"}
	if _, err := gitEnv(p.Root, env, "read-tree", base); err != nil {
		return "", err
	}
	for _, change := range ordered {
		patch := filepath.Join(p.RunDir(r.ID), "changes", change.ID+".patch")
		if _, err := gitEnv(p.Root, env, "apply", "--cached", "--3way", patch); err != nil {
			return "", fmt.Errorf("dependency patches conflict; alternate index preserved at %s: %w", index, err)
		}
	}
	tree, err := gitEnv(p.Root, env, "write-tree")
	if err != nil {
		return "", err
	}
	commit, err := gitEnv(p.Root, env, "commit-tree", tree, "-p", base, "-m", "Arena dependency snapshot")
	if err != nil {
		return "", err
	}
	if _, err := Git(p.Root, "update-ref", "refs/arena/checkpoints/"+id, commit); err != nil {
		return "", err
	}
	checkpoint := Checkpoint{ID: id, Commit: commit, Head: base, Tree: tree, CreatedAt: Now(), Directory: p.Root, Reason: "task dependency snapshot", Untracked: []string{}}
	if err := Atomic(p.Path(".state", "checkpoints", id+".json"), JSON(checkpoint), 0600); err != nil {
		return "", err
	}
	if err := os.Remove(index); err != nil {
		return "", err
	}
	_, err = p.Append(r.ID, r.Workflow.Id, "", "checkpoint.created", map[string]any{"checkpointId": id, "commit": commit, "dependencies": dependencies})
	return commit, err
}
