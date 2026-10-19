package arena

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"google.golang.org/genai"
)

// These tests feed each provider's usage in the shape its interface sends, with
// a cache-heavy request, and check that every input token is counted once.
//
// Where the shapes came from, since none of this is a captured live run:
//   - Claude Code: the key names and magnitudes of a Claude Code session's own
//     usage objects (input_tokens in single digits, cache reads in the hundreds of
//     thousands) and the stream-json frame layout Arena already parses.
//   - Codex: the app-server protocol schema from `codex app-server
//     generate-json-schema` (codex-cli 0.163.0): ThreadTokenUsage{total, last} of
//     TokenUsageBreakdown. The fake app server below speaks that protocol.
//   - Gemini: the Gemini API's wire JSON, parsed by the genai SDK into
//     GenerateContentResponseUsageMetadata, whose field documentation defines
//     prompt, cached, tool-use and thoughts counts.
//   - Local models: Ollama's /api/chat final frame (prompt_eval_count, eval_count).
//   - OpenCode: the step-finish part of `opencode run --format json`, read from
//     the 1.19.3 binary's bundled source: tokens {input, output, reasoning,
//     cache: {read, write}}. No live run has confirmed these field names.
//
// The values are made up; a live capture of each provider's usage is still owed
// (see docs/VALIDATION.md).

// reported collects the maps an invocation passes to Invocation.Usage.
type reported struct{ maps []map[string]any }

func (r *reported) usage(m map[string]any) error {
	r.maps = append(r.maps, m)
	return nil
}

// total sums what one invocation reported, the way the journal's roll-up does.
func (r *reported) total(agent string) RunUsage {
	events := []Event{{AgentID: agent, Type: "agent.started"}}
	for _, m := range r.maps {
		// The journal stores the map as JSON, so numbers come back as json.Number.
		var stored map[string]any
		if err := DecodeJSON(JSON(m), &stored); err != nil {
			panic(err)
		}
		events = append(events, usageEvent(agent, stored))
	}
	return SumUsage(events)
}

func requireUsage(t *testing.T, got RunUsage, in, out int64) {
	t.Helper()
	if !got.Reported || got.InputTokens != in || got.OutputTokens != out {
		t.Fatalf("counted %d in / %d out (reported %v), want %d in / %d out", got.InputTokens, got.OutputTokens, got.Reported, in, out)
	}
}

// A Claude Code run streams a usage object on every assistant message and ends
// with a result whose usage is the cumulative total. Arena reads only the
// result, and input_tokens excludes the cache categories, which add to input.
func TestUsageClaudeCodeCacheHeavyRunCountsTheResultOnce(t *testing.T) {
	fakeCLI(t, "claude", `cat <<'EOF'
{"type":"system","subtype":"init","mcp_servers":[]}
{"type":"assistant","message":{"id":"msg_1","role":"assistant","content":[{"type":"text","text":"Reading the code."}],"usage":{"input_tokens":3,"cache_creation_input_tokens":22430,"cache_read_input_tokens":0,"output_tokens":150}}}
{"type":"assistant","message":{"id":"msg_2","role":"assistant","content":[{"type":"text","text":"Done."}],"usage":{"input_tokens":2,"cache_creation_input_tokens":5135,"cache_read_input_tokens":308401,"output_tokens":5715,"output_tokens_details":{"thinking_tokens":5518}}}}
{"type":"result","subtype":"success","is_error":false,"result":"{}","structured_output":{"summary":"fixture"},"usage":{"input_tokens":5,"cache_creation_input_tokens":27565,"cache_read_input_tokens":308401,"output_tokens":5865,"output_tokens_details":{"thinking_tokens":5518},"server_tool_use":{"web_search_requests":0,"web_fetch_requests":0},"service_tier":"standard","cache_creation":{"ephemeral_5m_input_tokens":0,"ephemeral_1h_input_tokens":27565}},"modelUsage":{"claude-fixture":{"inputTokens":5,"outputTokens":5865,"cacheReadInputTokens":308401,"cacheCreationInputTokens":27565,"costUSD":0.5}}}
EOF
`)
	var r reported
	in := Invocation{Runtime: Runtime{Provider: "claude-code", Model: "fixture", ReasoningEffort: "low", Options: map[string]any{"authMethod": claudeSignInAuth}}, Dir: t.TempDir(), Prompt: "x", Usage: r.usage}
	if _, err := invokeClaudeNative(context.Background(), in); err != nil {
		t.Fatal(err)
	}
	if len(r.maps) != 1 {
		t.Fatalf("%d usage reports; only the result's cumulative usage may be reported, not each message's", len(r.maps))
	}
	// input_tokens 5 + cache creation 27565 + cache reads 308401. The thinking
	// tokens (inside output_tokens), the cache_creation ephemeral breakdown (inside
	// cache_creation_input_tokens) and modelUsage (a sibling of usage) add nothing.
	requireUsage(t, r.total("builder"), 5+27565+308401, 5865)
	// The cache categories stay in the report, which is what the journal stores.
	for _, key := range []string{"cache_read_input_tokens", "cache_creation_input_tokens", "cache_creation"} {
		if _, ok := r.maps[0][key]; !ok {
			t.Fatalf("the report dropped %s: %v", key, r.maps[0])
		}
	}
}

