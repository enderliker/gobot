package ai

import (
	"context"
	"net/http"
	"os"
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
	ToolCalls     []*ToolCall
	ImageData     []byte
	ImageMimeType string
}

var TesterAPIKey = os.Getenv("TESTER_API_KEY")
var ProviderTesterAPIKey = os.Getenv("PROVIDER_TESTER_API_KEY")
var RealTesterAPIKey = os.Getenv("TESTER_REAL_API_KEY")

func isTesterKey(key string) bool {
	return (TesterAPIKey != "" && key == TesterAPIKey) || (ProviderTesterAPIKey != "" && key == ProviderTesterAPIKey)
}

type Manager struct {
	providers []Provider
}

var DefaultManager *Manager

type testerDecorator struct {
	underlying Provider
	manager    *Manager
}

func (d *testerDecorator) Name() string {
	return d.underlying.Name()
}

func (d *testerDecorator) Validate(ctx context.Context, apiKey string) error {
	if isTesterKey(apiKey) {
		gemini := d.manager.Get("Gemini")
		if gemini == nil {
			return d.underlying.Validate(ctx, apiKey)
		}
		return gemini.Validate(ctx, RealTesterAPIKey)
	}
	return d.underlying.Validate(ctx, apiKey)
}

func (d *testerDecorator) ListModels(ctx context.Context, apiKey string) ([]string, error) {
	if isTesterKey(apiKey) {
		gemini := d.manager.Get("Gemini")
		if gemini == nil {
			return d.underlying.ListModels(ctx, apiKey)
		}
		return gemini.ListModels(ctx, RealTesterAPIKey)
	}
	return d.underlying.ListModels(ctx, apiKey)
}

func (d *testerDecorator) Ask(ctx context.Context, apiKey, model string, prompt PromptEnvelope, tools []ToolDefinition) (*AskResult, error) {
	if isTesterKey(apiKey) {
		gemini := d.manager.Get("Gemini")
		if gemini == nil {
			return d.underlying.Ask(ctx, apiKey, model, prompt, tools)
		}
		if !strings.Contains(model, "gemini") {
			model = "gemini-2.5-flash"
		}
		return gemini.Ask(ctx, RealTesterAPIKey, model, prompt, tools)
	}
	return d.underlying.Ask(ctx, apiKey, model, prompt, tools)
}

func NewManager(providers ...Provider) *Manager {
	m := &Manager{}
	decorated := make([]Provider, len(providers))
	for i, p := range providers {
		decorated[i] = &testerDecorator{
			underlying: p,
			manager:    m,
		}
	}
	m.providers = decorated
	return m
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
