package mock

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type PVMBGConfig struct{ Token string }
type SleepFunc func(context.Context, time.Duration) error
type DelayPicker func(time.Duration, time.Duration) time.Duration

func NewPVMBGHandler(cfg PVMBGConfig, catalog *PVMBGCatalog, sleep SleepFunc, pick DelayPicker) http.Handler {
	if sleep == nil {
		sleep = sleepContext
	}
	if pick == nil {
		pick = randomDelay
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			if r.Method != http.MethodGet {
				methodNotAllowed(w, http.MethodGet)
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		case "/ready":
			if r.Method != http.MethodGet {
				methodNotAllowed(w, http.MethodGet)
				return
			}
			if catalog.IsOutage() {
				writeJSON(w, http.StatusServiceUnavailable, map[string]string{"status": "outage"})
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		case "/volcanic-reports":
			if r.Method != http.MethodGet {
				methodNotAllowed(w, http.MethodGet)
				return
			}
			if !authorizedPVMBG(r, cfg.Token) {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			since, err := parseSince(r.URL.RawQuery)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_query")
				return
			}
			if catalog.IsOutage() {
				writeError(w, http.StatusServiceUnavailable, "source_outage")
				return
			}
			minDelay, maxDelay := catalog.DelayRange()
			delay := pick(minDelay, maxDelay)
			if delay < minDelay || delay > maxDelay {
				writeError(w, http.StatusInternalServerError, "invalid_delay_state")
				return
			}
			if err := sleep(r.Context(), delay); err != nil {
				writeError(w, http.StatusRequestTimeout, "request_cancelled")
				return
			}
			filtered := make([]VolcanicReport, 0)
			for _, report := range catalog.Snapshot() {
				if since.IsZero() || !report.ReportedAt.Before(since) {
					filtered = append(filtered, report)
				}
			}
			writeJSON(w, http.StatusOK, filtered)
		case "/admin/schema-version":
			if r.Method != http.MethodPost {
				methodNotAllowed(w, http.MethodPost)
				return
			}
			if !authorizedPVMBG(r, cfg.Token) {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			var body struct {
				Version int `json:"version"`
			}
			if err := decodeAdmin(w, r, &body); err != nil {
				writeError(w, statusForBodyError(err), "invalid_body")
				return
			}
			if err := catalog.SetSchemaVersion(body.Version); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_version")
				return
			}
			writeJSON(w, http.StatusOK, body)
		case "/admin/outage":
			if r.Method != http.MethodPost {
				methodNotAllowed(w, http.MethodPost)
				return
			}
			if !authorizedPVMBG(r, cfg.Token) {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			var body struct {
				Enabled *bool `json:"enabled"`
			}
			if err := decodeAdmin(w, r, &body); err != nil || body.Enabled == nil {
				writeError(w, statusForBodyError(err), "invalid_body")
				return
			}
			catalog.SetOutage(*body.Enabled)
			writeJSON(w, http.StatusOK, body)
		case "/admin/delay":
			if r.Method != http.MethodPost {
				methodNotAllowed(w, http.MethodPost)
				return
			}
			if !authorizedPVMBG(r, cfg.Token) {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			var body struct {
				MinMS *int64 `json:"min_ms"`
				MaxMS *int64 `json:"max_ms"`
			}
			if err := decodeAdmin(w, r, &body); err != nil || body.MinMS == nil || body.MaxMS == nil {
				writeError(w, statusForBodyError(err), "invalid_body")
				return
			}
			if *body.MinMS < 0 || *body.MaxMS < *body.MinMS || *body.MaxMS > int64((time.Duration(1<<63-1))/time.Millisecond) {
				writeError(w, http.StatusBadRequest, "invalid_delay_range")
				return
			}
			if err := catalog.SetDelayRange(time.Duration(*body.MinMS)*time.Millisecond, time.Duration(*body.MaxMS)*time.Millisecond); err != nil {
				writeError(w, http.StatusBadRequest, "invalid_delay_range")
				return
			}
			writeJSON(w, http.StatusOK, body)
		default:
			writeError(w, http.StatusNotFound, "not_found")
		}
	})
}

func authorizedPVMBG(r *http.Request, token string) bool {
	if token == "" {
		return false
	}
	value := r.Header.Get("Authorization")
	prefix, credential, ok := strings.Cut(value, " ")
	return ok && prefix == "Bearer" && subtle.ConstantTimeCompare([]byte(credential), []byte(token)) == 1 && !strings.Contains(credential, " ")
}

func parseSince(rawQuery string) (time.Time, error) {
	values, err := url.ParseQuery(rawQuery)
	if err != nil {
		return time.Time{}, err
	}
	for key := range values {
		if key != "since" {
			return time.Time{}, fmt.Errorf("unknown query parameter %q", key)
		}
	}
	entries, ok := values["since"]
	if !ok {
		return time.Time{}, nil
	}
	if len(entries) != 1 || entries[0] == "" {
		return time.Time{}, errors.New("since must appear once and be non-empty")
	}
	parsed, err := time.Parse(time.RFC3339, entries[0])
	if err != nil {
		return time.Time{}, err
	}
	return parsed.UTC(), nil
}

func decodeAdmin(w http.ResponseWriter, r *http.Request, target any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 4*1024)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}
func statusForBodyError(err error) int {
	var maxErr *http.MaxBytesError
	if errors.As(err, &maxErr) {
		return http.StatusRequestEntityTooLarge
	}
	return http.StatusBadRequest
}
func sleepContext(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		return nil
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
func randomDelay(min, max time.Duration) time.Duration {
	span := int64(max - min)
	if span <= 0 {
		return min
	}
	if span == int64(^uint64(0)>>1) {
		return min + time.Duration(rand.Int63())
	}
	return min + time.Duration(rand.Int63n(span+1))
}
func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}
func writeError(w http.ResponseWriter, status int, code string) {
	writeJSON(w, status, map[string]string{"error": code})
}
func methodNotAllowed(w http.ResponseWriter, allow string) {
	w.Header().Set("Allow", allow)
	writeError(w, http.StatusMethodNotAllowed, "method_not_allowed")
}
