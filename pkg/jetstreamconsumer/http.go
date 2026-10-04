package jetstreamconsumer

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var correlationPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

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
	logger := runner.Logger
	if logger == nil {
		logger = slog.Default()
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
		if !correlationPattern.MatchString(id) {
			id = newCorrelationID()
		}
		w.Header().Set("X-Correlation-ID", id)
		started := time.Now()
		response := &statusWriter{ResponseWriter: w}
		mux.ServeHTTP(response, r)
		logger.Info("request complete", "operation", r.Method+" "+r.URL.Path, "correlation_id", id, "duration_ms", time.Since(started).Milliseconds(), "status", response.status)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}

func (w *statusWriter) Write(body []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(body)
}

func newCorrelationID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(value[:])
}

func respond(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(data)
}
