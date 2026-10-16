package arena

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"

	"google.golang.org/genai"
)

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type inference func(context.Context, []chatMessage) (string, error)

// turnUsage copies a provider's usage map and tags it with the tool-loop turn
// it belongs to (see turnKey). The conversation starts with one message and
// each turn appends two, so the length gives the turn.
func turnUsage(usage map[string]any, messages []chatMessage) map[string]any {
	tagged := make(map[string]any, len(usage)+1)
	for k, v := range usage {
		tagged[k] = v
	}
	tagged[turnKey] = (len(messages) - 1) / 2
	return tagged
}

func invokeLocal(ctx context.Context, in Invocation) (map[string]any, error) {
	endpoint, e := LocalEndpoint(in.Runtime)
	if e != nil {
		return nil, e
	}
	messages := []chatMessage{}
	_ = messages
	return toolLoop(ctx, in, func(ctx context.Context, messages []chatMessage) (string, error) {
		body := map[string]any{"model": in.Runtime.Model, "messages": messages, "stream": true}
		url := endpoint + "/chat/completions"
		if in.Runtime.Options["backend"] == "ollama" {
			url = endpoint + "/api/chat"
			body["format"] = "json"
			if options, ok := in.Runtime.Options["generation"].(map[string]any); ok {
				body["options"] = options
			}
			if in.Runtime.ReasoningEffort != "" {
				body["think"] = in.Runtime.ReasoningEffort
			}
		} else {
			if generation, ok := in.Runtime.Options["generation"].(map[string]any); ok {
				for k, v := range generation {
					if !Has([]string{"temperature", "top_p", "max_tokens", "seed", "reasoning_effort"}, k) {
						return "", fmt.Errorf("unsupported local generation option %s", k)
					}
					body[k] = v
				}
			}
			if in.Runtime.ReasoningEffort != "" {
				return "", fmt.Errorf("this backend cannot verify reasoning effort; configure documented generation options explicitly")
			}
		}
		b, e := json.Marshal(body)
		if e != nil {
			return "", e
		}
		req, e := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(b))
		if e != nil {
			return "", e
		}
		req.Header.Set("Content-Type", "application/json")
		client := &http.Client{CheckRedirect: localClient.CheckRedirect}
		response, e := client.Do(req)
		if e != nil {
			return "", fmt.Errorf("local backend request failed: %w", e)
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			return "", fmt.Errorf("local inference HTTP %d", response.StatusCode)
		}
		if in.Delivered != nil {
			if err := in.Delivered(); err != nil {
				return "", err
			}
		}
		scanner := bufio.NewScanner(io.LimitReader(response.Body, 32<<20))
		scanner.Buffer(make([]byte, 64<<10), 4<<20)
		var result strings.Builder
		done := false
		for scanner.Scan() {
			line := strings.TrimSpace(scanner.Text())
			if line == "" {
				continue
			}
			if strings.HasPrefix(line, "data:") {
				line = strings.TrimSpace(line[5:])
			}
			if line == "[DONE]" {
				done = true
				break
			}
			var chunk map[string]any
			if e := json.Unmarshal([]byte(line), &chunk); e != nil {
				return "", fmt.Errorf("invalid local stream frame")
			}
			if message, ok := chunk["message"].(map[string]any); ok {
				result.WriteString(str(message["content"]))
			}
			if chunk["done"] == true {
				done = true
				if in.Usage != nil {
					if err := in.Usage(turnUsage(map[string]any{"promptTokens": chunk["prompt_eval_count"], "outputTokens": chunk["eval_count"]}, messages)); err != nil {
						return "", err
					}
				}
			}
			if choices, ok := chunk["choices"].([]any); ok {
				for _, choice := range choices {
					m, _ := choice.(map[string]any)
					delta, _ := m["delta"].(map[string]any)
					result.WriteString(str(delta["content"]))
					if m["finish_reason"] != nil {
						done = true
					}
				}
			}
			if chunk["error"] != nil {
				return "", fmt.Errorf("local backend reported inference error")
			}
		}
		if e := scanner.Err(); e != nil {
			return "", e
		}
		if !done {
			return "", fmt.Errorf("local inference stream ended without completion")
		}
		return result.String(), nil
	})
}
func geminiClient(ctx context.Context, r Runtime) (*genai.Client, error) {
	cfg := &genai.ClientConfig{}
	switch r.Options["authMethod"] {
	case "google-ai-studio-api-key":
		if os.Getenv("GEMINI_API_KEY") == "" && os.Getenv("GOOGLE_API_KEY") == "" {
			return nil, fmt.Errorf("configure GEMINI_API_KEY through the provider-supported environment")
		}
		cfg.Backend = genai.BackendGeminiAPI
	case "vertex-ai":
		cfg.Backend = genai.BackendVertexAI
		cfg.Project = os.Getenv("GOOGLE_CLOUD_PROJECT")
		cfg.Location = os.Getenv("GOOGLE_CLOUD_LOCATION")
		if cfg.Project == "" || cfg.Location == "" {
			return nil, fmt.Errorf("Vertex AI requires GOOGLE_CLOUD_PROJECT, GOOGLE_CLOUD_LOCATION and provider-managed ADC")
		}
	default:
		return nil, fmt.Errorf("Gemini personal OAuth is unsupported")
	}
	client, e := genai.NewClient(ctx, cfg)
	if e != nil {
		return nil, fmt.Errorf("Google SDK could not initialize the selected authentication route")
	}
	return client, nil
}
func invokeGemini(ctx context.Context, in Invocation) (map[string]any, error) {
	client, e := geminiClient(ctx, in.Runtime)
	if e != nil {
		return nil, e
	}
	config := &genai.GenerateContentConfig{ResponseMIMEType: "application/json"}
	if generation, ok := in.Runtime.Options["generation"]; ok {
		decoder := json.NewDecoder(bytes.NewReader(JSON(generation)))
		decoder.DisallowUnknownFields()
		if e := decoder.Decode(config); e != nil {
			return nil, fmt.Errorf("invalid Gemini generation options: %w", e)
		}
		if config.HTTPOptions != nil || len(config.Tools) > 0 || config.CachedContent != "" || config.SystemInstruction != nil {
			return nil, fmt.Errorf("Gemini generation options cannot replace transport, tools, cache or system instructions")
		}
	}
	no := false
	config.AutomaticContinuation = &no
	config.ResponseMIMEType = "application/json"
	return toolLoop(ctx, in, func(ctx context.Context, messages []chatMessage) (string, error) {
		contents := []*genai.Content{}
		for _, m := range messages {
			role := "user"
			if m.Role == "assistant" {
				role = "model"
			}
			contents = append(contents, &genai.Content{Role: role, Parts: []*genai.Part{{Text: m.Content}}})
		}
		var text strings.Builder
		for chunk, e := range client.Models.GenerateContentStream(ctx, in.Runtime.Model, contents, config) {
			if e != nil {
				return "", fmt.Errorf("Gemini generation failed through the selected API/Vertex route (provider error; credentials omitted)")
			}
			if in.Delivered != nil {
				if err := in.Delivered(); err != nil {
					return "", err
				}
			}
			if chunk.UsageMetadata != nil && in.Usage != nil {
				if err := in.Usage(turnUsage(convert[map[string]any](chunk.UsageMetadata), messages)); err != nil {
					return "", err
				}
			}
			for _, candidate := range chunk.Candidates {
				if candidate.Content != nil {
					for _, part := range candidate.Content.Parts {
						if !part.Thought {
							text.WriteString(part.Text)
						}
					}
				}
			}
		}
		return text.String(), nil
	})
}
func invokeClaude(ctx context.Context, in Invocation) (map[string]any, error) {
	if os.Getenv("ANTHROPIC_API_KEY") == "" && os.Getenv("CLAUDE_CODE_USE_BEDROCK") != "1" && os.Getenv("CLAUDE_CODE_USE_VERTEX") != "1" && os.Getenv("CLAUDE_CODE_USE_FOUNDRY") != "1" {
		return nil, fmt.Errorf("Claude adapter requires API-key or supported cloud authentication; subscription OAuth is unsupported")
	}
	return toolLoop(ctx, in, func(ctx context.Context, messages []chatMessage) (string, error) {
		args := []string{"--bare", "-p", "--model", in.Runtime.Model, "--effort", in.Runtime.ReasoningEffort, "--tools", "", "--output-format", "stream-json", "--verbose", "--no-session-persistence"}
		cmd := exec.Command("claude", args...)
		cmd.Dir = in.Dir
		var prompt strings.Builder
		for _, m := range messages {
			fmt.Fprintf(&prompt, "%s:\n%s\n", m.Role, m.Content)
		}
		cmd.Stdin = strings.NewReader(prompt.String())
		cmd.Stderr = io.Discard
		stdout, e := cmd.StdoutPipe()
		if e != nil {
			return "", e
		}
		process, e := StartProcess(ctx, cmd)
		if e != nil {
			return "", e
		}
		if in.Delivered != nil {
			if err := in.Delivered(); err != nil {
				process.Stop()
				_ = process.Wait()
				return "", err
			}
		}
		scanner := bufio.NewScanner(io.LimitReader(stdout, 32<<20))
		scanner.Buffer(make([]byte, 64<<10), 8<<20)
		result := ""
		failed := false
		for scanner.Scan() {
			var event map[string]any
			if e := json.Unmarshal(scanner.Bytes(), &event); e != nil {
				process.Stop()
				_ = process.Wait()
				return "", fmt.Errorf("invalid Claude stream frame")
			}
			if event["type"] == "result" {
				if usage, ok := event["usage"].(map[string]any); ok && in.Usage != nil {
					if err := in.Usage(turnUsage(usage, messages)); err != nil {
						process.Stop()
						_ = process.Wait()
						return "", err
					}
				}
				failed = event["is_error"] == true
				result = str(event["result"])
			}
		}
		scanErr := scanner.Err()
		waitErr := process.Wait()
		if scanErr != nil {
			return "", scanErr
		}
		if waitErr != nil || failed {
			return "", fmt.Errorf("Claude Code generation failed (credentials and raw diagnostics omitted)")
		}
		if result == "" {
			return "", fmt.Errorf("Claude Code returned no final result")
		}
		return result, nil
	})
}
func inferenceDeadline(parent context.Context, minutes int) (context.Context, context.CancelFunc) {
	return context.WithTimeout(parent, time.Duration(minutes)*time.Minute)
}
