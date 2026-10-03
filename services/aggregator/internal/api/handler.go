package api

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/mapper"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/store"
)

type Repository interface {
	Ping(context.Context) error
	List(context.Context, string, int, int) ([]mapper.HazardEvent, bool, error)
	Get(context.Context, string) (mapper.HazardEvent, bool, error)
	SourceStates(context.Context, []mapper.Source, time.Time) (map[mapper.Source]store.SourceState, error)
}

type Handler struct {
	repository  Repository
	internalKey string
	now         func() time.Time
	mux         *http.ServeMux
	inflight    chan struct{}
}

type requestContextKey struct{}

var correlationPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func NewHandler(repository Repository, internalKey string, now func() time.Time) *Handler {
	return NewHandlerWithLimit(repository, internalKey, now, 128)
}

func NewHandlerWithLimit(repository Repository, internalKey string, now func() time.Time, maxInflight int) *Handler {
	if now == nil {
		now = time.Now
	}
	if maxInflight < 1 {
		maxInflight = 128
	}
	h := &Handler{repository: repository, internalKey: internalKey, now: now, mux: http.NewServeMux(), inflight: make(chan struct{}, maxInflight)}
	h.mux.HandleFunc("GET /health", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	h.mux.HandleFunc("GET /ready", h.ready)
	h.mux.Handle("GET /internal/v1/hazards", h.admit(http.HandlerFunc(h.list)))
	h.mux.Handle("GET /internal/v1/hazards/{id}", h.admit(http.HandlerFunc(h.detail)))
	return h
}

func (h *Handler) admit(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case h.inflight <- struct{}{}:
			defer func() { <-h.inflight }()
			next.ServeHTTP(w, r)
		default:
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "Aggregator request capacity is full", correlationID(r))
		}
	})
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	correlationID := strings.TrimSpace(r.Header.Get("X-Correlation-ID"))
	if !correlationPattern.MatchString(correlationID) {
		correlationID = makeCorrelationID()
	}
	w.Header().Set("X-Correlation-ID", correlationID)
	ctx := context.WithValue(r.Context(), requestContextKey{}, correlationID)
	started := time.Now()
	response := &statusWriter{ResponseWriter: w}
	h.mux.ServeHTTP(response, r.WithContext(ctx))
	slog.Info("request complete", "service", "aggregator", "operation", r.Method+" "+r.URL.Path, "correlation_id", correlationID, "duration_ms", time.Since(started).Milliseconds(), "status", response.status)
}

func (h *Handler) ready(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 200*time.Millisecond)
	defer cancel()
	if h.repository == nil || h.repository.Ping(ctx) != nil {
		writeError(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Aggregator storage is not ready", correlationID(r))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(w, r) {
		return
	}
	query := r.URL.Query()
	for name, values := range query {
		if name != "source" && name != "view" && name != "limit" && name != "offset" || len(values) != 1 {
			writeError(w, http.StatusBadRequest, "INVALID_QUERY", "Query parameters are invalid", correlationID(r))
			return
		}
	}
	source := query.Get("source")
	if source != "" && source != string(mapper.SourceBMKG) && source != string(mapper.SourcePVMBG) {
		writeError(w, http.StatusBadRequest, "INVALID_QUERY", "Query parameters are invalid", correlationID(r))
		return
	}
	view := query.Get("view")
	if view == "" {
		view = "summary"
	}
	if view != "summary" && view != "full" {
		writeError(w, http.StatusBadRequest, "INVALID_QUERY", "Query parameters are invalid", correlationID(r))
		return
	}
	limit, err := parseBoundedInt(query.Get("limit"), 50, 1, 100)
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_QUERY", "Query parameters are invalid", correlationID(r))
		return
	}
	offset, err := parseBoundedInt(query.Get("offset"), 0, 0, int(^uint(0)>>1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_QUERY", "Query parameters are invalid", correlationID(r))
		return
	}
	items, hasMore, err := h.repository.List(r.Context(), source, limit, offset)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Aggregator storage is unavailable", correlationID(r))
		return
	}
	sources := selectedSources(source)
	states, err := h.repository.SourceStates(r.Context(), sources, h.now().UTC())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Aggregator storage is unavailable", correlationID(r))
		return
	}
	if allUnavailable(sources, states) {
		writeErrorWithMeta(w, http.StatusServiceUnavailable, "SOURCE_UNAVAILABLE", "Requested source data is not available yet", correlationID(r), map[string]any{"sources": statesJSON(states)})
		return
	}
	data := make([]any, 0, len(items))
	for _, item := range items {
		if view == "summary" {
			data = append(data, summarize(item))
		} else {
			data = append(data, item)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"data": data,
		"meta": map[string]any{"correlation_id": correlationID(r), "limit": limit, "offset": offset, "has_more": hasMore, "sources": statesJSON(states)},
	})
}

