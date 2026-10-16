package arena

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type Run struct {
	Attachments []Attachment       `json:"attachments,omitempty"`
	Version     int                `json:"version"`
	ID          string             `json:"id"`
	Workflow    Workflow           `json:"workflow"`
	Profiles    map[string]Profile `json:"profiles"`
	Settings    Settings           `json:"settings"`
	Objective   string             `json:"objective"`
	CreatedAt   string             `json:"createdAt"`
	BaseCommit  string             `json:"baseCommit"`
	Retention   string             `json:"retention"`
	Simulated   bool               `json:"simulated"`
	// FanOutChildren are the worker instances fan-out nodes started in this
	// run. They stay out of Workflow, which remains the validated static graph.
	FanOutChildren []FanOutChild `json:"fanOutChildren,omitempty"`
	// Score caches the arena's grade for the live view; it is never persisted.
	Score *Scorecard `json:"-"`
}

// scorecard returns the cached grade, scoring the workflow when needed.
func (r Run) scorecard() Scorecard {
	if r.Score != nil {
		return *r.Score
	}
	return ScoreArena(r.Workflow, r.Profiles)
}

type RunState struct {
	Status    string                    `json:"status"`
	Agents    map[string]string         `json:"agents"`
	Completed map[string]map[string]any `json:"completed"`
	Pending   int                       `json:"pending"`
	LastEvent string                    `json:"lastEvent"`
	// Iterations is the current pass of agents a loop route restarted.
	Iterations map[string]int `json:"iterations,omitempty"`
}

func (p *Project) CreateRun(w Workflow, profiles map[string]Profile, s Settings, objective, base string) (Run, error) {
	r := Run{Version: 1, ID: ID("run-"), Workflow: w, Profiles: profiles, Settings: s, Objective: objective, CreatedAt: Now(), BaseCommit: base, Retention: s.Audit.Retention}
	if w.Settings.Audit.Retention == "redacted-local" && s.Audit.Retention == "full-local" {
		r.Retention = "redacted-local"
	}
	if strings.TrimSpace(objective) == "" {
		return r, fmt.Errorf("task objective is required")
	}
	stored := r
	if r.Retention != "full-local" {
		stored.Objective = ""
	}
	if e := Atomic(filepath.Join(p.RunDir(r.ID), "run.json"), SafeJSON(stored), 0600); e != nil {
		return r, e
	}
	_, e := p.Append(r.ID, w.Id, "", "workflow.created", map[string]any{"retention": r.Retention, "configHash": Hash(JSON(w)), "settingsHash": Hash(JSON(s)), "baseCommit": base, "simulated": false})
	return r, e
}
func (p *Project) ReadRun(id string) (Run, error) {
	var r Run
	if e := validID(id); e != nil {
		return r, e
	}
	if e := ReadJSON(filepath.Join(p.RunDir(id), "run.json"), &r); e != nil {
		return r, e
	}
	var current Run
	if err := ReadJSON(filepath.Join(p.RunDir(id), currentConfigFile), &current); err == nil {
		r = current
	} else if !os.IsNotExist(err) {
		return r, err
	}
	if r.Version != 1 || r.ID != id {
		return r, fmt.Errorf("unsupported or mismatched run manifest")
	}
	return r, nil
}
func Reduce(events []Event) (RunState, error) {
	s := RunState{Status: "new", Agents: map[string]string{}, Completed: map[string]map[string]any{}}
	for _, e := range events {
		s.LastEvent = e.ID
		switch e.Type {
		case "workflow.created":
			if s.Status != "new" {
				return s, fmt.Errorf("duplicate run creation")
			}
			s.Status = "created"
		case "workflow.started":
			if s.Status != "created" && s.Status != "paused" && s.Status != "interrupted" && !(s.Status == "failed" && e.Data["retryInterrupted"] == true) {
				return s, fmt.Errorf("invalid start from %s", s.Status)
			}
			s.Status = "running"
		case "workflow.paused":
			if s.Status != "running" {
				return s, fmt.Errorf("cannot pause %s", s.Status)
			}
			s.Status = "paused"
		case "workflow.resumed":
			if s.Status != "paused" && s.Status != "interrupted" && !(s.Status == "failed" && e.Data["retryInterrupted"] == true) {
				return s, fmt.Errorf("cannot resume %s", s.Status)
			}
			s.Status = "running"
		case "workflow.interrupted":
			if s.Status != "running" && s.Status != "paused" {
				return s, fmt.Errorf("cannot interrupt %s", s.Status)
			}
			s.Status = "interrupted"
			for id, status := range s.Agents {
				if status == "running" || status == "waiting" {
					s.Agents[id] = "interrupted"
				}
			}
		case "workflow.cancelled":
			if s.Status == "succeeded" || s.Status == "cancelled" {
				return s, fmt.Errorf("cannot cancel %s", s.Status)
			}
			s.Status = "cancelled"
		case "workflow.failed":
			s.Status = "failed"
		case "workflow.succeeded":
			if s.Status != "running" {
				return s, fmt.Errorf("cannot complete %s", s.Status)
			}
			s.Status = "succeeded"
		case "agent.queued":
			s.Agents[e.AgentID] = "queued"
		case "agent.started":
			if s.Agents[e.AgentID] == "running" {
				return s, fmt.Errorf("agent %s already running", e.AgentID)
			}
			s.Agents[e.AgentID] = "running"
		case "agent.waiting":
			s.Agents[e.AgentID] = "waiting"
		case "agent.succeeded":
			if s.Agents[e.AgentID] != "running" && s.Agents[e.AgentID] != "waiting" {
				return s, fmt.Errorf("agent %s completed without start", e.AgentID)
			}
			s.Agents[e.AgentID] = "succeeded"
			if result, ok := e.Data["result"].(map[string]any); ok {
				s.Completed[e.AgentID] = result
			}
		case "agent.failed":
			s.Agents[e.AgentID] = "failed"
		case "agent.cancelled":
			s.Agents[e.AgentID] = "cancelled"
		case "loop.iteration":
			// A loop route starts the next pass of its body: the earlier
			// pass stays in the journal, the projection shows the new one.
			if s.Iterations == nil {
				s.Iterations = map[string]int{}
			}
			for _, id := range stringSlice(e.Data["agents"]) {
				if s.Agents[id] == "running" || s.Agents[id] == "waiting" {
					return s, fmt.Errorf("loop restarted %s while it was %s", id, s.Agents[id])
				}
				delete(s.Agents, id)
				delete(s.Completed, id)
				s.Iterations[id] = max(1, s.Iterations[id]) + 1
			}
		case "decision.requested":
			s.Pending++
		case "decision.answered":
			if s.Pending > 0 {
				s.Pending--
			}
		}
	}
	return s, nil
}

