package arena

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testSecret = "sk-abcdefghijklmnop1234567890"

// auditExportRun builds a run with a proposed change, a retained packet and a
// journaled secret under the given retention.
func auditExportRun(t *testing.T, retention string) (*Project, Run) {
	t.Helper()
	p := committedProject(t)
	s := DefaultSettings(Runtime{Provider: "codex", Model: "explicit-test-model", ReasoningEffort: "high"})
	s.Audit.Retention, s.Audit.ProcessOutput = retention, "full"
	if _, err := p.Commit("retention", map[string][]byte{"settings.arena.json": JSON(s)}); err != nil {
		t.Fatal(err)
	}
	r := testRun(t, p)
	w, err := p.Workspace(r, "one", "one", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(w.Path, "one.txt"), []byte("one\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := p.FinishWorkspace(r, w); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Append(r.ID, r.Workflow.Id, "head", "process.output", map[string]any{"bytes": 9, "output": "token " + testSecret}); err != nil {
		t.Fatal(err)
	}
	if err := Atomic(filepath.Join(p.RunDir(r.ID), "messages", "message-aaaa.json"), SafeJSON(map[string]any{"messageId": "message-aaaa", "text": "hello " + testSecret}), 0600); err != nil {
		t.Fatal(err)
	}
	return p, r
}

func readBundle(t *testing.T, path string) auditBundle {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var bundle auditBundle
	if err := json.Unmarshal(b, &bundle); err != nil {
		t.Fatal(err)
	}
	return bundle
}

// writeBundle re-seals a hand-edited bundle: file entries and checksum follow
// the edit, so only the check under test can object.
func writeBundle(t *testing.T, b auditBundle, reseal bool) string {
	t.Helper()
	if reseal {
		b.Manifest.Files = nil
		for _, f := range b.Files {
			b.Manifest.Files = append(b.Manifest.Files, AuditFileEntry{Path: f.Path, Size: len(f.Content), SHA256: sha256Hex([]byte(f.Content))})
		}
		b.Checksum = auditChecksum(b.Manifest)
	}
	path := filepath.Join(t.TempDir(), "bundle.json")
	data, err := json.Marshal(b)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func bundleFile(b *auditBundle, path string) *auditFile {
	for i := range b.Files {
		if b.Files[i].Path == path {
			return &b.Files[i]
		}
	}
	return nil
}

func exportTo(t *testing.T, p *Project, id string) string {
	t.Helper()
	dest := filepath.Join(t.TempDir(), "run.audit.json")
	if _, err := p.ExportAudit(id, dest); err != nil {
		t.Fatal(err)
	}
	return dest
}

func TestAuditExportImportRoundTrip(t *testing.T) {
	p, r := auditExportRun(t, "full-local")
	dest := exportTo(t, p, r.ID)
	raw, _ := os.ReadFile(dest)
	if bytes.Contains(raw, []byte(testSecret)) {
		t.Fatal("bundle holds a secret")
	}
	bundle := readBundle(t, dest)
	var paths []string
	for _, f := range bundle.Files {
		paths = append(paths, f.Path)
	}
	joined := strings.Join(paths, " ")
	for _, want := range []string{"events.ndjson", "packets/message-aaaa.json", ".patch", "changes/", "checkpoints/"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("bundle lacks %s: %s", want, joined)
		}
	}
	for _, banned := range []string{"stagedDiff", "workingDiff", "settings.arena", "run.json", "baseline.json"} {
		if bytes.Contains(raw, []byte(banned)) {
			t.Fatalf("bundle holds %s", banned)
		}
	}

	// A second project: its worktrees and live state must not change.
	q := committedProject(t)
	before, _ := Git(q.Root, "worktree", "list", "--porcelain")
	var out bytes.Buffer
	cli := NewCLI(strings.NewReader(""), &out, io.Discard)
	if err := cli.Run(context.Background(), []string{"history", "import", dest, "--project", q.Root}); err != nil {
		t.Fatal(err)
	}
	if after, _ := Git(q.Root, "worktree", "list", "--porcelain"); after != before {
		t.Fatal("import touched worktrees")
	}
	dir := q.importedDir(r.ID)
	for _, f := range bundle.Files {
		info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(f.Path)))
		if err != nil || info.Mode().Perm() != 0400 {
			t.Fatalf("%s not landed read-only: %v %v", f.Path, info, err)
		}
	}
	want, _ := p.Events(r.ID)
	got, err := q.ImportedEvents(r.ID)
	if err != nil || len(got) != len(want) || got[len(got)-1].Hash != want[len(want)-1].Hash {
		t.Fatalf("imported events differ: %v", err)
	}
	// Not live state, not resumable.
	if _, err := q.ReadRun(r.ID); err == nil {
		t.Fatal("imported run is readable as a live run")
	}
	if ids, _ := q.Runs(); len(ids) != 0 {
		t.Fatalf("imported run listed as live: %v", ids)
	}
	if _, err := os.Stat(q.RunDir(r.ID)); !os.IsNotExist(err) {
		t.Fatal("import created live run state")
	}
	if err := cli.Run(context.Background(), []string{"resume", r.ID, "--project", q.Root}); err == nil {
		t.Fatal("imported run resumed")
	}
	if events, _ := q.Events("project"); events[len(events)-1].Type != "history.imported" {
		t.Fatalf("import not journaled: %+v", events)
	}
	out.Reset()
	if err := cli.Run(context.Background(), []string{"history", "--json", "--project", q.Root}); err != nil {
		t.Fatal(err)
	}
	var list []HistoryEntry
	if err := json.Unmarshal(out.Bytes(), &list); err != nil || len(list) != 1 || list[0] != (HistoryEntry{ID: r.ID, Imported: true}) {
		t.Fatalf("history list: %s %v", out.String(), err)
	}
	out.Reset()
	if err := cli.Run(context.Background(), []string{"history", r.ID, "--json", "--project", q.Root}); err != nil {
		t.Fatal(err)
	}
	var h RunHistory
	if err := json.Unmarshal(out.Bytes(), &h); err != nil || !h.Imported || len(h.Events) != len(want) {
		t.Fatalf("imported history: %s %v", out.String(), err)
	}
	// The live project lists its own run as not imported; the plain list is unchanged.
	out.Reset()
	if err := cli.Run(context.Background(), []string{"history", "--json", "--project", p.Root}); err != nil || !strings.Contains(out.String(), `"imported": false`) && !strings.Contains(out.String(), `"imported":false`) {
		t.Fatalf("live list: %s %v", out.String(), err)
	}
	out.Reset()
	if err := cli.Run(context.Background(), []string{"history", "--project", q.Root}); err != nil || strings.Contains(out.String(), r.ID) {
		t.Fatalf("plain list changed: %s %v", out.String(), err)
	}
}

