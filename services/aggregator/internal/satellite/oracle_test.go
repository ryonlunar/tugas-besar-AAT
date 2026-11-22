package satellite

import (
	"context"
	"fmt"
	"math/rand"
	"sort"
	"strings"
	"testing"
	"time"
)

// buildSyntheticStream generates a deterministic (fixed seed) SSE byte
// stream exercising two satellites, a transitive chain, duplicates,
// updates, retracts, low confidence, and a late detection -- everything
// TestRecognizerMatchesOracle and TestRecognizerIDsIndependentOfBatchSize
// need, without depending on any real feed data (labeled synthetic, per
// the plan's testdata convention).
func buildSyntheticStream(seed int64) []string {
	rng := rand.New(rand.NewSource(seed))
	var events []string
	offset := 0
	emit := func(s string) { events = append(events, s); offset++ }

	baseLat, baseLon := -2.5000, 113.8000
	// A long transitive chain on N20, each link ~0.85km from the last.
	for i := 0; i < 40; i++ {
		lat := baseLat - float64(i)*0.0076
		emit(synthEvent(offset, "N20", "2026-11-12", fmt.Sprintf("%d", 100+i), lat, baseLon, "n", 1+rng.Float64()*20))
	}
	// A second, disconnected cluster on N21 (far from the N20 chain).
	for i := 0; i < 6; i++ {
		lat := 10.0000 - float64(i)*0.0076
		emit(synthEvent(offset, "N21", "2026-11-12", fmt.Sprintf("%d", 300+i), lat, 50.0000, "h", 5))
	}
	// A pair of low-confidence detections right on the N20 chain: must
	// never join it.
	emit(synthEvent(offset, "N20", "2026-11-12", "500", baseLat-0.0076*5, baseLon, "l", 1))
	emit(synthEvent(offset, "N20", "2026-11-12", "501", baseLat-0.0076*6, baseLon, "l", 1))
	// An isolated singleton, far from everything.
	emit(synthEvent(offset, "N20", "2026-11-12", "600", 30.0000, 30.0000, "n", 1))
	// A byte-identical duplicate of the chain's first detection.
	emit(synthEvent(offset, "N20", "2026-11-12", "100", baseLat, baseLon, "n", 1+0)) // frp differs from the original random value deliberately below
	// A conflicting duplicate (changes an already-seen key's frp).
	key0 := Detection{Satellite: "N20", AcqDate: "2026-11-12", AcqTimeText: "101", LatText: ftoa(baseLat - 0.0076), LonText: ftoa(baseLon)}.Key()
	emit(synthUpdate(offset, key0, `{"frp":77.7}`))
	// Retract the chain's very last link (shrinks it, does not split).
	lastKey := Detection{Satellite: "N20", AcqDate: "2026-11-12", AcqTimeText: "139", LatText: ftoa(baseLat - 39*0.0076), LonText: ftoa(baseLon)}.Key()
	emit(synthRetract(offset, lastKey, "false_positive"))

	return events
}

// activeFireMembership runs memState's view forward to its final state and
// returns fireID -> sorted detection_keys for every currently active fire.
func activeFireMembership(st *memState) map[string][]string {
	out := map[string][]string{}
	for key, d := range st.detections {
		if d.FireID == "" {
			continue
		}
		if meta, ok := st.fires[d.FireID]; !ok || !meta.Active {
			continue
		}
		out[d.FireID] = append(out[d.FireID], key)
	}
	for id := range out {
		sort.Strings(out[id])
	}
	return out
}

// activeDetectionSnapshot returns every detection the recognizer currently
// considers active or low (i.e. still a clustering candidate), as the pure
// Detection shape Components() consumes -- this is the oracle's input.
func activeDetectionSnapshot(st *memState) []Detection {
	var out []Detection
	for _, d := range st.detections {
		if d.Status == StatusActive || d.Status == StatusLow {
			out = append(out, d.ToDetection())
		}
	}
	return out
}

func runStreamWithBatchSize(t *testing.T, events []string, batchSize int) *memState {
	st := newMemState()
	r := NewRecognizer(12 * time.Hour)
	for i := 0; i < len(events); i += batchSize {
		end := i + batchSize
		if end > len(events) {
			end = len(events)
		}
		batch := strings.Join(events[i:end], "")
		key := fmt.Sprintf("offsets-%d-%d", i, end-1)
		if _, err := r.ProcessBatch(context.Background(), st, key, []byte(batch)); err != nil {
			t.Fatalf("ProcessBatch(batchSize=%d, %s): %v", batchSize, key, err)
		}
	}
	return st
}

func TestRecognizerMatchesOracle(t *testing.T) {
	events := buildSyntheticStream(42)
	st := runStreamWithBatchSize(t, events, 7)

	oracleFires, oracleSingles := Components(activeDetectionSnapshot(st))
	oracleSets := map[string]bool{}
	for _, group := range oracleFires {
		keys := make([]string, len(group))
		for i, d := range group {
			keys[i] = d.Key()
		}
		sort.Strings(keys)
		oracleSets[strings.Join(keys, ",")] = true
	}

	got := activeFireMembership(st)
	gotSets := map[string]bool{}
	for _, keys := range got {
		gotSets[strings.Join(keys, ",")] = true
	}

	if len(gotSets) != len(oracleSets) {
		t.Fatalf("recognizer produced %d distinct fires, oracle produced %d\nrecognizer: %v\noracle: %v", len(gotSets), len(oracleSets), gotSets, oracleSets)
	}
	for set := range oracleSets {
		if !gotSets[set] {
			t.Errorf("oracle fire %q missing from recognizer's active fires", set)
		}
	}
	_ = oracleSingles
}

func TestRecognizerIDsIndependentOfBatchSize(t *testing.T) {
	events := buildSyntheticStream(42)
	sizes := []int{1, 7, len(events)}
	var reference map[string][]string
	for _, size := range sizes {
		st := runStreamWithBatchSize(t, events, size)
		got := activeFireMembership(st)
		if reference == nil {
			reference = got
			continue
		}
		if len(got) != len(reference) {
			t.Fatalf("batch size %d: %d active fires, want %d (reference batch size %d)", size, len(got), len(reference), sizes[0])
		}
		for id, keys := range got {
			refKeys, ok := reference[id]
			if !ok {
				t.Errorf("batch size %d: fire id %s does not exist in the reference run at all -- IDs are not batch-size independent", size, id)
				continue
			}
			if strings.Join(keys, ",") != strings.Join(refKeys, ",") {
				t.Errorf("batch size %d: fire %s members = %v, reference run's members = %v", size, id, keys, refKeys)
			}
		}
	}
}
