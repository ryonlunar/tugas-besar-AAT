package auth

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type Handler struct {
	service  *Service
	maxBody  int64
	inflight chan struct{}
	mux      *http.ServeMux
}
type contextKey struct{}

var corrPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func NewHandler(service *Service, maxBody int64, maxInflight int) *Handler {
	if maxBody < 1 {
		maxBody = 8192
	}
	if maxInflight < 1 {
		maxInflight = 16
	}
	h := &Handler{service: service, maxBody: maxBody, inflight: make(chan struct{}, maxInflight), mux: http.NewServeMux()}
	h.mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	h.mux.HandleFunc("GET /ready", h.ready)
	h.mux.Handle("POST /v1/auth/login", h.admit(http.HandlerFunc(h.login)))
	h.mux.Handle("POST /v1/auth/refresh", h.admit(http.HandlerFunc(h.refresh)))
	return h
}
func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	corr := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
	if !corrPattern.MatchString(corr) {
		corr = correlation()
	}
	w.Header().Set("X-Correlation-ID", corr)
	started := time.Now()
	recorder := &responseRecorder{ResponseWriter: w}
	h.mux.ServeHTTP(recorder, r.WithContext(context.WithValue(r.Context(), contextKey{}, corr)))
	status := recorder.status
	if status == 0 {
		status = http.StatusOK
	}
	slog.Info("request complete", "service", "auth", "operation", r.Method+" "+r.URL.Path, "correlation_id", corr, "duration_ms", time.Since(started).Milliseconds(), "status", status)
}

type responseRecorder struct {
	http.ResponseWriter
	status int
}

func (r *responseRecorder) WriteHeader(status int) {
	r.status = status
	r.ResponseWriter.WriteHeader(status)
}
func (h *Handler) admit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case h.inflight <- struct{}{}:
			defer func() { <-h.inflight }()
			next.ServeHTTP(w, r)
		default:
			w.Header().Set("Retry-After", "1")
			writeError(w, 429, "RATE_LIMITED", "Authentication capacity is full", corrID(r))
		}
	})
}
func (h *Handler) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 200*time.Millisecond)
	defer cancel()
	if h.service == nil || h.service.db.PingContext(ctx) != nil {
		writeError(w, 503, "DEPENDENCY_UNAVAILABLE", "Authentication state is not ready", corrID(r))
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ready"})
}
func (h *Handler) login(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if !decodeBody(w, r, h.maxBody, &body, corrID(r)) {
		return
	}
	result, err := h.service.Login(r.Context(), body.ClientID, body.ClientSecret)
	if errors.Is(err, ErrInvalidRefresh) {
		writeError(w, 401, "UNAUTHORIZED", "Client authentication failed", corrID(r))
		return
	}
	if err != nil {
		writeError(w, 500, "INTERNAL_ERROR", "Authentication service failed", corrID(r))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}
func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) {
	var body struct {
		RefreshToken string `json:"refresh_token"`
	}
	if !decodeBody(w, r, h.maxBody, &body, corrID(r)) {
		return
	}
	result, err := h.service.Refresh(r.Context(), body.RefreshToken)
	if errors.Is(err, ErrInvalidRefresh) {
		writeError(w, 401, "UNAUTHORIZED", "Refresh token is invalid", corrID(r))
		return
	}
	if err != nil {
		writeError(w, 500, "INTERNAL_ERROR", "Authentication service failed", corrID(r))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, 200, result)
}
func decodeBody(w http.ResponseWriter, r *http.Request, max int64, target any, corr string) bool {
	r.Body = http.MaxBytesReader(w, r.Body, max)
	defer r.Body.Close()
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if dec.Decode(target) != nil || dec.Decode(&struct{}{}) != io.EOF {
		writeError(w, 400, "INVALID_BODY", "Request body is invalid", corr)
		return false
	}
	return true
}
func corrID(r *http.Request) string {
	value, _ := r.Context().Value(contextKey{}).(string)
	return value
}
func correlation() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return time.Now().UTC().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(b)
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, code, message, corr string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}, "correlation_id": corr})
}
