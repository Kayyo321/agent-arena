package arena

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode"

	c "github.com/Kayyo321/agent-arena/internal/contract"
)

type ContextEntry = c.ContextEntry
type ContextPolicy = c.ProjectSettingsSharedContext

func EffectiveContext(s Settings, w Workflow) (ContextPolicy, error) {
	p := *s.SharedContext
	x := w.Settings.SharedContext
	if x == nil {
		return p, nil
	}
	rank := func(s string) int {
		switch s {
		case "auto-refuse":
			return 0
		case "ask-user":
			return 1
		default:
			return 2
		}
	}
	for _, v := range []struct {
		a *string
		b string
	}{{&p.AgentWritePolicy, x.AgentWritePolicy}, {&p.AgentDeletePolicy, x.AgentDeletePolicy}} {
		if v.b != "" {
			if rank(v.b) > rank(*v.a) {
				return p, fmt.Errorf("workflow cannot loosen shared-context policy")
			}
			*v.a = v.b
		}
	}
	for _, v := range []struct {
		a *int
		b *int
	}{{&p.MaxEntries, x.MaxEntries}, {&p.MaxEntryBytes, x.MaxEntryBytes}, {&p.MaxTotalBytes, x.MaxTotalBytes}} {
		if v.b != nil {
			if *v.b > *v.a {
				return p, fmt.Errorf("workflow cannot increase shared-context limits")
			}
			*v.a = *v.b
		}
	}
	if x.StaleAfterDays != nil {
		if p.StaleAfterDays > 0 && (*x.StaleAfterDays == 0 || *x.StaleAfterDays > p.StaleAfterDays) {
			return p, fmt.Errorf("workflow cannot loosen stale interval")
		}
		p.StaleAfterDays = *x.StaleAfterDays
	}
	return p, nil
}
func (p *Project) ContextDir(workflow string) (string, error) {
	if e := validID(workflow); e != nil {
		return "", e
	}
	s, e := p.Settings()
	if e != nil {
		return "", e
	}
	rel := filepath.Join("context", workflow, "entries")
	if s.SharedContext.Storage == "local-only" {
		rel = filepath.Join(".state", rel)
	}
	path, e := SafePath(p.Path(), rel)
	if e != nil {
		return "", e
	}
	return path, os.MkdirAll(path, 0700)
}
func (p *Project) ContextEntries(workflow string, all bool) ([]ContextEntry, error) {
	dir, e := p.ContextDir(workflow)
	if e != nil {
		return nil, e
	}
	files, e := filepath.Glob(filepath.Join(dir, "*.context.json"))
	if e != nil {
		return nil, e
	}
	s, e := p.Settings()
	if e != nil {
		return nil, e
	}
	w, _, e := p.Load(workflow)
	if e != nil {
		return nil, e
	}
	policy, e := EffectiveContext(s, w)
	if e != nil {
		return nil, e
	}
	entries := []ContextEntry{}
	for _, f := range files {
		var v ContextEntry
		if e := ReadJSON(f, &v); e != nil {
			return nil, e
		}
		if e := c.ValidateContextEntry(JSON(v)); e != nil {
			return nil, e
		}
		if v.WorkflowId != workflow {
			return nil, fmt.Errorf("context workflow mismatch")
		}
		if v.Status == "archived" && !all {
			continue
		}
		if v.Status == "active" && policy.StaleAfterDays > 0 {
			since := v.CreatedAt
			if v.LastVerifiedAt != nil {
				since = *v.LastVerifiedAt
			}
			at, e := time.Parse(time.RFC3339, since)
			if e != nil {
				return nil, e
			}
			if time.Since(at) > time.Duration(policy.StaleAfterDays)*24*time.Hour {
				v.Status = "stale"
			}
		}
		entries = append(entries, v)
	}
	return entries, nil
}

type ContextProposal struct {
	WorkflowID string                       `json:"workflowId"`
	Operation  string                       `json:"operation"`
	EntryID    *string                      `json:"entryId"`
	Title      string                       `json:"title,omitempty"`
	Content    string                       `json:"content,omitempty"`
	Tags       []string                     `json:"tags,omitempty"`
	Confidence string                       `json:"confidence,omitempty"`
	Evidence   []c.ContextEntryEvidenceItem `json:"evidence,omitempty"`
	Reason     string                       `json:"reason,omitempty"`
}
type ContextResult struct {
	Format  string  `json:"format"`
	Outcome string  `json:"outcome"`
	EntryID *string `json:"entryId"`
	Reason  string  `json:"reason,omitempty"`
}

