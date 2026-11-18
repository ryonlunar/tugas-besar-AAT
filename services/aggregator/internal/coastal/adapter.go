// Package coastal reads the assistant-provided Sensor Pantai HTTP contract.
package coastal

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	randv2 "math/rand/v2"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/store"
	"go.opentelemetry.io/otel"
)

const (
	observationKind = "coastal_observation"
	responseKind    = "coastal_response"
	maxBody         = 1 << 20
	noProgressLimit = 30 * time.Second
)

type Adapter struct {
	base             *url.URL
	token            string
	client           http.Client
	interval, maxAge time.Duration
	Observe          func(string, string, error, time.Duration)
	catalogMu        sync.RWMutex
	catalog          map[string]json.RawMessage
}

func New(baseURL, token string, transport http.RoundTripper, interval, maxAge time.Duration) (*Adapter, error) {
	base, err := url.Parse(baseURL)
	if err != nil || base.Host == "" || (base.Scheme != "http" && base.Scheme != "https") || base.User != nil || base.RawQuery != "" || base.Fragment != "" || token == "" || interval < 250*time.Millisecond || maxAge <= interval {
		return nil, errors.New("invalid coastal URL, token, polling interval, or freshness limit")
	}
	return &Adapter{base: base, token: token, interval: interval, maxAge: maxAge, client: http.Client{
		Transport: transport, Timeout: time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}, nil
}

type page struct {
	GeneratedAt time.Time         `json:"generated_at"`
	Items       []json.RawMessage `json:"items"`
	NextCursor  *string           `json:"next_cursor"`
}

// Catalog entries are immutable. Refresh swaps the whole catalog while
// processing of journaled observations can continue on another goroutine.
func (a *Adapter) setCatalog(catalog map[string]json.RawMessage) {
	a.catalogMu.Lock()
	defer a.catalogMu.Unlock()
	a.catalog = catalog
}

func (a *Adapter) stationMetadata(id string) json.RawMessage {
	a.catalogMu.RLock()
	defer a.catalogMu.RUnlock()
	return a.catalog[id]
}

func (a *Adapter) Run(ctx context.Context, cursor string, commit func(context.Context, contracts.Batch) error) error {
	stations := map[string]json.RawMessage{}
	a.setCatalog(nil)
	var nextStations, latest, advancedAt time.Time
	backoff := time.Second
	lastHealth := ""
	for ctx.Err() == nil {
		if err := store.CheckOwnership(ctx); err != nil {
			return err
		}
		started := time.Now()
		pollCtx, span := otel.Tracer("aat/ingestion").Start(ctx, "coastal.poll")
		var entropy [16]byte
		_, _ = rand.Read(entropy[:])
		correlation := hex.EncodeToString(entropy[:])
		var records []contracts.RawRecord
		var degraded string
		next := cursor
		if !started.Before(nextStations) {
			// Keep the last successful station catalog during lookup failures.
			catalog, err := a.stations(pollCtx, correlation)
			if err == nil {
				stations = catalog
				a.setCatalog(catalog)
				nextStations = started.Add(time.Minute)
			} else {
				nextStations = started.Add(8 * time.Second)
				if len(stations) == 0 {
					degraded = "COASTAL_STATIONS_UNAVAILABLE"
				}
			}
		}
		if degraded == "" {
			query := url.Values{"limit": {"100"}}
			if cursor != "" {
				query.Set("cursor", cursor)
			}
			body, err := a.get(pollCtx, "/v1/observations", query, correlation)
			if err != nil {
				degraded = "COASTAL_FETCH_FAILED"
			} else {
				var p page
				if json.Unmarshal(body, &p) != nil || p.Items == nil || p.GeneratedAt.IsZero() || (p.NextCursor != nil && len(*p.NextCursor) > 4096) {
					degraded = "COASTAL_INVALID_RESPONSE"
					records = append(records, rawRecord(responseKind, "", body, nil, started, correlation))
				} else {
					if p.NextCursor != nil && *p.NextCursor != "" {
						next = *p.NextCursor
					}
					for _, raw := range p.Items {
						o, err := decodeObservation(raw)
						station := stations[o.Station]
						orderingKey := ""
						if err == nil {
							if _, known := decodeStation(station, o.Station); known {
								orderingKey = o.ID
							} else {
								nextStations = minTime(nextStations, started.Add(8*time.Second))
							}
							// Freshness describes the upstream feed. A station lookup
							// failure is reported separately by the pending input.
							if o.ObservedAt.After(latest) {
								latest = o.ObservedAt
								advancedAt = started
							}
						}
						record := rawRecord(observationKind, o.ID, raw, station, started, correlation)
						// Preserve journal identity and key ordering across the upgrade.
						record.OrderingKey = orderingKey
						records = append(records, record)
					}
					if latest.IsZero() || started.Sub(advancedAt) > noProgressLimit || p.GeneratedAt.Sub(latest) > a.maxAge {
						degraded = "COASTAL_OBSERVATIONS_STALE"
					}
				}
			}
		}
		if ctx.Err() != nil {
			span.End()
			return ctx.Err()
		}
		if a.Observe != nil {
			var err error
			if degraded != "" {
				err = errors.New(degraded)
			}
			a.Observe("source_poll", string(contracts.SourceCoastal), err, time.Since(started))
		}
		if degraded != lastHealth {
			slog.InfoContext(pollCtx, "coastal source health changed", "source", contracts.SourceCoastal, "reason", degraded, "correlation_id", correlation)
			lastHealth = degraded
		}
		err := commit(pollCtx, contracts.Batch{From: cursor, Cursor: next, Records: records, DegradedReason: degraded})
		span.End()
		if err != nil {
			return err
		}
		advanced := next != cursor
		cursor = next
		delay := a.interval
		if degraded != "" {
			// Continue draining backlog pages while the cursor advances,
			// even when the historical observations themselves are stale.
			if !advanced || len(records) == 0 || degraded != "COASTAL_OBSERVATIONS_STALE" {
				delay = backoff
				backoff = min(8*time.Second, backoff*2)
			}
		} else {
			backoff = time.Second
		}
		timer := time.NewTimer(delay + time.Duration(randv2.Int64N(int64(delay/5)+1)))
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return ctx.Err()
}

func (a *Adapter) stations(ctx context.Context, correlation string) (map[string]json.RawMessage, error) {
	body, err := a.get(ctx, "/v1/stations", nil, correlation)
	if err != nil {
		return nil, err
	}
	var result struct {
		Items []json.RawMessage `json:"items"`
	}
	if err = json.Unmarshal(body, &result); err != nil || result.Items == nil {
		return nil, errors.New("invalid station response")
	}
	stations := map[string]json.RawMessage{}
	for _, raw := range result.Items {
		var s station
		if json.Unmarshal(raw, &s) == nil && s.ID != "" {
			stations[s.ID] = raw
		}
	}
	return stations, nil
}

func (a *Adapter) get(ctx context.Context, path string, query url.Values, correlation string) ([]byte, error) {
	destination := a.base.JoinPath(path)
	destination.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, destination.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("X-Correlation-ID", correlation)
	resp, err := a.client.Do(req)
	if err != nil {
		return nil, errors.New("coastal request failed")
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("coastal HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil || len(body) > maxBody {
		return nil, errors.New("coastal response unreadable or oversized")
	}
	return body, nil
}

func rawRecord(kind, id string, body, station []byte, at time.Time, correlation string) contracts.RawRecord {
	return contracts.RawRecord{Kind: kind, Key: contracts.RawKey(contracts.RawKey(kind+":"+id, body), station), OrderingKey: id, Payload: body, Auxiliary: station, ReceivedAt: at.UTC(), CorrelationID: correlation}
}

func minTime(a, b time.Time) time.Time {
	if a.Before(b) {
		return a
	}
	return b
}