// Codex streams the thread's running totals on every update; input includes the
// cached part, and reasoning tokens are inside the output count.
func TestUsageCodexCacheHeavyStreamedTotalsCountOnce(t *testing.T) {
	fakeCLIReading(t, "codex", ":", `while IFS= read -r line; do
  id=$(printf '%s' "$line" | sed -n 's/^{"id":\([0-9]*\),.*/\1/p')
  case "$line" in
    *'"method":"initialize"'*) printf '{"id":%s,"result":{}}\n' "$id";;
    *'"method":"thread/start"'*) printf '{"id":%s,"result":{"thread":{"id":"th1"}}}\n' "$id";;
    *'"method":"turn/start"'*)
      printf '{"id":%s,"result":{"turn":{"id":"tu1"}}}\n' "$id"
      cat <<'EOF'
{"method":"thread/tokenUsage/updated","params":{"threadId":"th1","turnId":"tu1","tokenUsage":{"total":{"totalTokens":9100,"inputTokens":9000,"cachedInputTokens":8500,"cacheWriteInputTokens":0,"outputTokens":100,"reasoningOutputTokens":60},"last":{"totalTokens":9100,"inputTokens":9000,"cachedInputTokens":8500,"cacheWriteInputTokens":0,"outputTokens":100,"reasoningOutputTokens":60},"modelContextWindow":272000}}}
{"method":"thread/tokenUsage/updated","params":{"threadId":"th1","turnId":"tu1","tokenUsage":{"total":{"totalTokens":23200,"inputTokens":23000,"cachedInputTokens":22000,"cacheWriteInputTokens":0,"outputTokens":200,"reasoningOutputTokens":120},"last":{"totalTokens":14100,"inputTokens":14000,"cachedInputTokens":13500,"cacheWriteInputTokens":0,"outputTokens":100,"reasoningOutputTokens":60},"modelContextWindow":272000}}}
{"method":"thread/tokenUsage/updated","params":{"threadId":"th1","turnId":"tu1","tokenUsage":{"total":{"totalTokens":41300,"inputTokens":41000,"cachedInputTokens":40000,"cacheWriteInputTokens":0,"outputTokens":300,"reasoningOutputTokens":180},"last":{"totalTokens":18100,"inputTokens":18000,"cachedInputTokens":18000,"cacheWriteInputTokens":0,"outputTokens":100,"reasoningOutputTokens":60},"modelContextWindow":272000}}}
{"method":"item/completed","params":{"threadId":"th1","turnId":"tu1","item":{"type":"agentMessage","id":"i1","text":"{\"summary\":\"fixture\"}","phase":"final_answer"}}}
{"method":"turn/completed","params":{"threadId":"th1","turn":{"id":"tu1","status":"completed"}}}
EOF
      ;;
  esac
done
`)
	var r reported
	in := Invocation{Runtime: Runtime{Provider: "codex", Model: "fixture", ReasoningEffort: "low"}, Dir: t.TempDir(), Prompt: "x", Schema: objectSchema(map[string]any{"summary": map[string]any{"type": "string"}}), Usage: r.usage}
	out, err := invokeCodex(context.Background(), in)
	if err != nil || out["summary"] != "fixture" {
		t.Fatalf("codex run: %v %v", out, err)
	}
	if len(r.maps) != 3 {
		t.Fatalf("%d usage reports, want the 3 streamed updates", len(r.maps))
	}
	// total is cumulative, so the last update is the whole turn: 41000 input
	// (40000 of it cached) and 300 output (180 of it reasoning). Not their sum,
	// and not the cached or reasoning counts added on top.
	requireUsage(t, r.total("builder"), 41000, 300)
	if _, ok := r.maps[2]["tokenUsage"].(map[string]any)["total"].(map[string]any)["cachedInputTokens"]; !ok {
		t.Fatal("the report dropped the cached-input category")
	}
}

