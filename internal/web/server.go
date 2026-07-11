package web

import (
	"context"
	"embed"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

// StaticFS is seeded from main.go with the embedded static assets directory
var StaticFS embed.FS

// SecurityHeadersMiddleware adds mandatory security headers to all responses
func SecurityHeadersMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "strict-origin-when-cross-origin")

		// Content-Security-Policy (Allow local CSS, JS, self-hosted fonts, embeds, SVG, no CDNs)
		w.Header().Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; font-src 'self'; script-src 'self' 'unsafe-inline'; frame-ancestors 'none';")

		next.ServeHTTP(w, r)
	})
}

// StaticCacheMiddleware caches static assets aggressively
func StaticCacheMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		next.ServeHTTP(w, r)
	})
}

func NewRouter() http.Handler {
	r := chi.NewRouter()

	// Default Middlewares
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Compress(5))
	r.Use(middleware.Timeout(10 * time.Second))
	r.Use(SecurityHeadersMiddleware)

	// Sub-FS for static assets
	subFS, err := fs.Sub(StaticFS, "static")
	if err != nil {
		log.Fatalf("[WEB] Failed to read static FS: %v", err)
	}

	// Static assets handler with cache control
	fileServer := http.FileServer(http.FS(subFS))
	r.Route("/static", func(r chi.Router) {
		r.Use(StaticCacheMiddleware)
		r.Handle("/*", http.StripPrefix("/static", fileServer))
	})

	// Web Routes
	r.Get("/", HomeHandler)
	r.Get("/features", FeaturesHandler)
	r.Get("/how-it-works", HowItWorksHandler)
	r.Get("/commands", CommandsHandler)
	r.Get("/docs", DocsHandler)
	r.Get("/invite", InviteHandler)
	r.Get("/healthz", HealthzHandler)

	r.Route("/api", func(r chi.Router) {
		r.Use(APIAuthMiddleware)
		r.Get("/stats", StatsAPIHandler)
	})

	// 404 Handler
	r.NotFound(NotFoundHandler)

	return r
}

func StartServer(port string) {
	// Determine interface binding
	bind := os.Getenv("WEB_BIND")
	if bind == "" {
		bind = "127.0.0.1"
	}
	addr := bind + ":" + port

	srv := &http.Server{
		Addr:         addr,
		Handler:      NewRouter(),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		log.Printf("[WEB] Starting web server on %s", addr)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("[WEB] listen: %s\n", err)
		}
	}()

	// Graceful Shutdown
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit
	log.Println("[WEB] Shutting down web server...")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("[WEB] Server Shutdown Forced: %v", err)
	}

	log.Println("[WEB] Web server exited cleanly")
}

// APIAuthMiddleware protects /api/* endpoints using a token from WEB_API_TOKEN env var
func APIAuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token := os.Getenv("WEB_API_TOKEN")
		if token == "" {
			log.Printf("[WEB-AUTH] WEB_API_TOKEN is not configured in env; denying access to %s", r.URL.Path)
			http.Error(w, "Unauthorized (API token not configured)", http.StatusUnauthorized)
			return
		}

		authHeader := r.Header.Get("Authorization")
		reqToken := ""
		if strings.HasPrefix(authHeader, "Bearer ") {
			reqToken = strings.TrimPrefix(authHeader, "Bearer ")
		} else {
			reqToken = r.Header.Get("X-API-Token")
			if reqToken == "" {
				reqToken = r.Header.Get("X-API-Key")
			}
		}

		if reqToken == "" || reqToken != token {
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		next.ServeHTTP(w, r)
	})
}
