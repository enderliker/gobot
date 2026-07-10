package ai

import (
	"context"
	"net/http"
	"strings"
	"time"
)

var httpClient = &http.Client{Timeout: 30 * time.Second}

type Provider interface {
	Name() string
	Validate(ctx context.Context, apiKey string) error
	ListModels(ctx context.Context, apiKey string) ([]string, error)
	Ask(ctx context.Context, apiKey, model string, prompt PromptEnvelope, tools []ToolDefinition) (*AskResult, error)
}

type ToolDefinition struct {
	Name        string
	Description string
	InputSchema map[string]any
}

type AskResult struct {
	Text          string
	ToolCall      *ToolCall
	ImageData     []byte
	ImageMimeType string
}

type Manager struct {
	providers []Provider
}

var DefaultManager *Manager

func NewManager(providers ...Provider) *Manager {
	return &Manager{
		providers: append([]Provider(nil), providers...),
	}
}

func init() {
	DefaultManager = NewManager(
		NewOpenAI(),
		NewAnthropic(),
		NewGemini(),
		NewMistral(),
	)
}

func (m *Manager) Names() []string {
	names := make([]string, 0, len(m.providers))
	for _, p := range m.providers {
		names = append(names, p.Name())
	}
	return names
}

func (m *Manager) Get(name string) Provider {
	for _, p := range m.providers {
		if strings.EqualFold(p.Name(), name) {
			return p
		}
	}
	return nil
}

func (m *Manager) ValidateProvider(ctx context.Context, name, apiKey string) (Provider, error) {
	provider := m.Get(name)
	if provider == nil {
		return nil, ErrUnknownProvider
	}
	if err := provider.Validate(ctx, apiKey); err != nil {
		return nil, err
	}
	return provider, nil
}
