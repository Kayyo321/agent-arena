package arena

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	c "github.com/Kayyo321/agent-arena/internal/contract"
)

// auditFormat names the bundle `arena history RUN export FILE` writes; the
// contract is schemas/audit-bundle.schema.json.
const auditFormat = "arena.audit.v1"

// maxAuditBundle caps the bundle file and the decoded files inside it. It is a
// variable so tests can exercise the limit without a quarter-gigabyte file.
var maxAuditBundle int64 = 256 << 20

// auditPath is every path an import will write; anything else is refused, so a
// bundle cannot name a location outside its own directory.
var auditPath = regexp.MustCompile(`^(?:events\.ndjson|(?:packets|changes|checkpoints)/[a-z0-9][a-z0-9._-]{0,63}\.(?:json|patch))$`)

type AuditFileEntry struct {
	Path   string `json:"path"`
	Size   int    `json:"size"`
	SHA256 string `json:"sha256"`
}
type AuditManifest struct {
	RunID         string           `json:"runId"`
	WorkflowID    string           `json:"workflowId"`
	ExportedAt    string           `json:"exportedAt"`
	Retention     string           `json:"retention"`
	ProcessOutput string           `json:"processOutput"`
	EventCount    int              `json:"eventCount"`
	LastEventHash string           `json:"lastEventHash"`
	Files         []AuditFileEntry `json:"files"`
	Omitted       []string         `json:"omitted"`
}
type auditFile struct {
	Path     string `json:"path"`
	Encoding string `json:"encoding"`
	Content  string `json:"content"`
}
type auditBundle struct {
	Format   string        `json:"format"`
	Manifest AuditManifest `json:"manifest"`
	Checksum string        `json:"checksum"`
	Files    []auditFile   `json:"files"`
}

// auditCheckpoint is the checkpoint metadata a bundle carries. The staged and
// working diffs, the porcelain status and the local directory stay behind.
type auditCheckpoint struct {
	ID        string `json:"id"`
	Commit    string `json:"commit"`
	Head      string `json:"head"`
	Tree      string `json:"tree"`
	CreatedAt string `json:"createdAt"`
	Reason    string `json:"reason"`
}

func sha256Hex(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }

// auditChecksum covers the run, its retention, the journal head and every
// file's size and digest, so changing any of them changes the checksum.
func auditChecksum(m AuditManifest) string {
	files := append([]AuditFileEntry(nil), m.Files...)
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	var b strings.Builder
	fmt.Fprintf(&b, "%s\nrun %s\nretention %s\nevents %d %s\n", auditFormat, m.RunID, m.Retention, m.EventCount, m.LastEventHash)
	for _, f := range files {
		fmt.Fprintf(&b, "%s %d %s\n", f.SHA256, f.Size, f.Path)
	}
	return sha256Hex([]byte(b.String()))
}

// keepsBodies is whether the run's retention kept packet and patch bodies.
func keepsBodies(retention string) bool { return retention == "full-local" }

func readAuditSource(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if info.Size() > maxAuditBundle {
		return nil, fmt.Errorf("%s is %d MiB; audit files are limited to %d MiB", path, info.Size()>>20, maxAuditBundle>>20)
	}
	return os.ReadFile(path)
}

