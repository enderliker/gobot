package ai

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type geminiErrorDetails struct {
	Type       string `json:"@type"`
	RetryDelay string `json:"retryDelay"`
	Violations []struct {
		QuotaMetric string `json:"quotaMetric"`
		QuotaLimit  any    `json:"limit"`
		QuotaLimit2 any    `json:"quotaLimit"`
		Description string `json:"description"`
	} `json:"violations"`
}

type geminiErrorResponse struct {
	Error struct {
		Message string               `json:"message"`
		Status  string               `json:"status"`
		Code    int                  `json:"code"`
		Details []geminiErrorDetails `json:"details"`
	} `json:"error"`
}

func parseGeminiError(status int, body []byte) error {
	var apiErr geminiErrorResponse
	if err := json.Unmarshal(body, &apiErr); err == nil && apiErr.Error.Message != "" {
		msg := apiErr.Error.Message
		if status == http.StatusTooManyRequests && apiErr.Error.Status == "RESOURCE_EXHAUSTED" {
			var extra []string
			for _, d := range apiErr.Error.Details {
				if d.RetryDelay != "" {
					extra = append(extra, fmt.Sprintf("Please retry in %s.", d.RetryDelay))
				}
				for _, v := range d.Violations {
					limitVal := ""
					if v.QuotaLimit != nil {
						limitVal = fmt.Sprintf("%v", v.QuotaLimit)
					} else if v.QuotaLimit2 != nil {
						limitVal = fmt.Sprintf("%v", v.QuotaLimit2)
					}
					if limitVal != "" {
						metric := v.QuotaMetric
						if metric == "" {
							metric = "requests"
						}
						extra = append(extra, fmt.Sprintf("limit: %s (metric: %s)", limitVal, metric))
					} else if v.Description != "" {
						extra = append(extra, v.Description)
					}
				}
			}
			if len(extra) > 0 {
				msg = msg + " * " + strings.Join(extra, " * ")
			}
		}
		return fmt.Errorf("gemini (%d %s): %s", status, apiErr.Error.Status, msg)
	}
	return fmt.Errorf("gemini: status %d: %s", status, body)
}

type Gemini struct{}

func NewGemini() *Gemini { return &Gemini{} }

func (g *Gemini) Name() string { return "Gemini" }

const geminiMaxOutputTokens = 900

func (g *Gemini) Validate(ctx context.Context, apiKey string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, buildGeminiModelsURL(apiKey), nil)
	if err != nil {
		return sanitizeProviderError(err, apiKey)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return sanitizeProviderError(err, apiKey)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return sanitizeProviderError(parseGeminiError(resp.StatusCode, body), apiKey)
	}
	return nil
}

func (g *Gemini) ListModels(ctx context.Context, apiKey string) ([]string, error) {
	return []string{
		"gemini-3.5-flash",      // texto/chat, generación más nueva
		"gemini-3.1-flash-lite", // texto/chat, liviano

		"gemini-2.5-pro",   // texto/chat (deprecated, apaga 16 oct 2026)
		"gemini-2.5-flash", // texto/chat (deprecated, apaga 16 oct 2026)

		"gemini-3-pro-image",          // imagen — Nano Banana Pro
		"gemini-3.1-flash-image",      // imagen — Nano Banana 2
		"gemini-3.1-flash-lite-image", // imagen — Nano Banana 2 Lite
		"gemini-2.5-flash-image",      // imagen — Nano Banana (original)

		// "gemini-3.5-flash-image" NO EXISTE (404) — la familia 3.5 todavía
		// no tiene variante de imagen propia. No la agregues.
	}, nil
}

