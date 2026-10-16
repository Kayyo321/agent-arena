package arena

import (
	"fmt"
	"os"
	"path/filepath"
)

type ConfigUpdate struct {
	RequestID   string  `json:"requestId"`
	Parent      string  `json:"parent"`
	Agent       Agent   `json:"agent"`
	Profile     Profile `json:"profile"`
	Transaction string  `json:"transaction"`
}

func (p *Project) QueueCoworker(run string, update ConfigUpdate) error {
	if run == "" {
		return nil
	}
	if _, err := p.ReadRun(run); err != nil {
		return nil
	}
	return Atomic(filepath.Join(p.RunDir(run), "config-updates", update.RequestID+".json"), JSON(update), 0600)
}

// currentConfigFile holds a run's configuration once it has grown past
// run.json: approved coworkers and fan-out children.
const currentConfigFile = "current-config.json"

// writeConfig is the only writer of current-config.json. Coworker additions
// and fan-out child registration both go through it, under one lock, so
// neither drops the other's agents. The MCP bridge of each agent reads this
// file to resolve its actor.
func (e *Engine) writeConfig(next Run) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	next.FanOutChildren = []FanOutChild{}
	for _, id := range sortedKeys(e.children) {
		next.FanOutChildren = append(next.FanOutChildren, e.children[id])
	}
	return Atomic(filepath.Join(e.Project.RunDir(next.ID), currentConfigFile), SafeJSON(retainedRun(next)), 0600)
}

// retainedRun is what a run may keep on disk beyond run.json: as there, a
// redacted run keeps no objective.
func retainedRun(r Run) Run {
	if r.Retention != "full-local" {
		r.Objective = ""
	}
	return r
}

// Apply approved additions only between waves. Existing run roles/settings retain
// their recorded values; unrelated authoring edits do not mutate an active run.
func (e *Engine) applyCoworkers() (map[string]string, error) {
	paths, err := filepath.Glob(filepath.Join(e.Project.RunDir(e.Run.ID), "config-updates", "*.json"))
	if err != nil {
		return nil, err
	}
	added := map[string]string{}
	for _, path := range paths {
		var update ConfigUpdate
		if err := ReadJSON(path, &update); err != nil {
			return nil, err
		}
		if _, ok := agentByID(e.Run.Workflow, update.Agent.Id); ok {
			if err := os.Rename(path, path+".applied"); err != nil {
				return nil, err
			}
			continue
		}
		next := convert[Run](e.Run)
		next.Workflow.Agents = append(next.Workflow.Agents, update.Agent)
		next.Workflow.Relationships = append(next.Workflow.Relationships, Relationship{Type: "delegates", From: update.Parent, To: update.Agent.Id})
		next.Profiles[update.Agent.Id] = update.Profile
		if err := ValidateWorkflow(next.Workflow, next.Profiles, next.Settings); err != nil {
			return nil, fmt.Errorf("approved coworker cannot fit the recorded run; configuration preserved for the next run: %w", err)
		}
		revision := filepath.Join(e.Project.RunDir(e.Run.ID), "config-revisions", update.RequestID+".json")
		if err := Atomic(revision, SafeJSON(retainedRun(next)), 0600); err != nil {
			return nil, err
		}
		if _, err := e.event("arena-orchestrator", "workflow.config-updated", map[string]any{"requestId": update.RequestID, "parent": update.Parent, "agentId": update.Agent.Id, "revision": revision, "configHash": Hash(JSON(next.Workflow)), "transaction": update.Transaction}); err != nil {
			return nil, err
		}
		if err := e.writeConfig(next); err != nil {
			return nil, err
		}
		e.Run = next
		added[update.Agent.Id] = update.Parent
		if err := os.Rename(path, path+".applied"); err != nil {
			return nil, err
		}
	}
	return added, nil
}