// The Gemini API repeats a running usageMetadata on each streamed chunk. The
// prompt count includes cached content; tool-use prompt tokens and thinking
// tokens are separate counts that its total adds on. Two tool-loop turns add.
func TestUsageGeminiCacheHeavyChunksAndTurns(t *testing.T) {
	chunk := func(usage string) map[string]any {
		var response genai.GenerateContentResponse
		wire := fmt.Sprintf(`{"candidates":[{"content":{"parts":[{"text":"x"}],"role":"model"}}],"usageMetadata":%s,"modelVersion":"fixture"}`, usage)
		if err := json.Unmarshal([]byte(wire), &response); err != nil || response.UsageMetadata == nil {
			t.Fatalf("wire chunk: %v", err)
		}
		return convert[map[string]any](response.UsageMetadata)
	}
	var r reported
	firstTurn := []chatMessage{{}}
	secondTurn := []chatMessage{{}, {}, {}}
	for _, usage := range []string{
		`{"promptTokenCount":12034,"cachedContentTokenCount":11008,"candidatesTokenCount":6,"totalTokenCount":12040,"promptTokensDetails":[{"modality":"TEXT","tokenCount":12034}],"cacheTokensDetails":[{"modality":"TEXT","tokenCount":11008}]}`,
		`{"promptTokenCount":12034,"cachedContentTokenCount":11008,"candidatesTokenCount":60,"thoughtsTokenCount":150,"toolUsePromptTokenCount":800,"totalTokenCount":13044}`,
		`{"promptTokenCount":12034,"cachedContentTokenCount":11008,"candidatesTokenCount":120,"thoughtsTokenCount":400,"toolUsePromptTokenCount":800,"totalTokenCount":13354,"candidatesTokensDetails":[{"modality":"TEXT","tokenCount":120}]}`,
	} {
		r.maps = append(r.maps, turnUsage(chunk(usage), firstTurn))
	}
	r.maps = append(r.maps, turnUsage(chunk(`{"promptTokenCount":13200,"cachedContentTokenCount":12000,"candidatesTokenCount":60,"thoughtsTokenCount":90,"totalTokenCount":13350}`), secondTurn))
	// Turn 0 ends at 12034 + 800 tool-use in and 120 + 400 thoughts out; turn 1
	// at 13200 in and 60 + 90 out. Cached content is inside the prompt counts.
	requireUsage(t, r.total("builder"), (12034+800)+13200, (120+400)+(60+90))
	if _, ok := r.maps[2]["cachedContentTokenCount"]; !ok {
		t.Fatal("the report dropped the cached-content category")
	}
}