func TestAuditImportRefusesTamperedEvent(t *testing.T) {
	p, r := auditExportRun(t, "full-local")
	bundle := readBundle(t, exportTo(t, p, r.ID))
	q := committedProject(t)
	// Edited without resealing: the manifest digest objects.
	edited := bundle
	edited.Files = append([]auditFile(nil), bundle.Files...)
	bundleFile(&edited, "events.ndjson").Content = strings.Replace(bundleFile(&edited, "events.ndjson").Content, `"head"`, `"evil"`, 1)
	if _, err := q.ImportAudit(writeBundle(t, edited, false)); err == nil || !strings.Contains(err.Error(), "does not match the manifest") {
		t.Fatalf("unsealed tamper: %v", err)
	}
	// Edited and resealed: the hash chain objects.
	if _, err := q.ImportAudit(writeBundle(t, edited, true)); err == nil || !strings.Contains(err.Error(), "journal chain") {
		t.Fatalf("resealed tamper: %v", err)
	}
	// A dropped event with a consistent manifest still fails the chain or the count.
	short := bundle
	short.Files = append([]auditFile(nil), bundle.Files...)
	lines := strings.SplitAfter(bundleFile(&short, "events.ndjson").Content, "\n")
	bundleFile(&short, "events.ndjson").Content = strings.Join(append(lines[:1], lines[2:]...), "")
	if _, err := q.ImportAudit(writeBundle(t, short, true)); err == nil {
		t.Fatal("imported a journal with a missing event")
	}
	if ids, _ := q.ImportedRuns(); len(ids) != 0 {
		t.Fatalf("a refused bundle landed: %v", ids)
	}
}

