// Package api exposes HTTP endpoints for the dashboard and bot control.
package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/MoshkovSergey/mexc-grok-bot/backend/internal/bot"
	"github.com/MoshkovSergey/mexc-grok-bot/backend/internal/config"
)

// NewRouter creates the HTTP handler.
func NewRouter(b *bot.Bot, cfg *config.Config) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{
			"status":  "ok",
			"service": "mexc-grok-bot-backend",
		})
	})

	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, b.Status())
	})

	mux.HandleFunc("GET /api/dashboard", func(w http.ResponseWriter, r *http.Request) {
		d, err := b.Dashboard(r.Context())
		if err != nil {
			writeError(w, http.StatusInternalServerError, err)
			return
		}
		writeJSON(w, http.StatusOK, d)
	})

	mux.HandleFunc("POST /api/bot/start", func(w http.ResponseWriter, r *http.Request) {
		if err := b.Start(r.Context()); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "started"})
	})

	mux.HandleFunc("POST /api/bot/stop", func(w http.ResponseWriter, r *http.Request) {
		if err := b.Stop(r.Context()); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
	})

	return corsMiddleware(cfg, mux)
}

func corsMiddleware(_ *config.Config, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")

		// Allow local development origins. In production, put this behind a reverse proxy
		// and/or implement proper authentication/authorization.
		if origin != "" && (strings.HasPrefix(origin, "http://localhost:") || strings.HasPrefix(origin, "http://127.0.0.1:")) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{
		"error": err.Error(),
	})
}