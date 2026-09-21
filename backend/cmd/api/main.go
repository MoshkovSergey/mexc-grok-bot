// Package main is the entrypoint for the backend API and bot runner.
package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/MoshkovSergey/mexc-grok-bot/backend/internal/api"
	"github.com/MoshkovSergey/mexc-grok-bot/backend/internal/bot"
	"github.com/MoshkovSergey/mexc-grok-bot/backend/internal/config"
	"github.com/MoshkovSergey/mexc-grok-bot/backend/internal/db"
	"github.com/MoshkovSergey/mexc-grok-bot/backend/internal/mexc"
)

func main() {
	// Load configuration from environment variables.
	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("config load failed: %v", err)
	}

	// Root context allows graceful shutdown of background goroutines.
	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	// Connect to PostgreSQL.
	database, err := db.Open(rootCtx, cfg.DatabaseURL)
	if err != nil {
		log.Fatalf("database open failed: %v", err)
	}
	defer func() {
		if err := database.Close(); err != nil {
			log.Printf("database close error: %v", err)
		}
	}()

	// Apply embedded migration. For production, prefer versioned migrations.
	if err := db.Migrate(rootCtx, database); err != nil {
		log.Fatalf("database migration failed: %v", err)
	}

	// MEXC REST client. Public endpoints work without API keys.
	// Private endpoints require keys and are only used when live trading is enabled.
	client := mexc.NewClient(cfg.MexcBaseURL, cfg.MexcAPIKey, cfg.MexcAPISecret)

	// Bot engine. It persists state and risk events to PostgreSQL.
	b := bot.New(cfg, database, client)

	// Run bot in background. By default it only acts when started via API.
	go func() {
		if err := b.Run(rootCtx); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("bot run exited: %v", err)
		}
	}()

	// HTTP API for dashboard and control.
	router := api.NewRouter(b, cfg)
	srv := &http.Server{
		Addr:              ":" + cfg.Port,
		Handler:           router,
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		log.Printf("backend listening on :%s", cfg.Port)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("http server failed: %v", err)
		}
	}()

	// Wait for termination signal.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)
	<-stop

	log.Println("shutdown signal received")
	rootCancel()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Printf("http shutdown error: %v", err)
	}
}