package fieldreport

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
	"github.com/ryonlunar/tugas-besar-AAT/pkg/telemetry"
)

var (
	correlationPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
	clientPattern      = regexp.MustCompile(`^[a-z][a-z0-9-]{0,63}$`)
	reportIDPattern    = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)
)

// NewHandler serves the internal intake used by Client API. It has its own
// admission slots, so a burst of hazard reads cannot block report acceptance.
func NewHandler(repository Repository, internalKey string, maxInflight int) http.Handler {
	if maxInflight < 1 {
		maxInflight = 64
	}
	inflight := make(chan struct{}, maxInflight)
	mux := http.NewServeMux()
	guard := func(next http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if internalKey == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("X-Internal-Key")), []byte(internalKey)) != 1 {
				writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "Internal credential is invalid")
				return
			}
			if !clientPattern.MatchString(r.Header.Get("X-Client-ID")) {
				writeError(w, r, http.StatusBadRequest, "INVALID_CLIENT", "X-Client-ID is required")
				return
			}
			select {
			case inflight <- struct{}{}:
				defer func() { <-inflight }()
			default:
				w.Header().Set("Retry-After", "1")
				writeError(w, r, http.StatusTooManyRequests, "RATE_LIMITED", "Field report intake capacity is full")
				return
			}
			next(w, r)
		}
	}
	mux.HandleFunc("POST /internal/v1/field-reports", guard(func(w http.ResponseWriter, r *http.Request) {
		key := r.Header.Get("Idempotency-Key")
		if !contracts.IdempotencyKeyPattern.MatchString(key) {
			writeError(w, r, http.StatusBadRequest, "INVALID_IDEMPOTENCY_KEY", "Idempotency-Key is required (1-128 of A-Z a-z 0-9 . _ : -)")
			return
		}
		body, err := io.ReadAll(io.LimitReader(r.Body, contracts.MaxFieldReportBytes+1))
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "Request body could not be read")
			return
		}
		if len(body) > contracts.MaxFieldReportBytes {
			writeError(w, r, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "Field report body is too large")
			return
		}
		if _, err := contracts.ValidateFieldReport(body); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_FIELD_REPORT", "Body does not satisfy field-report.schema.json")
			return
		}
		started := time.Now()
		receipt, err := repository.AcceptFieldReport(r.Context(), Submission{
			ClientID: r.Header.Get("X-Client-ID"), IdempotencyKey: key, CorrelationID: correlation(r), Payload: body,
		})
		switch {
		case errors.Is(err, ErrConflict):
			writeError(w, r, http.StatusConflict, "IDEMPOTENCY_CONFLICT", "Idempotency-Key was already used with a different body")
			return
		case err != nil:
			slog.WarnContext(r.Context(), "field report not accepted", "service", "aggregator", "operation", "field_report_accept", "correlation_id", correlation(r), "reason", "STORAGE_UNAVAILABLE", "duration_ms", time.Since(started).Milliseconds())
			w.Header().Set("Retry-After", "1")
			writeError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "Field report was not stored; retry with the same Idempotency-Key")
			return
		}
		if receipt.Replayed {
			w.Header().Set("Idempotent-Replayed", "true")
		}
		slog.InfoContext(r.Context(), "field report accepted", "service", "aggregator", "operation", "field_report_accept", "correlation_id", correlation(r), "trace_id", telemetry.TraceID(r.Context()), "report_id", receipt.ReportID, "replayed", receipt.Replayed, "duration_ms", time.Since(started).Milliseconds())
		writeJSON(w, http.StatusAccepted, receipt)
	}))
	mux.HandleFunc("GET /internal/v1/field-reports/{report_id}", guard(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("report_id")
		if !reportIDPattern.MatchString(id) || len(r.URL.Query()) != 0 {
			writeError(w, r, http.StatusBadRequest, "INVALID_QUERY", "Report ID is invalid")
			return
		}
		status, found, err := repository.FieldReport(r.Context(), r.Header.Get("X-Client-ID"), id)
		switch {
		case err != nil:
			w.Header().Set("Retry-After", "1")
			writeError(w, r, http.StatusServiceUnavailable, "STORAGE_UNAVAILABLE", "Report storage is not fully reachable")
		case !found:
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Field report was not found")
		default:
			writeJSON(w, http.StatusOK, status)
		}
	}))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		corr := r.Header.Get("X-Correlation-ID")
		if !correlationPattern.MatchString(corr) {
			var b [16]byte
			_, _ = rand.Read(b[:])
			corr = hex.EncodeToString(b[:])
		}
		w.Header().Set("X-Correlation-ID", corr)
		ctx, cancel := context.WithTimeout(r.Context(), 4*time.Second)
		defer cancel()
		ctx = context.WithValue(ctx, correlationKey{}, corr)
		r = r.WithContext(ctx)
		if _, pattern := mux.Handler(r); pattern == "" {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "Endpoint was not found")
			return
		}
		mux.ServeHTTP(w, r)
	})
}

type correlationKey struct{}

func correlation(r *http.Request) string {
	value, _ := r.Context().Value(correlationKey{}).(string)
	return value
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}, "correlation_id": correlation(r)})
}
