package bot

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestShouldThrottleDiscordWrite(t *testing.T) {
	tests := []struct {
		name   string
		method string
		url    string
		want   bool
	}{
		{name: "discord post", method: http.MethodPost, url: "https://discord.com/api/v10/applications", want: true},
		{name: "discord patch", method: http.MethodPatch, url: "https://discord.com/api/v10/webhooks", want: true},
		{name: "discord get", method: http.MethodGet, url: "https://discord.com/api/v10/applications", want: false},
		{name: "other host post", method: http.MethodPost, url: "https://example.com/api", want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := http.NewRequest(tt.method, tt.url, nil)
			if err != nil {
				t.Fatalf("new request: %v", err)
			}

			if got := shouldThrottleDiscordWrite(req); got != tt.want {
				t.Fatalf("expected %v, got %v", tt.want, got)
			}
		})
	}
}

func TestParseDiscordRetryAfterHeader(t *testing.T) {
	tests := []struct {
		name   string
		raw    string
		want   time.Duration
		wantOK bool
	}{
		{name: "seconds integer", raw: "2", want: 2 * time.Second, wantOK: true},
		{name: "seconds float", raw: "2.5", want: 2500 * time.Millisecond, wantOK: true},
		{name: "millisecond style integer", raw: "2770", want: 2770 * time.Millisecond, wantOK: true},
		{name: "invalid", raw: "abc", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, ok := parseDiscordRetryAfterHeader(tt.raw)
			if ok != tt.wantOK {
				t.Fatalf("expected ok=%v, got %v", tt.wantOK, ok)
			}
			if ok && got != tt.want {
				t.Fatalf("expected %s, got %s", tt.want, got)
			}
		})
	}
}

func TestRateLimitTransportSynthesizesCloudflare1015AndOpensCooldown(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	baseCalls := 0

	transport := &rateLimitTransport{
		base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			baseCalls++
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Status:     "429 Too Many Requests",
				Header: http.Header{
					"Retry-After": []string{"2770"},
					"Server":      []string{"cloudflare"},
				},
				Body:    io.NopCloser(strings.NewReader("<html><title>1015</title><body>cloudflare error 1015</body></html>")),
				Request: req,
			}, nil
		}),
		writeLimiter: &discordWriteLimiter{},
		breaker:      newDiscordAPICircuitBreaker(func() time.Time { return now }),
	}

	req := mustNewRequest(t, http.MethodDelete, "https://discord.com/api/v10/webhooks/1/token/messages/@original")
	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip: %v", err)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429, got %d", resp.StatusCode)
	}
	if got := decodeSyntheticRetryAfter(t, resp); got != discord429CooldownBase {
		t.Fatalf("expected synthetic retry_after %s, got %s", discord429CooldownBase, got)
	}
	if baseCalls != 1 {
		t.Fatalf("expected base transport to be called once, got %d", baseCalls)
	}

	resp, err = transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip during cooldown: %v", err)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 during cooldown, got %d", resp.StatusCode)
	}
	if got := decodeSyntheticRetryAfter(t, resp); got != discord429CooldownBase {
		t.Fatalf("expected cooldown retry_after %s, got %s", discord429CooldownBase, got)
	}
	if baseCalls != 1 {
		t.Fatalf("expected cooldown to short-circuit without extra base call, got %d", baseCalls)
	}
}

func TestRateLimitTransportBypassesCooldownForInteractionCallback(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	baseCalls := 0

	transport := &rateLimitTransport{
		base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			baseCalls++
			if baseCalls == 1 {
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Status:     "429 Too Many Requests",
					Header: http.Header{
						"Retry-After": []string{"2770"},
						"Server":      []string{"cloudflare"},
					},
					Body:    io.NopCloser(strings.NewReader("<html>cloudflare 1015</html>")),
					Request: req,
				}, nil
			}

			return &http.Response{
				StatusCode: http.StatusNoContent,
				Status:     "204 No Content",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader("")),
				Request:    req,
			}, nil
		}),
		writeLimiter: &discordWriteLimiter{},
		breaker:      newDiscordAPICircuitBreaker(func() time.Time { return now }),
	}

	nonEssential := mustNewRequest(t, http.MethodDelete, "https://discord.com/api/v10/webhooks/1/token/messages/@original")
	if _, err := transport.RoundTrip(nonEssential); err != nil {
		t.Fatalf("initial round trip: %v", err)
	}

	callback := mustNewRequest(t, http.MethodPost, "https://discord.com/api/v10/interactions/1/token/callback")
	resp, err := transport.RoundTrip(callback)
	if err != nil {
		t.Fatalf("callback round trip: %v", err)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("expected callback to hit base transport and succeed, got %d", resp.StatusCode)
	}
	if baseCalls != 2 {
		t.Fatalf("expected callback to bypass cooldown and call base transport, got %d calls", baseCalls)
	}
}

func TestRateLimitTransportOpensCooldownAfterConsecutive429s(t *testing.T) {
	now := time.Date(2026, 7, 9, 12, 0, 0, 0, time.UTC)
	baseCalls := 0

	body := `{"message":"rate limited","retry_after":1}`
	transport := &rateLimitTransport{
		base: roundTripFunc(func(req *http.Request) (*http.Response, error) {
			baseCalls++
			return &http.Response{
				StatusCode: http.StatusTooManyRequests,
				Status:     "429 Too Many Requests",
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(body)),
				Request:    req,
			}, nil
		}),
		writeLimiter: &discordWriteLimiter{},
		breaker:      newDiscordAPICircuitBreaker(func() time.Time { return now }),
	}

	req := mustNewRequest(t, http.MethodPost, "https://discord.com/api/v10/channels/1/messages")
	for range discord429CooldownThreshold {
		resp, err := transport.RoundTrip(req)
		if err != nil {
			t.Fatalf("round trip: %v", err)
		}
		if resp.StatusCode != http.StatusTooManyRequests {
			t.Fatalf("expected 429, got %d", resp.StatusCode)
		}
	}

	resp, err := transport.RoundTrip(req)
	if err != nil {
		t.Fatalf("round trip during cooldown: %v", err)
	}
	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("expected 429 during cooldown, got %d", resp.StatusCode)
	}
	if baseCalls != discord429CooldownThreshold {
		t.Fatalf("expected cooldown to short-circuit after %d base calls, got %d", discord429CooldownThreshold, baseCalls)
	}
	if got := decodeSyntheticRetryAfter(t, resp); got != discord429CooldownBase {
		t.Fatalf("expected cooldown retry_after %s, got %s", discord429CooldownBase, got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func mustNewRequest(t *testing.T, method, rawURL string) *http.Request {
	t.Helper()

	req, err := http.NewRequest(method, rawURL, nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	return req
}

func decodeSyntheticRetryAfter(t *testing.T, resp *http.Response) time.Duration {
	t.Helper()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	var payload syntheticRateLimit
	if err := json.Unmarshal(body, &payload); err != nil {
		t.Fatalf("unmarshal synthetic body: %v", err)
	}
	return floatSecondsToDuration(payload.RetryAfter)
}
