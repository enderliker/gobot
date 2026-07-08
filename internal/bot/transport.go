package bot

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// rateLimitTransport wraps an http.RoundTripper and handles the case where
// Discord (or Cloudflare in front of Discord) returns a 429 Too Many Requests
// with a non-JSON body. discordgo v0.29.0 returns immediately on JSON parse
// failure (restapi.go:283-284) without sleeping, causing every retry to also
// hit the rate limit. This transport:
//  1. Detects 429 responses with non-JSON bodies (Cloudflare plain-text errors)
//  2. Reads the Retry-After header (or falls back to 5 seconds)
//  3. Sleeps for that duration
//  4. Replaces the body with a minimal valid JSON so discordgo can proceed
//     (it will return a RateLimitError, which our retry logic handles)
type rateLimitTransport struct {
	base         http.RoundTripper
	writeLimiter *discordWriteLimiter
}

const defaultDiscordWriteInterval = 350 * time.Millisecond

type discordWriteLimiter struct {
	mu          sync.Mutex
	minInterval time.Duration
	nextAllowed time.Time
}

// syntheticRateLimit is the JSON body we inject so discordgo can unmarshal it.
type syntheticRateLimit struct {
	Message    string  `json:"message"`
	RetryAfter float64 `json:"retry_after"`
	Global     bool    `json:"global"`
}

func (t *rateLimitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if t.writeLimiter != nil && shouldThrottleDiscordWrite(req) {
		t.writeLimiter.Wait()
	}

	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil {
		return resp, err
	}

	if resp.StatusCode != http.StatusTooManyRequests {
		return resp, nil
	}

	// Read the full body so we can inspect it.
	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	// Check whether the body is valid JSON.
	if readErr == nil && json.Valid(body) {
		// Normal Discord rate-limit — restore the body and let discordgo handle it.
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp, nil
	}

	// Non-JSON 429 (Cloudflare / nginx plain-text error).
	// Determine how long to sleep.
	sleep := 5 * time.Second // conservative default

	if ra := resp.Header.Get("Retry-After"); ra != "" {
		if secs, convErr := strconv.ParseFloat(ra, 64); convErr == nil && secs > 0 {
			sleep = time.Duration(secs*1000) * time.Millisecond
		}
	}

	rawBody := ""
	if readErr == nil {
		rawBody = strings.TrimSpace(string(body))
	}
	method := ""
	path := ""
	if req != nil && req.URL != nil {
		method = req.Method
		path = req.URL.Path
	}
	log.Printf("[HTTP] Non-JSON 429 from Discord %s %s (body: %q), sleeping %s", method, path, rawBody, sleep)

	time.Sleep(sleep)

	// Synthesise a JSON body that discordgo can unmarshal successfully.
	// RetryAfter=0 so discordgo won't sleep again on top of what we already slept.
	synthetic, _ := json.Marshal(syntheticRateLimit{
		Message:    "non-json rate limit intercepted by rateLimitTransport",
		RetryAfter: 0,
		Global:     false,
	})
	resp.Body = io.NopCloser(bytes.NewReader(synthetic))
	resp.Header.Set("Content-Type", "application/json")
	return resp, nil
}

// newRateLimitTransport wraps the given base transport (or http.DefaultTransport
// if base is nil) with the non-JSON 429 handling logic.
func newRateLimitTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	return &rateLimitTransport{
		base:         base,
		writeLimiter: newDiscordWriteLimiter(),
	}
}

func shouldThrottleDiscordWrite(req *http.Request) bool {
	if req == nil || req.URL == nil {
		return false
	}

	switch req.Method {
	case http.MethodPost, http.MethodPatch, http.MethodPut, http.MethodDelete:
	default:
		return false
	}

	host := strings.ToLower(req.URL.Host)
	return strings.Contains(host, "discord.com") || strings.Contains(host, "discordapp.com")
}

func newDiscordWriteLimiter() *discordWriteLimiter {
	interval := defaultDiscordWriteInterval
	if raw := strings.TrimSpace(os.Getenv("DISCORD_WRITE_MIN_INTERVAL")); raw != "" {
		if parsed, err := time.ParseDuration(raw); err == nil && parsed > 0 {
			interval = parsed
		} else {
			log.Printf("[HTTP] invalid DISCORD_WRITE_MIN_INTERVAL=%q, using default %s", raw, interval)
		}
	}

	return &discordWriteLimiter{minInterval: interval}
}

func (l *discordWriteLimiter) Wait() {
	if l == nil || l.minInterval <= 0 {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()
	if wait := time.Until(l.nextAllowed); wait > 0 {
		time.Sleep(wait)
		now = time.Now()
	}

	l.nextAllowed = now.Add(l.minInterval)
}
