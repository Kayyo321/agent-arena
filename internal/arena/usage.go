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
// Claude Code (camelCase and snake_case), Ollama, Gemini and, last, the bare
// input and output of OpenCode's step-finish tokens. A key that holds anything
// but a number, such as an object, is not a count and is skipped.
var usageFields = [2][]string{
	{"inputTokens", "input_tokens", "promptTokens", "prompt_tokens", "promptTokenCount", "input"},
	{"outputTokens", "output_tokens", "completionTokens", "completion_tokens", "candidatesTokenCount", "output"},
}

// usageBareFields are the last entries of usageFields: the bare input and output
// of OpenCode's step-finish tokens. When one of them supplies the count,
// usageBareExtras adds the counts OpenCode reports beside it.
var usageBareFields = [2]string{"input", "output"}

// Some providers report a category beside, not inside, the headline count, so
// it adds to it; others report a subset of the headline count, which must not
// be added again. Each provider's cache, tool and thinking tokens fall into one
// of the two (sources: the Anthropic Messages usage object, the Codex app-server
// protocol, and the Gemini API's GenerateContentResponseUsageMetadata):
//
//	additive to input:  Claude cache_read_input_tokens, cache_creation_input_tokens
//	                    (input_tokens excludes both); Gemini toolUsePromptTokenCount
//	                    (tool results fed back, not in promptTokenCount)
//	                    OpenCode tokens.cache.read and tokens.cache.write (the bare
//	                    input excludes both)
//	additive to output: Gemini thoughtsTokenCount (not in candidatesTokenCount);
//	                    OpenCode tokens.reasoning (the bare output excludes it)
//	subsets, not added: Codex cachedInputTokens, cacheWriteInputTokens and
//	                    reasoningOutputTokens; Gemini cachedContentTokenCount
//	                    (promptTokenCount includes the cached content); Claude
//	                    output_tokens_details.thinking_tokens and the cache_creation
//	                    ephemeral breakdown; every *TokensDetails modality list
//
// OpenCode's semantics come from the opencode 1.19.3 bundle, not from a live run
// (none exists yet; the sandbox the adapter was written in had no inference
// access). The Session namespace exports getUsage, and the session processor
// writes its result as the step-finish part's tokens. getUsage takes the
// provider's totals, inputTokens (Y) and outputTokens (W), with reasoningTokens (H),
// cacheReadInputTokens (Z) and cacheWriteInputTokens (J), and returns
// input = max(0, Y-Z-J), output = max(0, W-H), reasoning = H and
// cache = {read: Z, write: J}. The bare input and output therefore leave out the
// cache and the reasoning, and adding them back (usageBareExtras) restores the
// provider's own totals, Y and W. This is read from the formula; no recorded
// step-finish from a live run has been compared with it.
//
// The journal keeps each provider's whole usage map, so the categories a price
// needs stay recoverable from the usage.reported events; the roll-up only sums.
var (
	additiveInputFields  = []string{"cache_read_input_tokens", "cache_creation_input_tokens", "cacheReadInputTokens", "cacheCreationInputTokens", "toolUsePromptTokenCount"}
	additiveOutputFields = []string{"thoughtsTokenCount"}
)

// turnKey marks a usage map as belonging to one model call of a tool loop. The
// mediated routes (local models, the Gemini API, Claude through an API key) make
// up to forty calls for one invocation; each call's counts start over and
// include the conversation so far, so the calls add up instead of one hiding the
// others. Arena adds the key; providers do not send it.
const turnKey = "arenaTurn"

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
				if key == usageBareFields[i] {
					n += usageBareExtras(i, usage)
				}
				if i == 0 {
					in = n
				} else {
					out = n
				}
				break
			}
		}
	}
	for i, keys := range [2][]string{additiveInputFields, additiveOutputFields} {
		for _, key := range keys {
			if n, ok := usageCount(usage[key]); ok {
				found[i] = true
				if i == 0 {
					in += n
				} else {
					out += n
				}
			}
		}
	}
	return in, out, found
}

// usageBareExtras are the counts an OpenCode step-finish reports beside its bare
// input (i 0) or output (i 1) that the bare count leaves out: tokens.cache.read and
// tokens.cache.write for input, tokens.reasoning for output (see the comment on
// additiveInputFields). A value that is not a number adds nothing. They are read
// only when the bare key supplied the count, so another provider's named count is
// never topped up, and they do not make a usage map count as reported by
// themselves: found is set by the headline key alone.
func usageBareExtras(i int, usage map[string]any) int64 {
	var extra int64
	if i == 0 {
		cache, _ := usage["cache"].(map[string]any)
		for _, key := range []string{"read", "write"} {
			if n, ok := usageCount(cache[key]); ok {
				extra += n
			}
		}
		return extra
	}
	if n, ok := usageCount(usage["reasoning"]); ok {
		extra = n
	}
	return extra
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
// times within one model call with running totals (Codex, Gemini), so each
// agent.started opens a window and, per model call (turnKey), only the highest
// count per field is kept; the calls of a window add up, and the windows add up
// across retries and loop passes. A final summary that repeats the streamed
// totals therefore never counts twice, and separate calls never hide one another.
func SumUsage(events []Event) RunUsage {
	type counts struct {
		in, out int64
		found   [2]bool
	}
	type window map[int]*counts
	open := map[string]window{}
	closed := map[string][]window{}
	for _, e := range events {
		switch e.Type {
		case "agent.started":
			if w := open[e.AgentID]; w != nil {
				closed[e.AgentID] = append(closed[e.AgentID], w)
			}
			open[e.AgentID] = window{}
		case "usage.reported":
			usage, _ := e.Data["usage"].(map[string]any)
			in, out, found := usageTokens(usage)
			if !found[0] && !found[1] {
				continue
			}
			w := open[e.AgentID]
			if w == nil {
				w = window{}
				open[e.AgentID] = w
			}
			turn, _ := usageCount(usage[turnKey])
			c := w[int(turn)]
			if c == nil {
				c = &counts{}
				w[int(turn)] = c
			}
			c.in, c.out = max(c.in, in), max(c.out, out)
			c.found[0], c.found[1] = c.found[0] || found[0], c.found[1] || found[1]
		}
	}
	u := RunUsage{Agents: map[string]UsageTotals{}}
	for id, w := range open {
		closed[id] = append(closed[id], w)
	}
	for id, windows := range closed {
		var t UsageTotals
		for _, w := range windows {
			for _, c := range w {
				if c.found[0] || c.found[1] {
					t.Reported = true
					t.InputTokens += c.in
					t.OutputTokens += c.out
				}
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
