package jetstreamconsumer

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"time"
)

type Inspector interface {
	Ping(context.Context) error
	Inspect(context.Context) (map[string]any, error)
}

func Handler(runner *Runner, state Inspector, demoKey, inspectPath string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		respond(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /ready", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		if state.Ping(ctx) != nil || !runner.Ready() {
			respond(w, http.StatusServiceUnavailable, map[string]string{"status": "unavailable"})
			return
		}
		respond(w, http.StatusOK, map[string]string{"status": "ready"})
	})
	mux.HandleFunc("GET "+inspectPath, func(w http.ResponseWriter, r *http.Request) {
		if demoKey == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Demo-Key")), []byte(demoKey)) != 1 {
			respond(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), time.Second)
		defer cancel()
		data, err := state.Inspect(ctx)
		if err != nil {
			respond(w, http.StatusServiceUnavailable, map[string]string{"error": "storage_unavailable"})
			return
		}
		respond(w, http.StatusOK, data)
	})
	return mux
}

func respond(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
