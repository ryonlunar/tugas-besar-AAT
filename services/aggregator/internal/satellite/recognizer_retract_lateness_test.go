package satellite

import (
	"context"
	"testing"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
)

func TestRecognizerRetractShrinkSplitDisappear(t *testing.T) {
	st := newMemState()
	r := NewRecognizer(12 * time.Hour)
	// Chain of 5 along a line, each ~0.89km from its neighbor: A-B-C-D-E.
	lats := []float64{-2.5000, -2.5080, -2.5160, -2.5240, -2.5320}
	acqTimes := []string{"100", "110", "120", "130", "140"}
	var raw string
	for i, lat := range lats {
		raw += synthEvent(i, "N20", "2026-11-12", acqTimes[i], lat, 113.8000, "n", 1)
	}
	mutations, err := r.ProcessBatch(context.Background(), st, "offsets-0-4", []byte(raw))
	if err != nil || len(mutations) != 1 || mutations[0].Event.Attributes["detection_count"] != 5 {
		t.Fatalf("setup: mutations=%+v err=%v", mutations, err)
	}
	fireID := mutations[0].SourceRefID

	keyFor := func(lat float64) string {
		return Detection{Satellite: "N20", AcqDate: "2026-11-12", AcqTimeText: "x", LatText: ftoa(lat), LonText: "113.8"}.Key()
	}
	keyA := Detection{Satellite: "N20", AcqDate: "2026-11-12", AcqTimeText: "100", LatText: "-2.5", LonText: "113.8"}.Key()
	keyC := Detection{Satellite: "N20", AcqDate: "2026-11-12", AcqTimeText: "120", LatText: "-2.516", LonText: "113.8"}.Key()
	keyE := Detection{Satellite: "N20", AcqDate: "2026-11-12", AcqTimeText: "140", LatText: "-2.532", LonText: "113.8"}.Key()
	_ = keyFor

	// Retract E (an endpoint): fire shrinks to 4, same ID, no retract.
	mutations, err = r.ProcessBatch(context.Background(), st, "offsets-5-5", []byte(synthRetract(5, keyE, "false_positive")))
	if err != nil {
		t.Fatalf("retract E: %v", err)
	}
	if len(mutations) != 1 || mutations[0].Kind != contracts.MutationUpsert || mutations[0].SourceRefID != fireID || mutations[0].Event.Attributes["detection_count"] != 4 {
		t.Fatalf("after retract E: mutations=%+v, want 1 upsert id=%s count=4", mutations, fireID)
	}

	// Retract C (the bridge of what remains, A-B-C-D): splits into A-B
	// and D. The anchor (A, the earliest member) must keep the ID.
	mutations, err = r.ProcessBatch(context.Background(), st, "offsets-6-6", []byte(synthRetract(6, keyC, "false_positive")))
	if err != nil {
		t.Fatalf("retract C: %v", err)
	}
	var sawSameID, sawRetract bool
	for _, m := range mutations {
		if m.Kind == contracts.MutationUpsert && m.SourceRefID == fireID {
			sawSameID = true
			if m.Event.Attributes["detection_count"] != 2 {
				t.Errorf("surviving fire detection_count = %v, want 2 (A-B)", m.Event.Attributes["detection_count"])
			}
		}
		if m.Kind == contracts.MutationRetract {
			sawRetract = true
		}
	}
	if !sawSameID {
		t.Fatalf("after split, the anchor's side must keep fire ID %s; mutations=%+v", fireID, mutations)
	}
	_ = sawRetract // D alone is a singleton now, not necessarily its own retract mutation (it never had an id)
	_ = keyA

	// Retract B: A-B pair shrinks to just A, a singleton -> the fire
	// disappears (retract mutation, no new upsert for that ID).
	keyB := Detection{Satellite: "N20", AcqDate: "2026-11-12", AcqTimeText: "110", LatText: "-2.508", LonText: "113.8"}.Key()
	mutations, err = r.ProcessBatch(context.Background(), st, "offsets-7-7", []byte(synthRetract(7, keyB, "false_positive")))
	if err != nil {
		t.Fatalf("retract B: %v", err)
	}
	foundRetract := false
	for _, m := range mutations {
		if m.Kind == contracts.MutationRetract && m.SourceRefID == fireID {
			foundRetract = true
		}
	}
	if !foundRetract {
		t.Fatalf("after retracting down to a singleton, fire %s must be retracted; mutations=%+v", fireID, mutations)
	}

	// Retract of an unknown key is tombstoned and counted, not an error.
	if _, err := r.ProcessBatch(context.Background(), st, "offsets-8-8", []byte(synthRetract(8, "N20|2026-11-12|999|9.0|9.0", ""))); err != nil {
		t.Fatalf("retract unknown: %v", err)
	}
	if n := st.counter("retract_unknown"); n != 1 {
		t.Fatalf("retract_unknown = %d, want 1", n)
	}
}

