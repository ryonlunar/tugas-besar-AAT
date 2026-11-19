package satellite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"
)

// Detection status values stored in satellite_detections.status.
const (
	StatusActive    = "active"
	StatusLow       = "low"
	StatusLate      = "late"
	StatusRetracted = "retracted"
)

// StoredDetection is one detection's durable state. It carries the same
// lexical tokens Detection does (ToDetection converts back losslessly) so
// the pure Fase 3 functions (Connected, Components, BuildEvent) can run
// against durably-persisted data exactly as they would against a freshly
// parsed feed event.
type StoredDetection struct {
	Key              string
	Satellite        string
	AcqDate          string
	AcqTimeText      string
	LatText          string
	LonText          string
	Latitude         float64
	Longitude        float64
	Confidence       string
	FRPText          string
	Extra            map[string]json.RawMessage
	DetectedAt       time.Time
	AttributesDigest string
	Status           string // StatusActive/Low/Late/Retracted
	FireID           string // "" when not currently a member of any fire
	FirstOffset      string
	LastOffset       string
}

// ToDetection converts stored state back into the shape Connected,
// Components and BuildEvent operate on.
func (s StoredDetection) ToDetection() Detection {
	return Detection{
		Satellite: s.Satellite, AcqDate: s.AcqDate, AcqTimeText: s.AcqTimeText,
		Confidence: s.Confidence, FRPText: s.FRPText,
		LatText: s.LatText, Lat: s.Latitude, LonText: s.LonText, Lon: s.Longitude,
		Extra: s.Extra, At: s.DetectedAt,
	}
}

// AttributesDigest hashes everything FIRE.1's dedup rule treats as "this
// detection's content" (every known field plus every Extra field, but not
// the key itself, which identity already covers). An unchanged digest for
// a repeated `detection` event at the same key is a pure no-op; a changed
// digest is treated as an update (last offset wins), counted as
// duplicate_conflicting rather than silently applied.
func AttributesDigest(d Detection) string {
	h := sha256.New()
	write := func(s string) { h.Write([]byte(s)); h.Write([]byte{0}) }
	write(d.Satellite)
	write(d.AcqDate)
	write(d.AcqTimeText)
	write(d.LatText)
	write(d.LonText)
	write(d.Confidence)
	write(d.FRPText)
	keys := make([]string, 0, len(d.Extra))
	for k := range d.Extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		write(k)
		h.Write(d.Extra[k])
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Reject records one malformed feed event (D4): the micro-batch containing
// it is never parked for this reason alone -- only the single bad event is
// archived and counted.
type Reject struct {
	Offset   string
	BatchKey string
	Reason   string
	Raw      []byte
}

// Changes is everything one ProcessBatch call wants persisted. State.Apply
// must write it inside the same pgx.Tx the Processor received, so recognizer
// state changes and the returned Mutations commit atomically together
// (Fase 4's crash-recovery guarantee rests on this).
type Changes struct {
	Detections    []StoredDetection
	Fires         []FireRecord
	Watermarks    map[string]time.Time
	Rejects       []Reject
	CounterDeltas map[string]int64
}

// FireRecord is a fire's durable lineage/content state.
type FireRecord struct {
	FireID        string
	Anchor        Anchor
	Active        bool
	MemberCount   int
	ContentDigest string
	Settled       bool
}

// State is the durable-storage seam ProcessBatch depends on. pgState
// implements it over a pgx.Tx for production; memState implements it in
// memory so the recognizer's logic can be tested without a database.
type State interface {
	Detection(ctx context.Context, key string) (StoredDetection, bool, error)
	// Neighbors returns every currently-active detection that is even a
	// candidate for Connected(d, neighbor) == true: same or grid-adjacent
	// cell, within a +/-360 minute window. The caller still runs the
	// exact Connected check itself; this is only a cheap pre-filter.
	Neighbors(ctx context.Context, d StoredDetection) ([]StoredDetection, error)
	// Members returns every detection currently assigned to any of the
	// given fire IDs (active or not), so a retract/downgrade split can
	// recompute connectivity over a fire's complete former roster.
	Members(ctx context.Context, fireIDs []string) ([]StoredDetection, error)
	Fires(ctx context.Context, ids []string) (map[string]FireMeta, error)
	// FireRecords returns the full durable record (including
	// ContentDigest) for every id that currently has one, active or not.
	// Separate from Fires because lineage.Assign only ever needs
	// FireMeta; comparing digests to decide whether a revision changed
	// anything (Task 4.8) is the recognizer's own concern.
	FireRecords(ctx context.Context, ids []string) (map[string]FireRecord, error)
	Watermark(ctx context.Context, satellite string) (time.Time, bool, error)
	Apply(ctx context.Context, c Changes) error
}
