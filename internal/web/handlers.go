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

// HomeHandler serves the landing page
func HomeHandler(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		NotFoundHandler(w, r)
		return
	}

	// Pass any page config needed (such as environment info)
	data := map[string]any{
		"RepoURL": os.Getenv("WEB_REPO_URL"),
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := RenderTemplate(w, "home.html", data); err != nil {
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

// DocsHandler redirects to the repository README/Documentation
func DocsHandler(w http.ResponseWriter, r *http.Request) {
	repoURL := os.Getenv("WEB_REPO_URL")
	if repoURL == "" {
		repoURL = "https://github.com/enderliker/gobot"
	}
	http.Redirect(w, r, repoURL, http.StatusFound)
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
		// If DB is offline/fails and stats returns 0, degrade gracefully
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
