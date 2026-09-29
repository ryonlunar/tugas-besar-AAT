package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/mapper"
)

func validateDisposableTestDatabase(dsn string, allowDestructiveReset bool) error {
	config, err := pgx.ParseConfig(dsn)
	if err != nil || config.Database == "" {
		return errors.New("AAT_TEST_DATABASE_URL must be a valid PostgreSQL DSN with a database name")
	}
	if !strings.HasSuffix(strings.ToLower(config.Database), "_test") {
		return errors.New("database name must end with _test")
	}
	if !allowDestructiveReset {
		return errors.New("explicit reset opt-in required; set AAT_TEST_ALLOW_DESTRUCTIVE_RESET=yes only for a disposable database")
	}
	return nil
}

func requireTestDatabaseURL(t *testing.T) string {
	t.Helper()
	dsn := os.Getenv("AAT_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("AAT_TEST_DATABASE_URL is not set; PostgreSQL integration test not run")
		return ""
	}
	if err := validateDisposableTestDatabase(dsn, os.Getenv("AAT_TEST_ALLOW_DESTRUCTIVE_RESET") == "yes"); err != nil {
		t.Fatalf("refusing PostgreSQL integration test: %v", err)
	}
	return dsn
}

func TestDisposableDatabaseGuard(t *testing.T) {
	tests := []struct {
		name    string
		dsn     string
		allow   bool
		wantErr bool
	}{
		{name: "refuses non-test database", dsn: "postgres://localhost/aat", allow: true, wantErr: true},
		{name: "requires explicit reset opt-in", dsn: "postgres://localhost/aat_test", wantErr: true},
		{name: "accepts explicitly disposable test database", dsn: "postgres://localhost/aat_test", allow: true},
		{name: "refuses malformed DSN", dsn: "://bad", allow: true, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateDisposableTestDatabase(test.dsn, test.allow)
			if (err != nil) != test.wantErr {
				t.Fatalf("guard error=%v, wantErr=%v", err, test.wantErr)
			}
		})
	}
}

