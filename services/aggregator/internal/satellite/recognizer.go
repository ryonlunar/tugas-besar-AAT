package satellite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
)

// Recognizer turns one micro-batch of archived SSE bytes into durable
// recognition-state changes and the resulting Mutations, per D5: events
// are applied to an in-memory overlay in offset order, then the affected
// neighborhood is recomputed once at the end -- the result does not depend
// on where a batch boundary happened to fall (Task 4.9).
type Recognizer struct {
	// Lateness is SATELLITE_ALLOWED_LATENESS (D7): a detection whose
	// detected_at is more than Lateness behind its satellite's watermark
	// is marked late instead of joining clustering.
	Lateness time.Duration
}

// NewRecognizer constructs a Recognizer with the given allowed lateness.
func NewRecognizer(lateness time.Duration) *Recognizer {
	return &Recognizer{Lateness: lateness}
}

// maxSSELine bounds a single physical SSE line while re-parsing an archived
// micro-batch. It matches the adapter's own bound (sse.go callers use the
// same constant indirectly via the adapter); a batch the adapter accepted
// can never contain a line this parser would reject.
const maxSSELine = 1 << 20

// ProcessBatch decodes raw (the exact archived SSE bytes for one
// micro-batch), applies every event it contains in offset order, and
// returns the Mutations needed to bring canonical storage in line with the
// resulting recognition state. State changes are not returned separately:
// the caller (Processor) persists them via st.Apply inside the same
// transaction before this function returns.
func (r *Recognizer) ProcessBatch(ctx context.Context, st State, batchKey string, raw []byte) ([]contracts.Mutation, error) {
	b := &batchOverlay{
		st:         st,
		lateness:   r.Lateness,
		overlay:    map[string]StoredDetection{},
		watermarks: map[string]time.Time{},
		counters:   map[string]int64{},
	}

	reader := NewReader(bytes.NewReader(raw), maxSSELine)
	for {
		ev, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("satellite batch %s: archived SSE bytes are corrupt: %w", batchKey, err)
		}
		offset, payload, perr := ParseFeedEvent(ev)
		offsetText := ev.ID
		if perr != nil {
			b.rejects = append(b.rejects, Reject{Offset: offsetText, BatchKey: batchKey, Reason: perr.Error(), Raw: ev.Raw})
			b.counters["rejected"]++
			continue
		}
		offsetStr := strconv.FormatInt(offset, 10)
		switch p := payload.(type) {
		case Detection:
			if err := b.applyDetection(ctx, offsetStr, p); err != nil {
				return nil, err
			}
		case Update:
			if err := b.applyUpdate(ctx, offsetStr, p); err != nil {
				return nil, err
			}
		case Retract:
			if err := b.applyRetract(ctx, offsetStr, p); err != nil {
				return nil, err
			}
		}
	}

	return b.materialize(ctx)
}

// batchOverlay holds one ProcessBatch call's working state: durable state
// already loaded this batch (overlay), pending watermark advances, and
// accumulated rejects/counters, all merged into a single Changes at the end
// (materialize).
type batchOverlay struct {
	st         State
	lateness   time.Duration
	overlay    map[string]StoredDetection
	touched    []string // keys changed this batch, in the order they changed
	watermarks map[string]time.Time
	rejects    []Reject
	counters   map[string]int64
}

func (b *batchOverlay) get(ctx context.Context, key string) (StoredDetection, bool, error) {
	if d, ok := b.overlay[key]; ok {
		return d, true, nil
	}
	d, ok, err := b.st.Detection(ctx, key)
	if err != nil {
		return StoredDetection{}, false, err
	}
	if ok {
		b.overlay[key] = d
	}
	return d, ok, nil
}

func (b *batchOverlay) set(d StoredDetection) {
	b.overlay[d.Key] = d
	b.touched = append(b.touched, d.Key)
}

func (b *batchOverlay) watermark(ctx context.Context, satellite string) (time.Time, error) {
	if w, ok := b.watermarks[satellite]; ok {
		return w, nil
	}
	w, _, err := b.st.Watermark(ctx, satellite)
	if err != nil {
		return time.Time{}, err
	}
	b.watermarks[satellite] = w
	return w, nil
}

