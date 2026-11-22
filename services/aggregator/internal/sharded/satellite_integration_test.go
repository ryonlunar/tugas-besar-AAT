package sharded

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/pipeline"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/satellite"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/sourcejournal"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/store"
)

// TestSatelliteRecognizerPersistsThroughRealPostgres is the DB half of
// Fase 4 (Task 4.1-4.2, scoped down from the full Task 4.10 crash matrix):
// it exercises satellite.NewPGState end to end against real PostgreSQL --
// migration 007 applied, a micro-batch appended to the SATELIT journal
// (shard-2), Next -> Prepare(satellite.Processor) -> ApplyInput -> Finish,
// and the resulting HazardEvent found via FindHazardID. memState's tests
// already prove the recognizer's logic; this proves pgState's SQL actually
// round-trips through a real transaction, which go build alone cannot.
func TestSatelliteRecognizerPersistsThroughRealPostgres(t *testing.T) {
	dsns, repository, clean := openDisposablePVMBGHarness(t)
	defer repository.Close()
	clean()
	defer clean()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	session, owned, err := sourcejournal.Acquire(ctx, dsns[2], contracts.SourceSatellite, 30*time.Second)
	if err != nil || !owned {
		t.Fatalf("ownership: %v %v", owned, err)
	}
	defer func() { _ = session.Close(context.Background()) }()
	if err := repository.installFences(ctx, contracts.SourceSatellite, session.Generation()); err != nil {
		t.Fatal(err)
	}
	workCtx := store.WithFence(ctx, contracts.SourceSatellite, session.Generation(), session.Heartbeat)

	raw := satelliteFixtureTwoConnectedDetections()
	record := contracts.RawRecord{
		Key: "offsets-0-1", Kind: "sse_batch", Payload: []byte(raw),
		ReceivedAt: time.Now().UTC(), CorrelationID: "satellite-db-test", OrderingKey: "satellite-feed",
	}
	cursorBefore := session.Cursor()
	if err := session.Append(ctx, contracts.Batch{From: cursorBefore, Cursor: "2", Records: []contracts.RawRecord{record}}); err != nil {
		t.Fatalf("append: %v", err)
	}

	recognizer := satellite.NewRecognizer(12 * time.Hour)
	process := satellite.Processor(recognizer)

	m, err := session.Next(ctx)
	if err != nil || m == nil {
		t.Fatalf("next: %v %+v", err, m)
	}
	plan, procErr := session.Prepare(workCtx, *m, process)
	if procErr != nil {
		t.Fatalf("prepare: %v", procErr)
	}
	if len(plan.Mutations) != 1 || plan.Mutations[0].Kind != contracts.MutationUpsert {
		t.Fatalf("mutations = %+v, want exactly one upsert", plan.Mutations)
	}
	fireID := plan.Mutations[0].SourceRefID
	if plan.Mutations[0].Event.Attributes["detection_count"] != 2 {
		t.Fatalf("detection_count = %v, want 2", plan.Mutations[0].Event.Attributes["detection_count"])
	}

	// Re-Prepare the SAME message before Finish (simulating a crash
	// between Prepare's commit and ApplyInput/Finish, then a retry):
	// sourcejournal.Plan caching must return the identical cached plan
	// without re-invoking the processor, so recognizer state is never
	// double-applied (Task 4.10's correctness argument).
	replan, replanErr := session.Prepare(workCtx, *m, process)
	if replanErr != nil {
		t.Fatalf("re-prepare: %v", replanErr)
	}
	if replan.Sequence != plan.Sequence || len(replan.Mutations) != 1 || replan.Mutations[0].SourceRefID != fireID {
		t.Fatalf("re-prepare must return the identical cached plan: first=%+v second=%+v", plan, replan)
	}

	if err := repository.ApplyInput(workCtx, m.Key, m.Source, replan.Sequence, replan.Mutations, m.CorrelationID, time.Now().UTC()); err != nil {
		t.Fatalf("apply input: %v", err)
	}
	if err := session.Finish(ctx, m.Key, "", false, 0); err != nil {
		t.Fatal(err)
	}

	if _, found, err := repository.FindHazardID(ctx, contracts.SourceSatellite, fireID); err != nil || !found {
		t.Fatalf("fire's HazardEvent must exist after apply: found=%v err=%v", found, err)
	}
}

