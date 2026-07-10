package web

import (
	"encoding/json"
	"net/http"
	"os"
)

type APIResponse struct {
	Status string `json:"status"`
}

type StatsResponse struct {
	Servers int `json:"servers"`
}

// HomeHandler serves the short landing entrance page
func HomeHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		NotFoundHandler(w, r)
		return
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := RenderTemplate(w, "home.html", nil); err != nil {
		InternalError(w)
	}
}

// FeaturesHandler serves the detailed capabilities page
func FeaturesHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := RenderTemplate(w, "features.html", nil); err != nil {
		InternalError(w)
	}
}

// HowItWorksHandler serves the architecture flow steps page
func HowItWorksHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := RenderTemplate(w, "how-it-works.html", nil); err != nil {
		InternalError(w)
	}
}

// CommandsHandler serves the slash commands reference list
func CommandsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := RenderTemplate(w, "commands.html", nil); err != nil {
		InternalError(w)
	}
}

// DocsHandler serves the server admin documentation guide
func DocsHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := RenderTemplate(w, "docs.html", nil); err != nil {
		InternalError(w)
	}
}

// InviteHandler redirects to the Discord Bot OAuth URL
func InviteHandler(w http.ResponseWriter, r *http.Request) {
	inviteURL := os.Getenv("WEB_DISCORD_INVITE_URL")
	if inviteURL == "" {
		http.Error(w, "Invite URL not configured", http.StatusNotFound)
		return
	}
	http.Redirect(w, r, inviteURL, http.StatusFound)
}

// HealthzHandler is used for Docker container healthchecks
func HealthzHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(APIResponse{Status: "ok"})
}

// StatsAPIHandler returns server stats in JSON format
func StatsAPIHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	servers := GetLiveStats()
	if servers == 0 {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}

	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(StatsResponse{Servers: servers})
}

// NotFoundHandler serves the custom 404 page
func NotFoundHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusNotFound)

	data := map[string]any{
		"Path": r.URL.Path,
	}

	if err := RenderTemplate(w, "404.html", data); err != nil {
		InternalError(w)
	}
}