func (b *batchOverlay) advanceWatermark(satellite string, at time.Time) {
	if cur, ok := b.watermarks[satellite]; !ok || at.After(cur) {
		b.watermarks[satellite] = at
	}
}

// parseKeyComponents splits a detection_key (D8) back into its five
// lexical tokens. It never fails on a key this package produced itself
// (Detection.Key's format is exactly this), which is the only kind of key
// an update/retract can ever reference.
func parseKeyComponents(key string) (satellite, acqDate, acqTime, latText, lonText string, ok bool) {
	parts := strings.Split(key, "|")
	if len(parts) != 5 {
		return "", "", "", "", "", false
	}
	return parts[0], parts[1], parts[2], parts[3], parts[4], true
}

func (b *batchOverlay) applyDetection(ctx context.Context, offset string, d Detection) error {
	detectedAt, err := d.DetectedAt()
	if err != nil {
		b.rejects = append(b.rejects, Reject{Offset: offset, Reason: "acq_date/acq_time tidak valid: " + err.Error()})
		b.counters["rejected"]++
		return nil
	}
	d.At = detectedAt
	digest := AttributesDigest(d)

	existing, ok, err := b.get(ctx, d.Key())
	if err != nil {
		return err
	}
	if ok && existing.AttributesDigest == digest {
		// FIRE.1: byte-identical repeat is a pure no-op.
		return nil
	}

	stored := StoredDetection{
		Key: d.Key(), Satellite: d.Satellite, AcqDate: d.AcqDate, AcqTimeText: d.AcqTimeText,
		LatText: d.LatText, LonText: d.LonText, Latitude: d.Lat, Longitude: d.Lon,
		Confidence: d.Confidence, FRPText: d.FRPText, Extra: d.Extra,
		DetectedAt: detectedAt, AttributesDigest: digest, LastOffset: offset,
	}
	if ok {
		// Same key seen before with different content: last offset wins
		// (an update-in-disguise), not a brand-new arrival -- the
		// lateness check only ever applies to a key's first arrival.
		stored.FirstOffset = existing.FirstOffset
		stored.Status = existing.Status
		if existing.Status == StatusLate {
			stored.Status = StatusLate // a late detection's correction stays late
		} else {
			stored.FireID = existing.FireID
		}
		b.counters["duplicate_conflicting"]++
	} else {
		stored.FirstOffset = offset
		watermark, err := b.watermark(ctx, d.Satellite)
		if err != nil {
			return err
		}
		if !watermark.IsZero() && detectedAt.Before(watermark.Add(-b.lateness)) {
			stored.Status = StatusLate
			b.counters["late_beyond_bound"]++
		} else {
			stored.Status = StatusActive
			b.advanceWatermark(d.Satellite, detectedAt)
		}
	}
	b.set(stored)
	return nil
}

func (b *batchOverlay) applyUpdate(ctx context.Context, offset string, u Update) error {
	existing, ok, err := b.get(ctx, u.Key)
	if !ok {
		if err != nil {
			return err
		}
		satellite, acqDate, acqTime, latText, lonText, parseOK := parseKeyComponents(u.Key)
		if !parseOK {
			b.rejects = append(b.rejects, Reject{Offset: offset, Reason: "update merujuk detection_key yang tidak valid: " + u.Key})
			b.counters["rejected"]++
			return nil
		}
		lat, _ := strconv.ParseFloat(latText, 64)
		lon, _ := strconv.ParseFloat(lonText, 64)
		detectedAt, atErr := DetectedAt(acqDate, acqTime)
		existing = StoredDetection{
			Key: u.Key, Satellite: satellite, AcqDate: acqDate, AcqTimeText: acqTime,
			LatText: latText, LonText: lonText, Latitude: lat, Longitude: lon,
			Status: StatusActive, FirstOffset: offset,
		}
		if atErr == nil {
			existing.DetectedAt = detectedAt
		}
		b.counters["update_unknown_key"]++
	}
	merged, err := mergeChanges(existing, u.Changes)
	if err != nil {
		b.rejects = append(b.rejects, Reject{Offset: offset, Reason: err.Error()})
		b.counters["rejected"]++
		return nil
	}
	merged.AttributesDigest = AttributesDigest(merged.ToDetection())
	merged.LastOffset = offset
	if merged.Status != StatusRetracted && merged.Status != StatusLate {
		merged.Status = StatusActive
	}
	b.set(merged)
	return nil
}

