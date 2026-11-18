package satellite

import (
	"fmt"
	"math"
	"strings"
	"time"
)

// EarthRadiusKm is the sphere radius used by DistanceKm's Haversine
// formula. 6371.0 km is the conventional mean-Earth-radius approximation
// (ASSUMPTION, open question K5 in the plan): the feed/course specification
// does not state which radius or distance formula its own reference
// implementation uses, so a fire straddling the 1.0 km boundary could be
// classified differently by a reference built on a different value or a
// more exact ellipsoidal (e.g. Vincenty) distance. This is reported as a
// sensitivity in P4.6, not treated as settled fact.
const EarthRadiusKm = 6371.0

// DistanceKm returns the great-circle distance between two lat/lon points
// in kilometers via the Haversine formula.
func DistanceKm(lat1, lon1, lat2, lon2 float64) float64 {
	p1, p2 := lat1*math.Pi/180, lat2*math.Pi/180
	dp := p2 - p1
	dl := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dp/2)*math.Sin(dp/2) + math.Cos(p1)*math.Cos(p2)*math.Sin(dl/2)*math.Sin(dl/2)
	return 2 * EarthRadiusKm * math.Asin(math.Min(1, math.Sqrt(a)))
}

// Connected reports whether two detections belong to the same fire per
// Definisi Kebakaran point 4 (FIRE.4a/b): their detection times differ by at
// most 360 minutes (inclusive) AND their positions are at most 1.0 km apart
// (inclusive). Both bounds are checked regardless of which detection has
// the earlier time or which has the smaller latitude.
func Connected(a, b Detection) bool {
	dt := a.At.Sub(b.At)
	if dt < 0 {
		dt = -dt
	}
	if dt > 360*time.Minute {
		return false
	}
	return DistanceKm(a.Lat, a.Lon, b.Lat, b.Lon) <= 1.0
}

// ConfidenceRank classifies a feed-reported confidence value. low reports
// whether the detection must be excluded from clustering entirely (FIRE.3);
// rank orders the remaining values for computing a fire's max_confidence
// (MAP.7), where higher is more confident. Both the single-letter codes
// observed live (docs/m2-satellite-feed-notes.md: "n", "l") and their
// full-word spellings are accepted, since the course specification's own
// example ("max_confidence":"h") uses a different letter than any value
// observed from the live feed.
func ConfidenceRank(value string) (rank int, low bool, err error) {
	switch strings.ToLower(value) {
	case "l", "low":
		return 0, true, nil
	case "n", "nominal":
		return 1, false, nil
	case "h", "high":
		return 2, false, nil
	default:
		return 0, false, fmt.Errorf("confidence %q tidak dikenal", value)
	}
}

// ConfidenceLetter returns the canonical single-letter code (l/n/h) for a
// rank, matching the Lampiran A attribute example's alphabet. It is the
// inverse of ConfidenceRank's rank for the three valid ranks.
func ConfidenceLetter(rank int) string {
	switch rank {
	case 0:
		return "l"
	case 2:
		return "h"
	default:
		return "n"
	}
}
