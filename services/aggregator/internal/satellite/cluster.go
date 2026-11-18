package satellite

import (
	"math"
	"sort"
)

// gridCell identifies a 0.01-degree grid bucket used to find candidate
// neighbor detections in roughly constant time instead of comparing every
// detection against every other one.
type gridCell struct{ lat, lon int }

// Cell buckets a coordinate into a 0.01-degree grid (Task 3.5): each bucket
// is about 1.1 km tall, so any two points within 1.0 km of each other lie
// in the same or an adjacent latitude row.
func Cell(lat, lon float64) (int, int) {
	return int(math.Floor(lat * 100)), int(math.Floor(lon * 100))
}

// NeighborLonSpan returns how many 0.01-degree longitude buckets on either
// side of a point's own bucket can still hold a point within 1.0 km, at the
// given latitude. Longitude degrees shrink in ground distance by cos(lat)
// as latitude moves away from the equator, so the same 1.0 km radius spans
// more longitude buckets at higher latitude; the result is always at least
// 1 so a point's own adjacent bucket is never excluded even very close to
// the equator.
func NeighborLonSpan(lat float64) int {
	cosLat := math.Cos(lat * math.Pi / 180)
	if cosLat < 0.01 {
		cosLat = 0.01 // defensive floor; Indonesia's latitudes never approach this
	}
	span := int(math.Ceil(1.0 / (111.32 * cosLat * 0.01)))
	if span < 1 {
		span = 1
	}
	return span
}

// Components groups a set of detections into connected fires per Definisi
// Kebakaran points 4-5 (FIRE.4c, FIRE.5a-c): low-confidence detections are
// excluded entirely before clustering (FIRE.3); connectivity is transitive
// (A-B and B-C connected puts A, B, and C in one group even if A-C alone
// would not be); a group of two or more detections is a fire, and a group
// of exactly one is a singleton, reported separately but never emitted as
// a HazardEvent. Both the fires slice and each fire's member slice, and the
// singletons slice, are ordered deterministically by (At, Key) so
// reprocessing the same input always yields the same grouping and ordering
// (feeds Task 3.9's independence-from-batch-size property and MAP.2's
// identity stability).
func Components(ds []Detection) (fires [][]Detection, singletons []Detection) {
	active := make([]Detection, 0, len(ds))
	for _, d := range ds {
		if _, low, err := ConfidenceRank(d.Confidence); err != nil || low {
			continue
		}
		active = append(active, d)
	}
	sort.Slice(active, func(i, j int) bool {
		if !active[i].At.Equal(active[j].At) {
			return active[i].At.Before(active[j].At)
		}
		return active[i].Key() < active[j].Key()
	})

	n := len(active)
	parent := make([]int, n)
	for i := range parent {
		parent[i] = i
	}
	var find func(int) int
	find = func(x int) int {
		for parent[x] != x {
			parent[x] = parent[parent[x]]
			x = parent[x]
		}
		return x
	}
	union := func(a, b int) {
		ra, rb := find(a), find(b)
		if ra != rb {
			parent[rb] = ra
		}
	}

	buckets := make(map[gridCell][]int)
	for i, d := range active {
		lat, lon := Cell(d.Lat, d.Lon)
		buckets[gridCell{lat, lon}] = append(buckets[gridCell{lat, lon}], i)
	}
	for i, d := range active {
		lat, lon := Cell(d.Lat, d.Lon)
		span := NeighborLonSpan(d.Lat)
		for dlat := -1; dlat <= 1; dlat++ {
			for dlon := -span; dlon <= span; dlon++ {
				for _, j := range buckets[gridCell{lat + dlat, lon + dlon}] {
					if j <= i {
						continue // each ordered pair is evaluated exactly once, from its lower index
					}
					if Connected(d, active[j]) {
						union(i, j)
					}
				}
			}
		}
	}

	groups := make(map[int][]int)
	for i := range active {
		root := find(i)
		groups[root] = append(groups[root], i)
	}
	type group struct {
		minIdx int
		idx    []int
	}
	ordered := make([]group, 0, len(groups))
	for _, idx := range groups {
		sort.Ints(idx)
		ordered = append(ordered, group{minIdx: idx[0], idx: idx})
	}
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].minIdx < ordered[j].minIdx })

	for _, g := range ordered {
		members := make([]Detection, len(g.idx))
		for k, idx := range g.idx {
			members[k] = active[idx]
		}
		if len(members) >= 2 {
			fires = append(fires, members)
		} else {
			singletons = append(singletons, members[0])
		}
	}
	return fires, singletons
}
