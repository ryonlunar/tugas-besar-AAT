package sharded

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/ryonlunar/tugas-besar-AAT/contracts"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/mapper"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/partition"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/sourcejournal"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/store"
)

func TestRoutingFencingAndPartialReads(t *testing.T) {
	filename := os.Getenv("AAT_FOUNDATION_DSN_FILE")
	if filename == "" {
		t.Skip("run scripts/m2/check-foundation.py in the development VM")
	}
	raw, err := os.ReadFile(filename)
	var dsns []string
	if err != nil || json.Unmarshal(raw, &dsns) != nil || len(dsns) != 4 {
		t.Fatal("invalid disposable database configuration")
	}
	configs := []Config{}
	for i, dsn := range dsns {
		config, err := pgx.ParseConfig(dsn)
		if err != nil || !strings.HasPrefix(config.Database, "aat_foundation_") || !strings.HasSuffix(config.Database, "_test") {
			t.Fatal("refusing non-disposable database")
		}
		configs = append(configs, Config{fmt.Sprintf("shard-%d", i), dsn})
	}
	file := filepath.Join(t.TempDir(), "shards.json")
	raw, _ = json.Marshal(configs)
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	repository, err := Open(ctx, file, Options{ReadConns: 2, BMKGConns: 2, PVMBGConns: 2, RelayConns: 2, ReadTimeout: time.Second, WriteTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	t.Run("fresh installation and incomplete legacy schema", func(t *testing.T) {
		config, err := pgx.ParseConfig(dsns[0])
		if err != nil {
			t.Fatal(err)
		}
		// Isolate the legacy connection from the test database's public tables.
		config.RuntimeParams["search_path"] = "pg_temp"
		legacy := stdlib.OpenDB(*config)
		legacy.SetMaxOpenConns(1)
		defer legacy.Close()
		if err := repository.ImportLegacy(ctx, legacy); err != nil {
			t.Fatalf("fresh database must not require an M1 schema: %v", err)
		}
		if _, err := legacy.ExecContext(ctx, `CREATE TEMP TABLE hazard_events (id integer)`); err != nil {
			t.Fatal(err)
		}
		if err := repository.ImportLegacy(ctx, legacy); err == nil || !strings.Contains(err.Error(), "incomplete legacy schema") {
			t.Fatalf("partial schema must fail before copying data: %v", err)
		}
	})
	old, owned, err := sourcejournal.Acquire(ctx, dsns[0], contracts.SourceBMKG, 300*time.Millisecond)
	if err != nil || !owned {
		t.Fatalf("acquire owner: %v %v", owned, err)
	}
	defer old.Close(context.Background())
	if err := repository.installFences(ctx, contracts.SourceBMKG, old.Generation()); err != nil {
		t.Fatal(err)
	}
	ownerCtx := store.WithFence(ctx, contracts.SourceBMKG, old.Generation(), old.Heartbeat)
	at := time.Now().UTC().Truncate(time.Microsecond)
	events := []mapper.HazardEvent{}
	placement := map[string]int{}
	for i := 0; i < 40; i++ {
		ref := fmt.Sprintf("routing-%03d", i)
		unit, _ := partition.Unit(contracts.SourceBMKG, ref)
		placement[unit]++
		events = append(events, mapper.HazardEvent{Source: contracts.SourceBMKG, SourceRefID: ref, HazardType: contracts.HazardSeismic, Severity: contracts.SeverityNormal,
			AreaName: "Same region", Latitude: 0, Longitude: 100, OccurredAt: at.Add(-time.Duration(i) * time.Second).Format(time.RFC3339Nano), Attributes: map[string]any{"exact": json.Number("9007199254740993")}})
	}
	if len(placement) != 4 {
		t.Fatal("fixture must exercise all logical units")
	}
	if err := repository.ApplyCycle(ownerCtx, contracts.SourceBMKG, events, "routing-check", true, "", at, at); err != nil {
		t.Fatal(err)
	}
	for i, u := range repository.units {
		db, err := sql.Open("pgx", dsns[i])
		if err != nil {
			t.Fatal(err)
		}
		var count int
		err = db.QueryRowContext(ctx, `SELECT count(*) FROM hazard_events`).Scan(&count)
		db.Close()
		if err != nil || count != placement[u.name] {
			t.Fatalf("%s: records=%d expected=%d err=%v", u.name, count, placement[u.name], err)
		}
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		page, err := repository.Page(ctx, "BMKG", cursor, 7, 0)
		if err != nil || page.Partial {
			t.Fatalf("page: partial=%v err=%v", page.Partial, err)
		}
		for _, event := range page.Items {
			if seen[event.SourceRefID] {
				t.Fatal("duplicate across pages")
			}
			seen[event.SourceRefID] = true
			if event.Attributes["exact"].(json.Number).String() != "9007199254740993" {
				t.Fatal("dynamic number was rounded")
			}
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	if len(seen) != 40 {
		t.Fatalf("scan returned %d records", len(seen))
	}
	page, err := repository.Page(ctx, "BMKG", "", 7, 7)
	if err != nil || len(page.Items) != 7 || page.Items[0].SourceRefID != "routing-007" {
		t.Fatalf("legacy offset: %+v %v", page, err)
	}
	pending, err := repository.PendingOutbox(ctx, 100, at.Add(time.Minute))
	if err != nil || len(pending) != 40 {
		t.Fatalf("outbox: %d %v", len(pending), err)
	}
	if err := repository.MarkPublished(ctx, pending[0].QueueKey, at); err != nil {
		t.Fatal(err)
	}
	if err := repository.MarkPublished(ctx, pending[0].QueueKey, at.Add(time.Second)); err != nil {
		t.Fatalf("concurrent acknowledgement must be idempotent: %v", err)
	}
	if err := repository.RecordPublishFailure(ctx, pending[0].QueueKey, "TEST", at); err != nil {
		t.Fatalf("late failure must not requeue an acknowledged event: %v", err)
	}
	var next *sourcejournal.Session
	for next == nil && ctx.Err() == nil {
		candidate, ok, err := sourcejournal.Acquire(ctx, dsns[0], contracts.SourceBMKG, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if ok {
			next = candidate
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if next == nil {
		t.Fatal("owner takeover timed out")
	}
	defer next.Close(context.Background())
	if err := repository.installFences(ctx, contracts.SourceBMKG, next.Generation()); err != nil {
		t.Fatal(err)
	}
	if err := repository.ApplyCycle(ownerCtx, contracts.SourceBMKG, nil, "old-owner", true, "", at, at); !errors.Is(err, store.ErrFenced) {
		t.Fatalf("stale owner write: %v", err)
	}
	// Generation rejection must still work even if a caller's liveness check succeeds.
	forged := store.WithFence(ctx, contracts.SourceBMKG, old.Generation(), func(context.Context) error { return nil })
	if err := repository.ApplyCycle(forged, contracts.SourceBMKG, nil, "old-generation", true, "", at, at); !errors.Is(err, store.ErrFenced) {
		t.Fatalf("old generation accepted: %v", err)
	}
	newCtx := store.WithFence(ctx, contracts.SourceBMKG, next.Generation(), next.Heartbeat)
	if err := repository.ApplyCycle(newCtx, contracts.SourceBMKG, events, "new-owner", true, "", at, at); err != nil {
		t.Fatal(err)
	}
	locker, err := sql.Open("pgx", dsns[2])
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Close()
	tx, err := locker.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, `LOCK TABLE hazard_events IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	slowPage, err := repository.Page(ctx, "BMKG", "", 100, 0)
	if err != nil || !slowPage.Partial || len(slowPage.UnavailableUnits) != 1 || slowPage.UnavailableUnits[0] != "shard-2" || time.Since(started) > 1500*time.Millisecond {
		t.Fatalf("slow unit must produce a bounded partial page, not a caller deadline error: partial=%v unavailable=%v elapsed=%s err=%v", slowPage.Partial, slowPage.UnavailableUnits, time.Since(started), err)
	}
	if err := tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	// Closing a unit's pools models an inaccessible primary without stopping the
	// other three databases or inventing successful responses for the missing one.
	for _, db := range repository.pools[8:12] {
		_ = db.Close()
	}
	partial, err := repository.Page(ctx, "BMKG", "", 100, 0)
	if err != nil || !partial.Partial || len(partial.UnavailableUnits) != 1 || partial.UnavailableUnits[0] != "shard-2" || partial.NextCursor != nil || len(partial.Items) != 40-placement["shard-2"] {
		t.Fatalf("partial response: %+v err=%v", partial, err)
	}
	_, found, err := repository.Get(ctx, "00000000-0000-4000-8000-000000000000")
	if found || err == nil {
		t.Fatal("an incomplete lookup must not claim a definitive absence")
	}
	if err := repository.Ping(ctx); err != nil {
		t.Fatal("a partial router must stay ready")
	}
}