func (g *Gemini) Ask(ctx context.Context, apiKey, model string, prompt PromptEnvelope, tools []ToolDefinition) (*AskResult, error) {
	if model == "" {
		model = "gemini-3.5-flash"
	}

	if IsImageModel(model) {
		return g.generateImage(ctx, apiKey, model, prompt.UserPrompt)
	}

	payload := map[string]any{
		"contents": []map[string]any{
			{
				"parts": []map[string]string{
					{"text": prompt.UserPrompt},
				},
			},
		},
		"systemInstruction": map[string]any{
			"parts": []map[string]string{
				{"text": buildGeminiSystemPrompt(prompt)},
			},
		},
		"generationConfig": map[string]any{
			"maxOutputTokens": geminiMaxOutputTokens,
		},
	}
	if len(tools) > 0 {
		payload["tools"] = []map[string]any{
			{
				"functionDeclarations": geminiTools(tools),
			},
		}
	}

	body, _ := json.Marshal(payload)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, buildGeminiGenerateURL(model, apiKey), bytes.NewReader(body))
	if err != nil {
		return nil, sanitizeProviderError(err, apiKey)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, sanitizeProviderError(err, apiKey)
	}
	defer resp.Body.Close()

	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		return nil, sanitizeProviderError(parseGeminiError(resp.StatusCode, b), apiKey)
	}

	var result struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					Text         string         `json:"text"`
					Thought      bool           `json:"thought"`
					FunctionCall *geminiToolUse `json:"functionCall"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}
	if err := json.Unmarshal(b, &result); err != nil {
		return nil, sanitizeProviderError(fmt.Errorf("gemini: parse: %w", err), apiKey)
	}
	if len(result.Candidates) == 0 || len(result.Candidates[0].Content.Parts) == 0 {
		return nil, sanitizeProviderError(fmt.Errorf("gemini: no candidates"), apiKey)
	}

	var sb strings.Builder
	for _, part := range result.Candidates[0].Content.Parts {
		if part.FunctionCall != nil && part.FunctionCall.Name != "" {
			call, err := ParseToolArguments(part.FunctionCall.Name, part.FunctionCall.Args)
			if err != nil {
				return nil, sanitizeProviderError(err, apiKey)
			}
			return &AskResult{ToolCall: call}, nil
		}
		if !part.Thought {
			sb.WriteString(part.Text)
		}
	}

	return &AskResult{Text: cleanThoughtTags(sb.String())}, nil
}

// cleanThoughtTags strips `<thought>...</thought>` and `<thinking>...</thinking>`
// tag blocks (closed or unclosed due to truncation) from the model output.
func cleanThoughtTags(s string) string {
	s = removeTag(s, "thought")
	s = removeTag(s, "thinking")
	return strings.TrimSpace(s)
}

func removeTag(s, tagName string) string {
	startTag := "<" + tagName + ">"
	endTag := "</" + tagName + ">"

	for {
		startIdx := strings.Index(strings.ToLower(s), startTag)
		if startIdx == -1 {
			break
		}
		endIdx := strings.Index(strings.ToLower(s), endTag)
		if endIdx != -1 && endIdx > startIdx {
			s = s[:startIdx] + s[endIdx+len(endTag):]
		} else {
			s = s[:startIdx]
			break
		}
	}
	return s
}

func buildGeminiModelsURL(apiKey string) string {
	return "https://generativelanguage.googleapis.com/v1beta/models?key=" + url.QueryEscape(apiKey)
}

func buildGeminiGenerateURL(model, apiKey string) string {
	return "https://generativelanguage.googleapis.com/v1beta/models/" + url.PathEscape(model) + ":generateContent?key=" + url.QueryEscape(apiKey)
}

type geminiToolUse struct {
	Name string         `json:"name"`
	Args map[string]any `json:"args"`
}

func geminiTools(tools []ToolDefinition) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, tool := range tools {
		out = append(out, map[string]any{
			"name":        tool.Name,
			"description": tool.Description,
			"parameters":  sanitizeGeminiSchema(tool.InputSchema),
		})
	}
	return out
}

func sanitizeGeminiSchema(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(typed))
		for key, item := range typed {
			if key == "additionalProperties" {
				continue
			}
			out[key] = sanitizeGeminiSchema(item)
		}
		return out
	case []any:
		out := make([]any, len(typed))
		for idx, item := range typed {
			out[idx] = sanitizeGeminiSchema(item)
		}
		return out
	default:
		return value
	}
}

func (g *Gemini) generateImage(ctx context.Context, apiKey, model, prompt string) (*AskResult, error) {
	if strings.Contains(model, "imagen-") {
		// Legacy predict-based logic for imagen-* models
		urlStr := "https://generativelanguage.googleapis.com/v1beta/models/" + url.PathEscape(model) + ":predict?key=" + url.QueryEscape(apiKey)

		reqPayload := map[string]any{
			"instances": []map[string]any{
				{
					"prompt": prompt,
				},
			},
			"parameters": map[string]any{
				"sampleCount": 1,
			},
		}

		bodyBytes, err := json.Marshal(reqPayload)
		if err != nil {
			return nil, err
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, sanitizeProviderError(err, apiKey)
		}
		req.Header.Set("Content-Type", "application/json")

		resp, err := httpClient.Do(req)
		if err != nil {
			return nil, sanitizeProviderError(err, apiKey)
		}
		defer resp.Body.Close()

		respBody, err := io.ReadAll(resp.Body)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusOK {
			return nil, sanitizeProviderError(parseGeminiError(resp.StatusCode, respBody), apiKey)
		}

		var data struct {
			Predictions []struct {
				BytesBase64Encoded string `json:"bytesBase64Encoded"`
				MimeType           string `json:"mimeType"`
			} `json:"predictions"`
		}

		if err := json.Unmarshal(respBody, &data); err != nil {
			return nil, fmt.Errorf("failed to parse image predictions: %w", err)
		}

		if len(data.Predictions) == 0 {
			return nil, fmt.Errorf("no image predictions returned")
		}

		pred := data.Predictions[0]
		imgData, err := base64.StdEncoding.DecodeString(pred.BytesBase64Encoded)
		if err != nil {
			return nil, fmt.Errorf("failed to decode base64 image: %w", err)
		}

		return &AskResult{
			ImageData:     imgData,
			ImageMimeType: pred.MimeType,
		}, nil
	}

	// For models ending in "-image", use generateContent with responseModalities: ["IMAGE"]
	urlStr := "https://generativelanguage.googleapis.com/v1beta/models/" + url.PathEscape(model) + ":generateContent?key=" + url.QueryEscape(apiKey)

	reqPayload := map[string]any{
		"contents": []map[string]any{
			{
				"parts": []map[string]any{
					{"text": prompt},
				},
			},
		},
		"generationConfig": map[string]any{
			"responseModalities": []string{"IMAGE"},
		},
	}

	bodyBytes, err := json.Marshal(reqPayload)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, urlStr, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, sanitizeProviderError(err, apiKey)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, sanitizeProviderError(err, apiKey)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		return nil, sanitizeProviderError(parseGeminiError(resp.StatusCode, respBody), apiKey)
	}

	var data struct {
		Candidates []struct {
			Content struct {
				Parts []struct {
					InlineData struct {
						MimeType string `json:"mimeType"`
						Data     string `json:"data"`
					} `json:"inlineData"`
				} `json:"parts"`
			} `json:"content"`
		} `json:"candidates"`
	}

	if err := json.Unmarshal(respBody, &data); err != nil {
		return nil, fmt.Errorf("failed to parse generateContent image response: %w", err)
	}

	for _, cand := range data.Candidates {
		for _, part := range cand.Content.Parts {
			if part.InlineData.Data != "" {
				imgData, err := base64.StdEncoding.DecodeString(part.InlineData.Data)
				if err != nil {
					return nil, fmt.Errorf("failed to decode inline image data: %w", err)
				}
				mime := part.InlineData.MimeType
				if mime == "" {
					mime = "image/png"
				}
				return &AskResult{
					ImageData:     imgData,
					ImageMimeType: mime,
				}, nil
			}
		}
	}

	return nil, fmt.Errorf("no image data returned in generateContent response")
}
