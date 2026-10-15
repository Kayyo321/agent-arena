package arena

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func fixture(t *testing.T) (Workflow, map[string]Profile, Settings) {
	t.Helper()
	base := "../../examples/project/.arena"
	var w Workflow
	var s Settings
	load := func(path string, v any) {
		b, e := os.ReadFile(filepath.Join(base, path))
		if e != nil {
			t.Fatal(e)
		}
		if e := json.Unmarshal(b, v); e != nil {
			t.Fatal(e)
		}
	}
	load("code-review.arena.json", &w)
	load("settings.arena.json", &s)
	profiles := map[string]Profile{}
	for _, a := range w.Agents {
		var p Profile
		load(a.Profile, &p)
		profiles[a.Id] = p
	}
	return w, profiles, s
}
func TestGraphFixtureAndInvalidGraphs(t *testing.T) {
	w, p, s := fixture(t)
	if e := ValidateWorkflow(w, p, s); e != nil {
		t.Fatal(e)
	}
	tests := map[string]func(*Workflow){"unreachable": func(w *Workflow) { w.Relationships = nil; w.Relationships = []Relationship{} }, "duplicate ID": func(w *Workflow) { w.Agents[1].Id = w.Agents[0].Id }, "missing runtime": func(w *Workflow) { w.Agents[0].Runtime.Model = "" }, "on-call as head": func(w *Workflow) {
		for _, a := range w.Agents {
			if a.Activation == "on-call" {
				w.HeadAgent = a.Id
			}
		}
	}, "profile escape": func(w *Workflow) { w.Agents[0].Profile = "../../outside.json" }, "team member edge": func(w *Workflow) {
		w.Relationships = append(w.Relationships, Relationship{Type: "delegates", From: w.HeadAgent, To: w.Teams[0].Members[0]})
	}, "router cycle": func(w *Workflow) {
		w.Relationships = append(w.Relationships, Relationship{Type: "delegates", From: "implementer", To: w.HeadAgent})
	}, "limits": func(w *Workflow) { w.Settings.MaxConcurrentAgents = 1000 }}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			copy := convert[Workflow](w)
			mutate(&copy)
			if ValidateWorkflow(copy, p, s) == nil {
				t.Fatal("invalid graph accepted")
			}
		})
	}
}
func TestLocalEndpointBoundary(t *testing.T) {
	for _, endpoint := range []string{"https://example.com", "http://evil.test", "http://127.0.0.1@evil.test", "http://127.0.0.1/?key=secret"} {
		if _, e := LocalEndpoint(Runtime{Options: map[string]any{"backend": "ollama", "endpoint": endpoint}}); e == nil {
			t.Fatalf("accepted %s", endpoint)
		}
	}
}