// satelliteFixtureTwoConnectedDetections builds two detection events
// (~0.89km apart) in the live-observed wire shape (docs/design/satellite-feed.md).
func satelliteFixtureTwoConnectedDetections() string {
	return "id: 0\nevent: detection\ndata: {\"offset\":0,\"detection\":{\"satellite\":\"N20\",\"acq_date\":\"2026-11-12\",\"acq_time\":200,\"confidence\":\"n\",\"frp\":10.2,\"latitude\":-2.5000,\"longitude\":113.8000}}\n\n" +
		"id: 1\nevent: detection\ndata: {\"offset\":1,\"detection\":{\"satellite\":\"N20\",\"acq_date\":\"2026-11-12\",\"acq_time\":205,\"confidence\":\"n\",\"frp\":2.3,\"latitude\":-2.5080,\"longitude\":113.8000}}\n\n"
}

func TestSatelliteRetriesAfterTemporaryStorageTimeout(t *testing.T) {
	dsns, repository, clean := openDisposablePVMBGHarness(t)
	defer repository.Close()
	clean()
	defer clean()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	session, owned, err := sourcejournal.Acquire(ctx, dsns[2], contracts.SourceSatellite, 30*time.Second)
	if err != nil || !owned {
		t.Fatalf("ownership: %v %v", owned, err)
	}
	defer session.Close(ctx)

	record := contracts.RawRecord{
		Key: "offsets-0-1", Kind: "sse_batch", Payload: []byte(satelliteFixtureTwoConnectedDetections()),
		ReceivedAt: time.Now().UTC(), CorrelationID: "satellite-db-retry", OrderingKey: "satellite-feed",
	}
	if err := session.Append(ctx, contracts.Batch{From: session.Cursor(), Cursor: "2", Records: []contracts.RawRecord{record}}); err != nil {
		t.Fatal(err)
	}
	msg, err := session.Next(ctx)
	if err != nil || msg == nil {
		t.Fatalf("claim: %v %v", msg, err)
	}

	lock, err := repository.lookup("shard-2").readDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Rollback()
	if _, err := lock.ExecContext(ctx, "LOCK TABLE satellite_detections IN ACCESS EXCLUSIVE MODE"); err != nil {
		t.Fatal(err)
	}
	process := pipeline.ValidateProcessor(satellite.Processor(satellite.NewRecognizer(12*time.Hour)), 64<<10)
	_, processErr := session.Prepare(ctx, *msg, process)
	if err := lock.Rollback(); err != nil {
		t.Fatal(err)
	}
	var permanent *pipeline.PermanentError
	if processErr == nil || errors.As(processErr, &permanent) {
		t.Fatalf("expected a retryable PostgreSQL timeout, got %v", processErr)
	}
	// A client deadline may close the ownership connection. Exercise the
	// same persisted queue after a fresh session, as the coordinator does.
	if err := session.Close(ctx); err != nil {
		t.Fatal(err)
	}
	resumed, owned, err := sourcejournal.Acquire(ctx, dsns[2], contracts.SourceSatellite, 30*time.Second)
	if err != nil || !owned {
		t.Fatalf("ownership after timeout: %v %v", owned, err)
	}
	defer resumed.Close(ctx)
	retry, err := resumed.Next(ctx)
	if err != nil || retry == nil || retry.Key != msg.Key {
		t.Fatalf("retry claim: %v %v", retry, err)
	}
	plan, err := resumed.Prepare(ctx, *retry, process)
	if err != nil || len(plan.Mutations) != 1 {
		t.Fatalf("prepare after recovery: %v %v", plan, err)
	}
}