// ExportAudit writes one run's redacted audit trail to dest, which must not
// exist. It reads only what Arena already redacted and what the run's
// retention kept.
func (p *Project) ExportAudit(id, dest string) (AuditManifest, error) {
	var m AuditManifest
	if err := validRunID(id); err != nil {
		return m, err
	}
	if _, err := os.Lstat(dest); err == nil {
		return m, fmt.Errorf("%s already exists; export does not overwrite", dest)
	}
	r, err := p.ReadRun(id)
	if err != nil {
		return m, err
	}
	events, err := p.Events(id)
	if err != nil {
		return m, err
	}
	if len(events) == 0 {
		return m, fmt.Errorf("run %s has no journal events", id)
	}
	keepsOutput := r.Retention == "full-local" && r.Settings.Audit.ProcessOutput == "full"
	var journal bytes.Buffer
	for _, e := range events {
		// Retention was applied when the event was appended and the hash chain
		// forbids editing it now, so an event that kept raw output the run
		// excluded is refused rather than exported.
		if _, has := e.Data["output"]; has && e.Type == "process.output" && !keepsOutput {
			return m, fmt.Errorf("event %d keeps raw process output that this run's retention excluded; refusing to export", e.Sequence)
		}
		line, err := json.Marshal(e)
		if err != nil {
			return m, err
		}
		journal.Write(line)
		journal.WriteByte('\n')
	}
	files := map[string][]byte{"events.ndjson": journal.Bytes()}
	omitted := []string{}
	changes, err := p.Changes(id)
	if err != nil {
		return m, err
	}
	checkpoints := map[string]bool{}
	var baseline Checkpoint
	if ReadJSON(filepath.Join(p.RunDir(id), "baseline.json"), &baseline) == nil {
		checkpoints[baseline.ID] = true
	}
	for _, e := range events {
		if e.Type == "checkpoint.created" {
			checkpoints[str(e.Data["checkpointId"])] = true
		}
	}
	for _, change := range changes {
		if err := validID(change.ID); err != nil {
			return m, err
		}
		files["changes/"+change.ID+".json"] = SafeJSON(change)
		if change.Checkpoint != "" {
			checkpoints[change.Checkpoint] = true
		}
		if !keepsBodies(r.Retention) {
			continue
		}
		patch, err := os.ReadFile(filepath.Join(p.RunDir(id), "changes", change.ID+".patch"))
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return m, err
		}
		files["changes/"+change.ID+".patch"] = []byte(Redact(string(patch)))
	}
	if keepsBodies(r.Retention) {
		names, err := filepath.Glob(filepath.Join(p.RunDir(id), "messages", "*.json"))
		if err != nil {
			return m, err
		}
		for _, name := range names {
			packet, err := readAuditSource(name)
			if err != nil {
				return m, err
			}
			files["packets/"+filepath.Base(name)] = packet
		}
	} else {
		omitted = append(omitted, "packets", "patches")
	}
	for checkpoint := range checkpoints {
		var full Checkpoint
		if validID(checkpoint) != nil || ReadJSON(p.Path(".state", "checkpoints", checkpoint+".json"), &full) != nil {
			continue
		}
		files["checkpoints/"+checkpoint+".json"] = SafeJSON(auditCheckpoint{full.ID, full.Commit, full.Head, full.Tree, full.CreatedAt, full.Reason})
	}
	m = AuditManifest{RunID: id, WorkflowID: events[0].WorkflowID, ExportedAt: Now(), Retention: r.Retention, ProcessOutput: r.Settings.Audit.ProcessOutput, EventCount: len(events), LastEventHash: events[len(events)-1].Hash, Files: []AuditFileEntry{}, Omitted: omitted}
	paths := make([]string, 0, len(files))
	for path := range files {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	bundle := auditBundle{Format: auditFormat, Files: []auditFile{}}
	for _, path := range paths {
		body := files[path]
		if !auditPath.MatchString(path) {
			return m, fmt.Errorf("refusing to export unexpected path %q", path)
		}
		m.Files = append(m.Files, AuditFileEntry{Path: path, Size: len(body), SHA256: sha256Hex(body)})
		f := auditFile{Path: path, Encoding: "utf8", Content: string(body)}
		if !utf8.Valid(body) {
			f.Encoding, f.Content = "base64", base64.StdEncoding.EncodeToString(body)
		}
		bundle.Files = append(bundle.Files, f)
	}
	bundle.Manifest, bundle.Checksum = m, auditChecksum(m)
	data, err := json.Marshal(bundle)
	if err != nil {
		return m, err
	}
	if int64(len(data)) > maxAuditBundle {
		return m, fmt.Errorf("bundle would be %d MiB; audit files are limited to %d MiB", len(data)>>20, maxAuditBundle>>20)
	}
	if err := Atomic(dest, data, 0600); err != nil {
		return m, err
	}
	return m, nil
}

