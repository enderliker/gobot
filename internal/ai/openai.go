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

// parseOpenAIError extracts the human-readable message from an OpenAI API error
// response body. Falls back to the raw body if parsing fails.
func parseOpenAIError(status int, body []byte) error {
	var apiErr struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    any    `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Error.Message != "" {
		return fmt.Errorf("openai (%d): %s", status, apiErr.Error.Message)
	}
	return fmt.Errorf("openai: status %d: %s", status, body)
}

type OpenAI struct{}

func NewOpenAI() *OpenAI { return &OpenAI{} }

func (o *OpenAI) Name() string { return "OpenAI" }

func (o *OpenAI) Validate(ctx context.Context, apiKey string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.openai.com/v1/models", nil)
	if err != nil {
		return sanitizeProviderError(err, apiKey)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return sanitizeProviderError(err, apiKey)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return sanitizeProviderError(parseOpenAIError(resp.StatusCode, body), apiKey)
	}
	return nil
}

func (o *OpenAI) ListModels(ctx context.Context, apiKey string) ([]string, error) {
	return []string{
		"gpt-5.5-pro",
		"gpt-5.5",
		"gpt-5.4-pro",
		"gpt-5.4-mini",
		"gpt-5.4-nano",
		"gpt-4o",
		"gpt-4o-mini",
	}, nil
}

func (o *OpenAI) Ask(ctx context.Context, apiKey, model string, prompt PromptEnvelope, tools []ToolDefinition) (*AskResult, error) {
	if model == "" {
		model = "gpt-5.4-mini"
	}
	payload := map[string]any{
		"model": model,
		"messages": []map[string]string{
			{"role": "system", "content": buildOpenAISystemPrompt(prompt)},
			{"role": "user", "content": prompt.UserPrompt},
		},
		"max_tokens": 1000,
	}
	if len(tools) > 0 {
		payload["tools"] = openAITools(tools)
		payload["tool_choice"] = "auto"
	}

	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/chat/completions", bytes.NewReader(body))
	if err != nil {
		return nil, sanitizeProviderError(err, apiKey)
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, sanitizeProviderError(err, apiKey)
	}
	defer resp.Body.Close()

	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, sanitizeProviderError(parseOpenAIError(resp.StatusCode, b), apiKey)
	}

	var result struct {
		Choices []struct {
			Message struct {
				Content   string `json:"content"`
				ToolCalls []struct {
					Function struct {
						Name      string `json:"name"`
						Arguments string `json:"arguments"`
					} `json:"function"`
				} `json:"tool_calls"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(b, &result); err != nil {
		return nil, sanitizeProviderError(fmt.Errorf("openai: parse: %w", err), apiKey)
	}
	if len(result.Choices) == 0 {
		return nil, sanitizeProviderError(fmt.Errorf("openai: no choices"), apiKey)
	}

	message := result.Choices[0].Message
	if len(message.ToolCalls) > 0 {
		call, err := ParseToolArgumentsJSON(message.ToolCalls[0].Function.Name, message.ToolCalls[0].Function.Arguments)
		if err != nil {
			return nil, sanitizeProviderError(err, apiKey)
		}
		return &AskResult{ToolCall: call}, nil
	}

	return &AskResult{Text: strings.TrimSpace(message.Content)}, nil
}

func openAITools(tools []ToolDefinition) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		out = append(out, map[string]any{
			"type": "function",
			"function": map[string]any{
				"name":        tool.Name,
				"description": tool.Description,
				"parameters":  tool.InputSchema,
			},
		})
	}
	return out
}
