package embeds_test

import (
	"testing"

	"gobot/internal/embeds"
)

func TestPing(t *testing.T) {
	e := embeds.Ping(42, 120)

	if e == nil {
		t.Fatal("expected non-nil embed")
	}

	if e.Title != "Pong!" {
		t.Errorf("expected title %q, got %q", "Pong!", e.Title)
	}

	if e.Color != embeds.AccentColor {
		t.Errorf("expected color %d, got %d", embeds.AccentColor, e.Color)
	}

	if e.Description == "" {
		t.Error("expected non-empty description")
	}
	if e.Description != "Gateway: `42ms`\nMessage/API: `120ms`" {
		t.Errorf("unexpected description: %q", e.Description)
	}
}

func TestPing_ZeroLatency(t *testing.T) {
	e := embeds.Ping(0, 0)

	if e == nil {
		t.Fatal("expected non-nil embed")
	}
}

func TestPing_UnavailableLatency(t *testing.T) {
	e := embeds.Ping(-1, 15)

	if got, want := e.Description, "Gateway: `unavailable`\nMessage/API: `15ms`"; got != want {
		t.Fatalf("expected %q, got %q", want, got)
	}
}

func TestError(t *testing.T) {
	e := embeds.Error("Test Title", "Test message")

	if e == nil {
		t.Fatal("expected non-nil embed")
	}
	if e.Color != embeds.ErrorColor {
		t.Errorf("expected error color %d, got %d", embeds.ErrorColor, e.Color)
	}
}

func TestKeySet(t *testing.T) {
	e := embeds.KeySet("OpenAI", "gpt-4o")

	if e == nil {
		t.Fatal("expected non-nil embed")
	}
	if e.Color != embeds.SuccessColor {
		t.Errorf("expected success color %d, got %d", embeds.SuccessColor, e.Color)
	}
}

func TestAIResponse(t *testing.T) {
	e := embeds.AIResponse("OpenAI", "gpt-4o", "What is Go?", "Go is a programming language.")

	if e == nil {
		t.Fatal("expected non-nil embed")
	}
	if e.Color != embeds.AccentColor {
		t.Errorf("expected accent color %d, got %d", embeds.AccentColor, e.Color)
	}
	if e.Description != "Go is a programming language." {
		t.Errorf("unexpected description: %q", e.Description)
	}
}

func TestAIResponse_Truncation(t *testing.T) {
	long := make([]byte, 5000)
	for i := range long {
		long[i] = 'a'
	}
	e := embeds.AIResponse("Gemini", "gemini-1.5-flash", "question", string(long))

	if len(e.Description) > 4100 {
		t.Errorf("description too long: %d chars", len(e.Description))
	}
}
