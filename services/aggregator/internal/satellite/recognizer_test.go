package satellite

import (
	"context"
	"testing"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
)

func TestRecognizerMergesAcrossSatellites(t *testing.T) {
	st := newMemState()
	r := NewRecognizer(12 * time.Hour)
	raw := synthEvent(0, "N20", "2026-11-12", "300", -3.0000, 114.0000, "h", 5) +
		synthEvent(1, "N21", "2026-11-12", "302", -3.0036, 114.0000, "h", 5)
	_, err := r.ProcessBatch(context.Background(), st, "offsets-0-1", []byte(raw))
	if err != nil {
		t.Fatalf("ProcessBatch: %v", err)
	}
	if len(st.activeFireIDs()) != 1 {
		t.Fatalf("active fires = %d, want 1 (cross-satellite merge)", len(st.activeFireIDs()))
	}
}

func TestRecognizerTransitiveAcrossBatches(t *testing.T) {
	st := newMemState()
	r := NewRecognizer(12 * time.Hour)
	// A and C, 1.8km apart (not directly connected) in batch 1 -> two
	// singletons, no mutation.
	rawA := synthEvent(0, "N20", "2026-11-12", "400", -2.5000, 113.8000, "n", 1)
	rawC := synthEvent(1, "N20", "2026-11-12", "410", -2.5160, 113.8000, "n", 1)
	mutations, err := r.ProcessBatch(context.Background(), st, "offsets-0-1", []byte(rawA+rawC))
	if err != nil {
		t.Fatalf("batch 1: %v", err)
	}
	if len(mutations) != 0 {
		t.Fatalf("batch 1 mutations = %d, want 0 (both singletons)", len(mutations))
	}
	// B bridges A and C in batch 2 -> one fire with all three.
	rawB := synthEvent(2, "N20", "2026-11-12", "405", -2.5080, 113.8000, "n", 1)
	mutations, err = r.ProcessBatch(context.Background(), st, "offsets-2-2", []byte(rawB))
	if err != nil {
		t.Fatalf("batch 2: %v", err)
	}
	if len(mutations) != 1 || mutations[0].Event.Attributes["detection_count"] != 3 {
		t.Fatalf("batch 2 mutations = %+v, want one upsert with detection_count=3", mutations)
	}
}

func TestRecognizerMergeRetractsLoser(t *testing.T) {
	st := newMemState()
	r := NewRecognizer(12 * time.Hour)
	rawA := synthEvent(0, "N20", "2026-11-12", "100", -2.5000, 113.8000, "n", 1) +
		synthEvent(1, "N20", "2026-11-12", "110", -2.5080, 113.8000, "n", 1)
	rawB := synthEvent(2, "N20", "2026-11-12", "200", -2.5240, 113.8000, "n", 1) +
		synthEvent(3, "N20", "2026-11-12", "210", -2.5320, 113.8000, "n", 1)
	if _, err := r.ProcessBatch(context.Background(), st, "offsets-0-1", []byte(rawA)); err != nil {
		t.Fatalf("A: %v", err)
	}
	if _, err := r.ProcessBatch(context.Background(), st, "offsets-2-3", []byte(rawB)); err != nil {
		t.Fatalf("B: %v", err)
	}
	if len(st.activeFireIDs()) != 2 {
		t.Fatalf("active fires before merge = %d, want 2", len(st.activeFireIDs()))
	}
	// Bridge: ~0.89km from A's -2.5080 member and ~0.89km from B's
	// -2.5240 member; A's and B's own nearest members are ~1.78km apart
	// (not directly connected), so the merge only happens through this
	// single intermediate point.
	bridge := synthEvent(4, "N20", "2026-11-12", "150", -2.5160, 113.8000, "n", 1)
	mutations, err := r.ProcessBatch(context.Background(), st, "offsets-4-4", []byte(bridge))
	if err != nil {
		t.Fatalf("merge: %v", err)
	}
	var upserts, retracts int
	for _, m := range mutations {
		switch m.Kind {
		case contracts.MutationUpsert:
			upserts++
		case contracts.MutationRetract:
			retracts++
		}
	}
	if upserts != 1 || retracts != 1 {
		t.Fatalf("merge mutations = %+v, want exactly 1 upsert (survivor) + 1 retract (loser)", mutations)
	}
	if len(st.activeFireIDs()) != 1 {
		t.Fatalf("active fires after merge = %d, want 1", len(st.activeFireIDs()))
	}
}