func TestAuditImportRefusesTamperedManifest(t *testing.T) {
	p, r := auditExportRun(t, "full-local")
	bundle := readBundle(t, exportTo(t, p, r.ID))
	q := committedProject(t)
	for name, edit := range map[string]func(*auditBundle){
		"run id":     func(b *auditBundle) { b.Manifest.RunID = "run-0000000000000000" },
		"retention":  func(b *auditBundle) { b.Manifest.Retention = "redacted-local" },
		"count":      func(b *auditBundle) { b.Manifest.EventCount++ },
		"file size":  func(b *auditBundle) { b.Manifest.Files[0].Size++ },
		"file hash":  func(b *auditBundle) { b.Manifest.Files[0].SHA256 = strings.Repeat("0", 64) },
		"checksum":   func(b *auditBundle) { b.Checksum = strings.Repeat("0", 64) },
		"drop entry": func(b *auditBundle) { b.Manifest.Files = b.Manifest.Files[1:] },
	} {
		edited := bundle
		edited.Manifest.Files = append([]AuditFileEntry(nil), bundle.Manifest.Files...)
		edit(&edited)
		if _, err := q.ImportAudit(writeBundle(t, edited, false)); err == nil {
			t.Fatalf("%s: tampered manifest imported", name)
		}
	}
	if _, err := q.ImportAudit(writeBundle(t, bundle, false)); err != nil {
		t.Fatalf("control import: %v", err)
	}
	q = committedProject(t)
	// A manifest that was sealed consistently but lies about the journal.
	edited := bundle
	edited.Manifest.LastEventHash = strings.Repeat("a", 64)
	edited.Checksum = auditChecksum(edited.Manifest)
	if _, err := q.ImportAudit(writeBundle(t, edited, false)); err == nil || !strings.Contains(err.Error(), "does not match the manifest") {
		t.Fatalf("lying manifest: %v", err)
	}
	if ids, _ := q.ImportedRuns(); len(ids) != 0 {
		t.Fatalf("a refused bundle landed: %v", ids)
	}
}

func TestAuditImportRefusesExistingRun(t *testing.T) {
	p, r := auditExportRun(t, "full-local")
	dest := exportTo(t, p, r.ID)
	// The live run it came from.
	if _, err := p.ImportAudit(dest); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("imported over a live run: %v", err)
	}
	live, _ := p.Events(r.ID)
	// Twice into another project.
	q := committedProject(t)
	if _, err := q.ImportAudit(dest); err != nil {
		t.Fatal(err)
	}
	first, _ := os.ReadFile(filepath.Join(q.importedDir(r.ID), "manifest.json"))
	if _, err := q.ImportAudit(dest); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("imported twice: %v", err)
	}
	if again, _ := os.ReadFile(filepath.Join(q.importedDir(r.ID), "manifest.json")); !bytes.Equal(first, again) {
		t.Fatal("a refused import changed the earlier one")
	}
	// A retired run keeps its ID reserved.
	if err := p.PruneRun(r.ID, true); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ImportAudit(dest); err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("imported over retired history: %v", err)
	}
	retired, _ := p.retiredDirs(r.ID)
	if got, err := os.ReadFile(filepath.Join(retired[0], "events.ndjson")); err != nil || !bytes.Contains(got, []byte(live[0].Hash)) {
		t.Fatalf("retired history disturbed: %v", err)
	}
}

