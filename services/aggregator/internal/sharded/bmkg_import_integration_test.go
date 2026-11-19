package sharded

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/bmkgimport"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/mapper"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/pipeline"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/sourcejournal"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/store"
)

// TestBMKGCatalogImportResumesWithoutDuplicates is the DB half of Task 7.6
// (P1.1a, D11): it imports a synthetic historical-catalog CSV through
// bmkgimport.Adapter against real PostgreSQL, forces a mid-import failure
// (simulating a crash, Append succeeds but the "process" never drains
// before the pod dies), re-acquires ownership exactly as a restarted pod
// would, and resumes -- proving every row lands exactly once as a BMKG
// HazardEvent keyed by its own event_id, with no duplicates and no gaps,
// through a genuine durable PostgreSQL cursor (not just the in-memory
// adapter test in bmkgimport/adapter_test.go).
func TestBMKGCatalogImportResumesWithoutDuplicates(t *testing.T) {
	dsns, repository, clean := openDisposablePVMBGHarness(t)
	defer repository.Close()
	clean()
	defer clean()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	const totalRows = 30
	const crashAfterRows = 10
	path := writeSyntheticCatalogFixture(t, totalRows)
	process := pipeline.DefaultProcessor(mapper.New(mapper.DefaultVolcanoLocations()))

	// First attempt: append batches via the catalog adapter directly
	// (bypassing RunAdapter's own goroutines, same low-level pattern as
	// satellite_integration_test.go / pvmbg_schema_integration_test.go),
	// stopping after crashAfterRows have been durably committed to the
	// journal -- simulating a pod that dies before finishing the import.
	var survivingCursor string
	func() {
		session, owned, err := sourcejournal.Acquire(ctx, dsns[0], contracts.SourceBMKG, 30*time.Second)
		if err != nil || !owned {
			t.Fatalf("ownership (first attempt): %v %v", owned, err)
		}
		defer func() { _ = session.Close(context.Background()) }()
		if err := repository.installFences(ctx, contracts.SourceBMKG, session.Generation()); err != nil {
			t.Fatal(err)
		}

		rowsCommitted := 0
		adapter := &bmkgimport.Adapter{Path: path, BatchSize: 5}
		err = adapter.Run(ctx, session.Cursor(), func(batchCtx context.Context, batch contracts.Batch) error {
			if rowsCommitted >= crashAfterRows {
				return errors.New("simulated crash before import finished")
			}
			if err := session.Append(batchCtx, batch); err != nil {
				return err
			}
			rowsCommitted += len(batch.Records)
			survivingCursor = batch.Cursor
			return nil
		})
		if err == nil || errors.Is(err, io.EOF) {
			t.Fatalf("first attempt should stop on the simulated crash, got %v", err)
		}
		if rowsCommitted != crashAfterRows {
			t.Fatalf("rowsCommitted = %d, want exactly %d before the simulated crash", rowsCommitted, crashAfterRows)
		}
	}()

	if survivingCursor == "" {
		t.Fatal("the first attempt must have left a durable cursor behind")
	}

	// Second attempt: a fresh session acquisition (exactly what a
	// restarted pod does), resuming the import from the surviving
	// cursor until it finishes.
	func() {
		session, owned, err := sourcejournal.Acquire(ctx, dsns[0], contracts.SourceBMKG, 30*time.Second)
		if err != nil || !owned {
			t.Fatalf("ownership (second attempt): %v %v", owned, err)
		}
		defer func() { _ = session.Close(context.Background()) }()
		if err := repository.installFences(ctx, contracts.SourceBMKG, session.Generation()); err != nil {
			t.Fatal(err)
		}
		if session.Cursor() != survivingCursor {
			t.Fatalf("resumed session cursor = %q, want the surviving cursor %q", session.Cursor(), survivingCursor)
		}

		var resumedKeys []string
		adapter := &bmkgimport.Adapter{Path: path, BatchSize: 5}
		err = adapter.Run(ctx, session.Cursor(), func(batchCtx context.Context, batch contracts.Batch) error {
			if err := session.Append(batchCtx, batch); err != nil {
				return err
			}
			for _, r := range batch.Records {
				resumedKeys = append(resumedKeys, r.Key)
			}
			return nil
		})
		if !errors.Is(err, io.EOF) {
			t.Fatalf("second attempt should finish the import (io.EOF), got %v", err)
		}
		if len(resumedKeys) != totalRows-crashAfterRows {
			t.Fatalf("resumed import delivered %d records, want %d (%d total - %d already committed)", len(resumedKeys), totalRows-crashAfterRows, totalRows, crashAfterRows)
		}

		// Drain the whole journal (both the crash-survived rows and
		// the resumed rows) through the real pipeline -- Next ->
		// Prepare -> ApplyInput -> Finish, exactly like production.
		workCtx := store.WithFence(ctx, contracts.SourceBMKG, session.Generation(), session.Heartbeat)
		for {
			message, err := session.Next(ctx)
			if err != nil {
				t.Fatalf("session.Next: %v", err)
			}
			if message == nil {
				break
			}
			plan, procErr := session.Prepare(workCtx, *message, process)
			if procErr != nil {
				t.Fatalf("prepare %s: %v", message.Key, procErr)
			}
			if err := repository.ApplyInput(workCtx, message.Key, message.Source, plan.Sequence, plan.Mutations, message.CorrelationID, time.Now().UTC()); err != nil {
				t.Fatalf("apply input %s: %v", message.Key, err)
			}
			if err := session.Finish(ctx, message.Key, "", false, 0); err != nil {
				t.Fatalf("finish %s: %v", message.Key, err)
			}
		}
	}()

	for i := 1; i <= totalRows; i++ {
		eventID := fmt.Sprintf("synthetic-eq-%05d", i)
		if _, found, err := repository.FindHazardID(ctx, contracts.SourceBMKG, eventID); err != nil || !found {
			t.Fatalf("event %s must exist exactly once after resume: found=%v err=%v", eventID, found, err)
		}
	}
}

func writeSyntheticCatalogFixture(t *testing.T, rows int) string {
	t.Helper()
	content := "# SYNTHETIC — DB integration test fixture\n" +
		"event_id,occurred_at,latitude,longitude,depth_km,magnitude,region_name,potential_tsunami\n"
	for i := 1; i <= rows; i++ {
		content += fmt.Sprintf("synthetic-eq-%05d,2023-11-14T22:15:37Z,-0.73,100.23,10.6,4.5,Sumatera Barat,false\n", i)
	}
	path := filepath.Join(t.TempDir(), "catalog.csv")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}