func mergeChanges(existing StoredDetection, changes map[string]json.RawMessage) (StoredDetection, error) {
	out := existing
	if out.Extra == nil {
		out.Extra = map[string]json.RawMessage{}
	} else {
		cloned := make(map[string]json.RawMessage, len(out.Extra))
		for k, v := range out.Extra {
			cloned[k] = v
		}
		out.Extra = cloned
	}
	for field, raw := range changes {
		switch field {
		case "confidence":
			var s string
			if err := json.Unmarshal(raw, &s); err != nil {
				return existing, fmt.Errorf("changes.confidence bukan string JSON valid")
			}
			out.Confidence = s
		case "frp":
			out.FRPText = string(bytes.TrimSpace(raw))
		case "satellite", "acq_date", "acq_time", "latitude", "longitude":
			// These compose the detection_key itself; an update must
			// never be allowed to silently change a detection's
			// identity. Ignored rather than rejected: a feed that
			// echoes unchanged identity fields in `changes` alongside
			// real changes should not be treated as malformed.
		default:
			out.Extra[field] = raw
		}
	}
	return out, nil
}

func (b *batchOverlay) applyRetract(ctx context.Context, offset string, ret Retract) error {
	existing, ok, err := b.get(ctx, ret.Key)
	if err != nil {
		return err
	}
	if !ok {
		satellite, acqDate, acqTime, latText, lonText, parseOK := parseKeyComponents(ret.Key)
		if !parseOK {
			b.rejects = append(b.rejects, Reject{Offset: offset, Reason: "retract merujuk detection_key yang tidak valid: " + ret.Key})
			b.counters["rejected"]++
			return nil
		}
		lat, _ := strconv.ParseFloat(latText, 64)
		lon, _ := strconv.ParseFloat(lonText, 64)
		existing = StoredDetection{Key: ret.Key, Satellite: satellite, AcqDate: acqDate, AcqTimeText: acqTime, LatText: latText, LonText: lonText, Latitude: lat, Longitude: lon, FirstOffset: offset}
		b.counters["retract_unknown"]++
	}
	existing.Status = StatusRetracted
	existing.LastOffset = offset
	b.set(existing)
	return nil
}

