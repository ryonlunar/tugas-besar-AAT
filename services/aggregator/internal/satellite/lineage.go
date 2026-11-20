package satellite

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"time"
)

// FireID derives a stable identifier from the detection chosen at formation.
func FireID(anchorKey string) string {
	sum := sha256.Sum256([]byte(anchorKey))
	return "fire-" + hex.EncodeToString(sum[:])[:16]
}

type Anchor struct {
	Key string
	At  time.Time
}

func anchorOf(members []Detection) Anchor {
	best := Anchor{Key: members[0].Key(), At: members[0].At}
	for _, d := range members[1:] {
		candidate := Anchor{Key: d.Key(), At: d.At}
		if earlier(candidate, best) {
			best = candidate
		}
	}
	return best
}

func earlier(a, b Anchor) bool {
	if !a.At.Equal(b.At) {
		return a.At.Before(b.At)
	}
	return a.Key < b.Key
}

// lineage manages the affected detections for one batch. Membership changes
// are applied one event at a time, independently of the SSE flush boundary.
type lineage struct {
	state             State
	detections        map[string]StoredDetection
	grid              map[gridCell]map[string]struct{}
	members           map[string]map[string]struct{}
	fires             map[string]FireRecord
	originalFires     map[string]FireRecord
	changedDetections map[string]bool
	changedFires      map[string]bool
}

func loadLineage(ctx context.Context, st State, original map[string]StoredDetection, updates []StoredDetection) (*lineage, error) {
	l := &lineage{
		state: st, detections: original, grid: map[gridCell]map[string]struct{}{},
		members: map[string]map[string]struct{}{}, fires: map[string]FireRecord{},
		changedDetections: map[string]bool{}, changedFires: map[string]bool{},
	}

	// The database query returns a cell/time window, so identical windows only
	// need to be fetched once even when many detections share a location.
	type window struct {
		cell gridCell
		span int
		at   time.Time
	}
	windows := map[window]bool{}
	for _, d := range updates {
		if !qualifies(d) {
			continue
		}
		lat, lon := Cell(d.Latitude, d.Longitude)
		w := window{gridCell{lat, lon}, NeighborLonSpan(d.Latitude), d.DetectedAt}
		if windows[w] {
			continue
		}
		windows[w] = true
		neighbors, err := st.Neighbors(ctx, d)
		if err != nil {
			return nil, err
		}
		for _, n := range neighbors {
			original[n.Key] = n
		}
	}
	seen := map[string]bool{}
	var ids []string
	for _, d := range original {
		if d.FireID != "" && !seen[d.FireID] {
			seen[d.FireID] = true
			ids = append(ids, d.FireID)
		}
	}
	members, err := st.Members(ctx, ids)
	if err != nil {
		return nil, err
	}
	for _, d := range members {
		original[d.Key] = d
	}
	l.originalFires, err = st.FireRecords(ctx, ids)
	if err != nil {
		return nil, err
	}
	for id, fire := range l.originalFires {
		l.fires[id] = fire
	}
	for key, d := range original {
		if qualifies(d) {
			l.addToGrid(d)
			if d.FireID != "" {
				l.trackMember(d.FireID, key)
			}
		}
	}
	return l, nil
}

func qualifies(d StoredDetection) bool {
	_, low, err := ConfidenceRank(d.Confidence)
	return d.Status == StatusActive && err == nil && !low
}

func (l *lineage) addToGrid(d StoredDetection) {
	lat, lon := Cell(d.Latitude, d.Longitude)
	cell := gridCell{lat, lon}
	if l.grid[cell] == nil {
		l.grid[cell] = map[string]struct{}{}
	}
	l.grid[cell][d.Key] = struct{}{}
}

func (l *lineage) trackMember(id, key string) {
	if l.members[id] == nil {
		l.members[id] = map[string]struct{}{}
	}
	l.members[id][key] = struct{}{}
}

func (l *lineage) assignMember(id, key string) {
	d := l.detections[key]
	if d.FireID != id {
		d.FireID = id
		l.detections[key] = d
		l.changedDetections[key] = true
	}
	l.trackMember(id, key)
	l.changedFires[id] = true
}

func (l *lineage) fireMembers(id string) []Detection {
	ds := make([]Detection, 0, len(l.members[id]))
	for key := range l.members[id] {
		ds = append(ds, l.detections[key].ToDetection())
	}
	return ds
}

