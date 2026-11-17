// Package foundationtest exercises the checkpoint 2 interfaces against disposable
// PostgreSQL databases. It does not claim multi-node HA or VM-freeze coverage.
package foundationtest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/ryonlunar/tugas-besar-AAT/contracts"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/partition"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/sourcejournal"
)

func databases(t *testing.T) []string {
	t.Helper()
	path := os.Getenv("AAT_FOUNDATION_DSN_FILE")
	if path == "" {
		t.Skip("run python3 scripts/m2/check-foundation.py inside the development VM")
	}
	raw, err := os.ReadFile(path)
	var dsns []string
	if err != nil || json.Unmarshal(raw, &dsns) != nil || len(dsns) != partition.UnitCount {
		t.Fatal("invalid private foundation DSN file")
	}
	for _, dsn := range dsns {
		config, err := pgx.ParseConfig(dsn)
		if err != nil || !strings.HasPrefix(config.Database, "aat_cp2_") || !strings.HasSuffix(config.Database, "_test") {
			t.Fatal("refusing non-disposable database")
		}
	}
	return dsns
}

func connection(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close(context.Background()) })
	return conn
}

func journal(t *testing.T) (string, *pgx.Conn) {
	t.Helper()
	dsn := databases(t)[0]
	conn := connection(t, dsn)
	if err := sourcejournal.Install(context.Background(), conn); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Exec(context.Background(), `TRUNCATE ingest_records,ingest_sources`); err != nil {
		t.Fatal(err)
	}
	return dsn, conn
}

func acquire(t *testing.T, dsn string, source contracts.Source, timeout time.Duration) *sourcejournal.Session {
	t.Helper()
	session, owned, err := sourcejournal.Acquire(context.Background(), dsn, source, timeout)
	if err != nil || !owned {
		t.Fatalf("acquire: owned=%v err=%v", owned, err)
	}
	t.Cleanup(func() { _ = session.Close(context.Background()) })
	return session
}

func record(key string, payload []byte) contracts.RawRecord {
	return contracts.RawRecord{Key: key, Kind: "observation", Payload: payload, ReceivedAt: time.Now().UTC(), CorrelationID: "checkpoint-2"}
}

func TestSourceOwnershipExcludesOtherReplicas(t *testing.T) {
	dsn, _ := journal(t)
	_ = acquire(t, dsn, contracts.SourceBMKG, 10*time.Second)
	for i := 0; i < 2; i++ {
		session, owned, err := sourcejournal.Acquire(context.Background(), dsn, contracts.SourceBMKG, 10*time.Second)
		if session != nil || owned || err != nil {
			t.Fatalf("second owner admitted: owned=%v err=%v", owned, err)
		}
	}
	// A slow source does not prevent another source from acquiring its own session.
	_ = acquire(t, dsn, contracts.SourcePVMBG, 10*time.Second)
}

