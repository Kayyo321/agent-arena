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
		if err := Atomic(revision, SafeJSON(next), 0600); err != nil {
			return nil, err
		}
		if _, err := e.event("arena-orchestrator", "workflow.config-updated", map[string]any{"requestId": update.RequestID, "parent": update.Parent, "agentId": update.Agent.Id, "revision": revision, "configHash": Hash(JSON(next.Workflow)), "transaction": update.Transaction}); err != nil {
			return nil, err
		}
		if err := Atomic(filepath.Join(e.Project.RunDir(e.Run.ID), "current-config.json"), SafeJSON(next), 0600); err != nil {
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