func (h *Handler) detail(w http.ResponseWriter, r *http.Request) {
	if !h.authorized(w, r) {
		return
	}
	query := r.URL.Query()
	if len(query) > 1 || (len(query) == 1 && (len(query["view"]) != 1)) {
		writeError(w, http.StatusBadRequest, "INVALID_QUERY", "Query parameters are invalid", correlationID(r))
		return
	}
	view := query.Get("view")
	if view == "" {
		view = "summary"
	}
	if view != "summary" && view != "full" {
		writeError(w, http.StatusBadRequest, "INVALID_QUERY", "Query parameters are invalid", correlationID(r))
		return
	}
	item, found, err := h.repository.Get(r.Context(), r.PathValue("id"))
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Aggregator storage is unavailable", correlationID(r))
		return
	}
	if !found {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "Hazard was not found", correlationID(r))
		return
	}
	sources := []mapper.Source{item.Source}
	states, err := h.repository.SourceStates(r.Context(), sources, h.now().UTC())
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Aggregator storage is unavailable", correlationID(r))
		return
	}
	var data any = item
	if view == "summary" {
		data = summarize(item)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data, "meta": map[string]any{"correlation_id": correlationID(r), "sources": statesJSON(states)}})
}

func (h *Handler) authorized(w http.ResponseWriter, r *http.Request) bool {
	provided := r.Header.Get("X-Internal-Key")
	if h.internalKey == "" {
		writeError(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Aggregator internal authentication is not configured", correlationID(r))
		return false
	}
	if subtle.ConstantTimeCompare([]byte(provided), []byte(h.internalKey)) != 1 {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "Internal authentication is required", correlationID(r))
		return false
	}
	return true
}

type HazardSummary struct {
	HazardID   string            `json:"hazard_id"`
	Source     mapper.Source     `json:"source"`
	HazardType mapper.HazardType `json:"hazard_type"`
	Severity   mapper.Severity   `json:"severity"`
	AreaName   string            `json:"area_name"`
	OccurredAt string            `json:"occurred_at"`
	IngestedAt string            `json:"ingested_at"`
}

func summarize(event mapper.HazardEvent) HazardSummary {
	return HazardSummary{HazardID: event.HazardID, Source: event.Source, HazardType: event.HazardType, Severity: event.Severity, AreaName: event.AreaName, OccurredAt: event.OccurredAt, IngestedAt: event.IngestedAt}
}

func selectedSources(source string) []mapper.Source {
	if source == string(mapper.SourceBMKG) {
		return []mapper.Source{mapper.SourceBMKG}
	}
	if source == string(mapper.SourcePVMBG) {
		return []mapper.Source{mapper.SourcePVMBG}
	}
	return []mapper.Source{mapper.SourceBMKG, mapper.SourcePVMBG}
}
func allUnavailable(sources []mapper.Source, states map[mapper.Source]store.SourceState) bool {
	for _, source := range sources {
		if states[source].Status != "unavailable" {
			return false
		}
	}
	return len(sources) > 0
}
func statesJSON(states map[mapper.Source]store.SourceState) map[string]store.SourceState {
	result := make(map[string]store.SourceState, len(states))
	for source, state := range states {
		result[string(source)] = state
	}
	return result
}
func parseBoundedInt(value string, fallback, min, max int) (int, error) {
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < min || parsed > max {
		return 0, errors.New("integer out of range")
	}
	return parsed, nil
}
func correlationID(r *http.Request) string {
	if value, ok := r.Context().Value(requestContextKey{}).(string); ok {
		return value
	}
	return makeCorrelationID()
}
func makeCorrelationID() string {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:]
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, code, message, corr string) {
	writeErrorWithMeta(w, status, code, message, corr, nil)
}
func writeErrorWithMeta(w http.ResponseWriter, status int, code, message, corr string, meta map[string]any) {
	body := map[string]any{"error": map[string]string{"code": code, "message": message}, "correlation_id": corr}
	if meta != nil {
		body["meta"] = meta
	}
	writeJSON(w, status, body)
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