func TestExpiredOwnerCannotAppendAfterTakeover(t *testing.T) {
	dsn, _ := journal(t)
	old := acquire(t, dsn, contracts.SourceCoastal, 200*time.Millisecond)
	var next *sourcejournal.Session
	deadline := time.Now().Add(3 * time.Second)
	for next == nil && time.Now().Before(deadline) {
		candidate, owned, err := sourcejournal.Acquire(context.Background(), dsn, contracts.SourceCoastal, time.Second)
		if err != nil {
			t.Fatal(err)
		}
		if owned {
			next = candidate
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if next == nil {
		t.Fatal("idle owner did not release its server-side session")
	}
	defer next.Close(context.Background())
	batch := contracts.Batch{From: "", Cursor: "1", Records: []contracts.RawRecord{record("obs-1", []byte(`{}`))}}
	if err := old.Append(context.Background(), batch); err == nil {
		t.Fatal("expired owner stored data after takeover")
	}
	if err := next.Append(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
}

func TestRawBytesAndCursorAreAtomicAndReplayIsIdempotent(t *testing.T) {
	dsn, conn := journal(t)
	session := acquire(t, dsn, contracts.SourceSatellite, 10*time.Second)
	bad := []byte{'{', 0xff, 0, '['}
	batch := contracts.Batch{From: "", Cursor: "1", Records: []contracts.RawRecord{record("offset-0", bad)}}
	if err := session.Append(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	batch.From, batch.Cursor = "1", "2"
	if err := session.Append(context.Background(), batch); err != nil {
		t.Fatal(err)
	}
	var saved []byte
	var count int
	if err := conn.QueryRow(context.Background(), `SELECT payload,(SELECT count(*) FROM ingest_records) FROM ingest_records WHERE record_key='offset-0'`).Scan(&saved, &count); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(saved, bad) || count != 1 {
		t.Fatal("raw bytes changed or replay duplicated a record")
	}
	batch = contracts.Batch{From: "2", Cursor: "3", Records: []contracts.RawRecord{record("new", []byte(`{}`)), record("offset-0", []byte(`different`))}}
	if err := session.Append(context.Background(), batch); !errors.Is(err, sourcejournal.ErrRecordConflict) {
		t.Fatalf("conflicting source key accepted: %v", err)
	}
	var cursor string
	if err := conn.QueryRow(context.Background(), `SELECT cursor,(SELECT count(*) FROM ingest_records) FROM ingest_sources WHERE source='SATELIT'`).Scan(&cursor, &count); err != nil {
		t.Fatal(err)
	}
	if cursor != "2" || count != 1 {
		t.Fatal("failed batch advanced the cursor or partially persisted")
	}
	if err := session.Append(context.Background(), contracts.Batch{From: "0", Cursor: "9"}); !errors.Is(err, sourcejournal.ErrCursorConflict) {
		t.Fatal("stale cursor rewound progress")
	}
	if err := session.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	resumed := acquire(t, dsn, contracts.SourceSatellite, time.Second)
	if resumed.Cursor() != "2" {
		t.Fatal("new owner did not resume persisted progress")
	}
}

type waitingAdapter struct{ duration time.Duration }

func TestLostOwnershipCancelsTheSourceAdapter(t *testing.T) {
	dsn, conn := journal(t)
	session := acquire(t, dsn, contracts.SourcePVMBG, 300*time.Millisecond)
	done := make(chan error, 1)
	go func() { done <- session.Run(context.Background(), waitingAdapter{10 * time.Second}) }()
	_, err := conn.Exec(context.Background(), `SELECT pg_terminate_backend(pid) FROM pg_locks WHERE locktype='advisory' AND classid=4032 AND objid=2 AND database=(SELECT oid FROM pg_database WHERE datname=current_database())`)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("adapter continued after ownership loss")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("source adapter was not cancelled promptly")
	}
}

func (a waitingAdapter) Run(ctx context.Context, _ string, _ func(context.Context, contracts.Batch) error) error {
	timer := time.NewTimer(a.duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func TestHeartbeatKeepsOwnershipBetweenSourceBatches(t *testing.T) {
	dsn, _ := journal(t)
	session := acquire(t, dsn, contracts.SourcePVMBG, 300*time.Millisecond)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- session.Run(ctx, waitingAdapter{10 * time.Second}) }()
	// Stay idle for more than two lease intervals while Run is still active.
	time.Sleep(700 * time.Millisecond)
	other, owned, err := sourcejournal.Acquire(context.Background(), dsn, contracts.SourcePVMBG, time.Second)
	if other != nil {
		defer other.Close(context.Background())
	}
	if other != nil || owned || err != nil {
		t.Fatalf("ownership lost while adapter was idle: %v", err)
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("adapter cancellation: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("adapter did not stop after cancellation")
	}
}

func TestFourDatabaseRoutingPaginationAndPartialReads(t *testing.T) {
	dsns := databases(t)
	conns := make([]*pgx.Conn, partition.UnitCount)
	readers := make([]partition.Reader, partition.UnitCount)
	counts := make([]int, partition.UnitCount)
	ctx := context.Background()
	for i, dsn := range dsns {
		conn := connection(t, dsn)
		conns[i] = conn
		if _, err := conn.Exec(ctx, `CREATE TABLE IF NOT EXISTS cp2_events(hazard_id text PRIMARY KEY,source text NOT NULL,occurred_at timestamptz NOT NULL,data jsonb NOT NULL);TRUNCATE cp2_events`); err != nil {
			t.Fatal(err)
		}
		readers[i] = partition.Reader{Unit: fmt.Sprintf("shard-%d", i), Read: func(ctx context.Context, source contracts.Source, after *partition.Position, limit int) ([]contracts.HazardEvent, error) {
			var at any
			id := ""
			if after != nil {
				at = after.OccurredAt
				id = after.HazardID
			}
			rows, err := conn.Query(ctx, `SELECT data FROM cp2_events WHERE ($1='' OR source=$1) AND ($2::timestamptz IS NULL OR occurred_at<$2 OR (occurred_at=$2 AND hazard_id>$3)) ORDER BY occurred_at DESC,hazard_id ASC LIMIT $4`, string(source), at, id, limit)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			events := []contracts.HazardEvent{}
			for rows.Next() {
				var raw []byte
				var event contracts.HazardEvent
				if err := rows.Scan(&raw); err != nil {
					return nil, err
				}
				if err := json.Unmarshal(raw, &event); err != nil {
					return nil, err
				}
				events = append(events, event)
			}
			return events, rows.Err()
		}}
	}
	expected := []string{}
	for i := 0; i < 300; i++ {
		id := fmt.Sprintf("00000000-0000-4000-8000-%012d", i)
		event := contracts.HazardEvent{HazardID: id, Source: contracts.SourceBMKG, SourceRefID: fmt.Sprintf("quake-%d", i), OccurredAt: "2026-11-12T03:00:00Z", AreaName: "same-region"}
		unit, err := partition.Unit(event.Source, event.SourceRefID)
		if err != nil {
			t.Fatal(err)
		}
		index := int(unit[len(unit)-1] - '0')
		counts[index]++
		raw, _ := json.Marshal(event)
		if _, err := conns[index].Exec(ctx, `INSERT INTO cp2_events VALUES($1,$2,$3,$4)`, id, event.Source, event.OccurredAt, raw); err != nil {
			t.Fatal(err)
		}
		expected = append(expected, id)
	}
	t.Logf("300 same-region identities distributed across databases: %v", counts)
	for _, count := range counts {
		if count < 50 || count > 100 {
			t.Fatalf("unbalanced routing: %v", counts)
		}
	}
	got := []string{}
	token := ""
	for {
		page, err := partition.ReadPage(ctx, readers, partition.Query{Cursor: token, Limit: 17}, time.Second)
		if err != nil || page.Partial {
			t.Fatalf("healthy read: partial=%v err=%v", page.Partial, err)
		}
		for _, event := range page.Items {
			got = append(got, event.HazardID)
		}
		if page.NextCursor == nil {
			break
		}
		token = *page.NextCursor
		if len(got) > len(expected) {
			t.Fatal("pagination did not terminate")
		}
	}
	if strings.Join(got, ",") != strings.Join(expected, ",") || !sort.StringsAreSorted(got) {
		t.Fatal("global pagination omitted, repeated, or reordered events")
	}
	if _, err := partition.ReadPage(ctx, readers, partition.Query{Source: contracts.SourcePVMBG, Cursor: token, Limit: 17}, time.Second); err == nil {
		t.Fatal("cursor reused with a changed source filter")
	}
	conns[1].Close(ctx)
	page, err := partition.ReadPage(ctx, readers, partition.Query{Limit: 17}, time.Second)
	if err != nil || !page.Partial || page.NextCursor != nil || len(page.Items) != 17 || len(page.UnavailableUnits) != 1 || page.UnavailableUnits[0] != "shard-1" {
		t.Fatalf("partial read: %+v err=%v", page, err)
	}
}
