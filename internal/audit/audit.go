package audit

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"sync"
	"time"
)

const envAuditLogPath = "AUDIT_LOG_PATH"

var reservedFields = map[string]struct{}{
	"timestamp":   {},
	"guild_id":    {},
	"channel_id":  {},
	"actor_id":    {},
	"action_type": {},
	"outcome":     {},
}

type Event struct {
	Timestamp  time.Time
	GuildID    string
	ChannelID  string
	ActorID    string
	ActionType string
	Outcome    string
	Fields     map[string]any
}

type Logger struct {
	events    chan Event
	done      chan struct{}
	writer    io.Writer
	closeFn   func() error
	closeOnce sync.Once
}

var (
	defaultMu  sync.Mutex
	defaultLog *Logger
)

func Default() *Logger {
	defaultMu.Lock()
	defer defaultMu.Unlock()

	if defaultLog == nil {
		defaultLog = newDefaultLogger()
	}
	return defaultLog
}

// SetDefaultForTest swaps the process-wide default audit logger.
// It is intended for tests that need deterministic capture of audit output.
func SetDefaultForTest(logger *Logger) func() {
	defaultMu.Lock()
	prev := defaultLog
	defaultLog = logger
	defaultMu.Unlock()

	return func() {
		defaultMu.Lock()
		defaultLog = prev
		defaultMu.Unlock()
	}
}

func Log(event Event) {
	Default().Log(event)
}

func Close(ctx context.Context) error {
	return Default().Close(ctx)
}

func New(writer io.Writer, closeFn func() error) *Logger {
	l := &Logger{
		events:  make(chan Event, 256),
		done:    make(chan struct{}),
		writer:  writer,
		closeFn: closeFn,
	}
	go l.run()
	return l
}

func (l *Logger) Log(event Event) {
	if l == nil {
		return
	}

	if event.Timestamp.IsZero() {
		event.Timestamp = time.Now().UTC()
	}

	select {
	case l.events <- event:
	default:
		log.Printf("[AUDIT-DROP] action=%s outcome=%s", event.ActionType, event.Outcome)
	}
}

func (l *Logger) Close(ctx context.Context) error {
	if l == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}

	l.closeOnce.Do(func() {
		close(l.events)
	})

	select {
	case <-l.done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (l *Logger) run() {
	defer close(l.done)
	defer func() {
		if l.closeFn != nil {
			if err := l.closeFn(); err != nil {
				log.Printf("[AUDIT] close failed: %v", err)
			}
		}
	}()

	for event := range l.events {
		if event.Outcome == "error" {
			errStr := ""
			if event.Fields != nil {
				if val, ok := event.Fields["error"]; ok {
					errStr = fmt.Sprintf(" - error: %v", val)
				} else if val, ok := event.Fields["reason"]; ok {
					errStr = fmt.Sprintf(" - reason: %v", val)
				}
			}
			log.Printf("[AUDIT-ERROR] Guild: %s - Action: %s%s", event.GuildID, event.ActionType, errStr)
		}

		body, err := json.Marshal(eventMap(event))
		if err != nil {
			log.Printf("[AUDIT] marshal failed: %v", err)
			continue
		}
		if _, err := l.writer.Write(append(body, '\n')); err != nil {
			log.Printf("[AUDIT] write failed: %v", err)
		}
	}
}

func eventMap(event Event) map[string]any {
	fields := map[string]any{
		"timestamp":   event.Timestamp.UTC().Format(time.RFC3339Nano),
		"guild_id":    event.GuildID,
		"channel_id":  event.ChannelID,
		"actor_id":    event.ActorID,
		"action_type": event.ActionType,
		"outcome":     event.Outcome,
	}

	for key, value := range event.Fields {
		if value == nil {
			continue
		}
		if _, reserved := reservedFields[key]; reserved {
			continue
		}
		fields[key] = value
	}

	return fields
}

func newDefaultLogger() *Logger {
	path := os.Getenv(envAuditLogPath)
	return newLoggerFromPath(path)
}

func newLoggerFromPath(path string) *Logger {
	if path == "" {
		return New(io.Discard, nil)
	}

	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		log.Printf("[AUDIT] failed to open AUDIT_LOG_PATH=%q, falling back to discard: %v", path, err)
		return New(io.Discard, nil)
	}

	return New(file, file.Close)
}
