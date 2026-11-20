package satellite

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestRecognizerSplitAfterBridgeRetractionAcrossBatchBoundaries(t *testing.T) {
	const date = "2026-11-12"
	bridge := Detection{Satellite: "N20", AcqDate: date, AcqTimeText: "50", LatText: "-2.508", LonText: "113.8"}.Key()
	events := []string{
		synthEvent(0, "N20", date, "100", -2.500, 113.8, "n", 5),
		synthEvent(1, "N20", date, "105", -2.501, 113.8, "n", 5),
		synthEvent(2, "N20", date, "110", -2.515, 113.8, "n", 5),
		synthEvent(3, "N20", date, "115", -2.516, 113.8, "n", 5),
		synthEvent(4, "N20", date, "50", -2.508, 113.8, "n", 5),
		synthRetract(5, bridge, "false_positive"),
	}
	expected := activeFireMembership(runStreamWithBatchSize(t, events, 1))
	if len(expected) != 2 {
		t.Fatalf("bridge retraction yielded %d fires, want 2", len(expected))
	}
	for size := 2; size <= len(events); size++ {
		got := activeFireMembership(runStreamWithBatchSize(t, events, size))
		if !reflect.DeepEqual(got, expected) {
			t.Errorf("batch size %d produced %v, want %v", size, got, expected)
		}
	}
}

func TestLineageAllocatesDistinctIDsWhenAnchorIsTaken(t *testing.T) {
	ctx := context.Background()
	state := newMemState()
	anchor := Anchor{Key: "detector-key", At: time.Unix(0, 0).UTC()}
	existing := FireID(anchor.Key)
	state.fires[existing] = FireRecord{FireID: existing, Anchor: anchor, Active: true}
	l, err := loadLineage(ctx, state, map[string]StoredDetection{}, nil)
	if err != nil {
		t.Fatal(err)
	}
	id, err := l.activate(ctx, anchor)
	if err != nil || id != FireID(anchor.Key+"#1") {
		t.Fatalf("allocation = %q, %v, want first suffixed ID", id, err)
	}
	id, err = l.activate(ctx, anchor)
	if err != nil || id != FireID(anchor.Key+"#2") {
		t.Fatalf("allocation = %q, %v, want second suffixed ID", id, err)
	}
}

func TestFireIDIsDeterministic(t *testing.T) {
	a := FireID("N20|2026-11-12|243|-2.51230|113.80440")
	b := FireID("N20|2026-11-12|243|-2.51230|113.80440")
	if a != b {
		t.Fatalf("FireID is not deterministic: %s vs %s", a, b)
	}
	if FireID("other") == a {
		t.Fatalf("different anchors must not collide")
	}
}
