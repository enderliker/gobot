package handler

import (
	"net/http"

	"gobot/internal/web"
	webassets "gobot/web"
)

var router http.Handler

func init() {
	// Seed the embedded templates and static assets into the web server package
	web.StaticFS = webassets.StaticFS
	web.TemplatesFS = webassets.TemplatesFS

	// Initialize the Chi router
	router = web.NewRouter()
}

// Handler is the Vercel Serverless Function entry point
func Handler(w http.ResponseWriter, r *http.Request) {
	router.ServeHTTP(w, r)
}