func validRunID(id string) error {
	if err := validID(id); err != nil {
		return err
	}
	if !strings.HasPrefix(id, "run-") {
		return fmt.Errorf("%q is not a run ID", id)
	}
	return nil
}

// verifyAuditBundle checks everything an import trusts: the schema, the
// manifest against the files, the checksum, and the journal's hash chain. It
// returns the decoded files and events.
func verifyAuditBundle(data []byte) (auditBundle, map[string][]byte, []Event, error) {
	var b auditBundle
	if err := c.ValidateAuditBundle(data); err != nil {
		return b, nil, nil, fmt.Errorf("not a valid audit bundle: %w", err)
	}
	if err := DecodeJSON(data, &b); err != nil {
		return b, nil, nil, err
	}
	m := b.Manifest
	if b.Format != auditFormat {
		return b, nil, nil, fmt.Errorf("unsupported bundle format %q", b.Format)
	}
	if err := validRunID(m.RunID); err != nil {
		return b, nil, nil, err
	}
	if len(b.Files) != len(m.Files) {
		return b, nil, nil, fmt.Errorf("manifest lists %d files but the bundle holds %d", len(m.Files), len(b.Files))
	}
	listed := map[string]AuditFileEntry{}
	for _, f := range m.Files {
		if _, dup := listed[f.Path]; dup || !auditPath.MatchString(f.Path) {
			return b, nil, nil, fmt.Errorf("manifest path %q is duplicated or not allowed", f.Path)
		}
		listed[f.Path] = f
	}
	files := map[string][]byte{}
	var total int64
	for _, f := range b.Files {
		entry, ok := listed[f.Path]
		if !ok || files[f.Path] != nil {
			return b, nil, nil, fmt.Errorf("bundle file %q is not in the manifest or appears twice", f.Path)
		}
		body := []byte(f.Content)
		if f.Encoding == "base64" {
			var err error
			if body, err = base64.StdEncoding.Strict().DecodeString(f.Content); err != nil {
				return b, nil, nil, fmt.Errorf("%s: %w", f.Path, err)
			}
		}
		if total += int64(len(body)); total > maxAuditBundle {
			return b, nil, nil, fmt.Errorf("bundle holds more than %d MiB of files", maxAuditBundle>>20)
		}
		if len(body) != entry.Size || sha256Hex(body) != entry.SHA256 {
			return b, nil, nil, fmt.Errorf("%s does not match the manifest", f.Path)
		}
		files[f.Path] = body
	}
	if auditChecksum(m) != b.Checksum {
		return b, nil, nil, fmt.Errorf("bundle checksum does not match its manifest")
	}
	journal, ok := files["events.ndjson"]
	if !ok {
		return b, nil, nil, fmt.Errorf("bundle has no events.ndjson")
	}
	events, err := verifyJournalLines(journal, m.RunID)
	if err != nil {
		return b, nil, nil, err
	}
	if len(events) != m.EventCount || events[len(events)-1].Hash != m.LastEventHash || events[0].WorkflowID != m.WorkflowID {
		return b, nil, nil, fmt.Errorf("journal does not match the manifest")
	}
	if events[0].Type != "workflow.created" || events[0].Data["retention"] != m.Retention {
		return b, nil, nil, fmt.Errorf("journal does not start with the run's creation under the manifest's retention")
	}
	keepsOutput := m.Retention == "full-local" && m.ProcessOutput == "full"
	for _, e := range events {
		if _, has := e.Data["output"]; has && e.Type == "process.output" && !keepsOutput {
			return b, nil, nil, fmt.Errorf("event %d keeps raw process output that the run's retention excluded", e.Sequence)
		}
	}
	if !keepsBodies(m.Retention) {
		for path := range files {
			if strings.HasPrefix(path, "packets/") || strings.HasSuffix(path, ".patch") {
				return b, nil, nil, fmt.Errorf("%s is a body the run's retention excluded", path)
			}
		}
	}
	return b, files, events, nil
}

