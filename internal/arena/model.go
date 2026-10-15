package arena

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	c "github.com/Kayyo321/agent-arena/internal/contract"
)

type Workflow = c.Workflow
type Agent = c.WorkflowAgentNode
type Runtime = c.Runtime
type Profile = c.Profile
type Settings = c.ProjectSettings
type Team = c.WorkflowTeam
type Router = c.WorkflowDecisionRouter
type Relationship = c.WorkflowRelationship
type Trigger = c.WorkflowOnCallTrigger

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

func validID(s string) error {
	if !idPattern.MatchString(s) || s == "." || s == ".." {
		return fmt.Errorf("invalid ID %q (use 1–64 lowercase letters, digits, dots, underscores or hyphens)", s)
	}
	return nil
}
func Int(n int) *int { return &n }
func JSON(v any) []byte {
	b, e := json.MarshalIndent(v, "", "  ")
	if e != nil {
		panic(e)
	}
	return append(b, '\n')
}
func convert[T any](v any) T {
	var t T
	if e := DecodeJSON(JSON(v), &t); e != nil {
		panic(e)
	}
	return t
}
func Has(a []string, s string) bool {
	for _, v := range a {
		if v == s {
			return true
		}
	}
	return false
}
func DefaultSettings(r Runtime) Settings {
	return convert[Settings](map[string]any{
		"schemaVersion": 1, "defaultModel": r,
		"permanentOnCallAgent": map[string]any{"id": "arena-orchestrator", "profile": ".agents/arena-system/arena-orchestrator.agent.json", "runtimeSource": "defaultModel", "skill": "arena-orchestrator", "availability": "permanent-on-call"},
		"approvalPolicies":     map[string]any{"arena-subagent-modification-inquiry": "ask-user", "agent-workflow-edit": "ask-user", "cross-provider-context-sharing": "ask-user", "shell-command-execution": "adapter-default", "change-integration": "human-review", "workflow-deletion": "ask-user"},
		"limits":               map[string]any{"maxConcurrentAgents": 4, "maxTeamRounds": 2, "maxOnCallInvocationsPerRun": 3, "maxCoworkerAdditionsPerRun": 1, "maxAgentRunMinutes": 30, "maxRetriesPerAgent": 1},
		"context":              map[string]any{"defaultHandoffScope": "task-scoped", "maxFilesPerHandoff": 50},
		"sharedContext":        map[string]any{"storage": "git-tracked", "agentWritePolicy": "auto-accept", "agentDeletePolicy": "ask-user", "maxEntries": 500, "maxEntryBytes": 32768, "maxTotalBytes": 2097152, "staleAfterDays": 180},
		"display":              map[string]any{"colorMode": "auto", "animation": "auto", "gitTree": "show", "asciiTitle": "show"},
		"audit":                map[string]any{"retention": "full-local", "redactSecrets": true, "processOutput": "summary"},
	})
}
func NewWorkflow(id, name string) Workflow {
	return convert[Workflow](map[string]any{"schemaVersion": 1, "id": id, "name": name, "headAgent": "", "agents": []any{}, "relationships": []any{}, "settings": map[string]any{"maxConcurrentAgents": 4, "workspace": map[string]any{"writeIsolation": "git-worktree-per-task"}, "integration": map[string]any{"policy": "human-review"}, "audit": map[string]any{"retention": "full-local"}}})
}
func NewProfile(id, role, objective, instructions string, caps []string) Profile {
	p := Profile{SchemaVersion: 1, Id: id, Name: id, Role: role, Objective: objective, Instructions: instructions, Capabilities: caps}
	if role == "review" {
		p.ReviewContract = &c.ProfileReviewContract{Format: "arena.review-verdict.v1", Outcomes: []string{"Approved", "ChangesRequested", "Deny", "NeedsMoreInformation"}}
	}
	return p
}
func ValidateRuntime(r Runtime) error {
	if string(SafeJSON(r.Options)) != string(JSON(r.Options)) {
		return fmt.Errorf("provider options contain secret-like values; configure credentials through the provider environment or credential store")
	}
	if strings.TrimSpace(r.Provider) == "" || strings.TrimSpace(r.Model) == "" {
		return fmt.Errorf("provider and model must be explicitly selected")
	}
	if (r.Provider == "codex" || r.Provider == "claude-code") && r.ReasoningEffort == "" {
		return fmt.Errorf("%s: select reasoning effort explicitly (status shows verifiable choices)", r.Provider)
	}
	switch r.Provider {
	case "codex", "claude-code":
	case "gemini":
		a, _ := r.Options["authMethod"].(string)
		if a != "google-ai-studio-api-key" && a != "vertex-ai" {
			return fmt.Errorf("Gemini requires an explicit API-key or Vertex AI authMethod; personal OAuth is unsupported")
		}
	case "local":
		if !Has([]string{"ollama", "lm-studio", "llama.cpp", "vllm", "openai-compatible"}, str(r.Options["backend"])) {
			return fmt.Errorf("select a supported local backend explicitly")
		}
		if _, e := LocalEndpoint(r); e != nil {
			return e
		}
	default:
		return fmt.Errorf("provider %q has no installed adapter", r.Provider)
	}
	return nil
}
func ValidateSettings(s Settings) error {
	if e := c.ValidateProjectSettings(JSON(s)); e != nil {
		return e
	}
	if e := ValidateRuntime(s.DefaultModel); e != nil {
		return e
	}
	return nil
}
func ValidateWorkflow(w Workflow, profiles map[string]Profile, s Settings) error {
	if e := c.ValidateWorkflow(JSON(w)); e != nil {
		return e
	}
	if e := validID(w.Id); e != nil {
		return e
	}
	if w.Id == "settings" || w.Id == "arena-system" {
		return fmt.Errorf("reserved workflow ID %q", w.Id)
	}
	agents := map[string]Agent{}
	nodes := map[string]int{}
	members := map[string]string{}
	profilePaths := map[string]bool{}
	rules := map[string]bool{}
	add := func(id string, level int) error {
		if e := validID(id); e != nil {
			return e
		}
		if _, ok := nodes[id]; ok {
			return fmt.Errorf("duplicate node ID %q", id)
		}
		nodes[id] = level
		return nil
	}
	for _, a := range w.Agents {
		level := -1
		if a.Level != nil {
			level = *a.Level
		}
		if e := add(a.Id, level); e != nil {
			return e
		}
		agents[a.Id] = a
		if e := ValidateRuntime(a.Runtime); e != nil {
			return fmt.Errorf("agent %s: %w", a.Id, e)
		}
		expected := filepath.ToSlash(filepath.Join(".agents", w.Id, a.Id+".agent.json"))
		if a.Profile != expected {
			return fmt.Errorf("agent %s: profile must be %s", a.Id, expected)
		}
		if profilePaths[a.Profile] {
			return fmt.Errorf("duplicate profile %s", a.Profile)
		}
		profilePaths[a.Profile] = true
		p, ok := profiles[a.Id]
		if !ok {
			return fmt.Errorf("agent %s: missing profile", a.Id)
		}
		if p.Id != a.Id {
			return fmt.Errorf("profile ID does not match agent %s", a.Id)
		}
		if Redact(string(JSON(p))) != string(JSON(p)) {
			return fmt.Errorf("profile %s contains secret-like values", a.Id)
		}
		if e := c.ValidateProfile(JSON(p)); e != nil {
			return fmt.Errorf("profile %s: %w", a.Id, e)
		}
	}
	for _, t := range w.Teams {
		if e := add(t.Id, -1); e != nil {
			return e
		}
		level := -1
		if t.Consensus.MaxRounds > s.Limits.MaxTeamRounds {
			return fmt.Errorf("team %s exceeds project maxTeamRounds", t.Id)
		}
		if !Has(t.Members, t.Facilitator) {
			return fmt.Errorf("team %s facilitator is not a member", t.Id)
		}
		for _, id := range t.Members {
			a, ok := agents[id]
			if !ok || a.Activation == "on-call" || a.Level == nil {
				return fmt.Errorf("team %s has invalid member %s", t.Id, id)
			}
			if _, ok := members[id]; ok {
				return fmt.Errorf("agent %s belongs to multiple teams", id)
			}
			members[id] = t.Id
			if level < 0 {
				level = *a.Level
			}
			if level != *a.Level {
				return fmt.Errorf("team %s members need the same level", t.Id)
			}
		}
		nodes[t.Id] = level
	}
	for _, r := range w.DecisionRouters {
		if e := add(r.Id, -1); e != nil {
			return e
		}
	}
	head, ok := agents[w.HeadAgent]
	if !ok || head.Activation == "on-call" || members[w.HeadAgent] != "" {
		return fmt.Errorf("head must be a standalone path agent")
	}
	edges := map[string][]string{}
	seenEdges := map[string]bool{}
	edge := func(from, to string) error {
		if _, ok := nodes[from]; !ok {
			return fmt.Errorf("edge source %s does not exist", from)
		}
		if _, ok := nodes[to]; !ok {
			return fmt.Errorf("edge target %s does not exist", to)
		}
		if members[from] != "" || members[to] != "" || agents[from].Activation == "on-call" || agents[to].Activation == "on-call" {
			return fmt.Errorf("ordinary edges cannot reference team members or on-call agents (%s -> %s)", from, to)
		}
		key := from + "/" + to
		if seenEdges[key] {
			return fmt.Errorf("duplicate delegation edge %s -> %s", from, to)
		}
		seenEdges[key] = true
		if nodes[from] >= 0 && nodes[to] >= 0 && nodes[to] <= nodes[from] {
			return fmt.Errorf("delegation %s -> %s must advance levels", from, to)
		}
		edges[from] = append(edges[from], to)
		return nil
	}
	routers := map[string]bool{}
	for _, r := range w.DecisionRouters {
		routers[r.Id] = true
	}
	for _, e := range w.Relationships {
		if e.Type == "delegates" {
			if routers[e.From] {
				return fmt.Errorf("router %s outgoing edges must use its route table", e.From)
			}
			if err := edge(e.From, e.To); err != nil {
				return err
			}
		} else {
			a, aok := agents[e.From]
			b, bok := agents[e.To]
			if !aok || !bok || a.Activation == "on-call" || b.Activation == "on-call" || members[a.Id] != "" || members[b.Id] != "" || a.Id == b.Id {
				return fmt.Errorf("peer links require two standalone path agents")
			}
		}
	}
	for _, r := range w.DecisionRouters {
		matches := map[string]bool{}
		ids := map[string]bool{}
		targets := map[string]bool{}
		if _, e := Pointer(map[string]any{}, r.PayloadPointer); e != nil {
			return fmt.Errorf("router %s: %w", r.Id, e)
		}
		for _, route := range r.Routes {
			k := scalarKey(route.Equals)
			if matches[k] || ids[route.Id] {
				return fmt.Errorf("router %s duplicate match value or route ID", r.Id)
			}
			matches[k] = true
			ids[route.Id] = true
			if !targets[route.To] {
				if e := edge(r.Id, route.To); e != nil {
					return e
				}
				targets[route.To] = true
			}
		}
		if r.Fallback.Action == "route" && !targets[r.Fallback.To] {
			if e := edge(r.Id, r.Fallback.To); e != nil {
				return e
			}
		}
	}
	for _, rule := range w.OnCall {
		a, ok := agents[rule.AgentId]
		if !ok || a.Activation != "on-call" || rules[a.Id] {
			return fmt.Errorf("on-call rule must reference one distinct on-call agent")
		}
		rules[a.Id] = true
		if rule.MaxInvocationsPerRun > s.Limits.MaxOnCallInvocationsPerRun {
			return fmt.Errorf("on-call %s exceeds project invocation limit", a.Id)
		}
		for _, t := range rule.Triggers {
			switch t.Event {
			case "team.no-consensus":
				if _, ok := nodes[t.TeamId]; !ok || !isTeam(w, t.TeamId) {
					return fmt.Errorf("trigger references missing team %s", t.TeamId)
				}
			case "agent.failed":
				if _, ok := agents[t.AgentId]; !ok {
					return fmt.Errorf("trigger references missing agent %s", t.AgentId)
				}
			}
		}
	}
	for _, a := range w.Agents {
		if a.Activation == "on-call" && !rules[a.Id] {
			return fmt.Errorf("on-call agent %s has no trigger rule", a.Id)
		}
	}
	visiting := map[string]bool{}
	visited := map[string]bool{}
	var walk func(string) error
	walk = func(id string) error {
		if visiting[id] {
			return fmt.Errorf("delegation cycle at %s", id)
		}
		if visited[id] {
			return nil
		}
		visiting[id] = true
		for _, to := range edges[id] {
			if e := walk(to); e != nil {
				return e
			}
		}
		visiting[id] = false
		visited[id] = true
		return nil
	}
	if e := walk(w.HeadAgent); e != nil {
		return e
	}
	for id := range nodes {
		if agents[id].Activation == "on-call" || members[id] != "" {
			continue
		}
		if !visited[id] {
			return fmt.Errorf("path node %s is unreachable from head %s", id, w.HeadAgent)
		}
	}
	if w.Settings.MaxConcurrentAgents > s.Limits.MaxConcurrentAgents {
		return fmt.Errorf("workflow concurrency exceeds project limit")
	}
	if s.ApprovalPolicies.ChangeIntegration == "human-review" && w.Settings.Integration.Policy != "human-review" {
		return fmt.Errorf("workflow cannot loosen project integration policy")
	}
	if _, e := EffectiveContext(s, w); e != nil {
		return e
	}
	return nil
}
func isTeam(w Workflow, id string) bool {
	for _, t := range w.Teams {
		if t.Id == id {
			return true
		}
	}
	return false
}
func str(v any) string { s, _ := v.(string); return s }

