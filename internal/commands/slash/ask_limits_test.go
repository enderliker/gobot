package slash

import (
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestValidateAskQuestionLength(t *testing.T) {
	cfg := askLimitsConfig{MaxQuestionChars: 5}

	if err := validateAskQuestionLength(cfg, "hello"); err != nil {
		t.Fatalf("expected question at limit to pass, got %v", err)
	}

	err := validateAskQuestionLength(cfg, "hello!")
	if err == nil {
		t.Fatal("expected oversized question to fail")
	}

	limitErr, ok := err.(*askLimitError)
	if !ok {
		t.Fatalf("expected askLimitError, got %T", err)
	}
	if limitErr.Title != "Question Too Long" {
		t.Fatalf("expected Question Too Long title, got %q", limitErr.Title)
	}
	if !strings.Contains(limitErr.Message, "maximum allowed length is 5") {
		t.Fatalf("unexpected message: %q", limitErr.Message)
	}
}

func TestBuildAskPromptEnvelopeValidatesExactUserPromptLength(t *testing.T) {
	prev := defaultAskLimits
	defaultAskLimits = askLimitsConfig{MaxQuestionChars: 10}
	defer func() {
		defaultAskLimits = prev
	}()

	question := "1234567890"
	request, err := buildAskPromptEnvelope(question, "")
	if err != nil {
		t.Fatalf("expected question at limit to pass, got %v", err)
	}
	if request.UserPrompt != question {
		t.Fatalf("expected UserPrompt %q, got %q", question, request.UserPrompt)
	}

	err = validateAskQuestionLength(defaultAskLimits, request.UserPrompt)
	if err != nil {
		t.Fatalf("expected validated UserPrompt to pass, got %v", err)
	}

	_, err = buildAskPromptEnvelope(question+"x", "")
	if err == nil {
		t.Fatal("expected oversized UserPrompt to fail")
	}
}

func TestAskLimiterUserCooldown(t *testing.T) {
	now := time.Date(2026, 7, 7, 20, 0, 0, 0, time.UTC)
	limiter := newAskLimiter(askLimitsConfig{
		UserCooldown: 10 * time.Second,
	}, func() time.Time {
		return now
	})

	lease, err := limiter.Acquire("user-1", "guild-1")
	if err != nil {
		t.Fatalf("expected first acquire to pass, got %v", err)
	}
	lease.Release()

	_, err = limiter.Acquire("user-1", "guild-1")
	if err == nil {
		t.Fatal("expected cooldown rejection")
	}

	limitErr, ok := err.(*askLimitError)
	if !ok {
		t.Fatalf("expected askLimitError, got %T", err)
	}
	if limitErr.Title != "Slow Down" {
		t.Fatalf("expected Slow Down, got %q", limitErr.Title)
	}

	now = now.Add(10 * time.Second)
	lease, err = limiter.Acquire("user-1", "guild-1")
	if err != nil {
		t.Fatalf("expected acquire after cooldown to pass, got %v", err)
	}
	lease.Release()
}

func TestAskLimiterUserCooldownIsScopedPerGuild(t *testing.T) {
	now := time.Date(2026, 7, 7, 20, 0, 0, 0, time.UTC)
	limiter := newAskLimiter(askLimitsConfig{
		UserCooldown: 10 * time.Second,
	}, func() time.Time {
		return now
	})

	lease1, err := limiter.Acquire("user-1", "guild-1")
	if err != nil {
		t.Fatalf("expected first acquire to pass, got %v", err)
	}
	lease1.Release()

	lease2, err := limiter.Acquire("user-1", "guild-2")
	if err != nil {
		t.Fatalf("expected same user in another guild to bypass cooldown, got %v", err)
	}
	lease2.Release()
}

func TestAskLimiterGuildWindowLimit(t *testing.T) {
	now := time.Date(2026, 7, 7, 20, 0, 0, 0, time.UTC)
	limiter := newAskLimiter(askLimitsConfig{
		GuildRateWindow:  1 * time.Minute,
		GuildMaxRequests: 2,
	}, func() time.Time {
		return now
	})

	lease1, err := limiter.Acquire("user-1", "guild-1")
	if err != nil {
		t.Fatalf("expected first acquire to pass, got %v", err)
	}
	lease1.Release()

	lease2, err := limiter.Acquire("user-2", "guild-1")
	if err != nil {
		t.Fatalf("expected second acquire to pass, got %v", err)
	}
	lease2.Release()

	_, err = limiter.Acquire("user-3", "guild-1")
	if err == nil {
		t.Fatal("expected guild rate limit rejection")
	}

	limitErr, ok := err.(*askLimitError)
	if !ok {
		t.Fatalf("expected askLimitError, got %T", err)
	}
	if limitErr.Title != "Server Rate Limit Reached" {
		t.Fatalf("expected Server Rate Limit Reached, got %q", limitErr.Title)
	}

	now = now.Add(61 * time.Second)
	lease3, err := limiter.Acquire("user-3", "guild-1")
	if err != nil {
		t.Fatalf("expected acquire after window to pass, got %v", err)
	}
	lease3.Release()
}

func TestAskLimiterGuildConcurrencyLimit(t *testing.T) {
	limiter := newAskLimiter(askLimitsConfig{
		GuildMaxConcurrent: 2,
	}, time.Now)

	lease1, err := limiter.Acquire("user-1", "guild-1")
	if err != nil {
		t.Fatalf("expected first acquire to pass, got %v", err)
	}
	lease2, err := limiter.Acquire("user-2", "guild-1")
	if err != nil {
		t.Fatalf("expected second acquire to pass, got %v", err)
	}

	_, err = limiter.Acquire("user-3", "guild-1")
	if err == nil {
		t.Fatal("expected concurrency rejection")
	}

	limitErr, ok := err.(*askLimitError)
	if !ok {
		t.Fatalf("expected askLimitError, got %T", err)
	}
	if limitErr.Title != "Server Busy" {
		t.Fatalf("expected Server Busy, got %q", limitErr.Title)
	}

	lease1.Release()

	lease3, err := limiter.Acquire("user-3", "guild-1")
	if err != nil {
		t.Fatalf("expected acquire after release to pass, got %v", err)
	}

	lease2.Release()
	lease3.Release()
}

func TestAskLimiterConcurrentAcquireRelease(t *testing.T) {
	limiter := newAskLimiter(askLimitsConfig{
		GuildMaxConcurrent: 100,
	}, time.Now)

	var wg sync.WaitGroup
	for idx := 0; idx < 64; idx++ {
		wg.Add(1)
		go func(idx int) {
			defer wg.Done()

			lease, err := limiter.Acquire(fmt.Sprintf("user-%d", idx), "guild-1")
			if err != nil {
				t.Errorf("unexpected acquire error: %v", err)
				return
			}
			time.Sleep(time.Millisecond)
			lease.Release()
		}(idx)
	}
	wg.Wait()

	limiter.mu.Lock()
	defer limiter.mu.Unlock()
	if got := limiter.guildInFlight["guild-1"]; got != 0 {
		t.Fatalf("expected no in-flight requests after releases, got %d", got)
	}
}
