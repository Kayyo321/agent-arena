package arena

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This file is the conflict and rollback benchmark (docs/BENCHMARKS.md). It is
// deterministic and spends no inference: writers are fake invokers or a fake
// `claude` executable, repositories are temporary. Every scenario is scored
// twice, once through Arena's workspace/integrate/rollback code and once
// against a naive shared checkout, by counting writes that end up neither in
// the final checkout nor recoverable.

// benchEdit is one write: set a 1-based line of a file, or, with line 0,
// create or replace the whole file with text.
type benchEdit struct {
	path string
	line int
	text string
}

// benchWriter is one agent (or the user) and the edits it makes.
type benchWriter struct {
	id    string
	edits []benchEdit
}

func (e benchEdit) applied(content string) string {
	if e.line == 0 {
		return e.text + "\n"
	}
	lines := strings.Split(strings.TrimSuffix(content, "\n"), "\n")
	if content == "" {
		lines = nil
	}
	for len(lines) < e.line {
		lines = append(lines, "")
	}
	lines[e.line-1] = e.text
	return strings.Join(lines, "\n") + "\n"
}

// presentIn reports whether content still carries the edit. Texts are unique
// per edit, so a line or file match cannot come from another write.
func (e benchEdit) presentIn(content string) bool {
	if e.line == 0 {
		return content == e.text+"\n"
	}
	return Has(strings.Split(content, "\n"), e.text)
}

func readBench(dir, path string) string {
	b, _ := os.ReadFile(filepath.Join(dir, path))
	return string(b)
}

func applyBenchEdits(dir string, edits []benchEdit) error {
	for _, e := range edits {
		if strings.ContainsAny(e.text, "'\\\"\n") {
			return fmt.Errorf("bench text %q must be shell- and awk-safe", e.text)
		}
		full := filepath.Join(dir, e.path)
		if err := os.WriteFile(full, []byte(e.applied(readBench(dir, e.path))), 0644); err != nil {
			return err
		}
	}
	return nil
}

func benchCommit(t *testing.T, dir, message string, paths ...string) {
	t.Helper()
	if _, err := Git(dir, append([]string{"add", "--"}, paths...)...); err != nil {
		t.Fatal(err)
	}
	if _, err := Git(dir, "-c", "user.name=Test", "-c", "user.email=test@localhost", "commit", "-qm", message); err != nil {
		t.Fatal(err)
	}
}

// benchSeed is the committed content every scenario starts from. notes.txt is
// long enough that edits several lines apart are separate Git hunks.
func benchSeed() map[string]string {
	lines := make([]string, 30)
	for i := range lines {
		lines[i] = fmt.Sprintf("setting %02d: default", i+1)
	}
	return map[string]string{"notes.txt": strings.Join(lines, "\n") + "\n", "base.txt": "baseline\n"}
}

func benchProject(t *testing.T) *Project {
	t.Helper()
	p := committedProject(t)
	if err := os.WriteFile(filepath.Join(p.Root, "notes.txt"), []byte(benchSeed()["notes.txt"]), 0644); err != nil {
		t.Fatal(err)
	}
	benchCommit(t, p.Root, "seed notes", "notes.txt")
	return p
}