func TestRecognizerUpdateChangesFRPKeepsID(t *testing.T) {
	st := newMemState()
	r := NewRecognizer(12 * time.Hour)
	raw := synthEvent(0, "N20", "2026-11-12", "200", -2.5000, 113.8000, "n", 10) +
		synthEvent(1, "N20", "2026-11-12", "205", -2.5080, 113.8000, "n", 10)
	mutations, err := r.ProcessBatch(context.Background(), st, "offsets-0-1", []byte(raw))
	if err != nil || len(mutations) != 1 {
		t.Fatalf("setup: mutations=%+v err=%v", mutations, err)
	}
	fireID := mutations[0].SourceRefID

	key := Detection{Satellite: "N20", AcqDate: "2026-11-12", AcqTimeText: "200", LatText: "-2.5", LonText: "113.8"}.Key()
	update := synthUpdate(2, key, `{"frp":95.0}`)
	mutations, err = r.ProcessBatch(context.Background(), st, "offsets-2-2", []byte(update))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if len(mutations) != 1 || mutations[0].SourceRefID != fireID {
		t.Fatalf("update mutations = %+v, want one upsert keeping fire ID %s", mutations, fireID)
	}
	if mutations[0].Event.Severity != contracts.SeverityAwas {
		t.Fatalf("severity = %v, want AWAS (total FRP now >=100)", mutations[0].Event.Severity)
	}
}

func TestRecognizerExcludesLowConfidence(t *testing.T) {
	st := newMemState()
	r := NewRecognizer(12 * time.Hour)
	raw := synthEvent(0, "N20", "2026-11-12", "200", -2.5000, 113.8000, "n", 1) +
		synthEvent(1, "N20", "2026-11-12", "205", -2.5080, 113.8000, "l", 1)
	mutations, err := r.ProcessBatch(context.Background(), st, "offsets-0-1", []byte(raw))
	if err != nil {
		t.Fatalf("ProcessBatch: %v", err)
	}
	if len(mutations) != 0 {
		t.Fatalf("mutations = %+v, want none (low confidence excluded, only 1 real member)", mutations)
	}
	if len(st.activeFireIDs()) != 0 {
		t.Fatalf("active fires = %d, want 0", len(st.activeFireIDs()))
	}
}

func TestRecognizerDeduplicatesDetections(t *testing.T) {
	st := newMemState()
	r := NewRecognizer(12 * time.Hour)
	raw := synthEvent(0, "N20", "2026-11-12", "200", -2.5000, 113.8000, "n", 10) +
		synthEvent(1, "N20", "2026-11-12", "205", -2.5080, 113.8000, "n", 10)
	if _, err := r.ProcessBatch(context.Background(), st, "offsets-0-1", []byte(raw)); err != nil {
		t.Fatalf("setup: %v", err)
	}
	// Byte-identical repeat of offset 0's detection, at a later offset.
	repeat := synthEvent(2, "N20", "2026-11-12", "200", -2.5000, 113.8000, "n", 10)
	mutations, err := r.ProcessBatch(context.Background(), st, "offsets-2-2", []byte(repeat))
	if err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if len(mutations) != 0 {
		t.Fatalf("mutations = %+v, want none (byte-identical repeat is a no-op)", mutations)
	}
	if n := st.counter("duplicate_conflicting"); n != 0 {
		t.Fatalf("duplicate_conflicting = %d, want 0 for a byte-identical repeat", n)
	}
}