func Pointer(v any, p string) (any, error) { value, _, err := LookupPointer(v, p); return value, err }
func LookupPointer(v any, p string) (any, bool, error) {
	if !strings.HasPrefix(p, "/") {
		return nil, false, fmt.Errorf("JSON pointer must start with /")
	}
	parts := strings.Split(p[1:], "/")
	for i, part := range parts {
		if regexp.MustCompile(`~[^01]|~$`).MatchString(part) {
			return nil, false, fmt.Errorf("invalid JSON pointer escape")
		}
		parts[i] = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
	}
	for _, part := range parts {
		switch current := v.(type) {
		case map[string]any:
			var ok bool
			v, ok = current[part]
			if !ok {
				return nil, false, nil
			}
		case []any:
			n, err := strconv.Atoi(part)
			if err != nil || n < 0 || n >= len(current) || strconv.Itoa(n) != part {
				return nil, false, nil
			}
			v = current[n]
		default:
			return nil, false, nil
		}
	}
	return v, true, nil
}

func Required(w []Workflow, s Settings) []Runtime {
	all := []Runtime{s.DefaultModel}
	for _, v := range w {
		for _, a := range v.Agents {
			all = append(all, a.Runtime)
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].Provider+all[i].Model < all[j].Provider+all[j].Model })
	return all
}

