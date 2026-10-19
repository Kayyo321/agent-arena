package arena

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
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

// OpenCode takes a provider/model ID and an optional effort, and no options: its
// CLI owns sign-in, and it has no command sandbox for commandApproval to opt into.
func TestValidateRuntimeOpenCode(t *testing.T) {
	for _, tc := range []struct {
		name    string
		runtime Runtime
		refused string // empty when the runtime is valid
	}{
		{"model only", Runtime{Provider: "opencode", Model: "opencode/big-pickle"}, ""},
		{"effort is optional and opaque", Runtime{Provider: "opencode", Model: "opencode/cat-jam-free", ReasoningEffort: "xhigh"}, ""},
		{"empty options", Runtime{Provider: "opencode", Model: "amazon-bedrock/amazon.nova-2-lite-v1:0", Options: map[string]any{}}, ""},
		{"no model", Runtime{Provider: "opencode"}, "provider and model must be explicitly selected"},
		{"model without a provider", Runtime{Provider: "opencode", Model: "big-pickle"}, "provider/model"},
		{"flag as model", Runtime{Provider: "opencode", Model: "-m/x"}, "must not start with -"},
		{"flag as effort", Runtime{Provider: "opencode", Model: "opencode/big-pickle", ReasoningEffort: "--auto"}, "must not start with -"},
		{"authMethod", Runtime{Provider: "opencode", Model: "opencode/big-pickle", Options: map[string]any{"authMethod": "api-key-or-cloud"}}, "takes no runtime options"},
		{"unknown option", Runtime{Provider: "opencode", Model: "opencode/big-pickle", Options: map[string]any{"backend": "ollama"}}, "takes no runtime options"},
		{"effort belongs in reasoningEffort", Runtime{Provider: "opencode", Model: "opencode/big-pickle", Options: map[string]any{"effort": "high"}}, "reasoningEffort"},
		// The generic commandApproval message says the provider has its own
		// command sandbox, which is untrue for OpenCode.
		{"commandApproval", Runtime{Provider: "opencode", Model: "opencode/big-pickle", Options: map[string]any{"commandApproval": antigravityBlanketApproval}}, "takes no runtime options"},
		{"secret-like option", Runtime{Provider: "opencode", Model: "opencode/big-pickle", Options: map[string]any{"apiKey": "sk-live-abcdefghijklmnopqrstuvwxyz123456"}}, "secret-like"},
		{"other providers still fail closed", Runtime{Provider: "opencode-cloud", Model: "opencode/big-pickle"}, "no installed adapter"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateRuntime(tc.runtime)
			switch {
			case tc.refused == "" && err != nil:
				t.Fatalf("refused a valid runtime: %v", err)
			case tc.refused != "" && (err == nil || !strings.Contains(err.Error(), tc.refused)):
				t.Fatalf("error %v, want one containing %q", err, tc.refused)
			case err != nil && strings.Contains(err.Error(), "own command sandbox"):
				t.Fatalf("OpenCode has no command sandbox: %v", err)
			}
		})
	}
}