func (p *Project) ProposeContext(q ContextProposal, actor, provider, run, task string, approved bool) (ContextResult, error) {
	result := ContextResult{Format: "arena.context-result.v1", Outcome: "refused", EntryID: q.EntryID}
	w, _, e := p.Load(q.WorkflowID)
	if e != nil {
		return result, e
	}
	s, e := p.Settings()
	if e != nil {
		return result, e
	}
	policy, e := EffectiveContext(s, w)
	if e != nil {
		return result, e
	}
	l, e := p.WaitLock("context-" + q.WorkflowID)
	if e != nil {
		return result, e
	}
	defer l.Close()
	if actor != "user" {
		a, ok := agentByID(w, actor)
		if !ok || a.Runtime.Provider != provider {
			return result, fmt.Errorf("context actor is not in the workflow")
		}
		selected := policy.AgentWritePolicy
		if q.Operation == "archive" {
			selected = policy.AgentDeletePolicy
		}
		if selected == "auto-refuse" {
			result.Reason = "project/workflow context policy refuses this operation"
			return result, p.Audit(q.WorkflowID, "context.refused", map[string]any{"actor": actor, "proposal": q, "policy": selected})
		}
		if selected == "ask-user" && !approved {
			request := ID("context-request-")
			if e := Atomic(p.Path(".state", "context-requests", request+".json"), SafeJSON(map[string]any{"proposal": q, "actor": actor, "provider": provider, "run": run, "task": task}), 0600); e != nil {
				return result, e
			}
			result.Outcome = "needs-user"
			result.Reason = request
			return result, p.Audit(q.WorkflowID, "context.pending", map[string]any{"requestId": request, "actor": actor})
		}
	}
	if q.Operation != "add" && q.Operation != "revise" && q.Operation != "archive" && q.Operation != "restore" {
		return result, fmt.Errorf("unknown context operation")
	}
	if Redact(string(JSON(q))) != string(JSON(q)) {
		return result, fmt.Errorf("context contains secret-like content; remove it before saving")
	}
	entries, e := p.ContextEntries(q.WorkflowID, true)
	if e != nil {
		return result, e
	}
	var prior *ContextEntry
	for i := range entries {
		if q.EntryID != nil && entries[i].Id == *q.EntryID {
			prior = &entries[i]
		}
	}
	if q.Operation != "add" && prior == nil {
		result.Outcome = "not-found"
		return result, nil
	}
	dir, e := p.ContextDir(q.WorkflowID)
	if e != nil {
		return result, e
	}
	files := map[string][]byte{}
	archived := map[string]bool{}
	entry := ContextEntry{}
	switch q.Operation {
	case "archive", "restore":
		entry = *prior
		entry.Status = "archived"
		if q.Operation == "restore" {
			if actor != "user" {
				return result, fmt.Errorf("only the user can restore context")
			}
			entry.Status = "active"
			// Restoring a version replaces the active version of that lineage.
			// Every displaced version remains available for another restoration.
			root := func(id string) string {
				seen := map[string]bool{}
				for !seen[id] {
					seen[id] = true
					next := ""
					for _, version := range entries {
						if version.Id == id && version.Supersedes != nil {
							next = *version.Supersedes
							break
						}
					}
					if next == "" {
						break
					}
					id = next
				}
				return id
			}
			lineage := root(entry.Id)
			for _, version := range entries {
				if version.Id != entry.Id && version.Status != "archived" && root(version.Id) == lineage {
					version.Status, version.UpdatedAt = "archived", Now()
					archived[version.Id] = true
					rel, _ := filepath.Rel(p.Path(), filepath.Join(dir, version.Id+".context.json"))
					files[rel] = JSON(version)
				}
			}
		}
		entry.UpdatedAt = Now()
	case "add", "revise":
		if q.Title == "" || q.Content == "" || q.Confidence == "" {
			return result, fmt.Errorf("context requires title, content and explicit confidence")
		}
		if q.Confidence == "verified" && len(q.Evidence) == 0 {
			return result, fmt.Errorf("verified context requires evidence")
		}
		if q.Tags == nil {
			q.Tags = []string{}
		}
		if q.Evidence == nil {
			q.Evidence = []c.ContextEntryEvidenceItem{}
		}
		actorType := "agent"
		if actor == "user" {
			actorType = "user"
		}
		var providerPtr, runPtr, taskPtr *string
		if provider != "" {
			providerPtr = &provider
		}
		if run != "" {
			runPtr = &run
		}
		if task != "" {
			taskPtr = &task
		}
		entry = ContextEntry{SchemaVersion: 1, Id: ID("ctx-"), WorkflowId: q.WorkflowID, Title: q.Title, Content: q.Content, Tags: q.Tags, Confidence: q.Confidence, Status: "active", CreatedAt: Now(), UpdatedAt: Now(), CreatedBy: c.ContextEntryCreatedBy{ActorId: actor, ActorType: actorType, Provider: providerPtr}, Source: c.ContextEntrySource{RunId: runPtr, TaskId: taskPtr}, Evidence: q.Evidence}
		if q.Confidence == "verified" {
			now := Now()
			entry.LastVerifiedAt = &now
		}
		if prior != nil {
			entry.Supersedes = &prior.Id
			prior.Status = "archived"
			prior.UpdatedAt = Now()
			rel, _ := filepath.Rel(p.Path(), filepath.Join(dir, prior.Id+".context.json"))
			files[rel] = JSON(prior)
		}
	}
	if e := c.ValidateContextEntry(JSON(entry)); e != nil {
		return result, e
	}
	size := len(JSON(entry))
	total := size
	active := 0
	if entry.Status != "archived" {
		active = 1
	}
	for _, v := range entries {
		if v.Id == entry.Id {
			continue
		}
		if prior != nil && v.Id == prior.Id {
			if q.Operation == "revise" {
				total += len(JSON(prior))
			}
			continue
		}
		total += len(JSON(v))
		if v.Status != "archived" && !archived[v.Id] {
			active++
		}
	}
	if size > policy.MaxEntryBytes || total > policy.MaxTotalBytes || active > policy.MaxEntries {
		return result, fmt.Errorf("context size/count limit exceeded (includes recoverable entry content)")
	}
	rel, _ := filepath.Rel(p.Path(), filepath.Join(dir, entry.Id+".context.json"))
	files[rel] = JSON(entry)
	tx, e := p.Commit("context "+q.Operation, files)
	if e != nil {
		return result, e
	}
	result.Outcome = "accepted"
	result.EntryID = &entry.Id
	return result, p.Audit(q.WorkflowID, "context."+q.Operation, map[string]any{"actor": actor, "runId": run, "taskId": task, "entryId": entry.Id, "transaction": tx, "contentHash": Hash(JSON(entry))})
}
func agentByID(w Workflow, id string) (Agent, bool) {
	for _, a := range w.Agents {
		if a.Id == id {
			return a, true
		}
	}
	return Agent{}, false
}