func TestRecognizerLateness(t *testing.T) {
	st := newMemState()
	r := NewRecognizer(12 * time.Hour)

	// Establish N20's watermark at 2026-11-12T12:00:00Z via a lone
	// detection (a singleton; no mutation expected, but the watermark
	// still advances).
	setWatermark := synthEvent(0, "N20", "2026-11-12", "1200", 10.0000, 100.0000, "n", 1)
	if _, err := r.ProcessBatch(context.Background(), st, "offsets-0-0", []byte(setWatermark)); err != nil {
		t.Fatalf("watermark setup: %v", err)
	}

	// A detection at 02:00 (10 hours behind watermark) is within the 12h
	// allowance: it must be treated as active (not late). Pair it with a
	// nearby detection so a fire actually forms and we can observe it
	// landed as active.
	within1 := synthEvent(1, "N20", "2026-11-12", "200", 11.0000, 100.0000, "n", 1)
	within2 := synthEvent(2, "N20", "2026-11-12", "205", 11.0080, 100.0000, "n", 1)
	mutations, err := r.ProcessBatch(context.Background(), st, "offsets-1-2", []byte(within1+within2))
	if err != nil {
		t.Fatalf("within lateness: %v", err)
	}
	if len(mutations) != 1 || mutations[0].Event.Attributes["detection_count"] != 2 {
		t.Fatalf("within-lateness pair should form a fire: mutations=%+v", mutations)
	}

	// A detection at 23:00 the PREVIOUS day is 13 hours behind the
	// watermark: beyond the 12h bound -> late, excluded, no mutation,
	// counted.
	late1 := synthEvent(3, "N20", "2026-11-11", "2300", 12.0000, 100.0000, "n", 1)
	late2 := synthEvent(4, "N20", "2026-11-11", "2305", 12.0080, 100.0000, "n", 1)
	mutations, err = r.ProcessBatch(context.Background(), st, "offsets-3-4", []byte(late1+late2))
	if err != nil {
		t.Fatalf("late: %v", err)
	}
	if len(mutations) != 0 {
		t.Fatalf("late detections must not form a fire: mutations=%+v", mutations)
	}
	if n := st.counter("late_beyond_bound"); n != 2 {
		t.Fatalf("late_beyond_bound = %d, want 2", n)
	}

	// N21 has no watermark of its own yet: a detection far in the past
	// for N21 is not late (N20's watermark never affects N21).
	n21 := synthEvent(5, "N21", "2020-01-01", "0", 20.0000, 100.0000, "n", 1)
	if _, err := r.ProcessBatch(context.Background(), st, "offsets-5-5", []byte(n21)); err != nil {
		t.Fatalf("N21: %v", err)
	}
	if n := st.counter("late_beyond_bound"); n != 2 {
		t.Fatalf("late_beyond_bound after N21 = %d, want still 2 (N21 unaffected by N20's watermark)", n)
	}
}

func TestRecognizerUpdateUnknownKeyCreatesPartialDetection(t *testing.T) {
	st := newMemState()
	r := NewRecognizer(12 * time.Hour)
	key := "N20|2026-11-12|200|-2.50000|113.80000"
	update := synthUpdate(0, key, `{"confidence":"n"}`)
	if _, err := r.ProcessBatch(context.Background(), st, "offsets-0-0", []byte(update)); err != nil {
		t.Fatalf("ProcessBatch: %v", err)
	}
	if n := st.counter("update_unknown_key"); n != 1 {
		t.Fatalf("update_unknown_key = %d, want 1", n)
	}
	d, ok, err := st.Detection(context.Background(), key)
	if err != nil || !ok {
		t.Fatalf("Detection(%q) = ok=%v err=%v, want a partial detection to exist", key, ok, err)
	}
	if d.Confidence != "n" {
		t.Fatalf("Confidence = %q, want n (from changes)", d.Confidence)
	}
	if d.Satellite != "N20" || d.AcqDate != "2026-11-12" {
		t.Fatalf("partial detection fields not parsed from key: %+v", d)
	}
}
