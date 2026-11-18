package sharded

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/ryonlunar/tugas-besar-AAT/contracts"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/audit"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/mapper"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/partition"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/pipeline"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/sourcejournal"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/store"
)

func TestDurableProcessingReplayAndAudit(t *testing.T) {
	filename := os.Getenv("AAT_FOUNDATION_DSN_FILE")
	if filename == "" {
		t.Skip("run scripts/m2/check-foundation.py")
	}
	raw, err := os.ReadFile(filename)
	var dsns []string
	if err != nil || json.Unmarshal(raw, &dsns) != nil || len(dsns) != 4 {
		t.Fatal("invalid disposable configuration")
	}
	configs := []Config{}
	for i, dsn := range dsns {
		c, err := pgx.ParseConfig(dsn)
		if err != nil || !strings.HasPrefix(c.Database, "aat_foundation_") || !strings.HasSuffix(c.Database, "_test") {
			t.Fatal("refusing non-disposable database")
		}
		configs = append(configs, Config{fmt.Sprintf("shard-%d", i), dsn})
	}
	file := filepath.Join(t.TempDir(), "shards.json")
	raw, _ = json.Marshal(configs)
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	repository, err := Open(ctx, file, Options{ReadConns: 2, BMKGConns: 2, PVMBGConns: 2, RelayConns: 2, ReadTimeout: time.Second, WriteTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer repository.Close()
	clean := func() {
		for _, dsn := range dsns {
			db, err := sql.Open("pgx", dsn)
			if err != nil {
				t.Error(err)
				continue
			}
			_, err = db.ExecContext(context.Background(), `TRUNCATE ingest_records,ingest_snapshot_heads,ingest_sources,ingestion_fences,outbox,hazard_versions,hazard_events,processed_inputs,canonical_progress CASCADE`)
			db.Close()
			if err != nil {
				t.Error(err)
			}
		}
	}
	clean()
	defer clean()
	acquire := func() (*sourcejournal.Session, context.Context) {
		session, owned, err := sourcejournal.Acquire(ctx, dsns[1], contracts.SourcePVMBG, 30*time.Second)
		if err != nil || !owned {
			t.Fatalf("ownership: %v %v", owned, err)
		}
		if err := repository.installFences(ctx, contracts.SourcePVMBG, session.Generation()); err != nil {
			t.Fatal(err)
		}
		return session, store.WithFence(ctx, contracts.SourcePVMBG, session.Generation(), session.Heartbeat)
	}
	if _, err := repository.lookup("shard-1").readDB.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS foundation_processor_state (id integer PRIMARY KEY, changes integer NOT NULL);INSERT INTO foundation_processor_state VALUES(1,0) ON CONFLICT(id) DO UPDATE SET changes=0`); err != nil {
		t.Fatal(err)
	}
	session, workCtx := acquire()
	defer func() { _ = session.Close(context.Background()) }()
	report := func(ref, level string) []byte {
		return []byte(fmt.Sprintf(`{"report_id":%q,"volcano_id":"sim-tamriel-red-mountain","alert_level":%q,"eruption_count_24h":2,"ash_column_height_m":500,"reported_at":"2026-11-12T02:00:00Z"}`, ref, level))
	}
	now := time.Now().UTC()
	record := func(key, ref string, payload []byte) contracts.RawRecord {
		return contracts.RawRecord{Key: key, Kind: "volcanic_report", OrderingKey: ref, Payload: payload, ReceivedAt: now, CorrelationID: "pipeline-test"}
	}
	bad := bytes.Replace(report("ordered-a", "Normal"), []byte(`"alert_level"`), []byte(`"renamed_alert_level"`), 1)
	batch := contracts.Batch{From: "", Cursor: "one", Records: []contracts.RawRecord{record("a1", "ordered-a", bad), record("a2", "ordered-a", report("ordered-a", "Awas")), record("b1", "independent-b", report("independent-b", "Normal"))}}
	if err := session.Append(ctx, batch); err != nil {
		t.Fatal(err)
	}
	next := func() *sourcejournal.Message {
		m, err := session.Next(ctx)
		if err != nil || m == nil {
			t.Fatalf("next: %v %v", m, err)
		}
		return m
	}
	rawProcess := pipeline.DefaultProcessor(mapper.New(mapper.DefaultVolcanoLocations()))
	process := pipeline.ValidateProcessor(rawProcess, 64<<10)
	first := next()
	if first.Key != "a1" {
		t.Fatal("intake order changed")
	}
	if _, err := session.Prepare(workCtx, *first, func(ctx context.Context, tx pgx.Tx, m sourcejournal.Message) ([]contracts.Mutation, error) {
		if _, err := tx.Exec(ctx, `UPDATE foundation_processor_state SET changes=changes+1`); err != nil {
			return nil, err
		}
		return process(ctx, tx, m)
	}); err == nil {
		t.Fatal("incompatible schema accepted")
	}
	if err := session.Finish(ctx, first.Key, "schema_incompatible", false, 0); err != nil {
		t.Fatal(err)
	}
	independent := next()
	if independent.Key != "b1" {
		t.Fatal("parked identity blocked unrelated input or allowed a later version")
	}
	apply := func(m *sourcejournal.Message, repair bool) {
		plan, err := session.Prepare(workCtx, *m, func(ctx context.Context, tx pgx.Tx, copy sourcejournal.Message) ([]contracts.Mutation, error) {
			if repair {
				if _, err := tx.Exec(ctx, `UPDATE foundation_processor_state SET changes=changes+1`); err != nil {
					return nil, err
				}
				copy.Payload = bytes.Replace(copy.Payload, []byte(`"renamed_alert_level"`), []byte(`"alert_level"`), 1)
			}
			return process(ctx, tx, copy)
		})
		if err != nil {
			t.Fatal(err)
		}
		if err := repository.ApplyInput(workCtx, m.Key, m.Source, plan.Sequence, plan.Mutations, m.CorrelationID, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	apply(independent, false)
	if err := session.Finish(ctx, independent.Key, "", false, 0); err != nil {
		t.Fatal(err)
	}
	if m, err := session.Next(ctx); err != nil || m != nil {
		t.Fatal("later version bypassed parked predecessor")
	}
	failed, err := repository.AuditUnprocessed(ctx, audit.Query{Source: contracts.SourcePVMBG, Limit: 10})
	if err != nil || len(failed.Items) != 2 {
		t.Fatalf("unprocessed: %+v %v", failed, err)
	}
	id := messageID(contracts.SourcePVMBG, "a1")
	detail, found, err := repository.InspectFailed(ctx, id)
	if err != nil || !found || !bytes.Equal(detail.Payload, bad) {
		t.Fatal("failed input bytes changed")
	}
	for range 2 {
		r, found, err := repository.ReplayFailed(ctx, id)
		if err != nil || !found || r.Status != "queued" {
			t.Fatalf("replay: %+v %v", r, err)
		}
	}
	replayed := next()
	if replayed.Key != "a1" {
		t.Fatal("replay did not restore entity order")
	}
	apply(replayed, true)
	// Simulate a crash after effects commit but before the journal acknowledgement.
	if err := session.Close(ctx); err != nil {
		t.Fatal(err)
	}
	session, workCtx = acquire()
	replayed = next()
	if replayed.Key != "a1" {
		t.Fatal("unacknowledged input lost")
	}
	apply(replayed, true)
	if err := session.Finish(ctx, "a1", "", false, 0); err != nil {
		t.Fatal(err)
	}
	later := next()
	if later.Key != "a2" {
		t.Fatal("later entity version not released")
	}
	apply(later, false)
	if err := session.Finish(ctx, "a2", "", false, 0); err != nil {
		t.Fatal(err)
	}
	hazardID, found, err := repository.FindHazardID(ctx, contracts.SourcePVMBG, "ordered-a")
	if err != nil || !found {
		t.Fatal(err)
	}
	unitName, _ := partition.Unit(contracts.SourcePVMBG, "ordered-a")
	revision := func() int {
		var n int
		if err := repository.lookup(unitName).readDB.QueryRowContext(ctx, `SELECT revision FROM hazard_events WHERE hazard_id=$1`, hazardID).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	var changes int
	if err := repository.lookup("shard-1").readDB.QueryRowContext(ctx, `SELECT changes FROM foundation_processor_state WHERE id=1`).Scan(&changes); err != nil || changes != 1 {
		t.Fatalf("processor state was committed on failure or repeated after crash: changes=%d err=%v", changes, err)
	}

	if revision() != 2 {
		t.Fatal("crash retry duplicated a canonical revision")
	}
	retract := []contracts.Mutation{{Kind: contracts.MutationRetract, Source: contracts.SourcePVMBG, SourceRefID: "ordered-a"}}
	if err := repository.ApplyInput(workCtx, "retract-a", contracts.SourcePVMBG, 999, retract, "pipeline-test", now); err != nil {
		t.Fatal(err)
	}
	if _, found, err := repository.Get(ctx, hazardID); err != nil || found {
		t.Fatal("retracted event remains visible")
	}
	var tombstone []byte
	if err := repository.lookup(unitName).readDB.QueryRowContext(ctx, `SELECT envelope FROM outbox WHERE hazard_id=$1 AND revision=3`, hazardID).Scan(&tombstone); err != nil {
		t.Fatal(err)
	}
	if e, err := contracts.ParseEnvelope(tombstone, "hazard.events.v1.PVMBG", hazardID+":3"); err != nil || e.EventType != "hazard.retracted" {
		t.Fatalf("invalid retraction envelope: %v", err)
	}
	restore := record("restore-a", "ordered-a", report("ordered-a", "Awas"))
	restoreMessage := &sourcejournal.Message{RawRecord: restore, Source: contracts.SourcePVMBG}
	mutations, err := rawProcess(workCtx, nil, *restoreMessage)
	if err != nil {
		t.Fatal(err)
	}
	if err := repository.ApplyInput(workCtx, "restore-a", contracts.SourcePVMBG, 1000, mutations, "pipeline-test", now); err != nil {
		t.Fatal(err)
	}
	if revision() != 4 {
		t.Fatal("restoring identical content did not reactivate the original identity")
	}
	if err := repository.ApplyInput(workCtx, "late-retract", contracts.SourcePVMBG, 999, retract, "pipeline-test", now); err != nil {
		t.Fatal(err)
	}
	if revision() != 4 {
		t.Fatal("an older committed plan overwrote newer canonical state")
	}

	checksum, err := repository.AuditChecksum(ctx, contracts.SourcePVMBG)
	expected := sha256.Sum256([]byte("independent-b\nordered-a"))
	if err != nil || checksum.Partial || checksum.Count != 2 || checksum.Checksum != hex.EncodeToString(expected[:]) {
		t.Fatalf("checksum: %+v %v", checksum, err)
	}
	seen := map[string]bool{}
	cursor := ""
	for {
		page, err := repository.AuditEvents(ctx, audit.Query{Source: contracts.SourcePVMBG, Limit: 1, Cursor: cursor})
		if err != nil || page.Partial {
			t.Fatal(err)
		}
		for _, e := range page.Items {
			if seen[e.HazardID] {
				t.Fatal("audit pagination repeated an event")
			}
			seen[e.HazardID] = true
		}
		if page.NextCursor == nil {
			break
		}
		cursor = *page.NextCursor
	}
	firstPage, err := repository.AuditEvents(ctx, audit.Query{Source: contracts.SourcePVMBG, Limit: 10})
	if err != nil {
		t.Fatal(err)
	}
	firstAt, err := time.Parse(time.RFC3339Nano, firstPage.Items[0].IngestedAt)
	if err != nil {
		t.Fatal(err)
	}
	filtered, err := repository.AuditEvents(ctx, audit.Query{Source: contracts.SourcePVMBG, Since: firstAt.Add(time.Nanosecond), Limit: 10})
	if err != nil || len(filtered.Items) != 1 {
		t.Fatalf("since must compare persisted timestamps without rounding downward: items=%d err=%v", len(filtered.Items), err)
	}
	forged := store.WithFence(ctx, contracts.SourcePVMBG, session.Generation()-1, func(context.Context) error { return nil })
	if err := repository.ApplyInput(forged, "stale-input", contracts.SourcePVMBG, 2000, retract, "pipeline-test", now); !errors.Is(err, store.ErrFenced) {
		t.Fatalf("stale generation bypassed input fencing: %v", err)
	}

	if len(seen) != 2 {
		t.Fatal("audit pagination omitted an event")
	}
	empty, err := repository.AuditUnprocessed(ctx, audit.Query{Source: contracts.SourcePVMBG, Limit: 10})
	if err != nil || len(empty.Items) != 0 {
		t.Fatal("recovered input remains unprocessed")
	}
	// Snapshot deduplication must allow a later return to an earlier value.
	for i, value := range []string{"one", "one", "two", "one"} {
		r := record("same-"+value, "snap", []byte(value))
		r.Snapshot = true
		from := "snap-" + fmt.Sprint(i)
		if i == 0 {
			from = "one"
		}
		if err := session.Append(ctx, contracts.Batch{From: from, Cursor: "snap-" + fmt.Sprint(i+1), Records: []contracts.RawRecord{r}}); err != nil {
			t.Fatal(err)
		}
	}
	var snapshots int
	if err := repository.lookup("shard-1").readDB.QueryRowContext(ctx, `SELECT count(*) FROM ingest_records WHERE ordering_key='snap'`).Scan(&snapshots); err != nil || snapshots != 3 {
		t.Fatalf("snapshot versions=%d err=%v", snapshots, err)
	}

	// A plan spanning units must survive a crash/failure after only its first
	// unit committed, without creating another version when that unit is retried.
	multi := []contracts.Mutation{}
	for _, target := range []string{"shard-0", "shard-3"} {
		for n := 0; ; n++ {
			ref := fmt.Sprintf("multi-%s-%d", target, n)
			name, _ := partition.Unit(contracts.SourcePVMBG, ref)
			if name != target {
				continue
			}
			m := sourcejournal.Message{RawRecord: record("multi", ref, report(ref, "Normal")), Source: contracts.SourcePVMBG}
			mutations, err := rawProcess(workCtx, nil, m)
			if err != nil {
				t.Fatal(err)
			}
			multi = append(multi, mutations...)
			break
		}
	}
	locker, err := sql.Open("pgx", dsns[3])
	if err != nil {
		t.Fatal(err)
	}
	defer locker.Close()
	blocked, err := locker.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer blocked.Rollback()
	if _, err := blocked.ExecContext(ctx, `LOCK TABLE canonical_progress IN ACCESS EXCLUSIVE MODE`); err != nil {
		t.Fatal(err)
	}
	if err := repository.ApplyInput(workCtx, "multi-unit", contracts.SourcePVMBG, 1001, multi, "pipeline-test", now); err == nil {
		t.Fatal("expected the second unit to block")
	}
	if err := blocked.Rollback(); err != nil {
		t.Fatal(err)
	}
	if err := repository.ApplyInput(workCtx, "multi-unit", contracts.SourcePVMBG, 1001, multi, "pipeline-test", now); err != nil {
		t.Fatal(err)
	}
	for _, m := range multi {
		name, _ := partition.Unit(m.Source, m.SourceRefID)
		var revision int
		if err := repository.lookup(name).readDB.QueryRowContext(ctx, `SELECT revision FROM hazard_events WHERE source=$1 AND source_ref_id=$2`, m.Source, m.SourceRefID).Scan(&revision); err != nil || revision != 1 {
			t.Fatalf("partial-commit retry duplicated effects: revision=%d err=%v", revision, err)
		}
	}

	from := time.Date(2026, 11, 12, 0, 0, 0, 0, time.UTC)
	to := from.Add(24 * time.Hour)
	analytics := repository.Analytics(ctx, from, to, "1 hour")
	if analytics.Partial || analytics.BySeverity["AWAS"] != 1 || analytics.BySeverity["NORMAL"] != 3 || analytics.ByArea["Gunung Simulasi Red Mountain (Tamriel)"] != 4 || len(analytics.Series) != 1 || analytics.Series[0].Count != 4 {
		t.Fatalf("unexpected cross-unit analytics: %+v", analytics)
	}
	for _, db := range repository.pools[8:12] {
		_ = db.Close()
	}
	partialCounts := repository.Analytics(ctx, from, to, "1 hour")
	if !partialCounts.Partial || len(partialCounts.UnavailableUnits) != 1 || partialCounts.UnavailableUnits[0] != "shard-2" || len(partialCounts.Series) == 0 {
		t.Fatalf("analytics did not identify the unavailable unit: %+v", partialCounts)
	}
	partial, err := repository.AuditEvents(ctx, audit.Query{Limit: 1})
	if err != nil || !partial.Partial || partial.NextCursor != nil || len(partial.UnavailableUnits) != 1 {
		t.Fatalf("partial audit: %+v %v", partial, err)
	}
}
