package slash

import (
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

func TestParsePageButtonCustomID(t *testing.T) {
	customID := pageButtonCustomID("flow123", 2)

	page, ok := parsePageButtonCustomID("flow123", customID)
	if !ok {
		t.Fatal("expected custom id to parse")
	}
	if page != 2 {
		t.Fatalf("expected page 2, got %d", page)
	}
}

func TestParsePageButtonCustomIDRejectsOtherFlows(t *testing.T) {
	if _, ok := parsePageButtonCustomID("flow123", pageButtonCustomID("flow999", 1)); ok {
		t.Fatal("expected custom id from another flow to be rejected")
	}
}

func TestModelPage(t *testing.T) {
	models := make([]string, 30)
	for i := range models {
		models[i] = "model-" + strings.Repeat("x", i%3)
	}

	page, total, err := modelPage(models, 1)
	if err != nil {
		t.Fatalf("expected valid page, got error: %v", err)
	}
	if total != 2 {
		t.Fatalf("expected 2 pages, got %d", total)
	}
	if len(page) != 5 {
		t.Fatalf("expected 5 models on second page, got %d", len(page))
	}
}

func TestModelPageRejectsOutOfRange(t *testing.T) {
	if _, _, err := modelPage([]string{"one"}, 1); err == nil {
		t.Fatal("expected out-of-range page to be rejected")
	}
}

func TestContainsModel(t *testing.T) {
	if !containsModel([]string{"a", "b", "c"}, "b") {
		t.Fatal("expected existing model to be found")
	}
	if containsModel([]string{"a", "b", "c"}, "z") {
		t.Fatal("expected missing model to be rejected")
	}
}

func TestParseSetKeyOptions(t *testing.T) {
	provider, apiKey, err := parseSetKeyOptions([]*discordgo.ApplicationCommandInteractionDataOption{
		{
			Name:  "key",
			Type:  discordgo.ApplicationCommandOptionString,
			Value: "secret-key",
		},
		{
			Name:  "provider",
			Type:  discordgo.ApplicationCommandOptionString,
			Value: "Gemini",
		},
	})
	if err != nil {
		t.Fatalf("expected options to parse, got %v", err)
	}
	if provider != "Gemini" {
		t.Fatalf("expected provider Gemini, got %q", provider)
	}
	if apiKey != "secret-key" {
		t.Fatalf("expected API key secret-key, got %q", apiKey)
	}
}

func TestParseSetKeyOptionsRejectsMissingProvider(t *testing.T) {
	_, _, err := parseSetKeyOptions([]*discordgo.ApplicationCommandInteractionDataOption{
		{
			Name:  "key",
			Type:  discordgo.ApplicationCommandOptionString,
			Value: "secret-key",
		},
	})
	if err == nil {
		t.Fatal("expected missing provider to fail")
	}
}