func (l *lineage) retire(id string) {
	record := l.fires[id]
	record.Active = false
	record.MemberCount = 0
	record.ContentDigest = ""
	l.fires[id] = record
	l.changedFires[id] = true
	delete(l.members, id)
}

func (l *lineage) activate(ctx context.Context, anchor Anchor) (string, error) {
	for attempt := 0; ; attempt++ {
		key := anchor.Key
		if attempt > 0 {
			key += "#" + strconv.Itoa(attempt)
		}
		id := FireID(key)
		record, known := l.fires[id]
		if !known {
			stored, err := l.state.FireRecords(ctx, []string{id})
			if err != nil {
				return "", err
			}
			record, known = stored[id]
			if known {
				l.originalFires[id] = record
				l.fires[id] = record
			}
		}
		if !known || (!record.Active && record.Anchor.Key == anchor.Key) {
			l.fires[id] = FireRecord{FireID: id, Anchor: anchor, Active: true}
			l.changedFires[id] = true
			return id, nil
		}
	}
}

func (l *lineage) apply(ctx context.Context, d StoredDetection) error {
	old := l.detections[d.Key]
	wasActive, active := qualifies(old), qualifies(d)
	d.FireID = old.FireID
	if !active {
		d.FireID = ""
	}
	l.detections[d.Key] = d
	l.changedDetections[d.Key] = true

	switch {
	case active && wasActive:
		if d.FireID != "" {
			l.changedFires[d.FireID] = true
		}
	case active:
		return l.join(ctx, d)
	case wasActive:
		lat, lon := Cell(old.Latitude, old.Longitude)
		delete(l.grid[gridCell{lat, lon}], old.Key)
		if old.FireID != "" {
			return l.split(ctx, old)
		}
	}
	return nil
}

func (l *lineage) join(ctx context.Context, d StoredDetection) error {
	lat, lon := Cell(d.Latitude, d.Longitude)
	span := NeighborLonSpan(d.Latitude)
	joined := map[string]bool{}
	unassigned := []Detection{d.ToDetection()}
	for dy := -1; dy <= 1; dy++ {
		for dx := -span; dx <= span; dx++ {
			for key := range l.grid[gridCell{lat + dy, lon + dx}] {
				other := l.detections[key]
				if other.FireID != "" && joined[other.FireID] {
					continue
				}
				if !Connected(unassigned[0], other.ToDetection()) {
					continue
				}
				if other.FireID == "" {
					unassigned = append(unassigned, other.ToDetection())
				} else {
					joined[other.FireID] = true
				}
			}
		}
	}
	l.addToGrid(d)
	if len(joined) == 0 && len(unassigned) == 1 {
		return nil
	}

	// An addition to an existing fire retains that ID even if the new
	// detection is earlier in event time. A merge keeps the earliest fire
	// anchor, with the ID as a deterministic tie-breaker.
	var survivor string
	for id := range joined {
		a, b := l.fires[id].Anchor, l.fires[survivor].Anchor
		if survivor == "" || earlier(a, b) || (a == b && id < survivor) {
			survivor = id
		}
	}
	if survivor == "" {
		var err error
		survivor, err = l.activate(ctx, anchorOf(unassigned))
		if err != nil {
			return err
		}
	}
	for _, member := range unassigned {
		l.assignMember(survivor, member.Key())
	}
	for id := range joined {
		if id != survivor {
			for key := range l.members[id] {
				l.assignMember(survivor, key)
			}
			l.retire(id)
		}
	}
	return nil
}

func (l *lineage) split(ctx context.Context, removed StoredDetection) error {
	id := removed.FireID
	delete(l.members[id], removed.Key)
	groups, singletons := Components(l.fireMembers(id))
	delete(l.members, id)
	for _, d := range singletons {
		key := d.Key()
		row := l.detections[key]
		row.FireID = ""
		l.detections[key] = row
		l.changedDetections[key] = true
	}
	if len(groups) == 0 {
		l.retire(id)
		return nil
	}

	// Components is ordered by earliest remaining member. Keep the previous
	// ID on the anchor's side when it still forms a fire.
	survivor := 0
	for i, group := range groups {
		for _, d := range group {
			if d.Key() == l.fires[id].Anchor.Key {
				survivor = i
			}
		}
	}
	for i, group := range groups {
		assigned := id
		if i != survivor {
			var err error
			assigned, err = l.activate(ctx, anchorOf(group))
			if err != nil {
				return err
			}
		}
		for _, d := range group {
			l.assignMember(assigned, d.Key())
		}
	}
	return nil
}