// A local model's tool loop calls the server once per turn, and each call's
// prompt count includes the conversation so far. Every turn is billed work, so
// they add up; keeping only the largest would hide all the earlier ones.
func TestUsageLocalToolLoopAddsEveryTurn(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/chat" {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		if calls.Add(1) == 1 {
			fmt.Fprintln(w, `{"message":{"content":"{\"action\":\"list_files\",\"arguments\":{\"path\":\".\"}}"},"done":false}`)
			fmt.Fprintln(w, `{"model":"fixture-model","message":{"content":""},"done":true,"done_reason":"stop","prompt_eval_count":1500,"eval_count":40}`)
			return
		}
		fmt.Fprintln(w, `{"message":{"content":"{\"action\":\"complete\",\"result\":{\"summary\":\"fixture\"}}"},"done":false}`)
		fmt.Fprintln(w, `{"model":"fixture-model","message":{"content":""},"done":true,"done_reason":"stop","prompt_eval_count":1720,"eval_count":55}`)
	}))
	defer server.Close()
	var r reported
	in := Invocation{Runtime: Runtime{Provider: "local", Model: "fixture-model", Options: map[string]any{"backend": "ollama", "endpoint": server.URL}}, Dir: t.TempDir(), Prompt: "x", Schema: objectSchema(map[string]any{"summary": map[string]any{"type": "string"}}), Usage: r.usage}
	if out, err := invokeLocal(context.Background(), in); err != nil || out["summary"] != "fixture" {
		t.Fatalf("local run: %v %v", out, err)
	}
	if len(r.maps) != 2 || r.maps[0][turnKey] != 0 || r.maps[1][turnKey] != 1 {
		t.Fatalf("usage reports %v; want one per turn, tagged 0 and 1", r.maps)
	}
	requireUsage(t, r.total("builder"), 1500+1720, 40+55)
}

// The same holds for Claude through an API key: one `claude -p` call per turn,
// each with its own cumulative usage, and the cache categories on each.
func TestUsageClaudeAPIRouteAddsEveryTurnWithItsCacheTokens(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "fixture-not-a-key")
	count := filepath.Join(t.TempDir(), "calls")
	fakeCLI(t, "claude", `n=$(cat `+count+` 2>/dev/null || echo 0); n=$((n+1)); echo $n > `+count+`
if [ "$n" -eq 1 ]; then
cat <<'EOF'
{"type":"result","subtype":"success","is_error":false,"result":"{\"action\":\"list_files\",\"arguments\":{\"path\":\".\"}}","usage":{"input_tokens":4,"cache_creation_input_tokens":9000,"cache_read_input_tokens":0,"output_tokens":30}}
EOF
else
cat <<'EOF'
{"type":"result","subtype":"success","is_error":false,"result":"{\"action\":\"complete\",\"result\":{\"summary\":\"fixture\"}}","usage":{"input_tokens":3,"cache_creation_input_tokens":200,"cache_read_input_tokens":9000,"output_tokens":45}}
EOF
fi
`)
	var r reported
	in := Invocation{Runtime: Runtime{Provider: "claude-code", Model: "fixture", ReasoningEffort: "low"}, Dir: t.TempDir(), Prompt: "x", Schema: objectSchema(map[string]any{"summary": map[string]any{"type": "string"}}), Usage: r.usage}
	if out, err := invokeClaude(context.Background(), in); err != nil || out["summary"] != "fixture" {
		t.Fatalf("claude run: %v %v", out, err)
	}
	requireUsage(t, r.total("builder"), (4+9000)+(3+200+9000), 30+45)
}

