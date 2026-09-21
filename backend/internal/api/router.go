// Package api exposes HTTP endpoints for the dashboard, bot control and settings.
package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"github.com/MoshkovSergey/mexc-grok-bot/backend/internal/bot"
	"github.com/MoshkovSergey/mexc-grok-bot/backend/internal/config"
)

// NewRouter creates the HTTP handler.
func NewRouter(b *bot.Bot, cfg *config.Config) http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok", "service": "mexc-grok-bot-backend"})
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
	mux.HandleFunc("GET /api/settings", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, b.GetSettings())
	})
	mux.HandleFunc("PUT /api/settings", func(w http.ResponseWriter, r *http.Request) {
		var s bot.RuntimeSettings
		if err := json.NewDecoder(r.Body).Decode(&s); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		if err := b.UpdateSettings(r.Context(), s); err != nil {
			if errors.Is(err, bot.ErrLiveSymbolChangeWithPosition) || errors.Is(err, bot.ErrNoPriceForAutoClose) {
				writeError(w, http.StatusConflict, err)
				return
			}
			writeError(w, http.StatusBadRequest, err)
			return
		}
		writeJSON(w, http.StatusOK, b.GetSettings())
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
		if origin != "" && (strings.HasPrefix(origin, "http://localhost:") || strings.HasPrefix(origin, "http://127.0.0.1:")) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// writeJSON marshals to a buffer BEFORE writing headers, so a serialization error
// (e.g. a residual NaN/Inf outside the strategy package) cannot produce a truncated
// 200 body — the browser symptom we are fixing ("Unexpected end of JSON input").
// On error we log and return an explicit 500 with a JSON message instead.
func writeJSON(w http.ResponseWriter, status int, v any) {
	buf, err := json.Marshal(v)
	if err != nil {
		log.Printf("json encode failed: %v", err)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_ = json.NewEncoder(w).Encode(map[string]string{"error": "internal serialization failure"})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(buf)
}

func writeError(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}