// verifyJournalLines replays an NDJSON journal against the same chain rules as
// eventsUnlocked and requires each line to be the canonical encoding of its
// event, which is what lets an import land the file unchanged.
func verifyJournalLines(journal []byte, run string) ([]Event, error) {
	if len(journal) == 0 || journal[len(journal)-1] != '\n' {
		return nil, fmt.Errorf("journal is empty or lacks a final newline")
	}
	events := []Event{}
	previous := ""
	for i, line := range bytes.Split(journal[:len(journal)-1], []byte{'\n'}) {
		var v Event
		if err := DecodeJSON(line, &v); err != nil {
			return nil, fmt.Errorf("journal line %d: %w", i+1, err)
		}
		hash := v.Hash
		v.Hash = ""
		if v.Version != 1 || v.Sequence != i+1 || v.PreviousHash != previous || v.RunID != run || Hash(JSON(v)) != hash {
			return nil, fmt.Errorf("invalid journal chain at event %d", i+1)
		}
		v.Hash = hash
		if canonical, err := json.Marshal(v); err != nil || !bytes.Equal(canonical, line) {
			return nil, fmt.Errorf("journal line %d is not canonical", i+1)
		}
		events = append(events, v)
		previous = hash
	}
	return events, nil
}

func (p *Project) importedRoot() string         { return p.Path(".state", "imported") }
func (p *Project) importedDir(id string) string { return filepath.Join(p.importedRoot(), id) }
func (p *Project) runExists(id string) (bool, error) {
	for _, pattern := range []string{p.RunDir(id), p.importedDir(id), filepath.Join(p.Path(".state", "retired"), id+"-*")} {
		matches, err := filepath.Glob(pattern)
		if err != nil {
			return false, err
		}
		if len(matches) > 0 {
			return true, nil
		}
	}
	return false, nil
}

// ImportAudit verifies a bundle and lands it read-only under
// .arena/.state/imported/RUN. It never creates run state, so the run cannot be
// resumed, and it touches neither worktrees nor the live journal.
func (p *Project) ImportAudit(src string) (AuditManifest, error) {
	var zero AuditManifest
	data, err := readAuditSource(src)
	if err != nil {
		return zero, err
	}
	bundle, files, _, err := verifyAuditBundle(data)
	if err != nil {
		return zero, err
	}
	m := bundle.Manifest
	lock, err := p.WaitLock("import-" + m.RunID)
	if err != nil {
		return zero, err
	}
	defer lock.Close()
	if exists, err := p.runExists(m.RunID); err != nil || exists {
		return zero, errOr(err, fmt.Errorf("run %s already exists here (live, retired or imported); import never merges", m.RunID))
	}
	staging := filepath.Join(p.importedRoot(), ".staging-"+ID(""))
	defer os.RemoveAll(staging)
	files["manifest.json"] = JSON(map[string]any{"imported": true, "importedAt": Now(), "checksum": bundle.Checksum, "source": filepath.Base(src), "manifest": m})
	for path, body := range files {
		target := filepath.Join(staging, filepath.FromSlash(path))
		if err := Atomic(target, body, 0400); err != nil {
			return zero, err
		}
	}
	if err := os.Rename(staging, p.importedDir(m.RunID)); err != nil {
		return zero, err
	}
	return m, p.Audit(m.WorkflowID, "history.imported", map[string]any{"runId": m.RunID, "checksum": bundle.Checksum, "eventCount": m.EventCount, "retention": m.Retention})
}

func errOr(a, b error) error {
	if a != nil {
		return a
	}
	return b
}

// ImportedRuns lists the runs imported into this project.
func (p *Project) ImportedRuns() ([]string, error) {
	entries, err := os.ReadDir(p.importedRoot())
	if os.IsNotExist(err) {
		return []string{}, nil
	}
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for _, d := range entries {
		if d.IsDir() && validRunID(d.Name()) == nil {
			ids = append(ids, d.Name())
		}
	}
	return ids, nil
}

// ImportedEvents re-verifies an imported journal on every read.
func (p *Project) ImportedEvents(id string) ([]Event, error) {
	if err := validRunID(id); err != nil {
		return nil, err
	}
	journal, err := os.ReadFile(filepath.Join(p.importedDir(id), "events.ndjson"))
	if err != nil {
		return nil, err
	}
	return verifyJournalLines(journal, id)
}