func TestApplyCycleKeepsStableIDsHistoryAndOutboxIdempotently(t *testing.T) {
	dsn := requireTestDatabaseURL(t)
	ctx := context.Background()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	readDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer readDB.Close()
	pvmbgDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pvmbgDB.Close()
	relayDB, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer relayDB.Close()
	db.SetMaxOpenConns(2)
	readDB.SetMaxOpenConns(2)
	pvmbgDB.SetMaxOpenConns(2)
	relayDB.SetMaxOpenConns(2)
	s := NewWithPools(readDB, db, pvmbgDB, relayDB, 15*time.Second)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `TRUNCATE outbox, hazard_versions, hazard_events, source_state CASCADE`); err != nil {
		t.Fatal(err)
	}

	at := time.Date(2026, 9, 28, 3, 0, 0, 0, time.UTC)
	first := testHazard("", "PVMBG", "pv-1", at, map[string]any{"ash_column_height_m": 500})
	if err := s.ApplyCycle(ctx, mapper.SourcePVMBG, []mapper.HazardEvent{first}, "poll-1", true, "", at); err != nil {
		t.Fatal(err)
	}
	id, found, err := s.FindHazardID(ctx, mapper.SourcePVMBG, "pv-1")
	if err != nil || !found {
		t.Fatalf("identity lookup id=%q found=%v err=%v", id, found, err)
	}

	second := testHazard(id, "PVMBG", "pv-1", at.Add(time.Second), map[string]any{"ash_column_height_m": 700, "confidence_level": json.Number("0.9")})
	if err := s.ApplyCycle(ctx, mapper.SourcePVMBG, []mapper.HazardEvent{second}, "poll-2", true, "", at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	third := second
	third.IngestedAt = at.Add(2 * time.Second).Format(time.RFC3339Nano)
	if err := s.ApplyCycle(ctx, mapper.SourcePVMBG, []mapper.HazardEvent{third}, "poll-3", true, "", at.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}

	var latestRevision int
	if err := db.QueryRowContext(ctx, `SELECT revision FROM hazard_events WHERE hazard_id=$1`, id).Scan(&latestRevision); err != nil {
		t.Fatal(err)
	}
	var versions, outboxRows int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM hazard_versions WHERE hazard_id=$1`, id).Scan(&versions); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM outbox WHERE hazard_id=$1`, id).Scan(&outboxRows); err != nil {
		t.Fatal(err)
	}
	if latestRevision != 2 || versions != 2 || outboxRows != 2 {
		t.Fatalf("revision=%d versions=%d outbox=%d; want 2,2,2", latestRevision, versions, outboxRows)
	}
	v1, found, err := s.GetVersion(ctx, id, 1)
	if err != nil || !found || strings.Contains(string(v1), "confidence_level") {
		t.Fatalf("v1 history was not preserved without the later field: found=%v err=%v", found, err)
	}
	v2, found, err := s.GetVersion(ctx, id, 2)
	if err != nil || !found || !strings.Contains(string(v2), "confidence_level") {
		t.Fatalf("v2 history is missing its additive field: found=%v err=%v", found, err)
	}
	latest, found, err := s.Get(ctx, id)
	if err != nil || !found || latest.HazardID != id || latest.Attributes["confidence_level"] != float64(0.9) {
		t.Fatalf("latest event does not expose stable ID and v2 data: found=%v event=%+v err=%v", found, latest, err)
	}
	items, hasMore, err := s.List(ctx, string(mapper.SourcePVMBG), 1, 0)
	if err != nil || len(items) != 1 || hasMore || items[0].HazardID != id {
		t.Fatalf("read pool list returned items=%d hasMore=%v err=%v", len(items), hasMore, err)
	}
	pending, err := s.PendingOutbox(ctx, 10, at.Add(3*time.Second))
	if err != nil || len(pending) != 2 {
		t.Fatalf("relay pool returned pending=%d err=%v; want 2", len(pending), err)
	}

	states, err := s.SourceStates(ctx, []mapper.Source{mapper.SourcePVMBG}, at.Add(3*time.Second))
	if err != nil || states[mapper.SourcePVMBG].Status != "fresh" {
		t.Fatalf("freshness after successful poll: states=%+v err=%v", states, err)
	}
	if err := s.ApplyCycle(ctx, mapper.SourcePVMBG, nil, "poll-4", false, "SOURCE_FETCH_FAILED", at.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	states, err = s.SourceStates(ctx, []mapper.Source{mapper.SourcePVMBG}, at.Add(5*time.Second))
	if err != nil || states[mapper.SourcePVMBG].Status != "stale" || states[mapper.SourcePVMBG].StaleSince == nil || !states[mapper.SourcePVMBG].StaleSince.Equal(at.Add(4*time.Second)) {
		t.Fatalf("stale state lost failure time: states=%+v err=%v", states, err)
	}
}

