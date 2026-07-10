package bot

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// rateLimitTransport wraps Discord REST requests so the bot behaves sanely on
// shared-host/IP rate limits:
//   - Non-JSON 429 bodies (e.g. Cloudflare 1015 HTML) are converted to valid
//     JSON so discordgo can parse them.
//   - Repeated 429s open a short-lived cooldown for non-essential REST calls so
//     we stop contributing traffic while the host IP is being throttled.
//   - Cloudflare 1015 is logged distinctly to make infra-level issues obvious.
type rateLimitTransport struct {
	base         http.RoundTripper
	writeLimiter *discordWriteLimiter
	breaker      *discordAPICircuitBreaker
}

const (
	defaultDiscordWriteInterval      = 350 * time.Millisecond
	defaultDiscordRateLimitRetry     = 5 * time.Second
	discord429CooldownThreshold      = 3
	discord429CooldownBase           = 30 * time.Second
	discord429CooldownCap            = 4 * time.Minute
	cloudflareRateLimitCode          = "1015"
	maxLoggedDiscordRateLimitBodyLen = 256
)

type discordWriteLimiter struct {
	mu          sync.Mutex
	minInterval time.Duration
	nextAllowed time.Time
}

type discordAPICircuitBreaker struct {
	mu             sync.Mutex
	now            func() time.Time
	consecutive429 int
	cooldownUntil  time.Time
	nextCooldown   time.Duration
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

	if t.breaker != nil && isDiscordAPIRequest(req) {
		if remaining, ok := t.breaker.cooldownRemaining(); ok && shouldDeferDiscordRequestDuringCooldown(req) {
			return newSyntheticRateLimitResponse(req, remaining, "discord api cooldown active"), nil
		}
	}

	resp, err := t.base.RoundTrip(req)
	if err != nil || resp == nil || !isDiscordAPIRequest(req) {
		return resp, err
	}

	if resp.StatusCode != http.StatusTooManyRequests {
		if t.breaker != nil {
			t.breaker.recordSuccess()
		}
		return resp, nil
	}

	body, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()

	var rawBody string
	if readErr == nil {
		rawBody = strings.TrimSpace(string(body))
	}

	retryAfter := extractDiscordRetryAfter(resp.Header, body)
	isCloudflare := isCloudflare1015(resp, rawBody)

	var cooldown time.Duration
	var opened bool
	if t.breaker != nil {
		cooldown, opened = t.breaker.recordRateLimit(isCloudflare, retryAfter)
	}

	if cooldown > retryAfter {
		retryAfter = cooldown
	}
	if retryAfter <= 0 {
		retryAfter = defaultDiscordRateLimitRetry
	}
	retryAfter = capDiscordCooldown(retryAfter)

	method, path := requestDebugFields(req)
	if isCloudflare {
		log.Printf("[HTTP] Cloudflare 1015 IP rate limit on Discord %s %s; cooldown=%s body=%q", method, path, retryAfter, truncateDiscordRateLimitBody(rawBody))
	} else if opened {
		log.Printf("[HTTP] Discord API returned %d consecutive 429s; opening cooldown=%s on %s %s", discord429CooldownThreshold, retryAfter, method, path)
	}

	if readErr == nil && json.Valid(body) {
		if opened && shouldDeferDiscordRequestDuringCooldown(req) {
			return newSyntheticRateLimitResponse(req, retryAfter, "discord api cooldown active"), nil
		}

		resp.Body = io.NopCloser(bytes.NewReader(body))
		return resp, nil
	}

	log.Printf("[HTTP] Non-JSON 429 from Discord %s %s; synthesized retry_after=%s body=%q", method, path, retryAfter, truncateDiscordRateLimitBody(rawBody))
	return newSyntheticRateLimitResponse(req, retryAfter, "non-json rate limit intercepted by rateLimitTransport"), nil
}

// newRateLimitTransport wraps the given base transport (or http.DefaultTransport
// if base is nil) with the Discord-specific non-JSON 429 handling logic.
func newRateLimitTransport(base http.RoundTripper) http.RoundTripper {
	return newRateLimitTransportWithClock(base, time.Now)
}

func newRateLimitTransportWithClock(base http.RoundTripper, now func() time.Time) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}
	if now == nil {
		now = time.Now
	}

	return &rateLimitTransport{
		base:         base,
		writeLimiter: newDiscordWriteLimiter(),
		breaker:      newDiscordAPICircuitBreaker(now),
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

	return isDiscordAPIRequest(req)
}

func isDiscordAPIRequest(req *http.Request) bool {
	if req == nil || req.URL == nil {
		return false
	}

	host := strings.ToLower(req.URL.Host)
	return strings.Contains(host, "discord.com") || strings.Contains(host, "discordapp.com")
}