func TestAuditExportHonorsRedactedRetention(t *testing.T) {
	p, r := auditExportRun(t, "redacted-local")
	if r.Retention != "redacted-local" {
		t.Fatalf("fixture retention %s", r.Retention)
	}
	dest := exportTo(t, p, r.ID)
	raw, _ := os.ReadFile(dest)
	bundle := readBundle(t, dest)
	if bundle.Manifest.Retention != "redacted-local" || strings.Join(bundle.Manifest.Omitted, ",") != "packets,patches" {
		t.Fatalf("manifest %+v", bundle.Manifest)
	}
	for _, f := range bundle.Files {
		if strings.HasPrefix(f.Path, "packets/") || strings.HasSuffix(f.Path, ".patch") {
			t.Fatalf("bundle keeps %s the retention excluded", f.Path)
		}
	}
	if bytes.Contains(raw, []byte(testSecret)) || bytes.Contains(raw, []byte("token ")) || bytes.Contains(raw, []byte("hello")) {
		t.Fatal("bundle holds payload text the retention excluded")
	}
	if !strings.Contains(bundleFile(&bundle, "events.ndjson").Content, "outputOmitted") {
		t.Fatal("journal does not show the omitted output")
	}
	if _, err := committedProject(t).ImportAudit(dest); err != nil {
		t.Fatal(err)
	}

	// An export is refused when the journal keeps output the retention excluded,
	// and so is an import of such a bundle, even with a valid chain and seal.
	events := signedEvents(t, "run-aaaaaaaaaaaaaaaa", "redacted-local", map[string]any{"output": "raw"})
	forged := auditBundle{Format: auditFormat, Manifest: AuditManifest{RunID: "run-aaaaaaaaaaaaaaaa", WorkflowID: "wf", ExportedAt: Now(), Retention: "redacted-local", ProcessOutput: "none", EventCount: 2, LastEventHash: events[1].Hash, Omitted: []string{"packets", "patches"}}, Files: []auditFile{{Path: "events.ndjson", Encoding: "utf8", Content: ndjson(t, events)}}}
	if _, err := committedProject(t).ImportAudit(writeBundle(t, forged, true)); err == nil || !strings.Contains(err.Error(), "raw process output") {
		t.Fatalf("forged output: %v", err)
	}
	// And a reduced-retention bundle may not smuggle packet bodies.
	forged.Manifest.ProcessOutput = "none"
	forged.Files = append(forged.Files, auditFile{Path: "packets/message-x.json", Encoding: "utf8", Content: `{}`})
	forged.Files[0].Content = ndjson(t, signedEvents(t, "run-aaaaaaaaaaaaaaaa", "redacted-local", map[string]any{"outputHash": "x"}))
	if _, err := committedProject(t).ImportAudit(writeBundle(t, forged, true)); err == nil {
		t.Fatal("imported packet bodies for a reduced-retention run")
	}
}

// signedEvents builds a valid two-event chain for hand-made bundles.
func signedEvents(t *testing.T, run, retention string, output map[string]any) []Event {
	t.Helper()
	events := []Event{}
	previous := ""
	for i, kind := range []string{"workflow.created", "process.output"} {
		data := map[string]any{"retention": retention}
		if i == 1 {
			data = output
		}
		e := Event{Version: 1, Sequence: i + 1, ID: ID("event-"), At: Now(), RunID: run, WorkflowID: "wf", Type: kind, Data: data, PreviousHash: previous}
		e.Hash = Hash(JSON(e))
		var decoded Event
		if err := DecodeJSON(JSON(e), &decoded); err != nil {
			t.Fatal(err)
		}
		events = append(events, decoded)
		previous = e.Hash
	}
	return events
}
func ndjson(t *testing.T, events []Event) string {
	var b strings.Builder
	for _, e := range events {
		line, err := json.Marshal(e)
		if err != nil {
			t.Fatal(err)
		}
		b.Write(line)
		b.WriteByte('\n')
	}
	return b.String()
}