// The roll-up sums for a token budget and a /usage panel; what a price needs
// (cache reads, cache writes, thinking tokens) stays in each usage.reported
// event as the provider sent it. Arena derives no cost.
func TestUsageJournalKeepsEveryCategoryTheProviderSent(t *testing.T) {
	p := committedProject(t)
	r := testRun(t, p)
	claude := map[string]any{"input_tokens": 5, "cache_creation_input_tokens": 27565, "cache_read_input_tokens": 308401, "output_tokens": 5865, "output_tokens_details": map[string]any{"thinking_tokens": 5518}, "cache_creation": map[string]any{"ephemeral_1h_input_tokens": 27565}}
	engine := NewEngine(p, r, testInvoker(func(ctx context.Context, in Invocation) (map[string]any, error) {
		if err := in.Usage(claude); err != nil {
			return nil, err
		}
		return ordinaryResult(), nil
	}))
	if err := engine.Execute(context.Background(), false, false); err != nil {
		t.Fatal(err)
	}
	events := eventsOf(t, p, r.ID, "usage.reported")
	if len(events) != 1 {
		t.Fatalf("%d usage events", len(events))
	}
	stored, _ := events[0].Data["usage"].(map[string]any)
	if strings.Join(sortedKeys(stored), ",") != strings.Join(sortedKeys(claude), ",") {
		t.Fatalf("the journal kept %v, the provider sent %v", sortedKeys(stored), sortedKeys(claude))
	}
	all, _ := p.Events(r.ID)
	requireUsage(t, SumUsage(all), 5+27565+308401, 5865)
}

// OpenCode ends every model call of a run with a step-finish whose tokens count
// that call alone. Each step reports under its own turn key, so the steps add up
// instead of the largest hiding the rest; input and output are summed, and the
// reasoning and cache counts stay in the report without being added again.
func TestUsageOpenCodeStepsAddUp(t *testing.T) {
	openCodeFake(t, `cat <<'EOF'
{"type":"step_start","timestamp":1,"sessionID":"ses_1","part":{"type":"step-start"}}
{"type":"step_finish","timestamp":2,"sessionID":"ses_1","part":{"type":"step-finish","reason":"tool-calls","cost":0,"tokens":{"input":120,"output":30,"reasoning":10,"cache":{"read":9000,"write":0}}}}
{"type":"step_finish","timestamp":3,"sessionID":"ses_1","part":{"type":"step-finish","reason":"tool-calls","cost":0,"tokens":{"input":40,"output":20,"reasoning":0,"cache":{"read":9500,"write":300}}}}
{"type":"text","timestamp":4,"sessionID":"ses_1","part":{"type":"text","text":"{\"summary\":\"fixture\"}","time":{"start":3,"end":4}}}
{"type":"step_finish","timestamp":5,"sessionID":"ses_1","part":{"type":"step-finish","reason":"stop","cost":0,"tokens":{"total":9990,"input":5,"output":7,"reasoning":0,"cache":{"read":9900,"write":0}}}}
EOF
`)
	var r reported
	in := openCodeInvocation(t)
	in.Usage = r.usage
	if out, err := invokeOpenCode(context.Background(), in); err != nil || out["summary"] != "fixture" {
		t.Fatalf("opencode run: %v %v", out, err)
	}
	if len(r.maps) != 3 {
		t.Fatalf("%d usage reports, want one per step", len(r.maps))
	}
	requireUsage(t, r.total("builder"), 120+40+5, 30+20+7)
	for i, m := range r.maps {
		if _, ok := m["reasoning"]; !ok {
			t.Fatalf("report %d dropped reasoning: %v", i, m)
		}
		if cache, ok := m["cache"].(map[string]any); !ok || cache["read"] == nil {
			t.Fatalf("report %d dropped the cache counts: %v", i, m)
		}
	}
	// Cache and reasoning counts alone are not a usage count: unreported, not zero.
	if got := (&reported{maps: []map[string]any{{"cache": map[string]any{"read": 5}, "reasoning": 3, turnKey: 1}}}).total("builder"); got.Reported {
		t.Fatalf("cache and reasoning alone are not a usage count: %+v", got)
	}
	// The bare keys come last: another provider's named counts win.
	if in, out, _ := usageTokens(map[string]any{"input_tokens": 7, "input": 99, "output_tokens": 3, "output": 98}); in != 7 || out != 3 {
		t.Fatalf("named keys must win over the bare ones: %d %d", in, out)
	}
}
