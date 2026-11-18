package sharded

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
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

// openDisposablePVMBGHarness provides the shared disposable-DB harness (Open,
// shard config, clean()) both tests in this file need. Skips the test if
// AAT_FOUNDATION_DSN_FILE is unset.
func openDisposablePVMBGHarness(t *testing.T) (dsns []string, repository *Store, clean func()) {
	t.Helper()
	filename := os.Getenv("AAT_FOUNDATION_DSN_FILE")
	if filename == "" {
		t.Skip("run scripts/m2/check-foundation.py")
	}
	raw, err := os.ReadFile(filename)
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
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	repository, err = Open(ctx, file, Options{ReadConns: 2, BMKGConns: 2, PVMBGConns: 2, RelayConns: 2, ReadTimeout: time.Second, WriteTimeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	clean = func() {
		for _, dsn := range dsns {
			db, err := sql.Open("pgx", dsn)
			if err != nil {
				t.Error(err)
				continue
			}
			_, err = db.ExecContext(context.Background(), `TRUNCATE ingest_records,ingest_snapshot_heads,ingest_sources,ingestion_fences,outbox,hazard_versions,hazard_events,processed_inputs,canonical_progress,satellite_detections,satellite_fires,satellite_watermarks,satellite_rejects,satellite_counters CASCADE`)
			db.Close()
			if err != nil {
				t.Error(err)
			}
		}
	}
	return dsns, repository, clean
}

// TestPVMBGIncompatibleModesReplayAfterAdaptation exercises P3.4a end-to-end:
// park -> explicit adaptation registry -> replay -> no duplicate, with the
// per-source_ref_id order preserved. The three payload pairs below are
// copied verbatim from services/pvmbg-mock/internal/mock's RenderedSnapshot()
// output (versions 2/3/4/5) for three of its embedded fixture reports, with
// only the report_id substituted; services/aggregator cannot import
// services/pvmbg-mock/internal/mock (Go's internal/ visibility rule), so the
// mock's own schema-version tests (TestSchemaVersion{3,4,5}...) are the
// authority that these literals match real mock output.
func TestPVMBGIncompatibleModesReplayAfterAdaptation(t *testing.T) {
	dsns, repository, clean := openDisposablePVMBGHarness(t)
	defer repository.Close()
	clean()
	defer clean()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	session, owned, err := sourcejournal.Acquire(ctx, dsns[1], contracts.SourcePVMBG, 30*time.Second)
	if err != nil || !owned {
		t.Fatalf("ownership: %v %v", owned, err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	if err := repository.installFences(ctx, contracts.SourcePVMBG, session.Generation()); err != nil {
		t.Fatal(err)
	}
	workCtx := store.WithFence(ctx, contracts.SourcePVMBG, session.Generation(), session.Heartbeat)

	strictProcess := pipeline.ValidateProcessor(pipeline.DefaultProcessor(mapper.New(mapper.DefaultVolcanoLocations())), 64<<10)

	cursorSeq := 0
	nextCursor := func() string { cursorSeq++; return fmt.Sprintf("poll-%d", cursorSeq) }
	appendSnapshot := func(key, ref string, payload []byte) {
		batch := contracts.Batch{
			From:   session.Cursor(),
			Cursor: nextCursor(),
			Records: []contracts.RawRecord{{
				Key: key, Kind: "volcanic_report", Payload: payload, ReceivedAt: time.Now().UTC(),
				CorrelationID: "pvmbg-schema-test", OrderingKey: "volcanic_report:" + ref, Snapshot: true,
			}},
		}
		if err := session.Append(ctx, batch); err != nil {
			t.Fatalf("append %s: %v", key, err)
		}
	}
	next := func() *sourcejournal.Message {
		m, err := session.Next(ctx)
		if err != nil || m == nil {
			t.Fatalf("next: %v %v", m, err)
		}
		return m
	}
	apply := func(process pipeline.Processor, m *sourcejournal.Message) error {
		plan, err := session.Prepare(workCtx, *m, process)
		if err != nil {
			return err
		}
		return repository.ApplyInput(workCtx, m.Key, m.Source, plan.Sequence, plan.Mutations, m.CorrelationID, time.Now().UTC())
	}
	revisionOf := func(reportID string) int {
		hazardID, found, err := repository.FindHazardID(ctx, contracts.SourcePVMBG, reportID)
		if err != nil || !found {
			t.Fatalf("hazard id for %s: found=%v err=%v", reportID, found, err)
		}
		unitName, err := partition.Unit(contracts.SourcePVMBG, reportID)
		if err != nil {
			t.Fatal(err)
		}
		var revision int
		if err := repository.lookup(unitName).readDB.QueryRowContext(ctx, `SELECT revision FROM hazard_events WHERE hazard_id=$1`, hazardID).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		return revision
	}
	hazardIDOf := func(reportID string) string {
		id, found, err := repository.FindHazardID(ctx, contracts.SourcePVMBG, reportID)
		if err != nil || !found {
			t.Fatalf("hazard id for %s: found=%v err=%v", reportID, found, err)
		}
		return id
	}

	// scenario runs one (v2 -> incompatible mode -> v2 again) cycle for one
	// source_ref_id and asserts P3.4a's replay-without-duplicate guarantee.
	scenario := func(t *testing.T, reportID string, v2Payload, incompatiblePayload []byte, wantParkReason string, adapted mapper.VolcanicAdaptations) {
		adaptedProcess := pipeline.ValidateProcessor(pipeline.DefaultProcessor(mapper.New(mapper.DefaultVolcanoLocations()).WithVolcanicAdaptations(adapted)), 64<<10)

		appendSnapshot("v2-"+reportID, reportID, v2Payload)
		first := next()
		if first.OrderingKey != "volcanic_report:"+reportID {
			t.Fatalf("unexpected intake order: %+v", first)
		}
		if err := apply(strictProcess, first); err != nil {
			t.Fatalf("baseline v2 must process: %v", err)
		}
		if err := session.Finish(ctx, first.Key, "", false, 0); err != nil {
			t.Fatal(err)
		}
		baselineHazardID := hazardIDOf(reportID)
		baselineRevision := revisionOf(reportID)
		if baselineRevision < 1 {
			t.Fatalf("baseline revision=%d, want >= 1", baselineRevision)
		}

		appendSnapshot("incompatible-"+reportID, reportID, incompatiblePayload)
		incompatible := next()
		parkErr := apply(strictProcess, incompatible)
		if parkErr == nil || !strings.Contains(parkErr.Error(), wantParkReason) {
			t.Fatalf("incompatible payload must park with %q, got %v", wantParkReason, parkErr)
		}
		if err := session.Finish(ctx, incompatible.Key, parkErr.Error(), false, 0); err != nil {
			t.Fatal(err)
		}

		appendSnapshot("v2-again-"+reportID, reportID, v2Payload)
		if m, err := session.Next(ctx); err != nil || m != nil {
			t.Fatalf("v2-again must stay blocked behind the parked predecessor: %+v %v", m, err)
		}
		id := messageID(contracts.SourcePVMBG, incompatible.Key)
		detail, found, err := repository.InspectFailed(ctx, id)
		if err != nil || !found || detail.Retryable {
			t.Fatalf("parked detail must be inspectable and non-retryable: %+v %v", detail, err)
		}
		if !strings.Contains(detail.Reason, wantParkReason) {
			t.Fatalf("stored reason=%q, want substring %q", detail.Reason, wantParkReason)
		}
		// Not yet duplicated: still exactly the baseline hazard_id/revision.
		if hazardIDOf(reportID) != baselineHazardID || revisionOf(reportID) != baselineRevision {
			t.Fatal("a parked input must not have altered canonical state")
		}

		// Adaptation activated: replay the parked message and let the now-
		// adaptation-aware processor accept it, unblocking v2-again.
		replayResult, found, err := repository.ReplayFailed(ctx, id)
		if err != nil || !found || replayResult.Status != "queued" {
			t.Fatalf("replay: %+v %v", replayResult, err)
		}
		replayed := next()
		if replayed.Key != incompatible.Key {
			t.Fatalf("replay did not restore order: %+v", replayed)
		}
		if err := apply(adaptedProcess, replayed); err != nil {
			t.Fatalf("adapted processor must accept the previously-parked payload: %v", err)
		}
		if err := session.Finish(ctx, replayed.Key, "", false, 0); err != nil {
			t.Fatal(err)
		}
		// level_alias/eruption_count_string adapt to content identical to the
		// baseline, so the canonical store's unchanged-content no-op (store.go
		// upsertEvent: oldHash==hash / jsonb-equivalent) is expected to leave
		// the revision exactly where it was; ash_height_optional's content
		// differs (the attribute is now absent), so that one does bump. Both
		// are "revision never regresses, no duplicate hazard_events row" —
		// never a hardcoded post-replay revision count.
		postReplayRevision := revisionOf(reportID)
		if hazardIDOf(reportID) != baselineHazardID || postReplayRevision < baselineRevision {
			t.Fatalf("replay must reuse the same hazard_id without regressing revision: hazard_id_same=%v revision=%d->%d", hazardIDOf(reportID) == baselineHazardID, baselineRevision, postReplayRevision)
		}

		again := next()
		if again.OrderingKey != "volcanic_report:"+reportID || again.Key == first.Key || again.Key == incompatible.Key {
			t.Fatalf("unblocked successor mismatch: %+v", again)
		}
		if err := apply(adaptedProcess, again); err != nil {
			t.Fatalf("v2-again must process: %v", err)
		}
		if err := session.Finish(ctx, again.Key, "", false, 0); err != nil {
			t.Fatal(err)
		}
		finalRevision := revisionOf(reportID)
		if hazardIDOf(reportID) != baselineHazardID || finalRevision < postReplayRevision {
			t.Fatalf("final state must reuse the same hazard_id without regressing revision: hazard_id_same=%v revision=%d->%d", hazardIDOf(reportID) == baselineHazardID, postReplayRevision, finalRevision)
		}
		// v2-again's content is byte-identical to the original v2 payload, so
		// the final canonical event must carry the full v2 attribute set
		// (e.g. ash_column_height_m present again even for the mode5 case,
		// which omitted it only on the intermediate adapted revision).
		final, found, err := repository.Get(ctx, baselineHazardID)
		if err != nil || !found {
			t.Fatalf("final event lookup: found=%v err=%v", found, err)
		}
		if _, ok := final.Attributes["ash_column_height_m"]; !ok {
			t.Fatalf("final state must match the full v2 payload: %+v", final.Attributes)
		}

		empty, err := repository.AuditUnprocessed(ctx, audit.Query{Source: contracts.SourcePVMBG, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		if len(empty.Items) != 0 {
			t.Fatalf("%s left unprocessed after replay: %+v", reportID, empty.Items)
		}
	}

	reportIDs := []string{"pvmbg-mode3-report", "pvmbg-mode4-report", "pvmbg-mode5-report"}

	t.Run("mode3_rename_alert_level", func(t *testing.T) {
		scenario(t, reportIDs[0],
			[]byte(`{"report_id":"pvmbg-mode3-report","volcano_id":"sim-tamriel-red-mountain","alert_level":"Normal","eruption_count_24h":1,"ash_column_height_m":125,"reported_at":"2026-11-17T23:03:00Z","confidence_level":0.58}`),
			[]byte(`{"ash_column_height_m":125,"confidence_level":0.58,"eruption_count_24h":1,"level":"Normal","report_id":"pvmbg-mode3-report","reported_at":"2026-11-17T23:03:00Z","volcano_id":"sim-tamriel-red-mountain"}`),
			"field wajib 'alert_level' hilang",
			mapper.VolcanicAdaptations{LevelAlias: true},
		)
	})

	t.Run("mode4_eruption_count_string", func(t *testing.T) {
		scenario(t, reportIDs[1],
			[]byte(`{"report_id":"pvmbg-mode4-report","volcano_id":"sim-tamriel-ash-peak","alert_level":"Waspada","eruption_count_24h":2,"ash_column_height_m":250,"reported_at":"2026-11-17T23:06:00Z","confidence_level":0.66}`),
			[]byte(`{"alert_level":"Waspada","ash_column_height_m":250,"confidence_level":0.66,"eruption_count_24h":"2","report_id":"pvmbg-mode4-report","reported_at":"2026-11-17T23:06:00Z","volcano_id":"sim-tamriel-ash-peak"}`),
			"field 'eruption_count_24h' berubah dari number menjadi string",
			mapper.VolcanicAdaptations{EruptionCountString: true},
		)
	})

	t.Run("mode5_missing_ash_height", func(t *testing.T) {
		scenario(t, reportIDs[2],
			[]byte(`{"report_id":"pvmbg-mode5-report","volcano_id":"sim-tamriel-north-crag","alert_level":"Siaga","eruption_count_24h":3,"ash_column_height_m":375,"reported_at":"2026-11-17T23:09:00Z","confidence_level":0.74}`),
			[]byte(`{"alert_level":"Siaga","confidence_level":0.74,"eruption_count_24h":3,"report_id":"pvmbg-mode5-report","reported_at":"2026-11-17T23:09:00Z","volcano_id":"sim-tamriel-north-crag"}`),
			"field wajib 'ash_column_height_m' hilang",
			mapper.VolcanicAdaptations{AshHeightOptional: true},
		)
	})

	checksum, err := repository.AuditChecksum(ctx, contracts.SourcePVMBG)
	sorted := append([]string(nil), reportIDs...)
	// sha256 input must be sorted by byte order (Lampiran A); these three IDs
	// already sort as mode3 < mode4 < mode5 lexically.
	expected := sha256.Sum256([]byte(strings.Join(sorted, "\n")))
	if err != nil || checksum.Partial || checksum.Count != 3 || checksum.Checksum != hex.EncodeToString(expected[:]) {
		t.Fatalf("checksum after replay: %+v %v", checksum, err)
	}
}

// TestPVMBGMalformedInputsDoNotBlockOthers is the DB half of Task 1.13
// (P3.3a-d): three independently-ordering-keyed malformed inputs each park
// with their own foundation reason code (never a crash/panic/restart loop),
// a concurrent valid report on a distinct source_ref_id still reaches
// "done", the parked bytes are byte-identical on inspection, and the session
// cursor still advances (Append succeeding below is that proof).
func TestPVMBGMalformedInputsDoNotBlockOthers(t *testing.T) {
	dsns, repository, clean := openDisposablePVMBGHarness(t)
	defer repository.Close()
	clean()
	defer clean()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	session, owned, err := sourcejournal.Acquire(ctx, dsns[1], contracts.SourcePVMBG, 30*time.Second)
	if err != nil || !owned {
		t.Fatalf("ownership: %v %v", owned, err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	if err := repository.installFences(ctx, contracts.SourcePVMBG, session.Generation()); err != nil {
		t.Fatal(err)
	}
	workCtx := store.WithFence(ctx, contracts.SourcePVMBG, session.Generation(), session.Heartbeat)
	process := pipeline.ValidateProcessor(pipeline.DefaultProcessor(mapper.New(mapper.DefaultVolcanoLocations())), 64<<10)

	validPayload := []byte(`{"report_id":"corrupt-matrix-valid","volcano_id":"sim-tamriel-red-mountain","alert_level":"Normal","eruption_count_24h":1,"ash_column_height_m":125,"reported_at":"2026-11-12T02:00:00Z"}`)
	deeplyNested := []byte(strings.Repeat("[", 129) + strings.Repeat("]", 129))
	records := []contracts.RawRecord{
		// (a) The HTTP layer (Task 1.13's TestPVMBGMalformedResponses) wraps
		// an undecodable array response as a single "invalid_X_response"
		// record; DefaultProcessor's switch default then parks it.
		{Key: "corrupt-truncated", Kind: "invalid_volcanic_report_response", Payload: []byte(`[{"report_id":"r1","alert_level":`), OrderingKey: "volcanic_report:corrupt-truncated", Snapshot: true},
		// (b) A successfully-array-split element whose own JSON nests past
		// the 128-level limit.
		{Key: "corrupt-nested", Kind: "volcanic_report", Payload: deeplyNested, OrderingKey: "volcanic_report:corrupt-nested", Snapshot: true},
		// (c) A successfully-array-split element containing invalid UTF-8.
		{Key: "corrupt-utf8", Kind: "volcanic_report", Payload: []byte{0xff}, OrderingKey: "volcanic_report:corrupt-utf8", Snapshot: true},
		// (d) One unrelated, valid report on its own source_ref_id.
		{Key: "corrupt-matrix-valid", Kind: "volcanic_report", Payload: validPayload, OrderingKey: "volcanic_report:corrupt-matrix-valid", Snapshot: true},
	}
	for i := range records {
		records[i].ReceivedAt = time.Now().UTC()
		records[i].CorrelationID = "malformed-matrix-test"
	}
	cursorBefore := session.Cursor()
	if err := session.Append(ctx, contracts.Batch{From: cursorBefore, Cursor: "malformed-matrix-1", Records: records}); err != nil {
		t.Fatalf("append malformed matrix: %v", err)
	}
	if session.Cursor() == cursorBefore || session.Cursor() != "malformed-matrix-1" {
		t.Fatalf("cursor did not advance past the malformed batch: before=%q after=%q", cursorBefore, session.Cursor())
	}

	wantReasons := map[string]string{
		"corrupt-truncated": "invalid_source_response",
		"corrupt-nested":    "json_nesting_too_deep",
		"corrupt-utf8":      "invalid_utf8",
	}
	seenValid := false
	for range records {
		m, err := session.Next(ctx)
		if err != nil {
			t.Fatalf("next: %v", err)
		}
		if m == nil {
			t.Fatal("fewer records surfaced than were appended")
		}
		plan, procErr := session.Prepare(workCtx, *m, process)
		switch m.OrderingKey {
		case "volcanic_report:corrupt-matrix-valid":
			if procErr != nil {
				t.Fatalf("valid report must not be blocked by unrelated malformed inputs: %v", procErr)
			}
			if err := repository.ApplyInput(workCtx, m.Key, m.Source, plan.Sequence, plan.Mutations, m.CorrelationID, time.Now().UTC()); err != nil {
				t.Fatalf("apply valid report: %v", err)
			}
			if err := session.Finish(ctx, m.Key, "", false, 0); err != nil {
				t.Fatal(err)
			}
			seenValid = true
		default:
			ref, ok := strings.CutPrefix(m.OrderingKey, "volcanic_report:")
			if !ok {
				t.Fatalf("unexpected ordering key: %q", m.OrderingKey)
			}
			wantReason, known := wantReasons[ref]
			if !known {
				t.Fatalf("unexpected record in malformed matrix: %+v", m)
			}
			if procErr == nil || !strings.Contains(procErr.Error(), wantReason) {
				t.Fatalf("%s: got %v, want reason containing %q", ref, procErr, wantReason)
			}
			if err := session.Finish(ctx, m.Key, procErr.Error(), false, 0); err != nil {
				t.Fatal(err)
			}
			id := messageID(contracts.SourcePVMBG, m.Key)
			detail, found, dErr := repository.InspectFailed(ctx, id)
			if dErr != nil || !found || detail.Retryable {
				t.Fatalf("%s: parked detail missing or retryable: %+v %v", ref, detail, dErr)
			}
			var original []byte
			for _, r := range records {
				// These records are all Snapshot:true, so Append
				// re-derives the stored record_key via
				// contracts.RawKey(from+ordering_key, digest) --
				// m.Key is never the literal Key the test set above.
				// OrderingKey, by contrast, is stored and returned
				// verbatim (session.go's Append), so match on that.
				if r.OrderingKey == m.OrderingKey {
					original = r.Payload
				}
			}
			if !bytes.Equal(detail.Payload, original) {
				t.Fatalf("%s: inspected bytes changed: got %q want %q", ref, detail.Payload, original)
			}
			if !strings.Contains(detail.Reason, wantReason) {
				t.Fatalf("%s: stored reason=%q, want substring %q", ref, detail.Reason, wantReason)
			}
		}
	}
	if !seenValid {
		t.Fatal("the valid report never surfaced from the journal")
	}
	if m, err := session.Next(ctx); err != nil || m != nil {
		t.Fatalf("journal must be drained: %+v %v", m, err)
	}

	unprocessed, err := repository.AuditUnprocessed(ctx, audit.Query{Source: contracts.SourcePVMBG, Limit: 10})
	if err != nil || len(unprocessed.Items) != 3 {
		t.Fatalf("exactly the three malformed inputs must remain parked: %+v %v", unprocessed, err)
	}
	for _, item := range unprocessed.Items {
		if item.Status != "parked" || item.Retryable {
			t.Fatalf("malformed input must be parked, not retryable: %+v", item)
		}
	}
	if _, found, err := repository.FindHazardID(ctx, contracts.SourcePVMBG, "corrupt-matrix-valid"); err != nil || !found {
		t.Fatalf("valid report's HazardEvent is missing: found=%v err=%v", found, err)
	}
}
