package repository

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
)

// NewAdminHandler returns an http.Handler for backfill admin endpoints.
//
// Endpoints:
//
//	POST /admin/backfill/start   — start a run (409 if already running)
//	POST /admin/backfill/stop    — cancel the active run (no-op if idle)
//	GET  /admin/backfill/status  — return current Progress as JSON
//
// All requests require the X-Admin-Token header to match the ADMIN_TOKEN
// environment variable. If ADMIN_TOKEN is unset, auth is skipped (dev mode).
func NewAdminHandler(ctx context.Context, m *BackfillManager) http.Handler {
	token := os.Getenv("ADMIN_TOKEN")

	mux := http.NewServeMux()

	auth := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if token != "" && r.Header.Get("X-Admin-Token") != token {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			next(w, r)
		}
	}

	writeJSON := func(w http.ResponseWriter, code int, v any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		json.NewEncoder(w).Encode(v)
	}

	mux.HandleFunc("/admin/backfill/start", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if err := m.Start(ctx); err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "started"})
	}))

	mux.HandleFunc("/admin/backfill/stop", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		m.Stop()
		writeJSON(w, http.StatusOK, map[string]string{"status": "stopped"})
	}))

	mux.HandleFunc("/admin/backfill/status", auth(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		writeJSON(w, http.StatusOK, m.Status())
	}))

	return mux
}
