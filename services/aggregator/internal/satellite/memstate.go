package satellite

import (
	"context"
	"time"
)

// memState is an in-memory State used by the recognizer's unit tests
// (Task 4.2+): it has the exact same semantics pgState has, just without a
// database, so recognizer logic can be tested fast and without
// AAT_FOUNDATION_DSN_FILE.
type memState struct {
	detections map[string]StoredDetection
	fires      map[string]FireRecord
	watermarks map[string]time.Time
	counters   map[string]int64
	rejects    []Reject
}

func newMemState() *memState {
	return &memState{
		detections: map[string]StoredDetection{},
		fires:      map[string]FireRecord{},
		watermarks: map[string]time.Time{},
		counters:   map[string]int64{},
	}
}

func (m *memState) Detection(_ context.Context, key string) (StoredDetection, bool, error) {
	d, ok := m.detections[key]
	return d, ok, nil
}

func (m *memState) Neighbors(_ context.Context, d StoredDetection) ([]StoredDetection, error) {
	var out []StoredDetection
	for _, other := range m.detections {
		if other.Key == d.Key || other.Status != StatusActive {
			continue
		}
		dt := d.DetectedAt.Sub(other.DetectedAt)
		if dt < 0 {
			dt = -dt
		}
		if dt > 360*time.Minute {
			continue
		}
		out = append(out, other)
	}
	return out, nil
}

func (m *memState) Members(_ context.Context, fireIDs []string) ([]StoredDetection, error) {
	want := make(map[string]bool, len(fireIDs))
	for _, id := range fireIDs {
		want[id] = true
	}
	var out []StoredDetection
	for _, d := range m.detections {
		if d.FireID != "" && want[d.FireID] {
			out = append(out, d)
		}
	}
	return out, nil
}

func (m *memState) FireRecords(_ context.Context, ids []string) (map[string]FireRecord, error) {
	out := make(map[string]FireRecord, len(ids))
	for _, id := range ids {
		if rec, ok := m.fires[id]; ok {
			out[id] = rec
		}
	}
	return out, nil
}

func (m *memState) Watermark(_ context.Context, satellite string) (time.Time, bool, error) {
	w, ok := m.watermarks[satellite]
	return w, ok, nil
}

func (m *memState) Apply(_ context.Context, c Changes) error {
	for _, d := range c.Detections {
		m.detections[d.Key] = d
	}
	for _, f := range c.Fires {
		m.fires[f.FireID] = f
	}
	for sat, w := range c.Watermarks {
		m.watermarks[sat] = w
	}
	for name, delta := range c.CounterDeltas {
		m.counters[name] += delta
	}
	m.rejects = append(m.rejects, c.Rejects...)
	return nil
}

// counter reads a named counter's current total, defaulting to 0 -- a test
// convenience so assertions read naturally (st.counter("late_beyond_bound")).
func (m *memState) counter(name string) int64 { return m.counters[name] }

// activeFireIDs returns every fire ID currently marked active, for test
// assertions that need to enumerate "what fires exist right now" without
// reaching into memState's private maps directly.
func (m *memState) activeFireIDs() []string {
	var out []string
	for id, meta := range m.fires {
		if meta.Active {
			out = append(out, id)
		}
	}
	return out
}
