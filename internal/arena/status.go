package arena

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

var Version = "0.1.0-dev"

type Readiness struct {
	Provider      string   `json:"provider"`
	Model         string   `json:"model"`
	Version       string   `json:"version"`
	State         string   `json:"state"`
	Auth          string   `json:"auth"`
	ModelState    string   `json:"modelState"`
	Efforts       []string `json:"efforts"`
	Detail        string   `json:"detail"`
	Compatibility string   `json:"compatibility"`
	Skill         string   `json:"skill"`
	CanStart      bool     `json:"canStart"`
}

func Probe(ctx context.Context, root string, r Runtime) Readiness {
	result := Readiness{Provider: r.Provider, Model: r.Model, State: "unknown", Auth: "unknown", ModelState: "unknown", Efforts: []string{}, Skill: providerSkillState(root, r.Provider)}
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	if r.Provider == "local" {
		return probeLocal(ctx, r, result)
	}
	cli := map[string]string{"codex": "codex", "claude-code": "claude"}[r.Provider]

	if r.Provider == "gemini" {
		result.Version = "Google GenAI SDK (Arena " + Version + ")"
		client, err := geminiClient(ctx, r)
		if err != nil {
			result.State = "not configured"
			result.Auth = err.Error()
			return result
		}
		model, err := client.Models.Get(ctx, r.Model, nil)
		if err != nil {
			result.State = "unknown"
			result.Auth = "API/Vertex credential or model verification failed"
			result.Detail = "Google rejected model discovery; no personal Gemini CLI OAuth is used"
			return result
		}
		result.Auth = "selected API/Vertex route verified"
		result.ModelState = "reported by Google SDK: " + model.Name
		result.CanStart = r.ReasoningEffort == ""
		if !result.CanStart {
			result.Detail = "reasoningEffort is not a verified Gemini control; use explicit generation.thinkingConfig"
		}
		return result
	}

	if cli == "" {
		result.State = "unsupported"
		result.Detail = "adapter not installed"
		return result
	}
	if _, e := exec.LookPath(cli); e != nil {
		result.State = "missing"
		result.Detail = cli + " is not installed"
		return result
	}
	cmd := exec.CommandContext(ctx, cli, "--version")
	cmd.Stderr = io.Discard
	b, e := cmd.Output()
	if e != nil {
		result.State = "incompatible version"
		result.Detail = "version probe failed"
		return result
	}
	result.Version = strings.TrimSpace(Redact(string(b)))
	result.Compatibility = VersionDrift(r.Provider, result.Version)
	if r.Provider == "claude-code" {
		cmd = exec.CommandContext(ctx, cli, "--help")
		cmd.Stderr = io.Discard
		help, e := cmd.Output()
		if e != nil {
			return result
		}
		if !strings.Contains(string(help), "--output-format") || !strings.Contains(string(help), "--bare") {
			result.State = "incompatible version"
			result.Detail = "requires structured output and JSON Schema CLI flags"
			return result
		}
		if match := regexp.MustCompile(`(?s)--effort[^\(]*\(([^)]+)\)`).FindSubmatch(help); len(match) > 1 {
			result.Efforts = split(string(match[1]))
		}
		if r.ReasoningEffort != "" && len(result.Efforts) > 0 && !Has(result.Efforts, r.ReasoningEffort) {
			result.State = "unsupported effort"
			result.Detail = "effort not accepted by installed Claude CLI"
			return result
		}
		result.Auth = "API-key or supported cloud credentials required"
		if os.Getenv("ANTHROPIC_API_KEY") != "" {
			result.Auth = "API-key environment configured"
			result.CanStart = true
		}
		if os.Getenv("CLAUDE_CODE_USE_BEDROCK") == "1" || os.Getenv("CLAUDE_CODE_USE_VERTEX") == "1" || os.Getenv("CLAUDE_CODE_USE_FOUNDRY") == "1" {
			result.Auth = "supported cloud route configured"
			result.CanStart = true
		}
		result.Detail = "third-party orchestration requires API/cloud auth; subscription OAuth is not used"
		return result
	}
	rpc, e := StartRPC(ctx, root)
	if e != nil {
		result.Detail = Redact(e.Error())
		return result
	}
	defer rpc.Close()
	account, e := rpc.Call(ctx, "account/read", map[string]any{"refreshToken": false}, nil)
	if e != nil {
		result.Detail = e.Error()
		return result
	}
	var a struct {
		Account            map[string]any `json:"account"`
		RequiresOpenAIAuth bool           `json:"requiresOpenaiAuth"`
	}
	if e = json.Unmarshal(account, &a); e != nil {
		result.Detail = "unrecognized account response"
		return result
	}
	if a.Account == nil && a.RequiresOpenAIAuth {
		result.Auth = "signed out"
		result.State = "signed out"
		return result
	}
	result.Auth = "Codex-owned authentication ready"
	cursor := ""
	for pages := 0; pages < 20; pages++ {
		params := map[string]any{"limit": 100, "includeHidden": false}
		if cursor != "" {
			params["cursor"] = cursor
		}
		models, e := rpc.Call(ctx, "model/list", params, nil)
		if e != nil {
			result.Detail = e.Error()
			return result
		}
		var m struct {
			Data []struct {
				ID      string `json:"id"`
				Model   string `json:"model"`
				Efforts []struct {
					Effort string `json:"reasoningEffort"`
				} `json:"supportedReasoningEfforts"`
			} `json:"data"`
			NextCursor *string `json:"nextCursor"`
		}
		if e = json.Unmarshal(models, &m); e != nil {
			result.Detail = "unrecognized model catalog"
			return result
		}
		for _, model := range m.Data {
			if model.Model == r.Model || model.ID == r.Model {
				result.ModelState = "advertised; inference access unverified"
				for _, eff := range model.Efforts {
					result.Efforts = append(result.Efforts, eff.Effort)
				}
				result.CanStart = r.ReasoningEffort == "" || len(result.Efforts) == 0 || Has(result.Efforts, r.ReasoningEffort)
				if !result.CanStart {
					result.Detail = "configured effort is absent from the installed CLI catalog"
				} else {
					result.Detail = "catalog discovery does not prove subscription model access"
				}
				return result
			}
		}
		if m.NextCursor == nil {
			break
		}
		cursor = *m.NextCursor
	}
	result.State = "unavailable model"
	result.Detail = "selected model is absent from the installed CLI catalog"
	return result
}

