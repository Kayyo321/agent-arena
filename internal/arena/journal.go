package arena

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

type Event struct {
	Version      int            `json:"version"`
	Sequence     int            `json:"sequence"`
	ID           string         `json:"id"`
	At           string         `json:"at"`
	RunID        string         `json:"runId"`
	WorkflowID   string         `json:"workflowId"`
	AgentID      string         `json:"agentId,omitempty"`
	Type         string         `json:"type"`
	Data         map[string]any `json:"data"`
	PreviousHash string         `json:"previousHash"`
	Hash         string         `json:"hash"`
}

var secretPatterns = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(?:sk-[a-z0-9_-]{16,}|AIza[a-z0-9_-]{20,}|gh[pousr]_[a-z0-9]{20,})`),
	regexp.MustCompile(`(?i)(?:bearer\s+)[a-z0-9._~+/=-]+`),
	regexp.MustCompile(`(?i)(?:api[_-]?key|access[_-]?token|refresh[_-]?token|password|client[_-]?secret)["']?\s*[=:]\s*["']?[^\s,;"'}]+`),
	regexp.MustCompile(`-----BEGIN [A-Z ]*PRIVATE KEY-----[\s\S]*?-----END [A-Z ]*PRIVATE KEY-----`),
}

func Redact(s string) string {
	for _, r := range secretPatterns {
		s = r.ReplaceAllString(s, "[REDACTED]")
	}
	return s
}
func cleanValue(v any) any {
	switch x := v.(type) {
	case string:
		return Redact(x)
	case []any:
		a := make([]any, len(x))
		for i, v := range x {
			a[i] = cleanValue(v)
		}
		return a
	case map[string]any:
		m := map[string]any{}
		for k, v := range x {
			lower := strings.ToLower(k)
			if lower == "authorization" || lower == "password" || lower == "api_key" || lower == "apikey" || lower == "accesstoken" || lower == "refreshtoken" || lower == "access_token" || lower == "refresh_token" {
				m[k] = "[REDACTED]"
			} else {
				m[k] = cleanValue(v)
			}
		}
		return m
	}
	return v
}
func SafeJSON(v any) []byte {
	var x any
	if e := DecodeJSON(JSON(v), &x); e != nil {
		panic(e)
	}
	return JSON(cleanValue(x))
}
func (p *Project) RunDir(id string) string { return p.Path(".state", id) }
func (p *Project) Events(id string) ([]Event, error) {
	if e := validID(id); e != nil {
		return nil, e
	}
	l, e := p.WaitLock("events-" + id)
	if e != nil {
		return nil, e
	}
	defer l.Close()
	return p.eventsUnlocked(id)
}
func (p *Project) eventsUnlocked(id string) ([]Event, error) {
	if e := validID(id); e != nil {
		return nil, e
	}
	entries, e := filepath.Glob(filepath.Join(p.RunDir(id), "events", "*.json"))
	if e != nil {
		return nil, e
	}
	events := []Event{}
	previous := ""
	var mirror bytes.Buffer
	for i, path := range entries {
		b, e := os.ReadFile(path)
		if e != nil {
			return nil, e
		}
		var v Event
		if e = DecodeJSON(b, &v); e != nil {
			return nil, fmt.Errorf("preserved corrupt event %s: %w", path, e)
		}
		hash := v.Hash
		v.Hash = ""
		if v.Version != 1 || v.Sequence != i+1 || v.PreviousHash != previous || Hash(JSON(v)) != hash {
			return nil, fmt.Errorf("invalid journal chain at %s; original preserved", path)
		}
		v.Hash = hash
		events = append(events, v)
		previous = hash
		compact, e := json.Marshal(v)
		if e != nil {
			return nil, e
		}
		mirror.Write(compact)
		mirror.WriteByte('\n')
	}
	b, e := os.ReadFile(filepath.Join(p.RunDir(id), "events.ndjson"))
	if e != nil && !os.IsNotExist(e) {
		return nil, e
	}
	if !bytes.Equal(b, mirror.Bytes()) {
		return nil, fmt.Errorf("journal mirror interrupted or changed; atomic events preserved; run arena history repair %s", id)
	}
	return events, nil
}
func (p *Project) Append(run, workflow, agent, kind string, data map[string]any) (Event, error) {
	var zero Event
	l, e := p.WaitLock("events-" + run)
	if e != nil {
		return zero, e
	}
	defer l.Close()
	events, e := p.eventsUnlocked(run)
	if e != nil {
		return zero, e
	}
	var safe map[string]any
	if e = DecodeJSON(SafeJSON(data), &safe); e != nil {
		return zero, e
	}
	retention := "full-local"
	if run != "project" {
		var manifest Run
		if ReadJSON(filepath.Join(p.RunDir(run), "run.json"), &manifest) == nil {
			retention = manifest.Retention
		}
	} else {
		var settings Settings
		if ReadJSON(p.Path("settings.arena.json"), &settings) == nil {
			retention = settings.Audit.Retention
		}
	}
	if retention != "full-local" {
		safe = omitPayloads(safe).(map[string]any)
	}
	v := Event{Version: 1, Sequence: len(events) + 1, ID: ID("event-"), At: Now(), RunID: run, WorkflowID: workflow, AgentID: agent, Type: kind, Data: safe}
	if len(events) > 0 {
		v.PreviousHash = events[len(events)-1].Hash
	}
	v.Hash = Hash(JSON(v))
	dir := p.RunDir(run)
	if e = Atomic(filepath.Join(dir, "events", fmt.Sprintf("%012d.json", v.Sequence)), JSON(v), 0600); e != nil {
		return zero, e
	}
	f, e := os.OpenFile(filepath.Join(dir, "events.ndjson"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if e != nil {
		return zero, e
	}
	b, e := json.Marshal(v)
	if e == nil {
		_, e = f.Write(append(b, '\n'))
	}
	if e == nil {
		e = f.Sync()
	}
	ce := f.Close()
	if e == nil {
		e = ce
	}
	return v, e
}
func (p *Project) RepairJournal(id string) error {
	if e := validID(id); e != nil {
		return e
	}
	l, e := p.Lock("events-" + id)
	if e != nil {
		return e
	}
	defer l.Close()
	paths, e := filepath.Glob(filepath.Join(p.RunDir(id), "events", "*.json"))
	if e != nil || len(paths) == 0 {
		return fmt.Errorf("no atomic events to recover")
	}
	var b bytes.Buffer
	previous := ""
	for i, path := range paths {
		var v Event
		if e = ReadJSON(path, &v); e != nil {
			return e
		}
		hash := v.Hash
		v.Hash = ""
		if v.Version != 1 || v.Sequence != i+1 || v.PreviousHash != previous || Hash(JSON(v)) != hash {
			return fmt.Errorf("atomic journal is corrupt; refusing repair")
		}
		v.Hash = hash
		previous = hash
		line, _ := json.Marshal(v)
		b.Write(line)
		b.WriteByte('\n')
	}
	mirror := filepath.Join(p.RunDir(id), "events.ndjson")
	old, e := os.ReadFile(mirror)
	if e == nil {
		if e = Atomic(mirror+".preserved-"+ID(""), old, 0600); e != nil {
			return e
		}
	}
	return Atomic(mirror, b.Bytes(), 0600)
}
func (p *Project) Audit(workflow, kind string, data map[string]any) error {
	_, e := p.Append("project", workflow, "user", kind, data)
	return e
}
func (p *Project) Runs() ([]string, error) {
	entries, e := os.ReadDir(p.Path(".state"))
	if os.IsNotExist(e) {
		return []string{}, nil
	}
	if e != nil {
		return nil, e
	}
	ids := []string{}
	for _, d := range entries {
		if d.IsDir() && strings.HasPrefix(d.Name(), "run-") {
			ids = append(ids, d.Name())
		}
	}
	return ids, nil
}
func (p *Project) PruneRun(id string, includingCheckpoints bool) error {
	if e := validID(id); e != nil {
		return e
	}
	if !strings.HasPrefix(id, "run-") {
		return fmt.Errorf("only run history can be pruned")
	}
	if !includingCheckpoints {
		return fmt.Errorf("run deletion removes checkpoints too; explicitly acknowledge --including-checkpoints")
	}
	if _, e := os.Stat(p.Path("locks", "run-"+id)); e == nil {
		return fmt.Errorf("run is active or needs lock recovery")
	}
	target := p.Path(".state", "retired", id+"-"+time.Now().UTC().Format("20060102T150405"))
	if e := os.MkdirAll(filepath.Dir(target), 0700); e != nil {
		return e
	}
	if e := os.Rename(p.RunDir(id), target); e != nil {
		return e
	}
	return p.Audit("", "history.retired", map[string]any{"runId": id, "recoverablePath": target, "checkpointsIncluded": true})
}

func omitPayloads(v any) any {
	switch value := v.(type) {
	case map[string]any:
		out := map[string]any{}
		for k, v := range value {
			switch strings.ToLower(k) {
			case "prompt", "output", "payload", "objective", "text", "result", "proposal", "request", "answer", "context", "positions", "summary", "rationale", "evidence":
				out[k+"Hash"] = Hash(JSON(v))
				out[k+"Omitted"] = true
			default:
				out[k] = omitPayloads(v)
			}
		}
		return out
	case []any:
		out := []any{}
		for _, v := range value {
			out = append(out, omitPayloads(v))
		}
		return out
	}
	return v
}
