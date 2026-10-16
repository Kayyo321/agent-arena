package arena

import "fmt"

// FanOutChild is one worker instance a fan-out node started: a copy of the
// worker template under the synthetic ID <fanOutId>.<n>.
type FanOutChild struct {
	FanOutID string  `json:"fanOutId"`
	Agent    Agent   `json:"agent"`
	Profile  Profile `json:"profile"`
}

// agent resolves a workflow agent or a registered fan-out child; the MCP
// bridge and the packet router use it to identify an actor.
func (r Run) agent(id string) (Agent, bool) {
	if a, ok := agentByID(r.Workflow, id); ok {
		return a, true
	}
	for _, child := range r.FanOutChildren {
		if child.Agent.Id == id {
			return child.Agent, true
		}
	}
	return Agent{}, false
}

func (e *Engine) agentByID(id string) (Agent, bool) {
	if a, ok := agentByID(e.Run.Workflow, id); ok {
		return a, true
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	child, ok := e.children[id]
	return child.Agent, ok
}

func (e *Engine) profileOf(id string) Profile {
	e.mu.Lock()
	child, ok := e.children[id]
	e.mu.Unlock()
	if ok {
		return child.Profile
	}
	return e.Run.Profiles[id]
}

// fanOutChildID names the n-th (1-based) child of a fan-out.
func fanOutChildID(f FanOut, n int) string { return fmt.Sprintf("%s.%d", f.Id, n) }

// registerChildren records children 1..n of f and persists them before any
// of them starts, because each child's MCP bridge resolves its actor from
// the run's current configuration. Registration is idempotent, so a resumed
// or looped fan-out registers the same IDs again.
func (e *Engine) registerChildren(f FanOut, n int) ([]Agent, error) {
	worker, ok := agentByID(e.Run.Workflow, f.Worker)
	if !ok {
		return nil, fmt.Errorf("fan-out %s worker %s is missing", f.Id, f.Worker)
	}
	agents := []Agent{}
	e.mu.Lock()
	for i := 1; i <= n; i++ {
		a := convert[Agent](worker)
		a.Id = fanOutChildID(f, i)
		profile := convert[Profile](e.Run.Profiles[f.Worker])
		profile.Id = a.Id
		e.children[a.Id] = FanOutChild{FanOutID: f.Id, Agent: a, Profile: profile}
		agents = append(agents, a)
	}
	e.mu.Unlock()
	return agents, e.writeConfig(e.Run)
}