var localClient = &http.Client{Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return fmt.Errorf("local backend redirects are disabled") }}

func localGET(ctx context.Context, endpoint string, v any) error {
	req, e := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if e != nil {
		return e
	}
	resp, e := localClient.Do(req)
	if e != nil {
		return e
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("backend HTTP %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(v)
}
func probeLocal(ctx context.Context, r Runtime, result Readiness) Readiness {
	endpoint, e := LocalEndpoint(r)
	if e != nil {
		result.State = "unsupported"
		result.Detail = e.Error()
		return result
	}
	result.Version = "local " + str(r.Options["backend"])
	result.Auth = "same-machine backend"
	ids := []string{}
	if r.Options["backend"] == "ollama" {
		var response struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		e = localGET(ctx, endpoint+"/api/tags", &response)
		for _, v := range response.Models {
			ids = append(ids, v.Name)
		}
	} else {
		var response struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		e = localGET(ctx, endpoint+"/models", &response)
		for _, v := range response.Data {
			ids = append(ids, v.ID)
		}
	}
	if e != nil {
		result.State = "unavailable"
		result.Detail = Redact(e.Error())
		return result
	}
	if !Has(ids, r.Model) {
		result.State = "unavailable model"
		result.Detail = "selected model not reported by backend"
		return result
	}
	result.ModelState = "installed"
	result.CanStart = true
	result.Detail = "model discovered; tools/reasoning capabilities are verified separately"
	return result
}