// materialize recomputes connectivity over the closure of everything this
// batch touched (and everything reachable from it through grid-adjacency
// or shared fire membership), assigns stable IDs via lineage.Assign, and
// returns the resulting Mutations after persisting the new state through
// st.Apply.
func (b *batchOverlay) materialize(ctx context.Context) ([]contracts.Mutation, error) {
	universe := map[string]StoredDetection{}
	var frontier []string
	for _, key := range b.touched {
		if _, already := universe[key]; already {
			continue
		}
		universe[key] = b.overlay[key]
		frontier = append(frontier, key)
	}
	visitedFires := map[string]bool{}
	for len(frontier) > 0 {
		key := frontier[len(frontier)-1]
		frontier = frontier[:len(frontier)-1]
		d := universe[key]
		if d.Status == StatusActive {
			neighbors, err := b.st.Neighbors(ctx, d)
			if err != nil {
				return nil, err
			}
			for _, n := range neighbors {
				if _, seen := universe[n.Key]; !seen {
					universe[n.Key] = n
					frontier = append(frontier, n.Key)
				}
			}
		}
		if d.FireID != "" && !visitedFires[d.FireID] {
			visitedFires[d.FireID] = true
			members, err := b.st.Members(ctx, []string{d.FireID})
			if err != nil {
				return nil, err
			}
			for _, m := range members {
				if _, seen := universe[m.Key]; !seen {
					universe[m.Key] = m
					frontier = append(frontier, m.Key)
				}
			}
		}
	}

	oldFireIDSet := map[string]bool{}
	prev := map[string]string{}
	for key, d := range universe {
		if d.FireID != "" {
			oldFireIDSet[d.FireID] = true
			prev[key] = d.FireID
		}
	}
	oldFireIDs := make([]string, 0, len(oldFireIDSet))
	for id := range oldFireIDSet {
		oldFireIDs = append(oldFireIDs, id)
	}
	sort.Strings(oldFireIDs)
	firesMeta, err := b.st.Fires(ctx, oldFireIDs)
	if err != nil {
		return nil, err
	}
	firesRecords, err := b.st.FireRecords(ctx, oldFireIDs)
	if err != nil {
		return nil, err
	}

	activeDetections := make([]Detection, 0, len(universe))
	for _, d := range universe {
		if d.Status != StatusActive {
			continue // retracted/late never participate in clustering
		}
		activeDetections = append(activeDetections, d.ToDetection())
	}

	groups, singletons := Components(activeDetections)
	ids, retractedIDs, created := Assign(groups, prev, firesMeta)

	finalFireID := map[string]string{}
	for i, group := range groups {
		for _, d := range group {
			finalFireID[d.Key()] = ids[i]
		}
	}
	for _, d := range singletons {
		finalFireID[d.Key()] = "" // explicit: a singleton is never a member
	}

	var mutations []contracts.Mutation
	var fireRecords []FireRecord
	retractedSet := make(map[string]bool, len(retractedIDs))
	for _, id := range retractedIDs {
		retractedSet[id] = true
		anchor := firesMeta[id].Anchor
		fireRecords = append(fireRecords, FireRecord{FireID: id, Anchor: anchor, Active: false})
		mutations = append(mutations, contracts.Mutation{Kind: contracts.MutationRetract, Source: contracts.SourceSatellite, SourceRefID: id})
	}

	for i, group := range groups {
		id := ids[i]
		anchor, known := created[id]
		if !known {
			if meta, ok := firesMeta[id]; ok {
				anchor = meta.Anchor
			} else {
				anchor = anchorOf(group)
			}
		}
		event := BuildEvent(id, group)
		digest := hazardContentDigest(event)
		var previousRecord FireRecord
		hadRecord := false
		if rec, ok := firesRecords[id]; ok {
			previousRecord = rec
			hadRecord = true
		}
		settled := false // conservative: requires every satellite's watermark, not modeled yet (D7, informational only)
		fireRecords = append(fireRecords, FireRecord{FireID: id, Anchor: anchor, Active: true, MemberCount: len(group), ContentDigest: digest, Settled: settled})
		if !hadRecord || previousRecord.ContentDigest != digest || !previousRecord.Active {
			mutations = append(mutations, contracts.Mutation{Kind: contracts.MutationUpsert, Source: contracts.SourceSatellite, SourceRefID: id, Event: &event})
		}
	}

	detectionUpdates := make([]StoredDetection, 0, len(universe))
	for key, d := range universe {
		newID := finalFireID[key] // "" when not in any surviving fire
		d.FireID = newID
		detectionUpdates = append(detectionUpdates, d)
	}

	changes := Changes{
		Detections:    detectionUpdates,
		Fires:         fireRecords,
		Watermarks:    b.watermarks,
		Rejects:       b.rejects,
		CounterDeltas: b.counters,
	}
	if err := b.st.Apply(ctx, changes); err != nil {
		return nil, err
	}
	_ = retractedSet
	return mutations, nil
}

// hazardContentDigest hashes the parts of a HazardEvent that matter for
// Task 4.8's "no revision when nothing changed" rule: HazardID and
// IngestedAt are storage-assigned and excluded deliberately.
func hazardContentDigest(e contracts.HazardEvent) string {
	type comparable struct {
		Severity   contracts.Severity
		AreaName   string
		Latitude   float64
		Longitude  float64
		OccurredAt string
		Attributes map[string]any
	}
	raw, _ := json.Marshal(comparable{Severity: e.Severity, AreaName: e.AreaName, Latitude: e.Latitude, Longitude: e.Longitude, OccurredAt: e.OccurredAt, Attributes: e.Attributes})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
