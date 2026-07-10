package main

import (
	"log"
	"os"

	"gobot/internal/database"
	webserver "gobot/internal/web"
	webassets "gobot/web"

	"github.com/joho/godotenv"
)

func main() {
	// Load environment variables (from root .env if it exists)
	_ = godotenv.Load()

	// Initialize database read-only access if configured
	if os.Getenv("DB_DSN") != "" {
		log.Println("[WEB] Initializing read-only database connection...")
		if err := database.Init(); err != nil {
			log.Printf("[WEB-WARN] Database failed to initialize: %v. Live stats will be disabled.", err)
		} else {
			defer func() {
				if database.Default != nil {
					_ = database.Default.Close()
				}
			}()
		}
	} else {
		log.Println("[WEB] DB_DSN environment variable is empty. Live stats will be disabled.")
	}

	// Seed filesystems from the root embedded asset container
	webserver.StaticFS = webassets.StaticFS
	webserver.TemplatesFS = webassets.TemplatesFS

	// Port configuration
	port := os.Getenv("WEB_PORT")
	if port == "" {
		port = "8081"
	}

	// Start server
	webserver.StartServer(port)
}
