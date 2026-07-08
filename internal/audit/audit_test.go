package audit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log"
	"strings"
	"testing"
	"time"
)

func TestLoggerWritesJSONLines(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, nil)

	logger.Log(Event{
		Timestamp:  time.Date(2026, 7, 7, 0, 0, 0, 0, time.UTC),
		GuildID:    "guild-1",
		ChannelID:  "channel-1",
		ActorID:    "user-1",
		ActionType: "config_model_selected",
		Outcome:    "success",
		Fields: map[string]any{
			"provider": "OpenAI",
			"model":    "gpt-5.4-mini",
		},
	})

	if err := logger.Close(context.Background()); err != nil {
		t.Fatalf("close logger: %v", err)
	}

	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatal("expected audit output")
	}

	var payload map[string]any
	if err := json.Unmarshal([]byte(line), &payload); err != nil {
		t.Fatalf("unmarshal audit line: %v", err)
	}

	if got, want := payload["guild_id"], "guild-1"; got != want {
		t.Fatalf("expected guild_id %q, got %v", want, got)
	}
	if got, want := payload["action_type"], "config_model_selected"; got != want {
		t.Fatalf("expected action_type %q, got %v", want, got)
	}
	if got, want := payload["provider"], "OpenAI"; got != want {
		t.Fatalf("expected provider %q, got %v", want, got)
	}
}

func TestCloseReturnsContextErrorWhenWriterDoesNotDrain(t *testing.T) {
	blocker := make(chan struct{})
	logger := New(blockingWriter{blocker: blocker}, nil)
	logger.Log(Event{ActionType: "tool_call_proposed", Outcome: "success"})

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()

	err := logger.Close(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected context deadline exceeded, got %v", err)
	}

	close(blocker)
	if err := logger.Close(context.Background()); err != nil {
		t.Fatalf("expected logger to finish closing after unblock, got %v", err)
	}
}

func TestNewLoggerFromPathFallsBackToStderrWithExplicitMessage(t *testing.T) {
	var logBuf bytes.Buffer
	prevOutput := log.Writer()
	log.SetOutput(&logBuf)
	defer log.SetOutput(prevOutput)

	logger := newLoggerFromPath("/path/that/does/not/exist/audit.jsonl")
	if logger == nil {
		t.Fatal("expected fallback logger")
	}

	if err := logger.Close(context.Background()); err != nil {
		t.Fatalf("close fallback logger: %v", err)
	}

	got := logBuf.String()
	if !strings.Contains(got, "[AUDIT] failed to open AUDIT_LOG_PATH=") {
		t.Fatalf("expected explicit fallback log message, got %q", got)
	}
}

func TestSetDefaultForTestSwapsDefaultLogger(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, nil)
	restore := SetDefaultForTest(logger)
	defer restore()

	Log(Event{ActionType: "tool_call_proposed", Outcome: "success"})
	if err := logger.Close(context.Background()); err != nil {
		t.Fatalf("close swapped logger: %v", err)
	}

	if !strings.Contains(buf.String(), "tool_call_proposed") {
		t.Fatalf("expected swapped logger to receive event, got %q", buf.String())
	}
}

type blockingWriter struct {
	blocker chan struct{}
}

func (w blockingWriter) Write(p []byte) (int, error) {
	<-w.blocker
	return len(p), nil
}