// ContextSearch ranks entries by how many distinct query words they share,
// counting title and tag hits twice; newer entries win ties. An empty query
// (or one made only of stop words) lists every entry, newest first. Whole-
// phrase matching made natural queries such as "ledger API money" and task
// objectives find nothing, so agents never saw or reused shared context.
func ContextSearch(entries []ContextEntry, query string) []ContextEntry {
	terms := searchTerms(query)
	type scored struct {
		entry ContextEntry
		score int
	}
	ranked := []scored{}
	for _, e := range entries {
		heading := searchTerms(e.Title + " " + strings.Join(e.Tags, " "))
		body := searchTerms(e.Content)
		score := 0
		for term := range terms {
			if heading[term] {
				score += 2
			} else if body[term] {
				score++
			}
		}
		if len(terms) == 0 || score > 0 {
			ranked = append(ranked, scored{e, score})
		}
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].score != ranked[j].score {
			return ranked[i].score > ranked[j].score
		}
		return ranked[i].entry.CreatedAt > ranked[j].entry.CreatedAt
	})
	found := []ContextEntry{}
	for _, r := range ranked {
		found = append(found, r.entry)
	}
	return found
}

var contextStopWords = map[string]bool{"the": true, "and": true, "for": true, "with": true, "that": true, "this": true, "from": true, "into": true, "are": true, "was": true, "its": true, "use": true, "using": true, "should": true, "must": true, "before": true, "after": true, "about": true, "what": true, "which": true, "when": true, "where": true, "how": true, "all": true, "any": true, "each": true, "both": true, "not": true, "but": true, "you": true, "your": true, "our": true, "has": true, "have": true}

func searchTerms(text string) map[string]bool {
	terms := map[string]bool{}
	for _, word := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	}) {
		if len(word) >= 3 && !contextStopWords[word] {
			terms[word] = true
		}
	}
	return terms
}