func TestAuditImportRefusesOversizedAndMalformedBundles(t *testing.T) {
	p, r := auditExportRun(t, "full-local")
	dest := exportTo(t, p, r.ID)
	good := readBundle(t, dest)
	q := committedProject(t)
	raw, _ := os.ReadFile(dest)

	small := maxAuditBundle
	t.Cleanup(func() { maxAuditBundle = small })
	maxAuditBundle = int64(len(raw)) - 1
	if _, err := q.ImportAudit(dest); err == nil || !strings.Contains(err.Error(), "limited to") {
		t.Fatalf("oversized import: %v", err)
	}
	if _, err := p.ExportAudit(r.ID, filepath.Join(t.TempDir(), "big.json")); err == nil {
		t.Fatal("oversized export written")
	}
	maxAuditBundle = small

	write := func(name string, data []byte) string {
		path := filepath.Join(t.TempDir(), name)
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	withFile := func(f auditFile, listed bool) string {
		b := good
		b.Files = append(append([]auditFile(nil), good.Files...), f)
		if !listed {
			return writeBundle(t, b, false)
		}
		return writeBundle(t, b, true)
	}
	cases := map[string]string{
		"not json":       write("a.json", []byte("run history")),
		"empty":          write("b.json", nil),
		"truncated":      write("c.json", raw[:len(raw)/2]),
		"trailing data":  write("d.json", append(append([]byte(nil), raw...), []byte(" {}")...)),
		"unknown field":  write("e.json", bytes.Replace(raw, []byte(`"format"`), []byte(`"extra":1,"format"`), 1)),
		"duplicate key":  write("f.json", bytes.Replace(raw, []byte(`"format"`), []byte(`"format":"arena.audit.v1","format"`), 1)),
		"wrong format":   write("g.json", bytes.Replace(raw, []byte(`arena.audit.v1`), []byte(`arena.audit.v2`), 1)),
		"traversal":      withFile(auditFile{Path: "../../settings.arena.json", Encoding: "utf8", Content: "{}"}, true),
		"absolute path":  withFile(auditFile{Path: "/tmp/x.json", Encoding: "utf8", Content: "{}"}, true),
		"foreign path":   withFile(auditFile{Path: "run.json", Encoding: "utf8", Content: "{}"}, true),
		"bad base64":     withFile(auditFile{Path: "changes/c.patch", Encoding: "base64", Content: "!!not base64!!"}, true),
		"unlisted file":  withFile(auditFile{Path: "changes/c.patch", Encoding: "utf8", Content: "x"}, false),
		"duplicate file": withFile(good.Files[0], true),
		"directory":      t.TempDir(),
		"missing file":   filepath.Join(t.TempDir(), "nope.json"),
	}
	link := filepath.Join(t.TempDir(), "link.json")
	if err := os.Symlink(dest, link); err == nil {
		cases["symlink"] = link
	}
	noEvents := good
	noEvents.Files = nil
	for _, f := range good.Files {
		if f.Path != "events.ndjson" {
			noEvents.Files = append(noEvents.Files, f)
		}
	}
	cases["no journal"] = writeBundle(t, noEvents, true)
	for name, path := range cases {
		if _, err := q.ImportAudit(path); err == nil {
			t.Errorf("%s: imported", name)
		}
	}
	if ids, _ := q.ImportedRuns(); len(ids) != 0 {
		t.Fatalf("a refused bundle landed: %v", ids)
	}
	if entries, _ := os.ReadDir(q.importedRoot()); len(entries) != 0 {
		t.Fatalf("staging left behind: %v", entries)
	}
	// Control: the untouched bundle imports into the same project.
	if _, err := q.ImportAudit(dest); err != nil {
		t.Fatalf("control import: %v", err)
	}
}

func TestAuditExportRefusesOverwriteAndUnknownRun(t *testing.T) {
	p, r := auditExportRun(t, "full-local")
	dest := filepath.Join(t.TempDir(), "keep.json")
	if err := os.WriteFile(dest, []byte("mine"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.ExportAudit(r.ID, dest); err == nil {
		t.Fatal("overwrote an existing file")
	}
	if got, _ := os.ReadFile(dest); string(got) != "mine" {
		t.Fatal("existing file changed")
	}
	for _, id := range []string{"project", "../x", "run-ffffffffffffffff"} {
		if _, err := p.ExportAudit(id, filepath.Join(t.TempDir(), "x.json")); err == nil {
			t.Fatalf("exported %s", id)
		}
	}
}
