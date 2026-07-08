package ai

import (
	"net/url"
	"strings"
	"testing"
)

func TestBuildGeminiModelsURL(t *testing.T) {
	raw := buildGeminiModelsURL("key+with/special?chars")

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("expected valid URL, got error: %v", err)
	}
	if got := u.Query().Get("key"); got != "key+with/special?chars" {
		t.Fatalf("expected key to survive query encoding, got %q", got)
	}
}

func TestBuildGeminiGenerateURL(t *testing.T) {
	raw := buildGeminiGenerateURL("gemini/flash?beta=true", "key+with/special?chars")

	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("expected valid URL, got error: %v", err)
	}
	if !strings.Contains(raw, "/models/gemini%2Fflash%3Fbeta=true:generateContent") {
		t.Fatalf("expected escaped model in path, got %q", raw)
	}
	if got := u.Query().Get("key"); got != "key+with/special?chars" {
		t.Fatalf("expected key to survive query encoding, got %q", got)
	}
}
