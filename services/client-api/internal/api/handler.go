package api

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

type Config struct {
	PublicKey                                      *rsa.PublicKey
	Issuer, Audience, AggregatorURL, AggregatorKey string
	Timeout                                        time.Duration
	MaxInflight                                    int
	Now                                            func() time.Time
}
type Handler struct {
	cfg      Config
	client   *http.Client
	mux      *http.ServeMux
	inflight chan struct{}
}
type claims struct {
	Scope string `json:"scope"`
	jwt.RegisteredClaims
}
type ctxKey struct{}

var corrPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
var rawFields = map[string]bool{"source_ref_id": true, "latitude": true, "longitude": true, "attributes": true}

func NewHandler(cfg Config) *Handler {
	if cfg.Timeout <= 0 {
		cfg.Timeout = time.Second
	}
	if cfg.MaxInflight < 1 {
		cfg.MaxInflight = 128
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	// Reuse connections across concurrent reads; reserve one for readiness probes.
	transport.MaxIdleConnsPerHost = cfg.MaxInflight + 1
	transport.MaxIdleConns = cfg.MaxInflight + 1
	transport.MaxConnsPerHost = cfg.MaxInflight + 1
	client := &http.Client{Timeout: cfg.Timeout, Transport: transport, CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
		return errors.New("Aggregator redirects are not allowed")
	}}
	h := &Handler{cfg: cfg, client: client, mux: http.NewServeMux(), inflight: make(chan struct{}, cfg.MaxInflight)}
	h.mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) { writeJSON(w, 200, map[string]string{"status": "ok"}) })
	h.mux.HandleFunc("GET /ready", h.ready)
	h.mux.Handle("GET /v1/hazards", h.admit(http.HandlerFunc(h.list)))
	h.mux.Handle("GET /v1/hazards/{id}", h.admit(http.HandlerFunc(h.detail)))
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
	h.mux.ServeHTTP(recorder, r.WithContext(context.WithValue(r.Context(), ctxKey{}, corr)))
	status := recorder.status
	if status == 0 {
		status = http.StatusOK
	}
	slog.Info("request complete", "service", "client-api", "operation", r.Method+" "+r.URL.Path, "correlation_id", corr, "duration_ms", time.Since(started).Milliseconds(), "status", status)
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
			writeError(w, 429, "RATE_LIMITED", "Client API capacity is full", corrID(r))
		}
	})
}
func (h *Handler) ready(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	status := 0
	defer func() {
		slog.Info("Aggregator readiness complete", "service", "client-api", "operation", "aggregator_ready", "correlation_id", corrID(r), "duration_ms", time.Since(started).Milliseconds(), "status", status)
	}()
	req, err := http.NewRequestWithContext(r.Context(), http.MethodGet, strings.TrimRight(h.cfg.AggregatorURL, "/")+"/ready", nil)
	if err != nil {
		writeError(w, 503, "DEPENDENCY_UNAVAILABLE", "Aggregator is not ready", corrID(r))
		return
	}
	req.Header.Set("X-Correlation-ID", corrID(r))
	resp, err := h.client.Do(req)
	if err != nil {
		writeError(w, 503, "DEPENDENCY_UNAVAILABLE", "Aggregator is not ready", corrID(r))
		return
	}
	status = resp.StatusCode
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		writeError(w, 503, "DEPENDENCY_UNAVAILABLE", "Aggregator is not ready", corrID(r))
		return
	}
	writeJSON(w, 200, map[string]string{"status": "ready"})
}
func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if !validateList(w, r, scope, corrID(r)) {
		return
	}
	h.proxy(w, r, scope, "/internal/v1/hazards")
}
func (h *Handler) detail(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.authenticate(w, r)
	if !ok {
		return
	}
	if !validateDetail(w, r, scope, corrID(r)) {
		return
	}
	h.proxy(w, r, scope, "/internal/v1/hazards/"+url.PathEscape(r.PathValue("id")))
}
func (h *Handler) authenticate(w http.ResponseWriter, r *http.Request) (string, bool) {
	header := r.Header.Get("Authorization")
	parts := strings.Fields(header)
	if len(parts) != 2 || parts[0] != "Bearer" {
		writeError(w, 401, "UNAUTHORIZED", "Bearer token is required", corrID(r))
		return "", false
	}
	c := &claims{}
	token, err := jwt.ParseWithClaims(parts[1], c, func(token *jwt.Token) (any, error) {
		if token.Method.Alg() != jwt.SigningMethodRS256.Alg() {
			return nil, errors.New("unexpected signing method")
		}
		return h.cfg.PublicKey, nil
	}, jwt.WithValidMethods([]string{"RS256"}), jwt.WithIssuer(h.cfg.Issuer), jwt.WithAudience(h.cfg.Audience), jwt.WithExpirationRequired(), jwt.WithIssuedAt(), jwt.WithTimeFunc(h.cfg.Now))
	if err != nil || !token.Valid || c.Subject == "" || c.Scope == "" {
		writeError(w, 401, "UNAUTHORIZED", "Bearer token is invalid or expired", corrID(r))
		return "", false
	}
	if !validIdentityScope(c.Subject, c.Scope) {
		writeError(w, 401, "UNAUTHORIZED", "Bearer token is invalid or expired", corrID(r))
		return "", false
	}
	return c.Scope, true
}