func TestApplyCycleStopsAtPendingOutboxLimit(t *testing.T) {
	dsn := requireTestDatabaseURL(t)
	ctx := context.Background()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := New(db, 15*time.Second)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `TRUNCATE outbox, hazard_versions, hazard_events, source_state CASCADE`); err != nil {
		t.Fatal(err)
	}
	s.SetOutboxLimit(1)
	at := time.Date(2026, 9, 28, 4, 0, 0, 0, time.UTC)
	first := testHazard("", "PVMBG", "pv-cap-1", at, map[string]any{"ash_column_height_m": 100})
	if err := s.ApplyCycle(ctx, mapper.SourcePVMBG, []mapper.HazardEvent{first}, "poll-cap-1", true, "", at); err != nil {
		t.Fatal(err)
	}
	second := testHazard("", "PVMBG", "pv-cap-2", at.Add(time.Second), map[string]any{"ash_column_height_m": 200})
	if err := s.ApplyCycle(ctx, mapper.SourcePVMBG, []mapper.HazardEvent{second}, "poll-cap-2", true, "", at.Add(time.Second)); !errors.Is(err, ErrOutboxCapacity) {
		t.Fatalf("second cycle error=%v; want ErrOutboxCapacity", err)
	}
	var eventCount, pendingCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM hazard_events`).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&pendingCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 1 || pendingCount != 1 {
		t.Fatalf("capacity rejection wrote event_count=%d pending_count=%d; want 1,1", eventCount, pendingCount)
	}
}

func TestApplyCycleDoesNotOverrunPendingOutboxLimitWithinSnapshot(t *testing.T) {
	dsn := requireTestDatabaseURL(t)
	ctx := context.Background()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := New(db, 15*time.Second)
	s.SetOutboxLimit(1)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `TRUNCATE outbox, hazard_versions, hazard_events, source_state CASCADE`); err != nil {
		t.Fatal(err)
	}

	at := time.Date(2026, 9, 28, 6, 0, 0, 0, time.UTC)
	events := []mapper.HazardEvent{
		testHazard("", "PVMBG", "pv-batch-cap-1", at, map[string]any{"ash_column_height_m": 100}),
		testHazard("", "PVMBG", "pv-batch-cap-2", at.Add(time.Second), map[string]any{"ash_column_height_m": 200}),
	}
	err = s.ApplyCycle(ctx, mapper.SourcePVMBG, events, "poll-batch-cap", true, "", at)
	if !errors.Is(err, ErrOutboxCapacity) {
		t.Fatalf("snapshot error=%v; want ErrOutboxCapacity", err)
	}
	var eventCount, pendingCount int
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM hazard_events`).Scan(&eventCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&pendingCount); err != nil {
		t.Fatal(err)
	}
	if eventCount != 0 || pendingCount != 0 {
		t.Fatalf("rejected snapshot partially committed event_count=%d pending_count=%d; want 0,0", eventCount, pendingCount)
	}
}

func TestApplyCycleTreatsEquivalentJSONBNumbersAsUnchanged(t *testing.T) {
	dsn := requireTestDatabaseURL(t)
	ctx := context.Background()
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	s := New(db, 15*time.Second)
	if err := s.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := db.ExecContext(ctx, `TRUNCATE outbox, hazard_versions, hazard_events, source_state CASCADE`); err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 9, 28, 5, 0, 0, 0, time.UTC)
	first := testHazard("", "PVMBG", "pv-semantic-1", at, map[string]any{"value": json.Number("1.0")})
	if err := s.ApplyCycle(ctx, mapper.SourcePVMBG, []mapper.HazardEvent{first}, "poll-semantic-1", true, "", at); err != nil {
		t.Fatal(err)
	}
	second := testHazard("", "PVMBG", "pv-semantic-1", at, map[string]any{"value": json.Number("1e0")})
	if err := s.ApplyCycle(ctx, mapper.SourcePVMBG, []mapper.HazardEvent{second}, "poll-semantic-2", true, "", at.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var revision, versionCount, outboxCount int
	if err := db.QueryRowContext(ctx, `SELECT revision FROM hazard_events WHERE source_ref_id='pv-semantic-1'`).Scan(&revision); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM hazard_versions`).Scan(&versionCount); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRowContext(ctx, `SELECT count(*) FROM outbox`).Scan(&outboxCount); err != nil {
		t.Fatal(err)
	}
	if revision != 1 || versionCount != 1 || outboxCount != 1 {
		t.Fatalf("semantic duplicate created new data: revision=%d versions=%d outbox=%d", revision, versionCount, outboxCount)
	}
}

func testHazard(id, source, ref string, at time.Time, attrs map[string]any) mapper.HazardEvent {
	return mapper.HazardEvent{
		HazardID: id, Source: mapper.Source(source), SourceRefID: ref,
		HazardType: mapper.HazardVolcanic, Severity: mapper.SeveritySiaga,
		AreaName: "Gunung Simulasi (Tamriel)", Latitude: -7, Longitude: 108,
		OccurredAt: at.Format(time.RFC3339Nano), IngestedAt: at.Format(time.RFC3339Nano), Attributes: attrs,
	}
}
