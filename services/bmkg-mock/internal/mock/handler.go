package mock

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"time"
)

type BMKGConfig struct {
	APIKey string
	Delay  time.Duration
}
type SleepFunc func(context.Context, time.Duration) error

func NewBMKGHandler(cfg BMKGConfig, catalog *BMKGCatalog, sleep SleepFunc) http.Handler {
	if sleep == nil {
		sleep = sleepContext
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			if r.Method != http.MethodGet {
				methodNotAllowed(w, http.MethodGet)
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		case "/seismic-events":
			if r.Method != http.MethodGet {
				methodNotAllowed(w, http.MethodGet)
				return
			}
			if !authorizedBMKG(r, cfg.APIKey) {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			since, err := parseSince(r.URL.RawQuery)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_query")
				return
			}
			if err := sleep(r.Context(), cfg.Delay); err != nil {
				writeError(w, http.StatusRequestTimeout, "request_cancelled")
				return
			}
			events, _ := catalog.Snapshot()
			filtered := make([]SeismicEvent, 0, len(events))
			for _, event := range events {
				if since.IsZero() || !event.OccurredAt.Before(since) {
					filtered = append(filtered, event)
				}
			}
			writeJSON(w, http.StatusOK, filtered)
		case "/tsunami-warnings":
			if r.Method != http.MethodGet {
				methodNotAllowed(w, http.MethodGet)
				return
			}
			if !authorizedBMKG(r, cfg.APIKey) {
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}
			since, err := parseSince(r.URL.RawQuery)
			if err != nil {
				writeError(w, http.StatusBadRequest, "invalid_query")
				return
			}
			if err := sleep(r.Context(), cfg.Delay); err != nil {
				writeError(w, http.StatusRequestTimeout, "request_cancelled")
				return
			}
			_, warnings := catalog.Snapshot()
			filtered := make([]TsunamiWarning, 0, len(warnings))
			for _, warning := range warnings {
				if since.IsZero() || !warning.PublishedAt.Before(since) {
					filtered = append(filtered, warning)
				}
			}
			sort.Slice(filtered, func(i, j int) bool { return filtered[i].WarningID < filtered[j].WarningID })
			writeJSON(w, http.StatusOK, filtered)
		default:
			writeError(w, http.StatusNotFound, "not_found")
		}
	})
}

func authorizedBMKG(r *http.Request, key string) bool {
	return key != "" && subtle.ConstantTimeCompare([]byte(r.Header.Get("X-BMKG-Key")), []byte(key)) == 1
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