func TestRecognizerSkipsUnchangedFire(t *testing.T) {
	st := newMemState()
	r := NewRecognizer(12 * time.Hour)
	raw := synthEvent(0, "N20", "2026-11-12", "200", -2.5000, 113.8000, "n", 10) +
		synthEvent(1, "N20", "2026-11-12", "205", -2.5080, 113.8000, "n", 10)
	mutations, err := r.ProcessBatch(context.Background(), st, "offsets-0-1", []byte(raw))
	if err != nil || len(mutations) != 1 {
		t.Fatalf("setup: mutations=%+v err=%v", mutations, err)
	}

	// A batch that repeats offset 0's detection byte-for-byte touches
	// nothing new: no mutation should come out.
	repeat := synthEvent(2, "N20", "2026-11-12", "200", -2.5000, 113.8000, "n", 10)
	mutations, err = r.ProcessBatch(context.Background(), st, "offsets-2-2", []byte(repeat))
	if err != nil {
		t.Fatalf("repeat: %v", err)
	}
	if len(mutations) != 0 {
		t.Fatalf("mutations = %+v, want none (nothing changed)", mutations)
	}
}

// TestRecognizerFireIDStableAcrossReconnect is the end-to-end regression
// for the PR #6 review finding (l0stplains): three mutually-connected
// detections where C is chronologically earliest, processed either all
// in one batch or with A+B first and C arriving in a second batch
// (simulating the adapter's flush-on-disconnect splitting the group
// across a reconnect). Both must converge on the identical final fire
// ID, and the split-arrival case must show the re-anchor transition
// (retract the provisional ID, upsert the final one) rather than
// silently keeping a batch-boundary-dependent ID forever.
func TestRecognizerFireIDStableAcrossReconnect(t *testing.T) {
	rawA := synthEvent(0, "N20", "2026-11-12", "400", -2.5000, 113.8000, "n", 1) // 04:00
	rawB := synthEvent(1, "N20", "2026-11-12", "405", -2.5040, 113.8000, "n", 1) // 04:05
	rawC := synthEvent(2, "N20", "2026-11-12", "350", -2.5020, 113.8000, "n", 1) // 03:50 (earliest)

	st := newMemState()
	r := NewRecognizer(12 * time.Hour)
	if _, err := r.ProcessBatch(context.Background(), st, "offsets-0-2", []byte(rawA+rawB+rawC)); err != nil {
		t.Fatalf("combined batch: %v", err)
	}
	combinedIDs := st.activeFireIDs()
	if len(combinedIDs) != 1 {
		t.Fatalf("active fires (combined) = %d, want 1", len(combinedIDs))
	}
	wantID := combinedIDs[0]

	st2 := newMemState()
	r2 := NewRecognizer(12 * time.Hour)
	if _, err := r2.ProcessBatch(context.Background(), st2, "offsets-0-1", []byte(rawA+rawB)); err != nil {
		t.Fatalf("batch 1 (A+B): %v", err)
	}
	provisionalIDs := st2.activeFireIDs()
	if len(provisionalIDs) != 1 {
		t.Fatalf("active fires after A+B = %d, want 1", len(provisionalIDs))
	}
	provisionalID := provisionalIDs[0]

	mutations, err := r2.ProcessBatch(context.Background(), st2, "offsets-2-2", []byte(rawC))
	if err != nil {
		t.Fatalf("batch 2 (C): %v", err)
	}
	finalIDs := st2.activeFireIDs()
	if len(finalIDs) != 1 {
		t.Fatalf("active fires after A+B+C = %d, want 1", len(finalIDs))
	}
	if finalIDs[0] != wantID {
		t.Fatalf("split-arrival final ID = %s, want %s (same as the combined-batch run)", finalIDs[0], wantID)
	}
	if provisionalID == wantID {
		t.Fatal("test setup invalid: the provisional ID should differ from the final one, or this isn't exercising the reconnect scenario")
	}

	var sawRetractProvisional, sawUpsertFinal bool
	for _, m := range mutations {
		if m.Kind == contracts.MutationRetract && m.SourceRefID == provisionalID {
			sawRetractProvisional = true
		}
		if m.Kind == contracts.MutationUpsert && m.SourceRefID == wantID {
			sawUpsertFinal = true
		}
	}
	if !sawRetractProvisional {
		t.Errorf("mutations = %+v, want a retract for the provisional ID %s", mutations, provisionalID)
	}
	if !sawUpsertFinal {
		t.Errorf("mutations = %+v, want an upsert for the final ID %s", mutations, wantID)
	}
}

