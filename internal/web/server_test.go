package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	webassets "gobot/web"
)

func init() {
	TemplatesFS = webassets.TemplatesFS
	StaticFS = webassets.StaticFS
}

func TestHealthzHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()

	router := NewRouter()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	var res APIResponse
	if err := json.Unmarshal(w.Body.Bytes(), &res); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if res.Status != "ok" {
		t.Fatalf("expected status 'ok', got %q", res.Status)
	}
}

func TestInviteHandlerRedirects(t *testing.T) {
	t.Setenv("WEB_DISCORD_INVITE_URL", "https://discord.com/invite-test")

	req := httptest.NewRequest(http.MethodGet, "/invite", nil)
	w := httptest.NewRecorder()

	router := NewRouter()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusFound {
		t.Fatalf("expected 302 redirect, got %d", w.Code)
	}

	location := w.Header().Get("Location")
	if location != "https://discord.com/invite-test" {
		t.Fatalf("expected redirect to https://discord.com/invite-test, got %q", location)
	}
}

func TestStatsAPIDegradesTo503WithoutDB(t *testing.T) {
	t.Setenv("WEB_API_TOKEN", "test-token")
	// Database.Default is nil in test environment if not initialized
	req := httptest.NewRequest(http.MethodGet, "/api/stats", nil)
	req.Header.Set("Authorization", "Bearer test-token")
	w := httptest.NewRecorder()

	router := NewRouter()
	router.ServeHTTP(w, req)

	// Since database.Default is nil, it should degrade gracefully returning 503 Service Unavailable
	if w.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status 503, got %d", w.Code)
	}
}

func TestSecurityHeadersArePresent(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()

	router := NewRouter()
	router.ServeHTTP(w, req)

	if got := w.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("expected nosniff, got %q", got)
	}
	if got := w.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Fatalf("expected DENY, got %q", got)
	}
	if got := w.Header().Get("Referrer-Policy"); got != "strict-origin-when-cross-origin" {
		t.Fatalf("expected strict-origin-when-cross-origin, got %q", got)
	}
	if got := w.Header().Get("Content-Security-Policy"); got == "" {
		t.Fatal("expected Content-Security-Policy header to be set")
	}
}

func TestHomeHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	router := NewRouter()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d. Body: %s", w.Code, w.Body.String())
	}
}

func TestSubpagesHandlers(t *testing.T) {
	paths := []string{"/features", "/how-it-works", "/commands", "/docs"}
	router := NewRouter()

	for _, path := range paths {
		t.Run(path, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, path, nil)
			w := httptest.NewRecorder()
			router.ServeHTTP(w, req)

			if w.Code != http.StatusOK {
				t.Fatalf("expected status 200 for %s, got %d", path, w.Code)
			}
		})
	}
}

func TestStaticFilesHandler(t *testing.T) {
	req := httptest.NewRequest(http.MethodGet, "/static/css/tokens.css", nil)
	w := httptest.NewRecorder()

	router := NewRouter()
	router.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("expected status 200, got %d", w.Code)
	}

	if cacheControl := w.Header().Get("Cache-Control"); cacheControl == "" {
		t.Fatal("expected Cache-Control header to be set for static assets")
	}
}
