package ai

import (
	"errors"
	"net"
	"strings"
	"testing"
)

func TestSanitizeProviderErrorRedactsGeminiKeyFromURL(t *testing.T) {
	apiKey := "key+with/special?chars"
	err := sanitizeProviderError(errors.New("Get \"https://generativelanguage.googleapis.com/v1beta/models?key=key%2Bwith%2Fspecial%3Fchars\": dial tcp timeout"), apiKey)

	msg := err.Error()
	if msg == "" {
		t.Fatal("expected sanitized error message")
	}
	if strings.Contains(msg, apiKey) || strings.Contains(msg, "key%2Bwith%2Fspecial%3Fchars") {
		t.Fatalf("expected sanitized error to redact API key, got %q", msg)
	}
	if want := "key=[REDACTED]"; !strings.Contains(msg, want) {
		t.Fatalf("expected sanitized error to include %q, got %q", want, msg)
	}
}

func TestUserFacingErrorFallsBackToGenericMessage(t *testing.T) {
	msg := UserFacingError(errors.New("unexpected provider failure with opaque payload secret-value"))
	if msg != genericProviderErrorMessage {
		t.Fatalf("expected generic provider error, got %q", msg)
	}
}

func TestUserFacingErrorPreservesTimeoutClassification(t *testing.T) {
	err := sanitizeProviderError(timeoutErr{msg: "Get \"https://generativelanguage.googleapis.com/v1beta/models?key=secret\": i/o timeout"}, "secret")

	msg := UserFacingError(err)
	if msg != "The AI provider took too long to respond. Please try again in a moment." {
		t.Fatalf("expected timeout user-facing error, got %q", msg)
	}
}

type timeoutErr struct {
	msg string
}

func (e timeoutErr) Error() string   { return e.msg }
func (e timeoutErr) Timeout() bool   { return true }
func (e timeoutErr) Temporary() bool { return true }

var _ net.Error = timeoutErr{}

func TestIsImageModel(t *testing.T) {
	tests := []struct {
		model string
		want  bool
	}{
		{"gemini-3.1-flash-image-preview", false},
		{"imagen-3.0-generate-002", true},
		{"dall-e-3", true},
		{"gpt-4o", false},
		{"gemini-3.1-flash-image", true},
		{"gemini-3-pro-image", true},
		{"gemini-2.5-flash-image", true},
	}
	for _, tt := range tests {
		if got := IsImageModel(tt.model); got != tt.want {
			t.Errorf("IsImageModel(%q) = %v; want %v", tt.model, got, tt.want)
		}
	}
}

func TestIsUserFacingError(t *testing.T) {
	tests := []struct {
		err  error
		want bool
	}{
		{errors.New("429 resource_exhausted"), true},
		{errors.New("unauthorized api key"), true},
		{errors.New("some system error"), false},
	}
	for _, tt := range tests {
		if got := IsUserFacingError(tt.err); got != tt.want {
			t.Errorf("IsUserFacingError(%v) = %v; want %v", tt.err, got, tt.want)
		}
	}
}

func TestUserFacingErrorQuota(t *testing.T) {
	tests := []struct {
		err  error
		want string
	}{
		{
			errors.New("gemini (429 RESOURCE_EXHAUSTED): quota exceeded, limit: 20 * Please retry in 7.093550827s."),
			"The provider quota or rate limit has been reached (Limit: 20). Please retry in 7.093550827s.",
		},
		{
			errors.New("gemini (429 RESOURCE_EXHAUSTED): limit: 15 (metric: requests)"),
			"The provider quota or rate limit has been reached (Limit: 15). Please try again later.",
		},
		{
			errors.New("rate limited. Please retry in 10s"),
			"The provider rate limit has been reached. Please retry in 10s.",
		},
		{
			errors.New("generic quota error"),
			"The provider quota or rate limit has been reached. Please try again later.",
		},
	}
	for _, tt := range tests {
		if got := UserFacingError(tt.err); got != tt.want {
			t.Errorf("UserFacingError(%v) = %q; want %q", tt.err, got, tt.want)
		}
	}
}
