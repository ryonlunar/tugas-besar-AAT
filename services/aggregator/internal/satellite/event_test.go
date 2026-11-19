package satellite

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// callParseFeedEvent fails the test if ParseFeedEvent panics, enforcing the
// "field wajib hilang/tipe salah -> *RejectError (bukan panic)" requirement.
func callParseFeedEvent(t *testing.T, ev Event) (int64, any, error) {
	t.Helper()
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("ParseFeedEvent panicked: %v", r)
		}
	}()
	return ParseFeedEvent(ev)
}

func readFixtureEvents(t *testing.T, path string) []Event {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	reader := NewReader(strings.NewReader(string(data)), 64<<10)
	var events []Event
	for {
		ev, err := reader.Next()
		if err != nil {
			break
		}
		events = append(events, ev)
	}
	return events
}

func TestParseFeedEvent(t *testing.T) {
	events := readFixtureEvents(t, "testdata/detection_update_retract.sse")
	if len(events) != 3 {
		t.Fatalf("fixture produced %d events, want 3", len(events))
	}

	t.Run("detection event yields a Detection with lexical tokens preserved", func(t *testing.T) {
		offset, payload, err := callParseFeedEvent(t, events[0])
		if err != nil {
			t.Fatalf("ParseFeedEvent() error = %v, want nil", err)
		}
		if offset != 0 {
			t.Fatalf("offset = %d, want 0", offset)
		}
		detection, ok := payload.(Detection)
		if !ok {
			t.Fatalf("payload type = %T, want Detection", payload)
		}
		switch {
		case detection.Satellite != "N20":
			t.Errorf("Satellite = %q, want N20", detection.Satellite)
		case detection.AcqDate != "2026-01-15":
			t.Errorf("AcqDate = %q, want 2026-01-15", detection.AcqDate)
		case detection.AcqTimeText != "1234":
			t.Errorf("AcqTimeText = %q, want 1234", detection.AcqTimeText)
		case detection.Confidence != "n":
			t.Errorf("Confidence = %q, want n", detection.Confidence)
		case detection.FRPText != "12.50":
			t.Errorf("FRPText = %q, want 12.50 (lexical, trailing zero preserved)", detection.FRPText)
		case detection.LatText != "-2.51230":
			t.Errorf("LatText = %q, want -2.51230 (lexical, trailing zero preserved)", detection.LatText)
		case detection.LonText != "110.25000":
			t.Errorf("LonText = %q, want 110.25000 (lexical, trailing zeros preserved)", detection.LonText)
		}
		if detection.Lat < -2.5124 || detection.Lat > -2.5122 {
			t.Errorf("Lat = %v, want approx -2.5123", detection.Lat)
		}
		if detection.Lon < 110.249 || detection.Lon > 110.251 {
			t.Errorf("Lon = %v, want approx 110.25", detection.Lon)
		}
		if _, ok := detection.Extra["instrument"]; !ok {
			t.Errorf("Extra missing passthrough field %q", "instrument")
		}
		if _, ok := detection.Extra["satellite"]; ok {
			t.Errorf("Extra must not duplicate the known field %q", "satellite")
		}
	})

	t.Run("update event yields a partial Update, not a full Detection", func(t *testing.T) {
		offset, payload, err := callParseFeedEvent(t, events[1])
		if err != nil {
			t.Fatalf("ParseFeedEvent() error = %v, want nil", err)
		}
		if offset != 1 {
			t.Fatalf("offset = %d, want 1", offset)
		}
		update, ok := payload.(Update)
		if !ok {
			t.Fatalf("payload type = %T, want Update", payload)
		}
		wantKey := "N20|2026-01-15|1234|-2.51230|110.25000"
		if update.Key != wantKey {
			t.Errorf("Key = %q, want %q", update.Key, wantKey)
		}
		if len(update.Changes) != 2 {
			t.Fatalf("Changes = %v, want exactly 2 entries", update.Changes)
		}
		if string(update.Changes["bright_ti4"]) != "305.40" {
			t.Errorf("Changes[bright_ti4] = %s, want 305.40 (lexical)", update.Changes["bright_ti4"])
		}
		if string(update.Changes["confidence"]) != `"l"` {
			t.Errorf("Changes[confidence] = %s, want \"l\"", update.Changes["confidence"])
		}
	})

	t.Run("retract event yields the referenced detection key", func(t *testing.T) {
		offset, payload, err := callParseFeedEvent(t, events[2])
		if err != nil {
			t.Fatalf("ParseFeedEvent() error = %v, want nil", err)
		}
		if offset != 2 {
			t.Fatalf("offset = %d, want 2", offset)
		}
		retract, ok := payload.(Retract)
		if !ok {
			t.Fatalf("payload type = %T, want Retract", payload)
		}
		wantKey := "N20|2026-01-15|1234|-2.51230|110.25000"
		if retract.Key != wantKey {
			t.Errorf("Key = %q, want %q", retract.Key, wantKey)
		}
		if retract.Reason != "false_positive" {
			t.Errorf("Reason = %q, want false_positive", retract.Reason)
		}
	})

	t.Run("retract without a reason defaults to empty, not rejected", func(t *testing.T) {
		ev := Event{ID: "9", Type: "retract", Data: []byte(`{"offset":9,"key":"N20|2026-01-15|1234|-2.51230|110.25000"}`)}
		offset, payload, err := callParseFeedEvent(t, ev)
		if err != nil {
			t.Fatalf("ParseFeedEvent() error = %v, want nil", err)
		}
		if offset != 9 {
			t.Fatalf("offset = %d, want 9", offset)
		}
		retract, ok := payload.(Retract)
		if !ok {
			t.Fatalf("payload type = %T, want Retract", payload)
		}
		if retract.Reason != "" {
			t.Errorf("Reason = %q, want empty", retract.Reason)
		}
	})

	t.Run("rejects a detection missing a required field", func(t *testing.T) {
		ev := Event{ID: "3", Type: "detection", Data: []byte(`{"offset":3,"detection":{"acq_date":"2026-01-15","acq_time":1234,"confidence":"n","frp":1.0,"longitude":110.0,"satellite":"N20"}}`)}
		_, _, err := callParseFeedEvent(t, ev)
		assertRejectReasonContains(t, err, "latitude")
	})

	t.Run("rejects a detection with the wrong JSON type for a known field", func(t *testing.T) {
		ev := Event{ID: "4", Type: "detection", Data: []byte(`{"offset":4,"detection":{"acq_date":"2026-01-15","acq_time":"1234","confidence":"n","frp":1.0,"latitude":1.0,"longitude":110.0,"satellite":"N20"}}`)}
		_, _, err := callParseFeedEvent(t, ev)
		assertRejectReasonContains(t, err, "acq_time")
	})

	t.Run("rejects when the SSE id and the body offset disagree", func(t *testing.T) {
		ev := Event{ID: "5", Type: "detection", Data: []byte(`{"offset":6,"detection":{"acq_date":"2026-01-15","acq_time":1234,"confidence":"n","frp":1.0,"latitude":1.0,"longitude":110.0,"satellite":"N20"}}`)}
		_, _, err := callParseFeedEvent(t, ev)
		assertRejectReasonContains(t, err, "offset")
	})

	t.Run("rejects a non-numeric SSE id", func(t *testing.T) {
		ev := Event{ID: "abc", Type: "detection", Data: []byte(`{"offset":0,"detection":{}}`)}
		_, _, err := callParseFeedEvent(t, ev)
		assertRejectReasonContains(t, err, "abc")
	})

	t.Run("rejects an unknown event type", func(t *testing.T) {
		ev := Event{ID: "6", Type: "heartbeat", Data: []byte(`{"offset":6}`)}
		_, _, err := callParseFeedEvent(t, ev)
		assertRejectReasonContains(t, err, "heartbeat")
	})

	t.Run("rejects a body that is not valid JSON", func(t *testing.T) {
		ev := Event{ID: "7", Type: "detection", Data: []byte(`not json`)}
		_, _, err := callParseFeedEvent(t, ev)
		if err == nil {
			t.Fatal("ParseFeedEvent() error = nil, want a *RejectError")
		}
		var reject *RejectError
		if !asRejectError(err, &reject) {
			t.Fatalf("error type = %T, want *RejectError", err)
		}
	})

	t.Run("rejects an update missing the changes object", func(t *testing.T) {
		ev := Event{ID: "8", Type: "update", Data: []byte(`{"offset":8,"key":"N20|2026-01-15|1234|-2.51230|110.25000"}`)}
		_, _, err := callParseFeedEvent(t, ev)
		assertRejectReasonContains(t, err, "changes")
	})

	t.Run("rejects a retract missing the key", func(t *testing.T) {
		ev := Event{ID: "10", Type: "retract", Data: []byte(`{"offset":10,"reason":"false_positive"}`)}
		_, _, err := callParseFeedEvent(t, ev)
		assertRejectReasonContains(t, err, "key")
	})
}

func asRejectError(err error, target **RejectError) bool {
	reject, ok := err.(*RejectError)
	if !ok {
		return false
	}
	*target = reject
	return true
}

func assertRejectReasonContains(t *testing.T, err error, substr string) {
	t.Helper()
	if err == nil {
		t.Fatal("error = nil, want a *RejectError")
	}
	var reject *RejectError
	if !asRejectError(err, &reject) {
		t.Fatalf("error type = %T, want *RejectError", err)
	}
	if !strings.Contains(reject.Reason, substr) {
		t.Fatalf("Reason = %q, want it to mention %q", reject.Reason, substr)
	}
}

// Sanity: json.RawMessage values decode independent of UseNumber, used by
// the test above to assert lexical preservation in Update.Changes.
var _ = json.RawMessage(nil)