func DecodeJSON(b []byte, v any) error {
	decoder := json.NewDecoder(bytes.NewReader(b))
	decoder.UseNumber()
	if err := decoder.Decode(v); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		return fmt.Errorf("trailing JSON data")
	}
	return nil
}
func scalarKey(v any) string {
	switch v.(type) {
	case json.Number, float64, float32, int, int64, uint64:
		raw := strings.ToLower(strings.TrimSpace(string(JSON(v))))
		sign := ""
		if strings.HasPrefix(raw, "-") {
			sign = "-"
			raw = raw[1:]
		}
		mantissa, exponent, hasExponent := strings.Cut(raw, "e")
		if !hasExponent {
			exponent = "0"
		}
		power, ok := new(big.Int).SetString(exponent, 10)
		if !ok {
			return string(JSON(v))
		}
		fraction := 0
		if point := strings.IndexByte(mantissa, '.'); point >= 0 {
			fraction = len(mantissa) - point - 1
			mantissa = mantissa[:point] + mantissa[point+1:]
		}
		mantissa = strings.TrimLeft(mantissa, "0")
		if mantissa == "" {
			return "number:0"
		}
		trimmed := strings.TrimRight(mantissa, "0")
		power.Add(power, big.NewInt(int64(len(mantissa)-len(trimmed)-fraction)))
		return "number:" + sign + trimmed + "e" + power.String()
	}
	return string(JSON(v))
}
