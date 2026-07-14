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

// parseMistralError extracts the human-readable message from a Mistral API error
// response body. Mistral uses {"message":"..."} at the root level.
// Falls back to the raw body if parsing fails.
func parseMistralError(status int, body []byte) error {
	var apiErr struct {
		Message string `json:"message"`
		Detail  any    `json:"detail"`
	}
	if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Message != "" {
		return fmt.Errorf("mistral (%d): %s", status, apiErr.Message)
	}
	return fmt.Errorf("mistral: status %d: %s", status, body)
}

type Mistral struct{}

func NewMistral() *Mistral { return &Mistral{} }

func (m *Mistral) Name() string { return "Mistral" }

const mistralMaxTokens = 900

func (m *Mistral) Validate(ctx context.Context, apiKey string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.mistral.ai/v1/models", nil)
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
		return sanitizeProviderError(parseMistralError(resp.StatusCode, body), apiKey)
	}
	return nil
}

func (m *Mistral) ListModels(ctx context.Context, apiKey string) ([]string, error) {
	return []string{
		"mistral-large-3",
		"mistral-small-4",
		"devstral",
		"ministral-14b",
		"ministral-8b",
		"ministral-3b",
		"mistral-large-latest",
		"mistral-small-latest",
	}, nil
}

func (m *Mistral) Ask(ctx context.Context, apiKey, model string, prompt PromptEnvelope, tools []ToolDefinition) (*AskResult, error) {
	if model == "" {
		model = "mistral-small-4"
	}
	payload := map[string]any{
		"max_tokens": mistralMaxTokens,
		"model":      model,
		"messages": []map[string]string{
			{"role": "system", "content": buildMistralSystemPrompt(prompt)},
			{"role": "user", "content": prompt.UserPrompt},
		},
	}
	if len(tools) > 0 {
		payload["tools"] = mistralTools(tools)
		payload["tool_choice"] = "auto"
	}

	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.mistral.ai/v1/chat/completions", bytes.NewReader(body))
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
		return nil, sanitizeProviderError(parseMistralError(resp.StatusCode, b), apiKey)
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
		return nil, sanitizeProviderError(fmt.Errorf("mistral: parse: %w", err), apiKey)
	}
	if len(result.Choices) == 0 {
		return nil, sanitizeProviderError(fmt.Errorf("mistral: no choices"), apiKey)
	}

	message := result.Choices[0].Message
	if len(message.ToolCalls) > 0 {
		var toolCalls []*ToolCall
		for _, tc := range message.ToolCalls {
			call, err := ParseToolArgumentsJSON(tc.Function.Name, tc.Function.Arguments)
			if err != nil {
				return nil, sanitizeProviderError(err, apiKey)
			}
			toolCalls = append(toolCalls, call)
		}
		return &AskResult{ToolCalls: toolCalls, Text: strings.TrimSpace(message.Content)}, nil
	}

	return &AskResult{Text: strings.TrimSpace(message.Content)}, nil
}

func mistralTools(tools []ToolDefinition) []map[string]any {
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
