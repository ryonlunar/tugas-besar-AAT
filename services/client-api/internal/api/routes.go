package api

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"syscall"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
)

// Server-side permissions. Auth assigns them per client; a client cannot ask
// for a scope at login, and Client API rejects a token whose scopes exceed its
// subject's allowance.
const (
	scopeSummary        = "hazards:summary"
	scopeRaw            = "hazards:raw"
	scopeReportsWrite   = "reports:write"
	scopeFailedMessages = "ops:failed-messages"
)

var allowedScopes = map[string]map[string]bool{
	"media":         {scopeSummary: true},
	"field-team":    {scopeSummary: true, scopeRaw: true, scopeReportsWrite: true},
	"bnpb-internal": {scopeSummary: true, scopeRaw: true, scopeFailedMessages: true},
}

var messageIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:|=-]{1,512}$`)
var reportIDPattern = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

type caller struct{ subject, scope string }
type callerKey struct{}

func callerFrom(r *http.Request) caller { c, _ := r.Context().Value(callerKey{}).(caller); return c }

func (c caller) has(scope string) bool { return strings.Contains(" "+c.scope+" ", " "+scope+" ") }

// priority marks Tim Lapangan and BNPB Internal traffic. Media is the only
// class that can be shed while reserved capacity remains.
func (c caller) priority() bool { return c.subject == "field-team" || c.subject == "bnpb-internal" }

func validIdentityScope(subject, scope string) bool {
	allowed, ok := allowedScopes[subject]
	if !ok {
		return false
	}
	seen := map[string]bool{}
	for _, s := range strings.Split(scope, " ") {
		if !allowed[s] || seen[s] {
			return false
		}
		seen[s] = true
	}
	if !seen[scopeSummary] {
		return false
	}
	// Full-read identities must keep raw access; only additions are optional.
	return subject == "media" || seen[scopeRaw]
}

// splitCapacity reserves a quarter of the in-flight slots (at least one) for
// priority callers. Media uses only the general pool, so a Media overload is
// rejected with 429 while Tim Lapangan requests still find a free slot.
func splitCapacity(total int) (general, reserved int) {
	if total < 2 {
		return 1, 1
	}
	reserved = total / 4
	if reserved < 1 {
		reserved = 1
	}
	return total - reserved, reserved
}

// authorized authenticates before admission so the caller's class decides
// which capacity pool it may use. Authorization is checked server-side here,
// independent of any client UI.
func (h *Handler) authorized(required string, next http.HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		who, ok := h.authenticate(w, r)
		if !ok {
			return
		}
		if !who.has(required) {
			writeError(w, http.StatusForbidden, "FORBIDDEN", "This client is not permitted to use this endpoint", corrID(r))
			return
		}
		r = r.WithContext(context.WithValue(r.Context(), callerKey{}, who))
		select {
		case h.inflight <- struct{}{}:
			defer func() { <-h.inflight }()
			next(w, r)
			return
		default:
		}
		if who.priority() {
			select {
			case h.reserved <- struct{}{}:
				defer func() { <-h.reserved }()
				next(w, r)
				return
			default:
			}
		}
		slog.Warn("request shed", "service", "client-api", "operation", r.Method+" "+r.URL.Path, "correlation_id", corrID(r), "client", who.subject, "reason", "CAPACITY_FULL")
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusTooManyRequests, "RATE_LIMITED", "Client API capacity is full", corrID(r))
	})
}

func (h *Handler) submitFieldReport(w http.ResponseWriter, r *http.Request) {
	key := r.Header.Get("Idempotency-Key")
	if !contracts.IdempotencyKeyPattern.MatchString(key) {
		writeError(w, http.StatusBadRequest, "INVALID_IDEMPOTENCY_KEY", "Idempotency-Key header is required (1-128 of A-Z a-z 0-9 . _ : -)", corrID(r))
		return
	}
	if len(r.URL.Query()) != 0 {
		writeError(w, http.StatusBadRequest, "INVALID_QUERY", "Query parameters are not accepted", corrID(r))
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, contracts.MaxFieldReportBytes+1))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "Request body could not be read", corrID(r))
		return
	}
	if len(body) > contracts.MaxFieldReportBytes {
		writeError(w, http.StatusRequestEntityTooLarge, "BODY_TOO_LARGE", "Field report body is too large", corrID(r))
		return
	}
	if _, err := contracts.ValidateFieldReport(body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_FIELD_REPORT", "Body does not satisfy the field report schema", corrID(r))
		return
	}
	h.forward(w, r, http.MethodPost, "/internal/v1/field-reports", "", body, map[string]string{"Idempotency-Key": key, "Content-Type": "application/json"}, nil, false)
}

func (h *Handler) fieldReportStatus(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("report_id")
	if !reportIDPattern.MatchString(id) || len(r.URL.Query()) != 0 {
		writeError(w, http.StatusBadRequest, "INVALID_QUERY", "Report ID is invalid", corrID(r))
		return
	}
	h.forward(w, r, http.MethodGet, "/internal/v1/field-reports/"+id, "", nil, nil, nil, false)
}

func (h *Handler) listFailed(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	for name, values := range q {
		if len(values) != 1 || (name != "source" && name != "limit" && name != "cursor") {
			writeError(w, http.StatusBadRequest, "INVALID_QUERY", "Query parameters are invalid", corrID(r))
			return
		}
	}
	if q.Get("source") != "" && !contracts.Source(q.Get("source")).Valid() {
		writeError(w, http.StatusBadRequest, "INVALID_QUERY", "Query parameters are invalid", corrID(r))
		return
	}
	h.forward(w, r, http.MethodGet, "/internal/v1/failed-messages", q.Encode(), nil, nil, nil, false)
}

func (h *Handler) inspectFailed(w http.ResponseWriter, r *http.Request) {
	id, ok := failedMessageID(w, r)
	if ok {
		// The detail carries the original payload and auxiliary bytes in
		// base64, so it can exceed the buffered limit for payloads the journal
		// legitimately retains. Stream it instead of buffering.
		h.forward(w, r, http.MethodGet, "/internal/v1/failed-messages/"+url.PathEscape(id), "", nil, nil, nil, true)
	}
}

func (h *Handler) replayFailed(w http.ResponseWriter, r *http.Request) {
	id, ok := failedMessageID(w, r)
	if ok {
		slog.Info("failed message replay requested", "service", "client-api", "operation", "failed_message_replay", "correlation_id", corrID(r), "client", callerFrom(r).subject, "message_id", id)
		h.forward(w, r, http.MethodPost, "/internal/v1/failed-messages/"+url.PathEscape(id)+"/replay", "", nil, nil, nil, false)
	}
}

func failedMessageID(w http.ResponseWriter, r *http.Request) (string, bool) {
	id := r.PathValue("message_id")
	if !messageIDPattern.MatchString(id) || len(r.URL.Query()) != 0 {
		writeError(w, http.StatusBadRequest, "INVALID_QUERY", "Message ID is invalid", corrID(r))
		return "", false
	}
	return id, true
}

// maxBufferedResponse bounds Aggregator responses that are read fully before
// they are relayed.
const maxBufferedResponse = 1024 * 1024

// forward relays one request to an Aggregator replica with the internal
// credential and the caller's identity. Upstream status and body are returned
// unchanged unless transform rewrites a successful body. With stream set, the
// body is copied as it arrives without a size limit; memory stays constant and
// the request deadline still bounds the transfer.
func (h *Handler) forward(w http.ResponseWriter, r *http.Request, method, path, rawQuery string, body []byte, headers map[string]string, transform func([]byte) ([]byte, error), stream bool) {
	started := time.Now()
	upstreamStatus := 0
	defer func() {
		slog.Info("Aggregator request complete", "service", "client-api", "operation", "aggregator_"+strings.ToLower(method), "correlation_id", corrID(r), "duration_ms", time.Since(started).Milliseconds(), "status", upstreamStatus)
	}()
	ctx, cancel := context.WithTimeout(r.Context(), h.cfg.Timeout)
	defer cancel()
	target := strings.TrimRight(h.cfg.AggregatorURL, "/") + path
	if rawQuery != "" {
		target += "?" + rawQuery
	}
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target, reader)
	if err != nil {
		writeError(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Aggregator is unavailable", corrID(r))
		return
	}
	req.Header.Set("X-Internal-Key", h.cfg.AggregatorKey)
	req.Header.Set("X-Correlation-ID", corrID(r))
	who := callerFrom(r)
	req.Header.Set("X-Client-ID", who.subject)
	if who.priority() {
		req.Header.Set("X-Request-Priority", "high")
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := h.readUpstream(req)
	if err != nil {
		reason := "UPSTREAM_REQUEST_FAILED"
		if errors.Is(err, context.DeadlineExceeded) {
			reason = "UPSTREAM_TIMEOUT"
		} else if errors.Is(err, syscall.EADDRNOTAVAIL) {
			reason = "LOCAL_PORTS_EXHAUSTED"
		}
		slog.Warn("Aggregator request failed", "service", "client-api", "correlation_id", corrID(r), "reason", reason)
		w.Header().Set("Retry-After", "1")
		writeError(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Aggregator is unavailable", corrID(r))
		return
	}
	upstreamStatus = resp.StatusCode
	defer resp.Body.Close()
	if stream && transform == nil {
		relayHeaders(w, resp)
		if length := resp.Header.Get("Content-Length"); length != "" {
			w.Header().Set("Content-Length", length)
		}
		w.WriteHeader(resp.StatusCode)
		if _, err := io.Copy(w, resp.Body); err != nil {
			// Headers are already sent; a short body tells the client the
			// response is incomplete.
			slog.Warn("Aggregator response stream interrupted", "service", "client-api", "correlation_id", corrID(r), "error", err)
		}
		return
	}
	payload, err := io.ReadAll(io.LimitReader(resp.Body, maxBufferedResponse+1))
	if err != nil || len(payload) > maxBufferedResponse {
		writeError(w, http.StatusServiceUnavailable, "DEPENDENCY_UNAVAILABLE", "Aggregator response is unavailable", corrID(r))
		return
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 && transform != nil {
		if payload, err = transform(payload); err != nil {
			writeError(w, http.StatusBadGateway, "UPSTREAM_INVALID", "Aggregator response is invalid", corrID(r))
			return
		}
	}
	relayHeaders(w, resp)
	w.WriteHeader(resp.StatusCode)
	_, _ = w.Write(payload)
}

func relayHeaders(w http.ResponseWriter, resp *http.Response) {
	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode == http.StatusServiceUnavailable {
		if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "" {
			w.Header().Set("Retry-After", retryAfter)
		}
	}
	if resp.Header.Get("Idempotent-Replayed") == "true" {
		w.Header().Set("Idempotent-Replayed", "true")
	}
	w.Header().Set("Content-Type", "application/json")
}