func validIdentityScope(subject, scope string) bool {
	if subject == "media" {
		return scope == "hazards:summary"
	}
	return (subject == "field-team" || subject == "bnpb-internal") && scope == "hazards:summary hazards:raw"
}
func validateList(w http.ResponseWriter, r *http.Request, scope, corr string) bool {
	q := r.URL.Query()
	if scope == "hazards:summary" {
		if q.Get("view") == "full" || mentionsRaw(q.Get("fields")) {
			writeError(w, 403, "FORBIDDEN", "Raw hazard data is not permitted", corr)
			return false
		}
	}
	for name, vals := range q {
		if len(vals) != 1 || (name != "source" && name != "view" && name != "limit" && name != "offset") {
			writeError(w, 400, "INVALID_QUERY", "Query parameters are invalid", corr)
			return false
		}
	}
	if q.Get("source") != "" && q.Get("source") != "BMKG" && q.Get("source") != "PVMBG" {
		writeError(w, 400, "INVALID_QUERY", "Query parameters are invalid", corr)
		return false
	}
	if q.Get("view") != "" && q.Get("view") != "summary" && q.Get("view") != "full" {
		writeError(w, 400, "INVALID_QUERY", "Query parameters are invalid", corr)
		return false
	}
	if q.Get("view") == "full" && !hasRaw(scope) {
		writeError(w, 403, "FORBIDDEN", "Raw hazard data is not permitted", corr)
		return false
	}
	return true
}
func validateDetail(w http.ResponseWriter, r *http.Request, scope, corr string) bool {
	q := r.URL.Query()
	if scope == "hazards:summary" && (q.Get("view") == "full" || mentionsRaw(q.Get("fields"))) {
		writeError(w, 403, "FORBIDDEN", "Raw hazard data is not permitted", corr)
		return false
	}
	if len(q) > 1 || (len(q) == 1 && (len(q["view"]) != 1 || q.Get("view") == "")) {
		writeError(w, 400, "INVALID_QUERY", "Query parameters are invalid", corr)
		return false
	}
	if q.Get("view") != "" && q.Get("view") != "summary" && q.Get("view") != "full" {
		writeError(w, 400, "INVALID_QUERY", "Query parameters are invalid", corr)
		return false
	}
	if q.Get("view") == "full" && !hasRaw(scope) {
		writeError(w, 403, "FORBIDDEN", "Raw hazard data is not permitted", corr)
		return false
	}
	return true
}
func mentionsRaw(value string) bool {
	for _, f := range strings.Split(value, ",") {
		if rawFields[strings.TrimSpace(f)] {
			return true
		}
	}
	return false
}
func hasRaw(scope string) bool { return strings.Contains(" "+scope+" ", " hazards:raw ") }
func (h *Handler) proxy(w http.ResponseWriter, r *http.Request, scope, path string) {
	started := time.Now()
	upstreamStatus := 0
	defer func() {
		slog.Info("Aggregator read complete", "service", "client-api", "operation", "aggregator_read", "correlation_id", corrID(r), "duration_ms", time.Since(started).Milliseconds(), "status", upstreamStatus)
	}()
	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.Timeout)
	defer cancel()
	target := strings.TrimRight(h.cfg.AggregatorURL, "/") + path
	if r.URL.RawQuery != "" {
		target += "?" + r.URL.RawQuery
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		writeError(w, 503, "DEPENDENCY_UNAVAILABLE", "Aggregator is unavailable", corrID(r))
		return
	}
	req.Header.Set("X-Internal-Key", h.cfg.AggregatorKey)
	req.Header.Set("X-Correlation-ID", corrID(r))
	resp, err := h.client.Do(req)
	if err != nil {
		reason := "UPSTREAM_REQUEST_FAILED"
		if errors.Is(err, context.DeadlineExceeded) {
			reason = "UPSTREAM_TIMEOUT"
		} else if errors.Is(err, syscall.EADDRNOTAVAIL) {
			reason = "LOCAL_PORTS_EXHAUSTED"
		}
		slog.Warn("Aggregator read failed", "correlation_id", corrID(r), "reason", reason)
		writeError(w, 503, "DEPENDENCY_UNAVAILABLE", "Aggregator is unavailable", corrID(r))
		return
	}
	upstreamStatus = resp.StatusCode
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1024*1024+1))
	if err != nil || len(body) > 1024*1024 {
		writeError(w, 503, "DEPENDENCY_UNAVAILABLE", "Aggregator response is unavailable", corrID(r))
		return
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
			if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
				w.Header().Set("Retry-After", retryAfter)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(body)
		return
	}
	if scope == "hazards:summary" {
		body, err = summaryOnly(body)
		if err != nil {
			writeError(w, 502, "UPSTREAM_INVALID", "Aggregator response is invalid", corrID(r))
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(body)
}

type summary struct {
	HazardID   string `json:"hazard_id"`
	Source     string `json:"source"`
	HazardType string `json:"hazard_type"`
	Severity   string `json:"severity"`
	AreaName   string `json:"area_name"`
	OccurredAt string `json:"occurred_at"`
	IngestedAt string `json:"ingested_at"`
}

func summaryOnly(body []byte) ([]byte, error) {
	var env struct {
		Data json.RawMessage `json:"data"`
		Meta json.RawMessage `json:"meta"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, err
	}
	var many []summary
	if err := json.Unmarshal(env.Data, &many); err == nil {
		return json.Marshal(map[string]any{"data": many, "meta": json.RawMessage(env.Meta)})
	}
	var one summary
	if err := json.Unmarshal(env.Data, &one); err != nil {
		return nil, err
	}
	return json.Marshal(map[string]any{"data": one, "meta": json.RawMessage(env.Meta)})
}
func corrID(r *http.Request) string { v, _ := r.Context().Value(ctxKey{}).(string); return v }
func correlation() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return time.Now().UTC().Format("20060102150405")
	}
	return hex.EncodeToString(b)
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, code, msg, corr string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": msg}, "correlation_id": corr})
}
func ParsePublicKey(data []byte) (*rsa.PublicKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("JWT public key is not PEM")
	}
	key, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, err
	}
	rsaKey, ok := key.(*rsa.PublicKey)
	if !ok || rsaKey.N.BitLen() < 2048 {
		return nil, errors.New("RSA public key must be at least 2048 bits")
	}
	return rsaKey, nil
}
