package arena

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// PurgePlan is everything `arena history RUN purge` would delete, drawn only
// from a retired run's own records and confined to Arena's state directory,
// Arena's branch names and refs/arena/checkpoints.
type PurgePlan struct {
	RunID string `json:"runId"`
	// Retired are the retired-history directories of the run.
	Retired []string `json:"retired"`
	// Worktrees are git-registered worktrees that were created under the run's
	// state directory (retirement moved the directories, not the registrations).
	Worktrees   []string `json:"worktrees"`
	Branches    []string `json:"branches"`
	Checkpoints []string `json:"checkpoints"`
	// Unintegrated lists work that deleting would lose: changes still proposed,
	// conflicted, integration-prepared or in recovery, and worktrees holding
	// uncommitted files.
	Unintegrated  []string `json:"unintegrated"`
	EventCount    int      `json:"eventCount"`
	LastEventHash string   `json:"lastEventHash,omitempty"`
}

var unintegratedStates = []string{"proposed", "conflicted", "integration-prepared", "recovery"}

func (p *Project) retiredRoot() string { return p.Path(".state", "retired") }

// retiredDirs finds the retired directories of one run: exactly ID-<timestamp>
// directories, so run-a never matches run-ab.
func (p *Project) retiredDirs(id string) ([]string, error) {
	pattern := regexp.MustCompile("^" + regexp.QuoteMeta(id) + `-\d{8}T\d{6}$`)
	entries, err := os.ReadDir(p.retiredRoot())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	dirs := []string{}
	for _, d := range entries {
		if d.IsDir() && pattern.MatchString(d.Name()) {
			dirs = append(dirs, filepath.Join(p.retiredRoot(), d.Name()))
		}
	}
	return dirs, nil
}

// resolvedPath is path with the symlinks in its longest existing prefix
// resolved and the rest appended as written. Git records a worktree by its real
// path, and on macOS the temporary and home directories sit behind symlinks
// (/var is /private/var), so the same worktree can be spelled two ways. A
// retired run's worktree directories no longer exist, which rules out
// filepath.EvalSymlinks on the whole path: resolving what is left of it is what
// lets a purge recognise the registration Git still holds.
func resolvedPath(path string) string {
	path = filepath.Clean(path)
	rest := ""
	for current := path; ; {
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			return filepath.Join(resolved, rest)
		}
		parent := filepath.Dir(current)
		if parent == current {
			return path
		}
		rest = filepath.Join(filepath.Base(current), rest)
		current = parent
	}
}

// underRun is whether path is strictly inside the run's state directory. Both
// are compared as written, so callers resolve them first (see resolvedPath).
func underRun(live, path string) bool {
	path = filepath.Clean(path)
	return path != live && within(path, live)
}