func shouldDeferDiscordRequestDuringCooldown(req *http.Request) bool {
	if req == nil || req.URL == nil {
		return false
	}

	path := req.URL.Path
	return !(strings.Contains(path, "/interactions/") && strings.HasSuffix(path, "/callback"))
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

func newDiscordAPICircuitBreaker(now func() time.Time) *discordAPICircuitBreaker {
	if now == nil {
		now = time.Now
	}
	return &discordAPICircuitBreaker{now: now}
}

func (b *discordAPICircuitBreaker) cooldownRemaining() (time.Duration, bool) {
	if b == nil {
		return 0, false
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	if !b.cooldownUntil.After(now) {
		return 0, false
	}
	return b.cooldownUntil.Sub(now), true
}

func (b *discordAPICircuitBreaker) recordSuccess() {
	if b == nil {
		return
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	b.consecutive429 = 0
	if !b.cooldownUntil.After(now) {
		b.nextCooldown = 0
	}
}

func (b *discordAPICircuitBreaker) recordRateLimit(forceOpen bool, suggested time.Duration) (time.Duration, bool) {
	if b == nil {
		return 0, false
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.now()
	b.consecutive429++

	alreadyOpen := b.cooldownUntil.After(now)
	if !forceOpen && !alreadyOpen && b.consecutive429 < discord429CooldownThreshold {
		return 0, false
	}

	if b.nextCooldown <= 0 {
		b.nextCooldown = discord429CooldownBase
	} else {
		b.nextCooldown *= 2
	}
	if b.nextCooldown > discord429CooldownCap {
		b.nextCooldown = discord429CooldownCap
	}

	cooldown := b.nextCooldown
	if suggested > cooldown {
		cooldown = suggested
	}
	cooldown = capDiscordCooldown(cooldown)

	nextUntil := now.Add(cooldown)
	if nextUntil.After(b.cooldownUntil) {
		b.cooldownUntil = nextUntil
	}
	b.consecutive429 = 0
	return b.cooldownUntil.Sub(now), true
}

func extractDiscordRetryAfter(header http.Header, body []byte) time.Duration {
	if read := extractDiscordRetryAfterFromBody(body); read > 0 {
		return capDiscordCooldown(read)
	}

	if read, ok := parseDiscordRetryAfterHeader(header.Get("Retry-After")); ok {
		return capDiscordCooldown(read)
	}

	return 0
}

func extractDiscordRetryAfterFromBody(body []byte) time.Duration {
	if len(body) == 0 || !json.Valid(body) {
		return 0
	}

	var payload syntheticRateLimit
	if err := json.Unmarshal(body, &payload); err != nil {
		return 0
	}
	if payload.RetryAfter <= 0 {
		return 0
	}

	return floatSecondsToDuration(payload.RetryAfter)
}

func parseDiscordRetryAfterHeader(raw string) (time.Duration, bool) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, false
	}

	if at, err := http.ParseTime(raw); err == nil {
		if wait := time.Until(at); wait > 0 {
			return wait, true
		}
		return 0, false
	}

	value, err := strconv.ParseFloat(raw, 64)
	if err != nil || value <= 0 {
		return 0, false
	}

	// Cloudflare/proxies sometimes emit integer millisecond-style values in the
	// header, while Discord bodies use seconds. Treat very large integer values
	// as milliseconds to avoid hour-scale sleeps from bogus parsing.
	if !strings.ContainsAny(raw, ".eE") && value >= 1000 {
		return time.Duration(value) * time.Millisecond, true
	}

	return floatSecondsToDuration(value), true
}

func floatSecondsToDuration(seconds float64) time.Duration {
	whole, frac := math.Modf(seconds)
	return time.Duration(whole)*time.Second + time.Duration(frac*1000)*time.Millisecond
}

func capDiscordCooldown(d time.Duration) time.Duration {
	if d <= 0 {
		return 0
	}
	if d > discord429CooldownCap {
		return discord429CooldownCap
	}
	return d
}

func isCloudflare1015(resp *http.Response, rawBody string) bool {
	if resp == nil {
		return false
	}

	rawBody = strings.ToLower(rawBody)
	server := strings.ToLower(resp.Header.Get("Server"))
	return strings.Contains(rawBody, cloudflareRateLimitCode) &&
		(strings.Contains(rawBody, "cloudflare") || strings.Contains(server, "cloudflare"))
}

func newSyntheticRateLimitResponse(req *http.Request, retryAfter time.Duration, message string) *http.Response {
	if retryAfter <= 0 {
		retryAfter = defaultDiscordRateLimitRetry
	}

	synthetic, _ := json.Marshal(syntheticRateLimit{
		Message:    message,
		RetryAfter: retryAfter.Seconds(),
		Global:     false,
	})

	header := make(http.Header)
	header.Set("Content-Type", "application/json")
	header.Set("Retry-After", strconv.FormatFloat(retryAfter.Seconds(), 'f', 3, 64))

	return &http.Response{
		StatusCode: http.StatusTooManyRequests,
		Status:     "429 Too Many Requests",
		Header:     header,
		Body:       io.NopCloser(bytes.NewReader(synthetic)),
		Request:    req,
	}
}

func truncateDiscordRateLimitBody(raw string) string {
	raw = strings.TrimSpace(raw)
	if len(raw) <= maxLoggedDiscordRateLimitBodyLen {
		return raw
	}
	return raw[:maxLoggedDiscordRateLimitBodyLen] + "..."
}

func requestDebugFields(req *http.Request) (method, path string) {
	if req == nil || req.URL == nil {
		return "", ""
	}
	return req.Method, req.URL.Path
}
