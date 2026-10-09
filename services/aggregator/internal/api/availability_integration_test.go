package api

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/mapper"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/poller"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/store"
)

func TestPartialFirstIngestionRemainsReadable(t *testing.T) {
	dsn := os.Getenv("AAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("AAT_TEST_DATABASE_URL is not set; PostgreSQL integration test not run")
	}
	config, err := pgx.ParseConfig(dsn)
	if err != nil || !strings.HasSuffix(strings.ToLower(config.Database), "_test") || os.Getenv("AAT_TEST_ALLOW_DESTRUCTIVE_RESET") != "yes" {
		t.Fatal("test requires a disposable database ending in _test and AAT_TEST_ALLOW_DESTRUCTIVE_RESET=yes")
	}
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	repository := store.New(db, 15*time.Second)
	if err := repository.Migrate(ctx); err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		source mapper.Source
		valid  string
		bad    string
	}{
		{mapper.SourceBMKG, `{"event_id":"eq-valid","magnitude":5.5,"depth_km":10,"epicenter_lat":-6,"epicenter_lon":107,"region_name":"Simulasi","occurred_at":"2026-09-28T10:00:00Z","potential_tsunami":false}`, `{"event_id":"eq-invalid"}`},
		{mapper.SourcePVMBG, `{"report_id":"pv-valid","volcano_id":"sim-tamriel-red-mountain","alert_level":"Siaga","eruption_count_24h":1,"ash_column_height_m":400,"reported_at":"2026-09-28T10:00:00Z"}`, `{"report_id":"pv-invalid"}`},
	} {
		t.Run(string(test.source), func(t *testing.T) {
			if _, err := db.ExecContext(ctx, `TRUNCATE outbox, hazard_versions, hazard_events, source_state CASCADE`); err != nil {
				t.Fatal(err)
			}
			var payload atomic.Value
			payload.Store("[" + test.bad + "]")
			source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/tsunami-warnings" {
					_, _ = w.Write([]byte("[]"))
					return
				}
				_, _ = w.Write([]byte(payload.Load().(string)))
			}))
			defer source.Close()
			p := poller.New(repository,
				poller.NewBMKGClient(source.URL, "synthetic-source-key", source.Client(), 1<<20),
				poller.NewPVMBGClient(source.URL, "synthetic-source-token", source.Client(), 1<<20), nil, nil)
			poll := p.PollBMKG
			if test.source == mapper.SourcePVMBG {
				poll = p.PollPVMBG
			}
			handler := NewHandler(repository, "synthetic-internal-key", time.Now)
			listPath := "/internal/v1/hazards?source=" + string(test.source) + "&view=full"
			read := func(path string, wantStatus int) []byte {
				t.Helper()
				r := httptest.NewRequest(http.MethodGet, path, nil)
				r.Header.Set("X-Internal-Key", "synthetic-internal-key")
				response := httptest.NewRecorder()
				handler.ServeHTTP(response, r)
				if response.Code != wantStatus {
					t.Fatalf("GET %s: status=%d, want %d; body=%s", path, response.Code, wantStatus, response.Body.String())
				}
				return response.Body.Bytes()
			}

			read(listPath, http.StatusServiceUnavailable)
			if err := poll(ctx); err != nil {
				t.Fatal(err)
			}
			read(listPath, http.StatusServiceUnavailable) // No valid data was accepted.
			payload.Store("[" + test.valid + "," + test.bad + "]")
			if err := poll(ctx); err != nil {
				t.Fatal(err)
			}
			items, _, err := repository.List(ctx, string(test.source), 50, 0)
			if err != nil || len(items) != 1 {
				t.Fatalf("valid record was not retained: items=%d error=%v", len(items), err)
			}
			var lastSuccess sql.NullTime
			var failureSince time.Time
			var code string
			if err := db.QueryRowContext(ctx, `SELECT last_success_at,failure_since,last_error_code FROM source_state WHERE source=$1`, test.source).Scan(&lastSuccess, &failureSince, &code); err != nil {
				t.Fatal(err)
			}
			if lastSuccess.Valid || code != "INVALID_RECORD" {
				t.Fatalf("partial ingestion must remain degraded: success=%v error=%s", lastSuccess, code)
			}
			for _, path := range []string{listPath, listPath + "&offset=100", "/internal/v1/hazards/" + items[0].HazardID} {
				var body struct {
					Meta struct {
						Sources map[mapper.Source]store.SourceState `json:"sources"`
					} `json:"meta"`
				}
				if err := json.Unmarshal(read(path, http.StatusOK), &body); err != nil {
					t.Fatal(err)
				}
				state := body.Meta.Sources[test.source]
				if state.Status != "stale" || state.LastSuccessAt != nil || state.StaleSince == nil || !state.StaleSince.Equal(failureSince) {
					t.Fatalf("partial data has incorrect freshness: %+v", state)
				}
			}
			payload.Store("[" + test.valid + "]")
			if err := poll(ctx); err != nil {
				t.Fatal(err)
			}
			read(listPath, http.StatusOK)
			states, err := repository.SourceStates(ctx, []mapper.Source{test.source}, time.Now())
			if err != nil || states[test.source].Status != "fresh" || states[test.source].LastSuccessAt == nil || states[test.source].StaleSince != nil {
				t.Fatalf("successful poll did not restore freshness: %+v error=%v", states, err)
			}
		})
	}
}
