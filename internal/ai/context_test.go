package ai

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/bwmarrin/discordgo"
)

type mockRoundTripper func(req *http.Request) (*http.Response, error)

func (f mockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestFetchRecentChannelContext(t *testing.T) {
	// Create a mock session
	session, err := discordgo.New("Bot token")
	if err != nil {
		t.Fatalf("failed to create session: %v", err)
	}

	t.Run("successful formatting and chronological order", func(t *testing.T) {
		mockMessages := []*discordgo.Message{
			{
				ID:      "msg-2", // Newest message
				Content: "Hello world 2",
				Author:  &discordgo.User{Username: "User2"},
			},
			{
				ID:      "msg-1", // Oldest message
				Content: "Hello world 1",
				Author:  &discordgo.User{Username: "User1"},
			},
		}

		session.Client = &http.Client{
			Transport: mockRoundTripper(func(req *http.Request) (*http.Response, error) {
				if !strings.Contains(req.URL.Path, "/channels/chan-1/messages") {
					return nil, errors.New("unexpected url path: " + req.URL.Path)
				}
				body, err := json.Marshal(mockMessages)
				if err != nil {
					return nil, err
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			}),
		}

		got, err := FetchRecentChannelContext(session, "chan-1", "", 2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		want := "User1: Hello world 1\nUser2: Hello world 2\n"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("excludes message ID", func(t *testing.T) {
		mockMessages := []*discordgo.Message{
			{
				ID:      "msg-3",
				Content: "Hello world 3",
				Author:  &discordgo.User{Username: "User3"},
			},
			{
				ID:      "msg-2",
				Content: "Hello world 2",
				Author:  &discordgo.User{Username: "User2"},
			},
			{
				ID:      "msg-1",
				Content: "Hello world 1",
				Author:  &discordgo.User{Username: "User1"},
			},
		}

		session.Client = &http.Client{
			Transport: mockRoundTripper(func(req *http.Request) (*http.Response, error) {
				body, _ := json.Marshal(mockMessages)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			}),
		}

		got, err := FetchRecentChannelContext(session, "chan-1", "msg-2", 3)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// msg-2 should be excluded from final string, and order should be chronological
		want := "User1: Hello world 1\nUser3: Hello world 3\n"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("truncates message content", func(t *testing.T) {
		longContent := strings.Repeat("A", 350)
		mockMessages := []*discordgo.Message{
			{
				ID:      "msg-1",
				Content: longContent,
				Author:  &discordgo.User{Username: "User1"},
			},
		}

		session.Client = &http.Client{
			Transport: mockRoundTripper(func(req *http.Request) (*http.Response, error) {
				body, _ := json.Marshal(mockMessages)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			}),
		}

		got, err := FetchRecentChannelContext(session, "chan-1", "", 1)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		want := "User1: " + strings.Repeat("A", 300) + "...\n"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("handles empty text with attachments or embeds", func(t *testing.T) {
		mockMessages := []*discordgo.Message{
			{
				ID:          "msg-embed",
				Content:     "",
				Author:      &discordgo.User{Username: "UserEmbed"},
				Embeds:      []*discordgo.MessageEmbed{{Title: "some embed"}},
				Attachments: nil,
			},
			{
				ID:          "msg-attach",
				Content:     "",
				Author:      &discordgo.User{Username: "UserAttach"},
				Embeds:      nil,
				Attachments: []*discordgo.MessageAttachment{{Filename: "file.png"}},
			},
			{
				ID:      "msg-empty",
				Content: "",
				Author:  &discordgo.User{Username: "UserEmpty"},
			},
		}

		session.Client = &http.Client{
			Transport: mockRoundTripper(func(req *http.Request) (*http.Response, error) {
				body, _ := json.Marshal(mockMessages)
				return &http.Response{
					StatusCode: http.StatusOK,
					Body:       io.NopCloser(bytes.NewReader(body)),
					Header:     make(http.Header),
				}, nil
			}),
		}

		got, err := FetchRecentChannelContext(session, "chan-1", "", 3)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}

		// msg-empty has no text/embeds/attachments, so it should be skipped.
		// msg-embed and msg-attach should be [adjunto sin texto].
		// Order reversed (chronological): attach first, then embed.
		want := "UserAttach: [adjunto sin texto]\nUserEmbed: [adjunto sin texto]\n"
		if got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	})

	t.Run("handles error from discord API", func(t *testing.T) {
		session.Client = &http.Client{
			Transport: mockRoundTripper(func(req *http.Request) (*http.Response, error) {
				return nil, errors.New("network failure")
			}),
		}

		_, err := FetchRecentChannelContext(session, "chan-1", "", 5)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}