// TestRecognizerFireIDStableWhenAnchorIsRetracted is the regression for
// the second round of PR #6 review (l0stplains): three mutually-connected
// detections where C is chronologically earliest, then C itself is
// retracted. Round 1's fix (re-anchor when a later-arriving detection is
// earlier) only moves a fire's recorded anchor FORWARD in time -- it does
// nothing when the earliest member is later retracted, so whether C ever
// got the chance to be durably recorded as the anchor still depended on
// the batch boundary: processing A+B+C+retract(C) in one call never lets
// C exist long enough to be considered (C is excluded from clustering the
// moment it's retracted, before materialize ever runs), while processing
// A+B+C in one batch and retract(C) in a second batch lets C become the
// recorded anchor first, and retraction alone never un-does that. Both
// orderings must converge on the same final ID.
func TestRecognizerFireIDStableWhenAnchorIsRetracted(t *testing.T) {
	rawA := synthEvent(0, "N20", "2026-11-12", "400", -2.5000, 113.8000, "n", 1) // 04:00
	rawB := synthEvent(1, "N20", "2026-11-12", "405", -2.5040, 113.8000, "n", 1) // 04:05
	rawC := synthEvent(2, "N20", "2026-11-12", "350", -2.5020, 113.8000, "n", 1) // 03:50 (earliest)

	// Discover C's own detection_key (needed for the retract event)
	// without depending on any clustering outcome.
	var keyC string
	probe := newMemState()
	if _, err := NewRecognizer(12*time.Hour).ProcessBatch(context.Background(), probe, "probe", []byte(rawC)); err != nil {
		t.Fatalf("probe: %v", err)
	}
	for k := range probe.detections {
		keyC = k
	}
	if keyC == "" {
		t.Fatal("could not determine C's detection_key")
	}
	rawRetractC := synthRetract(3, keyC, "test")

	st1 := newMemState()
	r1 := NewRecognizer(12 * time.Hour)
	if _, err := r1.ProcessBatch(context.Background(), st1, "offsets-0-3", []byte(rawA+rawB+rawC+rawRetractC)); err != nil {
		t.Fatalf("combined batch: %v", err)
	}
	combinedIDs := st1.activeFireIDs()
	if len(combinedIDs) != 1 {
		t.Fatalf("active fires (combined) = %d, want 1", len(combinedIDs))
	}

	st2 := newMemState()
	r2 := NewRecognizer(12 * time.Hour)
	if _, err := r2.ProcessBatch(context.Background(), st2, "offsets-0-2", []byte(rawA+rawB+rawC)); err != nil {
		t.Fatalf("batch1 (A+B+C): %v", err)
	}
	if _, err := r2.ProcessBatch(context.Background(), st2, "offsets-3-3", []byte(rawRetractC)); err != nil {
		t.Fatalf("batch2 (retract C): %v", err)
	}
	splitIDs := st2.activeFireIDs()
	if len(splitIDs) != 1 {
		t.Fatalf("active fires (split) = %d, want 1", len(splitIDs))
	}

	if combinedIDs[0] != splitIDs[0] {
		t.Fatalf("fire ID depends on the batch boundary around the retract: combined=%s split=%s, want identical", combinedIDs[0], splitIDs[0])
	}
}
