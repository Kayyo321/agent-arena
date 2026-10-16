package arena

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"
)

// ModelOption is one exact model ID reported by a provider's own catalog.
// Listing a model is not evidence of inference access.
type ModelOption struct {
	ID      string   `json:"id"`
	Name    string   `json:"name,omitempty"`
	Efforts []string `json:"efforts,omitempty"`
}

// DiscoverModels asks the selected provider interface for its model catalog.
// Local runtimes need backend and endpoint options; Gemini needs authMethod.
func DiscoverModels(ctx context.Context, root string, r Runtime) ([]ModelOption, error) {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	switch r.Provider {
	case "codex":
		rpc, e := StartRPC(ctx, root)
		if e != nil {
			return nil, fmt.Errorf("start Codex app-server: %s", Redact(e.Error()))
		}
		defer rpc.Close()
		if signedOut, e := codexSignedOut(ctx, rpc); e != nil {
			return nil, e
		} else if signedOut {
			return nil, fmt.Errorf("Codex is signed out; run codex and complete its own sign-in first")
		}
		return codexCatalog(ctx, rpc)
	case "local":
		endpoint, e := LocalEndpoint(r)
		if e != nil {
			return nil, e
		}
		ids, e := localModelIDs(ctx, r, endpoint)
		if e != nil {
			return nil, e
		}
		models := []ModelOption{}
		for _, id := range ids {
			models = append(models, ModelOption{ID: id})
		}
		return models, nil
	case "gemini":
		client, e := geminiClient(ctx, r)
		if e != nil {
			return nil, e
		}
		models := []ModelOption{}
		for model, e := range client.Models.All(ctx) {
			if e != nil {
				return nil, fmt.Errorf("Google rejected model discovery for the selected route")
			}
			if len(model.SupportedActions) > 0 && !Has(model.SupportedActions, "generateContent") {
				continue
			}
			models = append(models, ModelOption{ID: strings.TrimPrefix(model.Name, "models/"), Name: model.DisplayName})
			if len(models) >= 500 {
				break
			}
		}
		sort.Slice(models, func(i, j int) bool { return models[i].ID < models[j].ID })
		return models, nil
	case "antigravity":
		return antigravityCatalog(ctx)
	case "claude-code":
		return nil, fmt.Errorf("the Claude Code CLI exposes no model catalog; enter an exact model ID or alias it accepts")
	}
	return nil, fmt.Errorf("unsupported provider %q", r.Provider)
}

func codexSignedOut(ctx context.Context, rpc *RPC) (bool, error) {
	account, e := rpc.Call(ctx, "account/read", map[string]any{"refreshToken": false}, nil)
	if e != nil {
		return false, e
	}
	var a struct {
		Account            map[string]any `json:"account"`
		RequiresOpenAIAuth bool           `json:"requiresOpenaiAuth"`
	}
	if e = json.Unmarshal(account, &a); e != nil {
		return false, fmt.Errorf("unrecognized account response")
	}
	return a.Account == nil && a.RequiresOpenAIAuth, nil
}

func codexCatalog(ctx context.Context, rpc *RPC) ([]ModelOption, error) {
	models := []ModelOption{}
	cursor := ""
	for pages := 0; pages < 20; pages++ {
		params := map[string]any{"limit": 100, "includeHidden": false}
		if cursor != "" {
			params["cursor"] = cursor
		}
		raw, e := rpc.Call(ctx, "model/list", params, nil)
		if e != nil {
			return nil, e
		}
		var m struct {
			Data []struct {
				ID          string `json:"id"`
				Model       string `json:"model"`
				DisplayName string `json:"displayName"`
				Efforts     []struct {
					Effort string `json:"reasoningEffort"`
				} `json:"supportedReasoningEfforts"`
			} `json:"data"`
			NextCursor *string `json:"nextCursor"`
		}
		if e = json.Unmarshal(raw, &m); e != nil {
			return nil, fmt.Errorf("unrecognized model catalog")
		}
		for _, model := range m.Data {
			option := ModelOption{ID: model.Model, Name: model.DisplayName, Efforts: []string{}}
			if option.ID == "" {
				option.ID = model.ID
			}
			for _, eff := range model.Efforts {
				option.Efforts = append(option.Efforts, eff.Effort)
			}
			models = append(models, option)
		}
		if m.NextCursor == nil {
			break
		}
		cursor = *m.NextCursor
	}
	return models, nil
}

func localModelIDs(ctx context.Context, r Runtime, endpoint string) ([]string, error) {
	ids := []string{}
	if r.Options["backend"] == "ollama" {
		var response struct {
			Models []struct {
				Name string `json:"name"`
			} `json:"models"`
		}
		if e := localGET(ctx, endpoint+"/api/tags", &response); e != nil {
			return nil, fmt.Errorf("%s", Redact(e.Error()))
		}
		for _, v := range response.Models {
			ids = append(ids, v.Name)
		}
		return ids, nil
	}
	var response struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if e := localGET(ctx, endpoint+"/models", &response); e != nil {
		return nil, fmt.Errorf("%s", Redact(e.Error()))
	}
	for _, v := range response.Data {
		ids = append(ids, v.ID)
	}
	return ids, nil
}
