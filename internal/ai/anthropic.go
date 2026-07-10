package ai

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// parseAnthropicError extracts the human-readable message from an Anthropic API
// error response body. Falls back to the raw body if parsing fails.
func parseAnthropicError(status int, body []byte) error {
	var apiErr struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Error.Message != "" {
		return fmt.Errorf("anthropic (%d): %s", status, apiErr.Error.Message)
	}
	return fmt.Errorf("anthropic: status %d: %s", status, body)
}

type Anthropic struct{}

func NewAnthropic() *Anthropic { return &Anthropic{} }

func (a *Anthropic) Name() string { return "Anthropic" }

func (a *Anthropic) Validate(ctx context.Context, apiKey string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.anthropic.com/v1/models", nil)
	if err != nil {
		return sanitizeProviderError(err, apiKey)
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")

	resp, err := httpClient.Do(req)
	if err != nil {
		return sanitizeProviderError(err, apiKey)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return sanitizeProviderError(parseAnthropicError(resp.StatusCode, body), apiKey)
	}
	return nil
}

func (a *Anthropic) ListModels(ctx context.Context, apiKey string) ([]string, error) {
	return []string{
		"claude-fable-5",
		"claude-sonnet-5",
		"claude-opus-4-8",
		"claude-opus-4-7",
		"claude-sonnet-4-6",
		"claude-haiku-4-5",
	}, nil
}

func (a *Anthropic) Ask(ctx context.Context, apiKey, model string, prompt PromptEnvelope, tools []ToolDefinition) (*AskResult, error) {
	if model == "" {
		model = "claude-sonnet-5"
	}
	payload := map[string]any{
		"model":      model,
		"max_tokens": 900,
		"system":     buildAnthropicSystemPrompt(prompt),
		"messages": []map[string]string{
			{"role": "user", "content": prompt.UserPrompt},
		},
	}
	if len(tools) > 0 {
		payload["tools"] = anthropicTools(tools)
	}

	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.anthropic.com/v1/messages", bytes.NewReader(body))
	if err != nil {
		return nil, sanitizeProviderError(err, apiKey)
	}
	req.Header.Set("x-api-key", apiKey)
	req.Header.Set("anthropic-version", "2023-06-01")
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, sanitizeProviderError(err, apiKey)
	}
	defer resp.Body.Close()

	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, sanitizeProviderError(parseAnthropicError(resp.StatusCode, b), apiKey)
	}

	var result struct {
		Content []struct {
			Type  string         `json:"type"`
			Text  string         `json:"text"`
			Name  string         `json:"name"`
			Input map[string]any `json:"input"`
		} `json:"content"`
	}
	if err := json.Unmarshal(b, &result); err != nil {
		return nil, sanitizeProviderError(fmt.Errorf("anthropic: parse: %w", err), apiKey)
	}
	if len(result.Content) == 0 {
		return nil, sanitizeProviderError(fmt.Errorf("anthropic: no content"), apiKey)
	}

	var sb strings.Builder
	for _, block := range result.Content {
		if block.Type == "tool_use" {
			call, err := ParseToolArguments(block.Name, block.Input)
			if err != nil {
				return nil, sanitizeProviderError(err, apiKey)
			}
			return &AskResult{ToolCall: call}, nil
		}
		if block.Type == "text" {
			sb.WriteString(block.Text)
		}
	}

	return &AskResult{Text: strings.TrimSpace(sb.String())}, nil
}

func anthropicTools(tools []ToolDefinition) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		out = append(out, map[string]any{
			"name":         tool.Name,
			"description":  tool.Description,
			"input_schema": tool.InputSchema,
		})
	}
	return out
}
