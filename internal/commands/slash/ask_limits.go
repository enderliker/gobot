package slash

import (
	"fmt"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	defaultMaxQuestionChars      = 4000
	defaultAskUserCooldown       = 10 * time.Second
	defaultAskGuildRateWindow    = 1 * time.Minute
	defaultAskGuildMaxRequests   = 12
	defaultAskGuildMaxConcurrent = 3
)

type askLimitsConfig struct {
	MaxQuestionChars   int
	UserCooldown       time.Duration
	GuildRateWindow    time.Duration
	GuildMaxRequests   int
	GuildMaxConcurrent int
}

var defaultAskLimits = askLimitsConfig{
	MaxQuestionChars:   defaultMaxQuestionChars,
	UserCooldown:       defaultAskUserCooldown,
	GuildRateWindow:    defaultAskGuildRateWindow,
	GuildMaxRequests:   defaultAskGuildMaxRequests,
	GuildMaxConcurrent: defaultAskGuildMaxConcurrent,
}

var defaultAskLimiter = newAskLimiter(defaultAskLimits, time.Now)

type askLimitError struct {
	Title   string
	Message string
}

func (e *askLimitError) Error() string {
	return e.Title + ": " + e.Message
}

type askLease struct {
	once    sync.Once
	release func()
}

func (l *askLease) Release() {
	if l == nil {
		return
	}
	l.once.Do(l.release)
}

type askLimiter struct {
	mu            sync.Mutex
	now           func() time.Time
	cfg           askLimitsConfig
	userLastAsk   map[string]time.Time
	guildRequests map[string][]time.Time
	guildInFlight map[string]int
}

func newAskLimiter(cfg askLimitsConfig, now func() time.Time) *askLimiter {
	if now == nil {
		now = time.Now
	}

	return &askLimiter{
		now:           now,
		cfg:           cfg,
		userLastAsk:   make(map[string]time.Time),
		guildRequests: make(map[string][]time.Time),
		guildInFlight: make(map[string]int),
	}
}

func validateAskQuestionLength(cfg askLimitsConfig, question string) error {
	if cfg.MaxQuestionChars <= 0 {
		return nil
	}

	count := utf8.RuneCountInString(question)
	if count <= cfg.MaxQuestionChars {
		return nil
	}

	return &askLimitError{
		Title: "Question Too Long",
		Message: fmt.Sprintf(
			"Your question is %d characters long. The maximum allowed length is %d characters.",
			count,
			cfg.MaxQuestionChars,
		),
	}
}

func (l *askLimiter) Acquire(userID, guildID string) (*askLease, error) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()

	if wait := l.userCooldownRemaining(userID, guildID, now); wait > 0 {
		return nil, &askLimitError{
			Title:   "Slow Down",
			Message: fmt.Sprintf("Please wait %s before using `/ask` again.", formatAskWait(wait)),
		}
	}

	if wait := l.guildRateLimitRemaining(guildID, now); wait > 0 {
		return nil, &askLimitError{
			Title:   "Server Rate Limit Reached",
			Message: fmt.Sprintf("This server has reached the current `/ask` rate limit. Please wait %s and try again.", formatAskWait(wait)),
		}
	}

	if l.cfg.GuildMaxConcurrent > 0 && guildID != "" && l.guildInFlight[guildID] >= l.cfg.GuildMaxConcurrent {
		return nil, &askLimitError{
			Title:   "Server Busy",
			Message: fmt.Sprintf("This server already has %d `/ask` requests in progress. Please wait for one to finish and try again.", l.cfg.GuildMaxConcurrent),
		}
	}

	if l.cfg.UserCooldown > 0 && userID != "" {
		l.userLastAsk[userGuildKey(userID, guildID)] = now
	}
	if l.cfg.GuildMaxRequests > 0 && l.cfg.GuildRateWindow > 0 && guildID != "" {
		pruned := l.pruneGuildRequests(guildID, now)
		l.guildRequests[guildID] = append(pruned, now)
	}
	if l.cfg.GuildMaxConcurrent > 0 && guildID != "" {
		l.guildInFlight[guildID]++
	}

	return &askLease{
		release: func() {
			l.mu.Lock()
			defer l.mu.Unlock()

			if guildID == "" || l.cfg.GuildMaxConcurrent <= 0 {
				return
			}

			if current := l.guildInFlight[guildID]; current <= 1 {
				delete(l.guildInFlight, guildID)
			} else {
				l.guildInFlight[guildID] = current - 1
			}
		},
	}, nil
}

func (l *askLimiter) userCooldownRemaining(userID, guildID string, now time.Time) time.Duration {
	if l.cfg.UserCooldown <= 0 || userID == "" {
		return 0
	}

	last, ok := l.userLastAsk[userGuildKey(userID, guildID)]
	if !ok {
		return 0
	}

	wait := last.Add(l.cfg.UserCooldown).Sub(now)
	if wait < 0 {
		return 0
	}
	return wait
}

func userGuildKey(userID, guildID string) string {
	return userID + ":" + guildID
}

func (l *askLimiter) guildRateLimitRemaining(guildID string, now time.Time) time.Duration {
	if l.cfg.GuildMaxRequests <= 0 || l.cfg.GuildRateWindow <= 0 || guildID == "" {
		return 0
	}

	pruned := l.pruneGuildRequests(guildID, now)
	l.guildRequests[guildID] = pruned
	if len(pruned) < l.cfg.GuildMaxRequests {
		return 0
	}

	wait := pruned[0].Add(l.cfg.GuildRateWindow).Sub(now)
	if wait < 0 {
		return 0
	}
	return wait
}

func (l *askLimiter) pruneGuildRequests(guildID string, now time.Time) []time.Time {
	requests := l.guildRequests[guildID]
	if len(requests) == 0 {
		return nil
	}

	cutoff := now.Add(-l.cfg.GuildRateWindow)
	keep := requests[:0]
	for _, ts := range requests {
		if ts.After(cutoff) {
			keep = append(keep, ts)
		}
	}

	if len(keep) == 0 {
		return nil
	}

	return keep
}

func formatAskWait(wait time.Duration) string {
	if wait <= 0 {
		return "1 second"
	}

	seconds := int((wait + time.Second - 1) / time.Second)
	if seconds == 1 {
		return "1 second"
	}
	return fmt.Sprintf("%d seconds", seconds)
}
