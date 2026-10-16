package arena

import (
	"encoding/json"
	"fmt"
	"sort"
)

// UsageTotals sums the token counts providers reported. Reported is false
// when no usage event carried a recognised token field, so callers can say
// "unreported" instead of a misleading zero.
type UsageTotals struct {
	Reported     bool  `json:"reported"`
	InputTokens  int64 `json:"inputTokens"`
	OutputTokens int64 `json:"outputTokens"`
}

// RunUsage is the per-run and per-agent roll-up of `usage.reported` events.
// The events keep each provider's opaque map; only the token fields named in
// usageFields are read, and no currency or pricing is derived.
type RunUsage struct {
	UsageTotals
	Agents map[string]UsageTotals `json:"agents,omitempty"`
}

// usageFields lists the key names providers use for each count: Codex and
// Claude Code (camelCase and snake_case), Ollama and Gemini.
var usageFields = [2][]string{
	{"inputTokens", "input_tokens", "promptTokens", "prompt_tokens", "promptTokenCount"},
	{"outputTokens", "output_tokens", "completionTokens", "completion_tokens", "candidatesTokenCount"},
}

// usageTokens reads the input and output counts a provider reported. Codex
// nests its running totals under tokenUsage.total.
func usageTokens(usage map[string]any) (in, out int64, found [2]bool) {
	if nested, ok := usage["tokenUsage"].(map[string]any); ok {
		if total, ok := nested["total"].(map[string]any); ok {
			usage = total
		}
	}
	for i, keys := range usageFields {
		for _, key := range keys {
			if n, ok := usageCount(usage[key]); ok {
				found[i] = true
				if i == 0 {
					in = n
				} else {
					out = n
				}
				break
			}
		}
	}
	return in, out, found
}

func usageCount(v any) (int64, bool) {
	switch n := v.(type) {
	case json.Number:
		i, err := n.Int64()
		return i, err == nil && i >= 0
	case float64:
		return int64(n), n >= 0
	case int:
		return int64(n), n >= 0
	case int64:
		return n, n >= 0
	}
	return 0, false
}

// SumUsage totals the journal's usage events. A provider may report several
// times within one invocation with running totals (Codex, Gemini), so each
// agent.started opens a window and only the highest count per field in that
// window is kept; the windows then add up across retries and loop passes.
func SumUsage(events []Event) RunUsage {
	type window struct {
		in, out int64
		found   [2]bool
	}
	open := map[string]*window{}
	closed := map[string][]window{}
	for _, e := range events {
		switch e.Type {
		case "agent.started":
			if w := open[e.AgentID]; w != nil {
				closed[e.AgentID] = append(closed[e.AgentID], *w)
			}
			open[e.AgentID] = &window{}
		case "usage.reported":
			usage, _ := e.Data["usage"].(map[string]any)
			in, out, found := usageTokens(usage)
			if !found[0] && !found[1] {
				continue
			}
			w := open[e.AgentID]
			if w == nil {
				w = &window{}
				open[e.AgentID] = w
			}
			w.in, w.out = max(w.in, in), max(w.out, out)
			w.found[0], w.found[1] = w.found[0] || found[0], w.found[1] || found[1]
		}
	}
	u := RunUsage{Agents: map[string]UsageTotals{}}
	for id, w := range open {
		closed[id] = append(closed[id], *w)
	}
	for id, windows := range closed {
		var t UsageTotals
		for _, w := range windows {
			if w.found[0] || w.found[1] {
				t.Reported = true
				t.InputTokens += w.in
				t.OutputTokens += w.out
			}
		}
		if t.Reported {
			u.Agents[id] = t
			u.Reported = true
			u.InputTokens += t.InputTokens
			u.OutputTokens += t.OutputTokens
		}
	}
	if len(u.Agents) == 0 {
		u.Agents = nil
	}
	return u
}

// Total is input plus output tokens; budgets compare against it.
func (t UsageTotals) Total() int64 { return t.InputTokens + t.OutputTokens }

// tokenText formats a count compactly: 842, 12.4k, 3.2M.
func tokenText(n int64) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 10_000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	}
	return fmt.Sprint(n)
}

// String reads "12.4k in · 3.1k out", or "unreported" when no provider said.
func (t UsageTotals) String() string {
	if !t.Reported {
		return "unreported"
	}
	return tokenText(t.InputTokens) + " in · " + tokenText(t.OutputTokens) + " out"
}

// Report is the /usage panel: the run total, then each reporting agent, then
// the agents whose providers reported nothing.
func (u RunUsage) Report(agents []string) []string {
	lines := []string{"Run total: " + u.UsageTotals.String(), "Counts are what each provider reported; Arena derives no cost.", ""}
	ids := make([]string, 0, len(agents))
	ids = append(ids, agents...)
	sort.Strings(ids)
	for _, id := range ids {
		lines = append(lines, fmt.Sprintf("%s | %s", id, u.Agents[id]))
	}
	return lines
}
