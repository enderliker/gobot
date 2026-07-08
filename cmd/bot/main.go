package main

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"gobot/internal/bot"
)

func main() {
	_ = godotenv.Load()

	b, err := bot.New(os.Getenv("DISCORD_TOKEN"))
	if err != nil {
		log.Fatal(err)
	}

	if err := b.Start(); err != nil {
		_ = b.Close()
		log.Fatal(err)
	}

	stopCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	<-stopCtx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := b.Shutdown(shutdownCtx); err != nil {
		log.Printf("[SHUTDOWN] Completed with errors: %v", err)
	}
}