// PlanPurge reads a retired run and reports what a purge would remove.
func (p *Project) PlanPurge(id string) (PurgePlan, error) {
	plan := PurgePlan{RunID: id, Worktrees: []string{}, Branches: []string{}, Checkpoints: []string{}, Unintegrated: []string{}}
	if err := validRunID(id); err != nil {
		return plan, err
	}
	if _, err := os.Lstat(p.RunDir(id)); err == nil {
		return plan, fmt.Errorf("run %s is not retired; run arena history %s retire first", id, id)
	}
	dirs, err := p.retiredDirs(id)
	if err != nil {
		return plan, err
	}
	if len(dirs) == 0 {
		return plan, fmt.Errorf("no retired history for %s; purge only deletes runs already retired with arena history %s retire", id, id)
	}
	plan.Retired = dirs
	// Records may spell a path differently from this process (they were written
	// through another spelling of the project root), and Git registers worktrees
	// by their real path. Every path below is compared and handed to Git in its
	// resolved form.
	// The run directory was just checked absent, so its own name is never
	// resolved (a symlink appearing there would redirect every record path
	// into another run); only the real .arena/.state parent is.
	live := filepath.Join(resolvedPath(p.Path(".state")), id)
	paths, branches, checkpoints := map[string]bool{}, map[string]bool{}, map[string]bool{}
	workers := map[string]Workspace{}
	for _, dir := range dirs {
		if info, err := os.Lstat(dir); err != nil || info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return plan, fmt.Errorf("%s is not a plain retired directory", dir)
		}
		if journal, err := os.ReadFile(filepath.Join(dir, "events.ndjson")); err == nil {
			if events, err := verifyJournalLines(journal, id); err == nil {
				plan.EventCount, plan.LastEventHash = len(events), events[len(events)-1].Hash
				for _, e := range events {
					if e.Type == "checkpoint.created" {
						checkpoints[str(e.Data["checkpointId"])] = true
					}
				}
			}
		}
		var baseline Checkpoint
		if ReadJSON(filepath.Join(dir, "baseline.json"), &baseline) == nil {
			checkpoints[baseline.ID] = true
		}
		manifests, _ := filepath.Glob(filepath.Join(dir, "workspaces", "*.json"))
		for _, name := range manifests {
			var w Workspace
			if err := ReadJSON(name, &w); err != nil {
				return plan, err
			}
			// The records are Arena's own, but a purge still only follows
			// paths and branches that sit where Arena puts them.
			if resolved := resolvedPath(w.Path); underRun(live, resolved) {
				paths[resolved] = true
				if w.Write {
					w.Path = resolved
					workers[resolved] = w
				}
			}
			if w.Branch != "" && strings.HasPrefix(w.Branch, "codex/arena/"+id+"/") {
				branches[w.Branch] = true
			}
		}
		changeFiles, _ := filepath.Glob(filepath.Join(dir, "changes", "*.json"))
		for _, name := range changeFiles {
			var change Change
			if err := ReadJSON(name, &change); err != nil {
				return plan, err
			}
			if change.RunID != id || validID(change.ID) != nil {
				return plan, fmt.Errorf("%s does not belong to %s", name, id)
			}
			if Has(unintegratedStates, change.State) {
				plan.Unintegrated = append(plan.Unintegrated, fmt.Sprintf("change %s (%s, agent %s, %d paths)", change.ID, change.State, change.AgentID, len(change.Paths)))
			}
			if change.Checkpoint != "" {
				checkpoints[change.Checkpoint] = true
			}
			for _, w := range []string{change.Workspace, change.IntegrationWorkspace} {
				if w == "" {
					continue
				}
				if resolved := resolvedPath(w); underRun(live, resolved) {
					paths[resolved] = true
				}
			}
			if change.IntegrationWorkspace != "" {
				branches["codex/arena/integrate/"+change.ID] = true
			}
			if strings.HasPrefix(change.ID, "rollback-") {
				branches["codex/arena/recovery/"+change.ID] = true
			}
		}
		// A writer's worktree is never committed to; its work lives in the
		// proposals. Work it holds beyond its last proposal would be lost.
		for path, w := range workers {
			if dirty := p.unproposedFiles(w, filepath.Join(dir, strings.TrimPrefix(path, live+string(filepath.Separator)))); dirty != "" {
				plan.Unintegrated = append(plan.Unintegrated, fmt.Sprintf("worktree %s: %s", filepath.Base(path), dirty))
			}
		}
	}
	registered, err := p.registeredWorktrees()
	if err != nil {
		return plan, err
	}
	for path := range paths {
		if git, ok := registered[path]; ok {
			plan.Worktrees = append(plan.Worktrees, git)
		}
	}
	for branch := range branches {
		if _, err := Git(p.Root, "show-ref", "--verify", "--quiet", "refs/heads/"+branch); err == nil {
			plan.Branches = append(plan.Branches, branch)
		}
	}
	for checkpoint := range checkpoints {
		if validID(checkpoint) != nil || !strings.HasPrefix(checkpoint, "checkpoint-") {
			continue
		}
		_, refErr := Git(p.Root, "show-ref", "--verify", "--quiet", "refs/arena/checkpoints/"+checkpoint)
		if _, statErr := os.Lstat(p.Path(".state", "checkpoints", checkpoint+".json")); refErr == nil || statErr == nil {
			plan.Checkpoints = append(plan.Checkpoints, checkpoint)
		}
	}
	sort.Strings(plan.Worktrees)
	sort.Strings(plan.Branches)
	sort.Strings(plan.Checkpoints)
	sort.Strings(plan.Unintegrated)
	return plan, nil
}

