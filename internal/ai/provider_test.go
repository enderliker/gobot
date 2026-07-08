package ai

import (
	"context"
	"errors"
	"testing"
)

type stubProvider struct {
	name          string
	validateErr   error
	validateCalls int
}

func (s *stubProvider) Name() string { return s.name }

func (s *stubProvider) Validate(ctx context.Context, apiKey string) error {
	s.validateCalls++
	return s.validateErr
}

func (s *stubProvider) ListModels(ctx context.Context, apiKey string) ([]string, error) {
	return nil, nil
}

func (s *stubProvider) Ask(ctx context.Context, apiKey, model string, prompt PromptEnvelope, tools []ToolDefinition) (*AskResult, error) {
	return nil, nil
}

func TestValidateProviderOnlyCallsSelectedProvider(t *testing.T) {
	openAI := &stubProvider{name: "OpenAI"}
	gemini := &stubProvider{name: "Gemini"}
	mistral := &stubProvider{name: "Mistral"}

	manager := &Manager{
		providers: []Provider{openAI, gemini, mistral},
	}

	provider, err := manager.ValidateProvider(context.Background(), "gemini", "secret-key")
	if err != nil {
		t.Fatalf("expected validation to succeed, got %v", err)
	}
	if provider != gemini {
		t.Fatalf("expected Gemini provider, got %#v", provider)
	}
	if openAI.validateCalls != 0 {
		t.Fatalf("expected OpenAI validate calls to stay at 0, got %d", openAI.validateCalls)
	}
	if gemini.validateCalls != 1 {
		t.Fatalf("expected Gemini validate calls to be 1, got %d", gemini.validateCalls)
	}
	if mistral.validateCalls != 0 {
		t.Fatalf("expected Mistral validate calls to stay at 0, got %d", mistral.validateCalls)
	}
}

func TestValidateProviderRejectsUnknownProvider(t *testing.T) {
	manager := &Manager{
		providers: []Provider{&stubProvider{name: "OpenAI"}},
	}

	_, err := manager.ValidateProvider(context.Background(), "does-not-exist", "secret-key")
	if !errors.Is(err, ErrUnknownProvider) {
		t.Fatalf("expected ErrUnknownProvider, got %v", err)
	}
}
