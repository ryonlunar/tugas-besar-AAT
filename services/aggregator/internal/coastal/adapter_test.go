package coastal

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/pipeline"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/sourcejournal"
)

const stations = `{"items":[{"station_id":"s-9","name":"Bay","province":"West","latitude":0,"longitude":0}]}`
const valid = `{"observation_id":"reading-947","station_id":"s-9","observed_at":"2026-01-01T00:00:00Z","sea_level_anomaly_m":-0.42,"wave_height_m":1.8,"quality":"ok","critical":true}`

func TestCursorResumesAndInvalidInputParks(t *testing.T) {
	stop := errors.New("journal commit rejected")
	requests := make(chan string, 2)
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing bearer credential")
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/v1/stations":
			_, _ = w.Write([]byte(stations))
		case "/v1/observations":
			cursor := r.URL.Query().Get("cursor")
			requests <- cursor
			if cursor == "resume+key=" {
				_, _ = w.Write([]byte(`{"generated_at":"2026-01-01T00:00:05Z","items":[` + valid + `,{"observation_id":null}],"next_cursor":"next"}`))
				return
			}
			_, _ = w.Write([]byte(`{"generated_at":"2026-01-01T00:01:00Z","items":[],"next_cursor":null}`))
		default:
			w.WriteHeader(404)
		}
	}))
	defer mock.Close()
	adapter, err := New(mock.URL, "test-token", nil, 250*time.Millisecond, 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var batches []contracts.Batch
	err = adapter.Run(ctx, "resume+key=", func(_ context.Context, batch contracts.Batch) error {
		batches = append(batches, batch)
		if len(batches) == 2 {
			return stop
		}
		return nil
	})
	if !errors.Is(err, stop) || len(batches) != 2 {
		t.Fatalf("adapter exit %v, batches %d", err, len(batches))
	}
	if first, second := <-requests, <-requests; first != "resume+key=" || second != "next" {
		t.Fatalf("cursor changed: %q then %q", first, second)
	}
	if len(batches[0].Records) != 2 || batches[0].DegradedReason != "" || batches[1].DegradedReason != "COASTAL_OBSERVATIONS_STALE" || batches[1].Cursor != "next" {
		t.Fatalf("unexpected page/freshness %#v", batches)
	}
	good, bad := batches[0].Records[0], batches[0].Records[1]
	if string(good.Payload) != valid || string(bad.Payload) != `{"observation_id":null}` {
		t.Fatal("raw bytes changed")
	}
	mutations, err := adapter.Process(ctx, nil, sourcejournal.Message{RawRecord: good})
	if err != nil || len(mutations) != 1 || mutations[0].Event.Severity != contracts.SeverityAwas || mutations[0].Event.SourceRefID != "reading-947" {
		t.Fatalf("mapping %v, %v", mutations, err)
	}
	var parked *pipeline.PermanentError
	if _, err := adapter.Process(ctx, nil, sourcejournal.Message{RawRecord: bad}); !errors.As(err, &parked) {
		t.Fatalf("invalid input was not parked: %v", err)
	}
}

func TestFailedFetchDoesNotAdvanceCursor(t *testing.T) {
	mock := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "outage", 503) }))
	defer mock.Close()
	a, err := New(mock.URL, "test", nil, 250*time.Millisecond, 15*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	stop := errors.New("stop")
	err = a.Run(context.Background(), "saved", func(_ context.Context, b contracts.Batch) error {
		if b.From != "saved" || b.Cursor != "saved" || len(b.Records) != 0 || b.DegradedReason == "" {
			t.Fatalf("failure altered cursor or health: %#v", b)
		}
		return stop
	})
	if !errors.Is(err, stop) {
		t.Fatal(err)
	}
}

func TestCriticalObservationCanUseRefreshedStationCatalog(t *testing.T) {
	a := &Adapter{}
	m := sourcejournal.Message{
		RawRecord: rawRecord(observationKind, "reading-947", []byte(valid), nil, time.Now(), "corr"),
		Attempts:  1,
	}
	var retry *pipeline.RetryableError
	if _, err := a.Process(context.Background(), nil, m); !errors.As(err, &retry) {
		t.Fatalf("missing station must stay retryable: %v", err)
	}
	m.Attempts = 8
	var parked *pipeline.PermanentError
	if _, err := a.Process(context.Background(), nil, m); !errors.As(err, &parked) {
		t.Fatalf("unresolved station must eventually park: %v", err)
	}
	a.setCatalog(map[string]json.RawMessage{"s-9": []byte(`{"station_id":"s-9","name":"Bay","province":"West","latitude":0,"longitude":0}`)})
	mutations, err := a.Process(context.Background(), nil, m)
	if err != nil || len(mutations) != 1 || mutations[0].Event.Attributes["critical"] != true || mutations[0].Event.AreaName != "West" {
		t.Fatalf("retry or replay did not recover the original critical observation: %v, %v", mutations, err)
	}
}