// RunHistory is `arena history RUN --json`: the run's reduced status beside
// its events, so scripts need not know which event types end a run.
type RunHistory struct {
	RunID  string `json:"runId"`
	Status string `json:"status"`
	// Terminal is true once the run has ended: succeeded, failed or cancelled.
	Terminal bool `json:"terminal"`
	// PendingDecisions counts open questions that wait on the user.
	PendingDecisions int `json:"pendingDecisions"`
	// StateError explains an "invalid" status: the events do not replay.
	StateError string `json:"stateError,omitempty"`
	// Usage totals the token counts providers reported; reported is false
	// when none did.
	Usage  RunUsage `json:"usage"`
	Events []Event  `json:"events"`
}

// terminalStatuses are the run statuses set by workflow.succeeded,
// workflow.failed and workflow.cancelled.
var terminalStatuses = []string{"succeeded", "failed", "cancelled"}

func (p *Project) History(id string, events []Event) (RunHistory, error) {
	pending, err := p.Decisions(id)
	if err != nil {
		return RunHistory{}, err
	}
	h := RunHistory{RunID: id, PendingDecisions: len(pending), Usage: SumUsage(events), Events: events}
	// History stays readable when the events do not replay; that is when it
	// matters most.
	if state, err := Reduce(events); err != nil {
		h.Status, h.StateError = "invalid", err.Error()
	} else {
		h.Status, h.Terminal = state.Status, Has(terminalStatuses, state.Status)
	}
	return h, nil
}
func (p *Project) State(id string) (RunState, error) {
	events, e := p.Events(id)
	if e != nil {
		return RunState{}, e
	}
	return Reduce(events)
}
func (p *Project) Transition(id, state string) error {
	r, e := p.ReadRun(id)
	if e != nil {
		return e
	}
	l, e := p.Lock("control-" + id)
	if e != nil {
		return e
	}
	defer l.Close()
	s, e := p.State(id)
	if e != nil {
		return e
	}
	allowed := false
	switch state {
	case "paused":
		allowed = s.Status == "running"
	case "resumed":
		allowed = s.Status == "paused"
	case "cancelled":
		allowed = s.Status != "succeeded" && s.Status != "cancelled"
	case "interrupted":
		allowed = s.Status == "running" || s.Status == "paused"
	}
	if !allowed {
		return fmt.Errorf("cannot transition %s to %s", s.Status, state)
	}
	_, e = p.Append(id, r.Workflow.Id, "user", "workflow."+state, map[string]any{})
	return e
}
func (p *Project) AwaitScheduling(ctx context.Context, id string) error {
	ticker := time.NewTicker(150 * time.Millisecond)
	defer ticker.Stop()
	for {
		s, e := p.State(id)
		if e != nil {
			return e
		}
		switch s.Status {
		case "cancelled":
			return context.Canceled
		case "paused":
		case "running":
			return nil
		default:
			return fmt.Errorf("run is %s", s.Status)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

type Decision struct {
	ClosedAt    string         `json:"closedAt,omitempty"`
	ContentHash string         `json:"contentHash,omitempty"`
	ID          string         `json:"id"`
	RunID       string         `json:"runId"`
	AgentID     string         `json:"agentId"`
	Kind        string         `json:"kind"`
	Prompt      string         `json:"prompt"`
	Choices     []string       `json:"choices"`
	Context     map[string]any `json:"context"`
	Answer      string         `json:"answer,omitempty"`
	CreatedAt   string         `json:"createdAt"`
	// Route names the cross-provider sharing route one answer covers for the
	// rest of the run; see shareRoute.
	Route string `json:"route,omitempty"`
}

func (p *Project) Decide(ctx context.Context, r Run, agent, kind, prompt string, choices []string, data map[string]any) (answer string, err error) {
	d := Decision{ID: ID("decision-"), RunID: r.ID, AgentID: agent, Kind: kind, Prompt: prompt, Choices: choices, Context: data, CreatedAt: Now()}
	if e := Atomic(filepath.Join(p.RunDir(r.ID), "decisions", d.ID+".json"), SafeJSON(d), 0600); e != nil {
		return "", e
	}
	if r.Retention != "full-local" {
		defer func() {
			if closeErr := p.closeDecision(r.ID, d.ID); closeErr != nil && err == nil {
				answer, err = "", closeErr
			}
		}()
	}
	if _, e := p.Append(r.ID, r.Workflow.Id, agent, "decision.requested", map[string]any{"decision": d}); e != nil {
		return "", e
	}
	return p.awaitDecision(ctx, r.ID, d.ID)
}

// awaitOpenDecision waits on a decision an interrupted supervisor left
// open, so a resumed run answers that question instead of asking again.
func (p *Project) awaitOpenDecision(ctx context.Context, r Run, id string) (answer string, err error) {
	if r.Retention != "full-local" {
		defer func() {
			if closeErr := p.closeDecision(r.ID, id); closeErr != nil && err == nil {
				answer, err = "", closeErr
			}
		}()
	}
	return p.awaitDecision(ctx, r.ID, id)
}

func (p *Project) awaitDecision(ctx context.Context, run, id string) (string, error) {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		var current Decision
		if e := ReadJSON(filepath.Join(p.RunDir(run), "decisions", id+".json"), &current); e != nil {
			return "", e
		}
		if current.Answer != "" {
			return current.Answer, nil
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-ticker.C:
		}
	}
}
func (p *Project) Answer(run, id, answer string) error {
	if e := validID(run); e != nil {
		return e
	}
	if e := validID(id); e != nil {
		return e
	}
	l, e := p.Lock("decisions-" + run)
	if e != nil {
		return e
	}
	defer l.Close()
	path := filepath.Join(p.RunDir(run), "decisions", id+".json")
	var d Decision
	if e := ReadJSON(path, &d); e != nil {
		return e
	}
	if d.Answer != "" || d.ClosedAt != "" {
		return fmt.Errorf("decision already answered")
	}
	if len(d.Choices) > 0 && !Has(d.Choices, answer) {
		return fmt.Errorf("answer must be one of %v", d.Choices)
	}
	r, e := p.ReadRun(run)
	if e != nil {
		return e
	}
	d.Answer = answer
	if _, e := p.Append(run, r.Workflow.Id, "user", "decision.answered", map[string]any{"decisionId": id, "answer": answer}); e != nil {
		return e
	}
	if d.Route != "" {
		if e := p.settleRoute(r, d, answer); e != nil {
			return e
		}
		// No waiter compacts a route decision: its grant carries the answer.
		if r.Retention != "full-local" {
			compactDecision(&d)
		}
	}
	return Atomic(path, SafeJSON(d), 0600)
}
func (p *Project) Decisions(id string) ([]Decision, error) {
	if e := validID(id); e != nil {
		return nil, e
	}
	paths, e := filepath.Glob(filepath.Join(p.RunDir(id), "decisions", "*.json"))
	if e != nil {
		return nil, e
	}
	result := []Decision{}
	for _, path := range paths {
		var d Decision
		if e := ReadJSON(path, &d); e != nil {
			return nil, e
		}
		if d.Answer == "" && d.ClosedAt == "" {
			result = append(result, d)
		}
	}
	return result, nil
}

func (p *Project) closeDecision(run, id string) error {
	l, err := p.WaitLock("decisions-" + run)
	if err != nil {
		return err
	}
	defer l.Close()
	path := filepath.Join(p.RunDir(run), "decisions", id+".json")
	var d Decision
	if err := ReadJSON(path, &d); err != nil {
		return err
	}
	compactDecision(&d)
	return Atomic(path, SafeJSON(d), 0600)
}

// compactDecision keeps a closed decision's hash but drops its content.
func compactDecision(d *Decision) {
	d.ContentHash = Hash(SafeJSON(d))
	d.Prompt, d.Answer, d.Context, d.Choices = "", "", nil, nil
	d.ClosedAt = Now()
}
func RunAlive(p *Project, id string) bool {
	var owner LockOwner
	if e := ReadJSON(p.Path("locks", "run-"+id, "owner.json"), &owner); e != nil {
		return false
	}
	host, _ := os.Hostname()
	if owner.Host != host || owner.PID < 1 || !ProcessAlive(owner.PID) {
		return false
	}
	boot, start := processIdentity(owner.PID)
	return !(owner.BootID != "" && boot != "" && owner.BootID != boot || owner.StartID != "" && start != "" && owner.StartID != start)
}
