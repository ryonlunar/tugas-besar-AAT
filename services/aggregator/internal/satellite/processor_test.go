package satellite

import (
	"context"
	"strconv"
	"testing"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
)

// synthEvent builds the SSE wire bytes for one `detection` event with the
// exact field shapes observed live (docs/m2-satellite-feed-notes.md):
// acq_time/latitude/longitude/frp as bare JSON numbers (no leading zero on
// acq_time), confidence as a one-letter string.
func synthEvent(offset int, satellite, acqDate, acqTime string, lat, lon float64, confidence string, frp float64) string {
	return "id: " + itoa(offset) + "\n" +
		"event: detection\n" +
		"data: {\"offset\":" + itoa(offset) + ",\"detection\":{\"satellite\":\"" + satellite + "\",\"acq_date\":\"" + acqDate + "\",\"acq_time\":" + acqTime +
		",\"confidence\":\"" + confidence + "\",\"frp\":" + ftoa(frp) + ",\"latitude\":" + ftoa(lat) + ",\"longitude\":" + ftoa(lon) + "}}\n\n"
}

func synthUpdate(offset int, key string, changesJSON string) string {
	return "id: " + itoa(offset) + "\n" +
		"event: update\n" +
		"data: {\"offset\":" + itoa(offset) + ",\"key\":\"" + key + "\",\"changes\":" + changesJSON + "}\n\n"
}

func synthRetract(offset int, key, reason string) string {
	return "id: " + itoa(offset) + "\n" +
		"event: retract\n" +
		"data: {\"offset\":" + itoa(offset) + ",\"key\":\"" + key + "\",\"reason\":\"" + reason + "\"}\n\n"
}

func ftoa(f float64) string {
	return strconv.FormatFloat(f, 'f', -1, 64)
}

func TestRecognizerTwoConnectedDetectionsCreateOneFire(t *testing.T) {
	st := newMemState()
	r := NewRecognizer(12 * time.Hour)

	raw := synthEvent(0, "N20", "2026-11-12", "200", -2.5000, 113.8000, "n", 10.2) +
		synthEvent(1, "N20", "2026-11-12", "205", -2.5080, 113.8000, "n", 2.3)

	mutations, err := r.ProcessBatch(context.Background(), st, "offsets-0-1", []byte(raw))
	if err != nil {
		t.Fatalf("ProcessBatch: %v", err)
	}
	if len(mutations) != 1 {
		t.Fatalf("got %d mutations, want 1; mutations=%+v", len(mutations), mutations)
	}
	m := mutations[0]
	if m.Kind != contracts.MutationUpsert {
		t.Fatalf("Kind = %v, want upsert", m.Kind)
	}
	if m.Event == nil || m.Event.Attributes["detection_count"] != 2 {
		t.Fatalf("detection_count = %v, want 2", m.Event.Attributes["detection_count"])
	}
	if len(st.activeFireIDs()) != 1 {
		t.Fatalf("active fires in state = %d, want 1", len(st.activeFireIDs()))
	}
}