// unproposedFiles names the files a retired writer's worktree holds that its
// last proposal does not. It snapshots the tree the way a checkpoint does, in a
// scratch index and without a ref, and compares that to the tree the last
// proposal recorded (or the base, if it never proposed). Like every Git call in
// a worker worktree it runs with the gitdir pinned to the main repository's
// record and the hardened config; a worktree it cannot inspect that way is
// reported, because that is not proof it is clean.
func (p *Project) unproposedFiles(w Workspace, tree string) string {
	if _, err := os.Lstat(tree); err != nil {
		return ""
	}
	const unknown = "could not compare with its last proposal"
	common, err := Git(p.Root, "rev-parse", "--git-common-dir")
	if err != nil {
		return unknown
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(p.Root, common)
	}
	admins, _ := filepath.Glob(filepath.Join(common, "worktrees", "*"))
	admin := ""
	for _, candidate := range admins {
		if b, err := os.ReadFile(filepath.Join(candidate, "gitdir")); err == nil && resolvedPath(strings.TrimSpace(string(b))) == filepath.Join(resolvedPath(w.Path), ".git") {
			admin = candidate
		}
	}
	if pointer, err := readGitPointer(tree); admin == "" || err != nil || !sameDir(pointer, admin) {
		return unknown
	}
	scratch, err := os.MkdirTemp(p.Path(".state"), "purge-index-")
	if err != nil {
		return unknown
	}
	defer os.RemoveAll(scratch)
	repo, env := gitRepo{dir: tree, gitDir: admin}, []string{"GIT_INDEX_FILE=" + filepath.Join(scratch, "index")}
	recorded := w.LastTree
	if recorded == "" {
		recorded = w.Base + "^{tree}"
	}
	var current, names string
	for _, step := range [][]string{{"read-tree", w.Base}, {"add", "-A"}, {"rm", "--cached", "-r", "-f", "--ignore-unmatch", "--", ".arena/.state", ".arena/locks", ".arena-root"}} {
		if _, err = repo.gitEnv(env, step...); err != nil {
			return unknown
		}
	}
	if current, err = repo.gitEnv(env, "write-tree"); err != nil {
		return unknown
	}
	if names, err = repo.git("diff-tree", "-r", "--name-only", "--no-renames", recorded, current); err != nil {
		return unknown
	}
	files := strings.Fields(names)
	switch {
	case len(files) == 0:
		return ""
	case len(files) > 5:
		return fmt.Sprintf("%d files beyond its last proposal (%s, ...)", len(files), strings.Join(files[:5], ", "))
	}
	return "files beyond its last proposal: " + strings.Join(files, ", ")
}

// registeredWorktrees maps each worktree Git has registered, by its resolved
// path, to the path as Git spells it, which is what Git accepts back.
func (p *Project) registeredWorktrees() (map[string]string, error) {
	out, err := Git(p.Root, "worktree", "list", "--porcelain")
	if err != nil {
		return nil, err
	}
	registered := map[string]string{}
	for _, line := range strings.Split(out, "\n") {
		if path, ok := strings.CutPrefix(line, "worktree "); ok {
			registered[resolvedPath(path)] = filepath.Clean(path)
		}
	}
	return registered, nil
}

// PurgeRun permanently deletes a retired run and records a tombstone in the
// project journal before and after. Unintegrated work blocks it unless
// discardUnintegrated; the caller lists the plan first.
func (p *Project) PurgeRun(id string, discardUnintegrated bool) (PurgePlan, error) {
	plan, err := p.PlanPurge(id)
	if err != nil {
		return plan, err
	}
	if len(plan.Unintegrated) > 0 && !discardUnintegrated {
		return plan, fmt.Errorf("%d unintegrated item(s) would be lost; integrate or inspect them, or pass --discard-unintegrated", len(plan.Unintegrated))
	}
	gitLock, err := p.WaitLock("git")
	if err != nil {
		return plan, err
	}
	defer gitLock.Close()
	if _, err := p.Append("project", "", "user", "history.purge.started", map[string]any{"plan": plan, "discardedUnintegrated": discardUnintegrated}); err != nil {
		return plan, err
	}
	failures := []string{}
	fail := func(what string, err error) { failures = append(failures, what+": "+Redact(err.Error())) }
	for _, path := range plan.Worktrees {
		// The directory moved when the run was retired, so this removes the
		// stale registration; git deletes nothing else.
		if _, err := Git(p.Root, "worktree", "remove", "--force", path); err != nil {
			fail("worktree "+path, err)
		}
	}
	for _, branch := range plan.Branches {
		if _, err := Git(p.Root, "branch", "-D", branch); err != nil {
			fail("branch "+branch, err)
		}
	}
	for _, checkpoint := range plan.Checkpoints {
		if _, err := Git(p.Root, "update-ref", "-d", "refs/arena/checkpoints/"+checkpoint); err != nil {
			fail("checkpoint ref "+checkpoint, err)
		}
		if err := os.Remove(p.Path(".state", "checkpoints", checkpoint+".json")); err != nil && !os.IsNotExist(err) {
			fail("checkpoint record "+checkpoint, err)
		}
	}
	// Only when everything above went through: a failed purge keeps the
	// retired history so it can be run again.
	if len(failures) == 0 {
		for _, dir := range plan.Retired {
			if filepath.Dir(dir) != p.retiredRoot() {
				fail("retired directory "+dir, fmt.Errorf("outside Arena's retired storage"))
			} else if err := os.RemoveAll(dir); err != nil {
				fail("retired directory "+dir, err)
			}
		}
	}
	data := map[string]any{"plan": plan, "discardedUnintegrated": discardUnintegrated, "complete": len(failures) == 0, "failures": failures}
	if _, err := p.Append("project", "", "user", "history.purged", data); err != nil {
		return plan, err
	}
	if len(failures) > 0 {
		return plan, fmt.Errorf("purge incomplete; retired history kept so it can be retried: %s", strings.Join(failures, "; "))
	}
	return plan, nil
}