// benchRun is a saved run whose head delegates to the given writing agents.
// The workflow files are committed so the checkout is clean, which is what
// integration requires of the target.
func benchRun(t *testing.T, ids []string, runtime Runtime) (*Project, Run) {
	t.Helper()
	p := benchProject(t)
	s, err := p.Settings()
	if err != nil {
		t.Fatal(err)
	}
	w := NewWorkflow("bench", "Conflict benchmark")
	w.HeadAgent = "head"
	w.Settings.MaxConcurrentAgents = len(ids)
	profiles := map[string]Profile{}
	for _, id := range append([]string{"head"}, ids...) {
		level, caps := 1, []string{"read", "write"}
		if id == "head" {
			level, caps = 0, []string{"read"}
		}
		w.Agents = append(w.Agents, Agent{Id: id, Level: Int(level), Profile: ".agents/bench/" + id + ".agent.json", Runtime: runtime})
		profiles[id] = NewProfile(id, "research", "Do the "+id+" work", "Use evidence", caps)
		if id != "head" {
			w.Relationships = append(w.Relationships, Relationship{Type: "delegates", From: "head", To: id})
		}
	}
	if err := ValidateWorkflow(w, profiles, s); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Save(w, profiles, ""); err != nil {
		t.Fatal(err)
	}
	head, err := Git(p.Root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	r, err := p.CreateRun(w, profiles, s, "Conflict benchmark", head)
	if err != nil {
		t.Fatal(err)
	}
	benchCommit(t, p.Root, "workflow", ".arena")
	return p, r
}

// benchExecute runs the workflow, checks that writing left the user's checkout
// alone, and returns each writer's proposed change.
func benchExecute(t *testing.T, p *Project, r Run, invoker Invoker) map[string]Change {
	t.Helper()
	before, err := Git(p.Root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := NewEngine(p, r, invoker).Execute(ctx, false, false); err != nil {
		t.Fatal(err)
	}
	benchCheckoutUntouched(t, p, before)
	return benchChanges(t, p, r)
}

func benchCheckoutUntouched(t *testing.T, p *Project, head string) {
	t.Helper()
	after, err := Git(p.Root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	status, err := Git(p.Root, "status", "--porcelain=v1")
	if err != nil || after != head || status != "" {
		t.Fatalf("the user's checkout changed before any approval: head %s -> %s, status %q, %v", head, after, status, err)
	}
}

func benchChanges(t *testing.T, p *Project, r Run) map[string]Change {
	t.Helper()
	changes, err := p.Changes(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]Change{}
	for _, c := range changes {
		if c.State == "proposed" {
			out[c.AgentID] = c
		}
	}
	return out
}

func benchIntegrate(t *testing.T, p *Project, r Run, c Change) Change {
	t.Helper()
	got, err := p.Integrate(r.ID, c.ID, true)
	if err != nil || got.State != "integrated" {
		t.Fatalf("integrate %s: %v (state %s)", c.AgentID, err, got.State)
	}
	return got
}

// benchRefused integrates a change that must not land and returns what the
// user saw: the error, with the change's persisted state.
func benchRefused(t *testing.T, p *Project, r Run, c Change) (string, string) {
	t.Helper()
	before, err := Git(p.Root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Integrate(r.ID, c.ID, true)
	if err == nil {
		t.Fatalf("integrating %s should have been refused", c.AgentID)
	}
	after, gitErr := Git(p.Root, "rev-parse", "HEAD")
	if gitErr != nil || after != before {
		t.Fatalf("refused integration still moved HEAD: %s -> %s %v", before, after, gitErr)
	}
	saved, readErr := p.Change(r.ID, c.ID)
	if readErr != nil {
		t.Fatal(readErr)
	}
	return err.Error(), saved.State
}

func benchChangeFor(t *testing.T, p *Project, r Run, agent string) []Change {
	t.Helper()
	all, err := p.Changes(r.ID)
	if err != nil {
		t.Fatal(err)
	}
	out := []Change{}
	for _, c := range all {
		if c.AgentID == agent {
			out = append(out, c)
		}
	}
	return out
}

type benchWrite struct {
	by   string
	edit benchEdit
}

func benchWrites(writers ...benchWriter) []benchWrite {
	out := []benchWrite{}
	for _, w := range writers {
		for _, e := range w.edits {
			out = append(out, benchWrite{w.id, e})
		}
	}
	return out
}

// benchCount scores every write that was supposed to survive. Lost counts
// writes that are neither in the final checkout nor recoverable from anything
// the tool recorded, whether they were dropped or silently overwritten.
type benchCount struct{ writes, landed, preserved, lost int }

// scoreArena looks for each write in the final checkout, then in the proposal
// commits Arena keeps under refs/arena/checkpoints for that writer.
func scoreArena(t *testing.T, p *Project, r Run, writes []benchWrite) benchCount {
	t.Helper()
	n := benchCount{writes: len(writes)}
	for _, w := range writes {
		if w.edit.presentIn(readBench(p.Root, w.edit.path)) {
			n.landed++
			continue
		}
		recovered := false
		for _, c := range benchChangeFor(t, p, r, w.by) {
			content, err := Git(p.Root, "show", c.Proposal+":"+w.edit.path)
			refs, refErr := Git(p.Root, "for-each-ref", "--contains", c.Proposal, "refs/arena/checkpoints")
			if err == nil && refErr == nil && refs != "" && w.edit.presentIn(content+"\n") {
				recovered = true
			}
		}
		if recovered {
			n.preserved++
		} else {
			n.lost++
		}
	}
	return n
}

// naiveCheckout is the baseline: every writer works in the one shared
// directory and nothing records what a later write replaces. Serial writers
// each start after the previous one finished. Concurrent writers all start
// from the same snapshot and write whole files back in order, so the last
// writer to touch a file wins it. later edits (a user, say) come after every
// writer has finished. This models a naive shared checkout only.
func naiveCheckout(t *testing.T, writers, later []benchWriter, concurrent bool) string {
	t.Helper()
	dir := t.TempDir()
	for path, content := range benchSeed() {
		if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(writers []benchWriter, concurrent bool) {
		snapshots := make([]map[string]string, len(writers))
		read := func(w benchWriter) map[string]string {
			files := map[string]string{}
			for _, e := range w.edits {
				files[e.path] = readBench(dir, e.path)
			}
			return files
		}
		for i, w := range writers {
			if concurrent {
				snapshots[i] = read(w)
			}
		}
		for i, w := range writers {
			files := snapshots[i]
			if !concurrent {
				files = read(w)
			}
			for _, e := range w.edits {
				files[e.path] = e.applied(files[e.path])
			}
			for path, content := range files {
				if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0644); err != nil {
					t.Fatal(err)
				}
			}
		}
	}
	run(writers, concurrent)
	run(later, false)
	return dir
}

// naiveRestore is the naive rollback: put each file the change touched back to
// its content from before the change.
func naiveRestore(t *testing.T, dir string, paths ...string) {
	t.Helper()
	for _, path := range paths {
		seed, ok := benchSeed()[path]
		if !ok {
			if err := os.Remove(filepath.Join(dir, path)); err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, path), []byte(seed), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

func scoreNaive(dir string, writes []benchWrite) benchCount {
	n := benchCount{writes: len(writes)}
	for _, w := range writes {
		if w.edit.presentIn(readBench(dir, w.edit.path)) {
			n.landed++
		} else {
			n.lost++
		}
	}
	return n
}

type benchRow struct {
	scenario      string
	arena, serial benchCount
	concurrent    *benchCount // nil where the scenario is inherently sequential
}

func benchTable(rows []benchRow) string {
	var b strings.Builder
	b.WriteString("| Scenario | Writes | Arena landed / preserved / lost | Naive serial landed / lost | Naive concurrent landed / lost |\n| --- | --: | --- | --- | --- |\n")
	var arena, serial, concurrent benchCount
	for _, r := range rows {
		cell := "n/a"
		if r.concurrent != nil {
			cell = fmt.Sprintf("%d / %d", r.concurrent.landed, r.concurrent.lost)
			concurrent.writes += r.concurrent.writes
			concurrent.landed += r.concurrent.landed
			concurrent.lost += r.concurrent.lost
		}
		fmt.Fprintf(&b, "| %s | %d | %d / %d / %d | %d / %d | %s |\n", r.scenario, r.arena.writes, r.arena.landed, r.arena.preserved, r.arena.lost, r.serial.landed, r.serial.lost, cell)
		arena.writes += r.arena.writes
		arena.landed += r.arena.landed
		arena.preserved += r.arena.preserved
		arena.lost += r.arena.lost
		serial.landed += r.serial.landed
		serial.lost += r.serial.lost
	}
	fmt.Fprintf(&b, "| **Total** | %d | %d / %d / %d | %d / %d | %d / %d (of %d) |\n", arena.writes, arena.landed, arena.preserved, arena.lost, serial.landed, serial.lost, concurrent.landed, concurrent.lost, concurrent.writes)
	return b.String()
}

// benchBackend is how the fake writers run: a Go invoker, or a fake `claude`
// executable behind the real Claude Code sign-in adapter.
type benchBackend struct {
	name    string
	runtime Runtime
	invoker func(t *testing.T, writers []benchWriter) Invoker
}

var benchBackends = []benchBackend{
	{"invoker", Runtime{Provider: "codex", Model: "explicit-test-model", ReasoningEffort: "high"}, func(t *testing.T, writers []benchWriter) Invoker {
		return testInvoker(func(ctx context.Context, in Invocation) (map[string]any, error) {
			for _, w := range writers {
				if w.id == in.AgentID {
					if err := applyBenchEdits(in.Dir, w.edits); err != nil {
						return nil, err
					}
				}
			}
			return ordinaryResult(), nil
		})
	}},
	{"claude-cli", Runtime{Provider: "claude-code", Model: "fixture", ReasoningEffort: "low", Options: map[string]any{"authMethod": claudeSignInAuth}}, func(t *testing.T, writers []benchWriter) Invoker {
		var script strings.Builder
		script.WriteString("setline() { awk -v n=\"$2\" -v t=\"$3\" 'NR==n{print t; next} {print}' \"$1\" > \"$1.tmp\" && mv \"$1.tmp\" \"$1\"; }\ncase \"$(basename \"$PWD\")\" in\n")
		for _, w := range writers {
			fmt.Fprintf(&script, "%s)\n", w.id)
			for _, e := range w.edits {
				if strings.ContainsAny(e.text, "'\\\"\n") {
					t.Fatalf("bench text %q must be shell- and awk-safe", e.text)
				}
				if e.line == 0 {
					fmt.Fprintf(&script, "printf '%%s\\n' '%s' > '%s'\n", e.text, e.path)
				} else {
					fmt.Fprintf(&script, "setline '%s' %d '%s'\n", e.path, e.line, e.text)
				}
			}
			script.WriteString(";;\n")
		}
		fmt.Fprintf(&script, "esac\ncat <<'EOF'\n{\"type\":\"result\",\"subtype\":\"success\",\"is_error\":false,\"result\":\"{}\",\"structured_output\":%s,\"usage\":{\"output_tokens\":3}}\nEOF\n", compactJSON(ordinaryResult()))
		fakeCLI(t, "claude", script.String())
		return Providers{}
	}},
}

func benchIDs(writers []benchWriter) []string {
	ids := []string{}
	for _, w := range writers {
		ids = append(ids, w.id)
	}
	return ids
}

// TestBenchConflictAndRollback is the benchmark. Run `make bench` to see its
// table. Arena must never lose or silently overwrite a write; the naive rows
// are measurements, not assertions.
func TestBenchConflictAndRollback(t *testing.T) {
	var rows []benchRow
	for _, s := range []struct {
		name string
		run  func(*testing.T) benchRow
	}{
		{"disjoint-writers", benchDisjointWriters},
		{"overlapping-hunks", benchOverlappingHunks},
		{"fan-out-overlapping-children", benchFanOutOverlap},
		{"user-edit-before-integrate", benchUserEditBeforeIntegrate},
		{"rollback-then-disjoint-edit", benchRollbackDisjointEdit},
		{"rollback-then-overlapping-edit", benchRollbackOverlappingEdit},
		{"loop-iteration", benchLoopIteration},
	} {
		t.Run(s.name, func(t *testing.T) { rows = append(rows, s.run(t)) })
	}
	if testing.Verbose() {
		fmt.Printf("\n%s\n", benchTable(rows))
	}
	for _, r := range rows {
		if r.arena.lost != 0 {
			t.Errorf("%s: Arena lost %d write(s)", r.scenario, r.arena.lost)
		}
	}
}

// Three agents write in parallel: separate files plus separate lines of one
// shared file. Nothing overlaps, so everything must integrate.
func benchDisjointWriters(t *testing.T) benchRow {
	writers := []benchWriter{
		{"writer-a", []benchEdit{{"notes.txt", 2, "A: raise timeout to 30s"}, {"a.txt", 0, "A: new module"}}},
		{"writer-b", []benchEdit{{"notes.txt", 15, "B: enable retries"}, {"b.txt", 0, "B: new module"}}},
		{"writer-c", []benchEdit{{"notes.txt", 28, "C: log to stderr"}, {"c.txt", 0, "C: new module"}}},
	}
	writes := benchWrites(writers...)
	var arena benchCount
	for _, backend := range benchBackends {
		p, r := benchRun(t, benchIDs(writers), backend.runtime)
		changes := benchExecute(t, p, r, backend.invoker(t, writers))
		if len(changes) != 3 {
			t.Fatalf("%s: want three proposals, got %v", backend.name, changes)
		}
		// A reviewer downstream of all three proposals sees every one of them.
		ids := []string{}
		for _, w := range writers {
			ids = append(ids, changes[w.id].ID)
		}
		reader, err := p.Workspace(r, "reviewer", "reviewer", false, ids...)
		if err != nil {
			t.Fatalf("%s: composeBase refused disjoint proposals: %v", backend.name, err)
		}
		for _, w := range writers {
			for _, e := range w.edits {
				if !e.presentIn(readBench(reader.Path, e.path)) {
					t.Fatalf("%s: the downstream snapshot lost %v", backend.name, e)
				}
			}
		}
		for _, w := range writers {
			benchIntegrate(t, p, r, changes[w.id])
		}
		count := scoreArena(t, p, r, writes)
		if backend.name == "invoker" {
			arena = count
		} else if count != arena {
			t.Fatalf("%s route scored %+v, the invoker route %+v", backend.name, count, arena)
		}
	}
	concurrent := scoreNaive(naiveCheckout(t, writers, nil, true), writes)
	return benchRow{scenario: "Disjoint parallel writers", arena: arena, serial: scoreNaive(naiveCheckout(t, writers, nil, false), writes), concurrent: &concurrent}
}

// Two writers set the same line to different values and a third edits another
// line. The second writer's change must end `conflicted` with its work kept.
func benchOverlappingHunks(t *testing.T) benchRow {
	writers := []benchWriter{
		{"writer-a", []benchEdit{{"notes.txt", 10, "A: cache size 64"}, {"a.txt", 0, "A: new module"}}},
		{"writer-b", []benchEdit{{"notes.txt", 10, "B: cache size 128"}, {"b.txt", 0, "B: new module"}}},
		{"writer-c", []benchEdit{{"notes.txt", 22, "C: pool size 8"}}},
	}
	writes := benchWrites(writers...)
	p, r := benchRun(t, benchIDs(writers), benchBackends[0].runtime)
	changes := benchExecute(t, p, r, benchBackends[0].invoker(t, writers))
	// A reviewer cannot be handed a tree that silently merges the two.
	if _, err := p.Workspace(r, "reviewer", "reviewer", false, changes["writer-a"].ID, changes["writer-b"].ID); err == nil || !strings.Contains(err.Error(), "dependency patches conflict") {
		t.Fatalf("composeBase should refuse conflicting proposals: %v", err)
	}
	benchIntegrate(t, p, r, changes["writer-a"])
	message, state := benchRefused(t, p, r, changes["writer-b"])
	if state != "conflicted" || message == "" {
		t.Fatalf("overlapping change should end conflicted, got %q (%s)", state, message)
	}
	benchIntegrate(t, p, r, changes["writer-c"])
	events, err := p.Events(r.ID)
	if err != nil || countEvents(events, "change.conflicted", "writer-b") != 1 {
		t.Fatalf("conflict was not journaled: %v", err)
	}
	if status, err := Git(p.Root, "status", "--porcelain=v1"); err != nil || status != "" {
		t.Fatalf("checkout is dirty after the conflict: %q %v", status, err)
	}
	arena := scoreArena(t, p, r, writes)
	if arena.preserved != 2 {
		t.Fatalf("writer-b's whole change should be preserved, not dropped: %+v", arena)
	}
	concurrent := scoreNaive(naiveCheckout(t, writers, nil, true), writes)
	return benchRow{scenario: "Overlapping hunks", arena: arena, serial: scoreNaive(naiveCheckout(t, writers, nil, false), writes), concurrent: &concurrent}
}

// The overlapping writers again, as the children of a fan-out. Arena finds the
// overlap before the next node starts, so the reviewer gets a snapshot of the
// clean child instead of a refusal, and both overlapping proposals stay
// reviewable. Integration then behaves as in the overlapping-hunks scenario.
func benchFanOutOverlap(t *testing.T) benchRow {
	writers := []benchWriter{
		{"writer-a", []benchEdit{{"notes.txt", 10, "A: cache size 64"}, {"a.txt", 0, "A: new module"}}},
		{"writer-b", []benchEdit{{"notes.txt", 10, "B: cache size 128"}, {"b.txt", 0, "B: new module"}}},
		{"writer-c", []benchEdit{{"notes.txt", 22, "C: pool size 8"}}},
	}
	writes := benchWrites(writers...)
	p, r := benchRun(t, benchIDs(writers), benchBackends[0].runtime)
	changes := benchExecute(t, p, r, benchBackends[0].invoker(t, writers))
	order := benchIDs(writers)
	byChild := map[string][]string{}
	for _, id := range order {
		byChild[id] = []string{changes[id].ID}
	}
	conflicts, err := p.fanOutConflicts(r, order, byChild)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(conflicts["writer-a"], ",") != "writer-b" || strings.Join(conflicts["writer-b"], ",") != "writer-a" || len(conflicts) != 2 {
		t.Fatalf("the overlap was not found, or too much was held: %v", conflicts)
	}
	reader, err := p.Workspace(r, "reviewer", "reviewer", false, changes["writer-c"].ID)
	if err != nil {
		t.Fatalf("the snapshot without the held changes failed: %v", err)
	}
	if !(benchEdit{"notes.txt", 22, "C: pool size 8"}).presentIn(readBench(reader.Path, "notes.txt")) {
		t.Fatal("the downstream snapshot lost the clean child's edit")
	}
	benchIntegrate(t, p, r, changes["writer-a"])
	if _, state := benchRefused(t, p, r, changes["writer-b"]); state != "conflicted" {
		t.Fatalf("the second overlapping change ended %q", state)
	}
	benchIntegrate(t, p, r, changes["writer-c"])
	arena := scoreArena(t, p, r, writes)
	if arena.preserved != 2 {
		t.Fatalf("writer-b's whole change should be preserved, not dropped: %+v", arena)
	}
	concurrent := scoreNaive(naiveCheckout(t, writers, nil, true), writes)
	return benchRow{scenario: "Overlapping fan-out children", arena: arena, serial: scoreNaive(naiveCheckout(t, writers, nil, false), writes), concurrent: &concurrent}
}

// The user edits the checkout after the proposals exist. Integration must
// refuse while the checkout is dirty, never touch the edit, and, once the
// edit is committed, conflict only where the lines overlap.
func benchUserEditBeforeIntegrate(t *testing.T) benchRow {
	writers := []benchWriter{
		{"writer-a", []benchEdit{{"notes.txt", 5, "A: use port 8080"}, {"a.txt", 0, "A: new module"}}},
		{"writer-b", []benchEdit{{"notes.txt", 20, "B: use tls"}, {"b.txt", 0, "B: new module"}}},
	}
	user := benchWriter{"user", []benchEdit{{"notes.txt", 5, "USER: use port 9090"}}}
	writes := benchWrites(append(writers, user)...)
	p, r := benchRun(t, benchIDs(writers), benchBackends[0].runtime)
	changes := benchExecute(t, p, r, benchBackends[0].invoker(t, writers))
	if err := applyBenchEdits(p.Root, user.edits); err != nil {
		t.Fatal(err)
	}
	dirty := readBench(p.Root, "notes.txt")
	for _, w := range writers {
		message, state := benchRefused(t, p, r, changes[w.id])
		if state != "proposed" || !strings.Contains(message, "user changes") {
			t.Fatalf("%s: a dirty target should refuse and keep the proposal pending, got %q (%s)", w.id, state, message)
		}
	}
	if readBench(p.Root, "notes.txt") != dirty {
		t.Fatal("refused integration touched the user's uncommitted edit")
	}
	benchCommit(t, p.Root, "user edit", "notes.txt")
	if _, state := benchRefused(t, p, r, changes["writer-a"]); state != "conflicted" {
		t.Fatalf("overlapping proposal should conflict with the committed edit, got %s", state)
	}
	benchIntegrate(t, p, r, changes["writer-b"])
	arena := scoreArena(t, p, r, writes)
	if arena.preserved != 2 {
		t.Fatalf("writer-a's refused change should be preserved: %+v", arena)
	}
	// Naively the agents wrote straight into the checkout, then the user edited it.
	concurrent := scoreNaive(naiveCheckout(t, writers, []benchWriter{user}, true), writes)
	return benchRow{scenario: "User edit between proposal and integrate", arena: arena, serial: scoreNaive(naiveCheckout(t, writers, []benchWriter{user}, false), writes), concurrent: &concurrent}
}

// rollbackScenario integrates two writers, then a later commit lands, then
// the first writer's change is rolled back.
func rollbackScenario(t *testing.T, later benchWriter) (*Project, Run, Change, []benchWriter, []benchWrite) {
	t.Helper()
	writers := []benchWriter{
		{"writer-a", []benchEdit{{"notes.txt", 8, "A: pin dependency v1"}, {"a.txt", 0, "A: new module"}}},
		{"writer-b", []benchEdit{{"notes.txt", 20, "B: enable cache"}, {"b.txt", 0, "B: new module"}}},
	}
	// writer-a's own edits are meant to go away; what must survive is everything else.
	survivors := benchWrites(writers[1], later)
	p, r := benchRun(t, benchIDs(writers), benchBackends[0].runtime)
	changes := benchExecute(t, p, r, benchBackends[0].invoker(t, writers))
	first := benchIntegrate(t, p, r, changes["writer-a"])
	benchIntegrate(t, p, r, changes["writer-b"])
	if err := applyBenchEdits(p.Root, later.edits); err != nil {
		t.Fatal(err)
	}
	benchCommit(t, p.Root, "later edit", "notes.txt")
	return p, r, first, writers, survivors
}

func benchRollbackDisjointEdit(t *testing.T) benchRow {
	later := benchWriter{"user", []benchEdit{{"notes.txt", 26, "LATER: bump release to 2.0"}}}
	p, r, first, writers, survivors := rollbackScenario(t, later)
	head, err := Git(p.Root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	rollback, err := p.Rollback(r.ID, first.ID, true)
	if err != nil || rollback.State != "integrated" {
		t.Fatalf("rollback: %v (%s)", err, rollback.State)
	}
	// A revert commit on top of the later work: history only grows.
	if parent, err := Git(p.Root, "rev-parse", "HEAD~1"); err != nil || parent != head {
		t.Fatalf("rollback is not one new commit on the previous head: %s vs %s %v", parent, head, err)
	}
	if _, err := Git(p.Root, "merge-base", "--is-ancestor", first.IntegrationCommit, "HEAD"); err != nil {
		t.Fatalf("rollback rewrote history: %v", err)
	}
	if benchRolledBack := (benchEdit{"notes.txt", 8, "A: pin dependency v1"}); benchRolledBack.presentIn(readBench(p.Root, "notes.txt")) {
		t.Fatal("the rolled-back edit is still present")
	}
	arena := scoreArena(t, p, r, survivors)
	// The rollback is itself reversible.
	if _, err := p.Rollback(r.ID, rollback.ID, true); err != nil {
		t.Fatal(err)
	}
	for _, e := range append(writers[0].edits, later.edits...) {
		if !e.presentIn(readBench(p.Root, e.path)) {
			t.Fatalf("undoing the rollback did not restore %v", e)
		}
	}
	return benchRow{scenario: "Rollback, then a disjoint later edit", arena: arena, serial: rollbackNaive(t, writers, later, survivors, false), concurrent: rollbackNaivePtr(t, writers, later, survivors)}
}

func benchRollbackOverlappingEdit(t *testing.T) benchRow {
	later := benchWriter{"user", []benchEdit{{"notes.txt", 8, "LATER: pin dependency v2"}}}
	p, r, first, writers, survivors := rollbackScenario(t, later)
	head, err := Git(p.Root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	rollback, err := p.Rollback(r.ID, first.ID, true)
	if err == nil || rollback.State != "conflicted" || !strings.Contains(err.Error(), "preserved at") {
		t.Fatalf("a rollback that conflicts with later work should be preserved, got %v (%s)", err, rollback.State)
	}
	if info, statErr := os.Stat(rollback.Workspace); statErr != nil || !info.IsDir() {
		t.Fatalf("recovery worktree missing: %v", statErr)
	}
	if after, gitErr := Git(p.Root, "rev-parse", "HEAD"); gitErr != nil || after != head {
		t.Fatalf("the user's checkout moved: %s -> %s %v", head, after, gitErr)
	}
	if status, gitErr := Git(p.Root, "status", "--porcelain=v1"); gitErr != nil || status != "" {
		t.Fatalf("checkout is dirty after a conflicting rollback: %q %v", status, gitErr)
	}
	events, err := p.Events(r.ID)
	if err != nil || countEvents(events, "rollback.created", "user") != 1 {
		t.Fatalf("rollback was not journaled: %v", err)
	}
	arena := scoreArena(t, p, r, survivors)
	return benchRow{scenario: "Rollback, then an overlapping later edit", arena: arena, serial: rollbackNaive(t, writers, later, survivors, false), concurrent: rollbackNaivePtr(t, writers, later, survivors)}
}

// rollbackNaive replays the same writes in a shared checkout and rolls the
// first writer back by restoring the files it touched.
func rollbackNaive(t *testing.T, writers []benchWriter, later benchWriter, survivors []benchWrite, concurrent bool) benchCount {
	t.Helper()
	dir := naiveCheckout(t, writers, []benchWriter{later}, concurrent)
	paths := []string{}
	for _, e := range writers[0].edits {
		paths = append(paths, e.path)
	}
	naiveRestore(t, dir, paths...)
	return scoreNaive(dir, survivors)
}

func rollbackNaivePtr(t *testing.T, writers []benchWriter, later benchWriter, survivors []benchWrite) *benchCount {
	n := rollbackNaive(t, writers, later, survivors, true)
	return &n
}

// A revision loop: the builder runs again in a new worktree built on its first
// proposal. Both proposals stay, the second depends on the first, and they
// integrate in that order.
func benchLoopIteration(t *testing.T) benchRow {
	edits := func(pass int) []benchEdit {
		return []benchEdit{{"base.txt", pass + 1, fmt.Sprintf("iteration %d: revise handler", pass)}, {fmt.Sprintf("pass%d.txt", pass), 0, "builder output"}}
	}
	builder := []benchWriter{{"builder", edits(1)}, {"builder", edits(2)}}
	writes := benchWrites(builder...)
	p, r := loopRun(t, 2)
	benchCommit(t, p.Root, "workflow", ".arena")
	fixture := &loopFixture{verdicts: func(pass int) string {
		if pass == 1 {
			return "ChangesRequested"
		}
		return "Approved"
	}, ran: map[string]int{}}
	before, err := Git(p.Root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	invoke := testInvoker(func(ctx context.Context, in Invocation) (map[string]any, error) {
		if in.AgentID == "builder" {
			pass := 1
			if _, n, ok := strings.Cut(filepath.Base(in.Dir), "-iter"); ok {
				fmt.Sscan(n, &pass)
			}
			if err := applyBenchEdits(in.Dir, edits(pass)[:1]); err != nil {
				return nil, err
			}
		}
		return fixture.invoke(ctx, in)
	})
	if err := NewEngine(p, r, invoke).Execute(ctx, false, false); err != nil {
		t.Fatal(err)
	}
	benchCheckoutUntouched(t, p, before)
	var first, second Change
	for _, c := range benchChangeFor(t, p, r, "builder") {
		if len(c.DependsOn) == 0 {
			first = c
		} else {
			second = c
		}
	}
	if first.ID == "" || second.ID == "" || !Has(second.DependsOn, first.ID) {
		t.Fatalf("the revision does not build on the first proposal: %+v %+v", first, second)
	}
	// The revision cannot land ahead of the proposal it was built on.
	if message, state := benchRefused(t, p, r, second); state != "proposed" || !strings.Contains(message, "prerequisite") {
		t.Fatalf("dependent change integrated out of order: %q (%s)", message, state)
	}
	benchIntegrate(t, p, r, first)
	benchIntegrate(t, p, r, second)
	arena := scoreArena(t, p, r, writes)
	// Iterations are sequential by nature, so there is no concurrent baseline.
	return benchRow{scenario: "Loop iteration (revision builds on the first pass)", arena: arena, serial: scoreNaive(naiveCheckout(t, builder, nil, false), writes)}
}
