package bot

import (
	"net/http"
	"testing"
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
