package audit

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/ryonlunar/tugas-besar-AAT/pkg/telemetry"
	"log/slog"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
)

// NewHandler separates the assistant's audit credential from the internal API
// credential. Raw inspection/replay are owner APIs, never audit-token privileges.
func NewHandler(repository Repository, auditToken, internalKey string) http.Handler {
	mux := http.NewServeMux()
	inflight := make(chan struct{}, 8)
	correlationPattern := regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	write := func(w http.ResponseWriter, status int, data any) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_ = json.NewEncoder(w).Encode(data)
	}
	failure := func(w http.ResponseWriter, status int, code, message string) {
		write(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
	}
	wrap := func(ops bool, fn func(http.ResponseWriter, *http.Request)) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			provided, expected := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), auditToken
			if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
				provided = ""
			}
			if ops {
				provided = r.Header.Get("X-Internal-Key")
				expected = internalKey
			}
			if expected == "" || subtle.ConstantTimeCompare([]byte(provided), []byte(expected)) != 1 {
				failure(w, 401, "unauthorized", "token tidak valid")
				return
			}
			select {
			case inflight <- struct{}{}:
				defer func() { <-inflight }()
			default:
				w.Header().Set("Retry-After", "1")
				failure(w, 429, "rate_limited", "kapasitas audit penuh")
				return
			}
			fn(w, r)
		}
	}
	respond := func(w http.ResponseWriter, data any, err error) {
		if err != nil {
			if errors.Is(err, ErrQuery) {
				failure(w, 400, "invalid_query", "parameter atau cursor tidak valid")
			} else {
				failure(w, 503, "unavailable", "penyimpanan belum tersedia")
			}
			return
		}
		write(w, 200, data)
	}
	query := func(r *http.Request, allowed string) (Query, error) {
		q := Query{Source: contracts.Source(r.URL.Query().Get("source")), Cursor: r.URL.Query().Get("cursor"), Limit: 100}
		for key, values := range r.URL.Query() {
			if len(values) != 1 || !strings.Contains(","+allowed+",", ","+key+",") {
				return q, ErrQuery
			}
		}
		if q.Source != "" && !q.Source.Valid() {
			return q, ErrQuery
		}
		if value := r.URL.Query().Get("limit"); value != "" {
			n, err := strconv.Atoi(value)
			if err != nil || n < 1 || n > 1000 {
				return q, ErrQuery
			}
			q.Limit = n
		}
		if value := r.URL.Query().Get("since"); value != "" {
			at, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				return q, ErrQuery
			}
			q.Since = at.UTC()
		}
		if len(q.Cursor) > 4096 {
			return q, ErrQuery
		}
		return q, nil
	}
	mux.HandleFunc("GET /internal/audit/events", wrap(false, func(w http.ResponseWriter, r *http.Request) {
		q, err := query(r, "source,since,limit,cursor")
		if err != nil {
			respond(w, nil, err)
			return
		}
		data, err := repository.AuditEvents(r.Context(), q)
		respond(w, data, err)
	}))
	mux.HandleFunc("GET /internal/audit/checksum", wrap(false, func(w http.ResponseWriter, r *http.Request) {
		q, err := query(r, "source")
		if err != nil {
			respond(w, nil, err)
			return
		}
		data, err := repository.AuditChecksum(r.Context(), q.Source)
		respond(w, data, err)
	}))
	mux.HandleFunc("GET /internal/audit/location", wrap(false, func(w http.ResponseWriter, r *http.Request) {
		values := r.URL.Query()
		id := values.Get("hazard_id")
		if len(values) != 1 || len(values["hazard_id"]) != 1 || len(id) != 36 {
			respond(w, nil, ErrQuery)
			return
		}
		data, found, err := repository.AuditLocation(r.Context(), id)
		if err == nil && !found {
			failure(w, 404, "not_found", "HazardEvent tidak ditemukan")
			return
		}
		respond(w, data, err)
	}))
	unprocessed := func(w http.ResponseWriter, r *http.Request) {
		q, err := query(r, "source,limit,cursor")
		if err != nil {
			respond(w, nil, err)
			return
		}
		data, err := repository.AuditUnprocessed(r.Context(), q)
		respond(w, data, err)
	}
	mux.HandleFunc("GET /internal/audit/unprocessed", wrap(false, unprocessed))
	mux.HandleFunc("GET /internal/v1/failed-messages", wrap(true, unprocessed))
	mux.HandleFunc("GET /internal/v1/failed-messages/{message_id}", wrap(true, func(w http.ResponseWriter, r *http.Request) {
		data, found, err := repository.InspectFailed(r.Context(), r.PathValue("message_id"))
		if err == nil && !found {
			failure(w, 404, "not_found", "pesan tidak ditemukan")
			return
		}
		respond(w, data, err)
	}))
	mux.HandleFunc("POST /internal/v1/failed-messages/{message_id}/replay", wrap(true, func(w http.ResponseWriter, r *http.Request) {
		if len(r.URL.Query()) != 0 {
			respond(w, nil, ErrQuery)
			return
		}
		data, found, err := repository.ReplayFailed(r.Context(), r.PathValue("message_id"))
		if err == nil && !found {
			failure(w, 404, "not_found", "pesan tidak ditemukan")
			return
		}
		if err != nil {
			respond(w, nil, err)
			return
		}
		status := 202
		if data.Status == "already_processed" {
			status = 200
		}
		write(w, status, data)
	}))
	// Go's default 404/405 responses are plain text. Keep the audit error contract
	// for unknown paths and unsupported methods as well.
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		correlation := r.Header.Get("X-Correlation-ID")
		if !correlationPattern.MatchString(correlation) {
			var id [16]byte
			_, _ = rand.Read(id[:])
			correlation = hex.EncodeToString(id[:])
		}
		w.Header().Set("X-Correlation-ID", correlation)
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		r = r.WithContext(ctx)
		_, pattern := mux.Handler(r)
		if pattern == "" {
			failure(w, 404, "not_found", "endpoint tidak ditemukan")
			return
		}
		defer func() {
			slog.InfoContext(r.Context(), "audit request complete", "service", "aggregator", "operation", r.Pattern, "correlation_id", correlation, "trace_id", telemetry.TraceID(r.Context()), "duration_ms", time.Since(started).Milliseconds())
		}()
		mux.ServeHTTP(w, r)
	})
}
