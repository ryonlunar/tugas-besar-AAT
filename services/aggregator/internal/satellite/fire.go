package satellite

import (
	"encoding/json"
	"math"
	"math/big"
	"sort"
	"strings"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
)

// Severity classifies a fire by its detection count and total FRP per
// Definisi Kebakaran's additional mapping rule (MAP.6a-c): AWAS if at least
// 20 detections OR at least 100 MW total FRP; otherwise SIAGA if at least 6
// detections OR at least 30 MW; otherwise WASPADA. frp is compared exactly
// as a rational number (see sumFRP) so the threshold never shifts due to
// float64 rounding near 30 or 100.
func Severity(count int, frp *big.Rat) contracts.Severity {
	hundred := big.NewRat(100, 1)
	thirty := big.NewRat(30, 1)
	switch {
	case count >= 20 || frp.Cmp(hundred) >= 0:
		return contracts.SeverityAwas
	case count >= 6 || frp.Cmp(thirty) >= 0:
		return contracts.SeveritySiaga
	default:
		return contracts.SeverityWaspada
	}
}

// sumFRP adds every member's FRPText as an exact rational number (D8):
// float64 addition is not associative, so summing the same set of FRP
// values in a different order can print a different digit sequence at the
// final decimal place -- an exact big.Rat sum avoids that non-determinism
// regardless of processing order. The returned scale is the largest number
// of decimal digits seen among the inputs, used only to format the sum
// back to text without inventing trailing precision the feed never sent. A
// member whose FRPText does not parse as a decimal number contributes zero
// to the sum and is skipped (defensive; FIRE event parsing already rejects
// a detection with a non-numeric frp before it reaches here).
func sumFRP(members []Detection) (sum *big.Rat, scale int) {
	sum = new(big.Rat)
	for _, d := range members {
		r, s, ok := parseDecimalRat(d.FRPText)
		if !ok {
			continue
		}
		sum.Add(sum, r)
		if s > scale {
			scale = s
		}
	}
	return sum, scale
}

func parseDecimalRat(text string) (*big.Rat, int, bool) {
	r := new(big.Rat)
	if _, ok := r.SetString(text); !ok {
		return nil, 0, false
	}
	scale := 0
	if dot := strings.IndexByte(text, '.'); dot >= 0 {
		scale = len(text) - dot - 1
	}
	return r, scale, true
}

func roundTo(v float64, places int) float64 {
	mult := math.Pow(10, float64(places))
	return math.Round(v*mult) / mult
}

// BuildEvent turns one fire's member detections into a canonical
// HazardEvent (MAP.1, MAP.4-7). HazardID and IngestedAt are left empty:
// storage assigns both. id is the caller-assigned stable fire identifier
// (lineage.go, MAP.2) and becomes SourceRefID -- BuildEvent itself has no
// opinion on fire identity, only on the event's content.
//
// members need not be sorted or deduplicated by the caller; BuildEvent
// re-sorts by detection key internally so its output (centroid, FRP sum,
// detection_keys ordering) is identical regardless of the order the
// recognizer happened to assemble the member list in.
func BuildEvent(id string, members []Detection) contracts.HazardEvent {
	sorted := append([]Detection(nil), members...)
	sort.Slice(sorted, func(i, j int) bool { return sorted[i].Key() < sorted[j].Key() })

	earliest := sorted[0].At
	satelliteSet := make(map[string]bool, len(sorted))
	maxRank := -1
	keys := make([]string, 0, len(sorted))
	var latSum, lonSum float64
	for _, d := range sorted {
		latSum += d.Lat
		lonSum += d.Lon
		if d.At.Before(earliest) {
			earliest = d.At
		}
		satelliteSet[d.Satellite] = true
		if rank, _, err := ConfidenceRank(d.Confidence); err == nil && rank > maxRank {
			maxRank = rank
		}
		keys = append(keys, d.Key())
	}
	sort.Strings(keys)
	satellites := make([]string, 0, len(satelliteSet))
	for s := range satelliteSet {
		satellites = append(satellites, s)
	}
	sort.Strings(satellites)

	n := float64(len(sorted))
	lat := roundTo(latSum/n, 6)
	lon := roundTo(lonSum/n, 6)

	frpSum, frpScale := sumFRP(sorted)
	if maxRank < 0 {
		maxRank = 1 // defensive: ConfidenceRank already validated upstream; "n" is a safe mid default
	}

	return contracts.HazardEvent{
		Source:      contracts.SourceSatellite,
		SourceRefID: id,
		HazardType:  contracts.HazardWildfire,
		Severity:    Severity(len(sorted), frpSum),
		AreaName:    NearestProvince(lat, lon).Name,
		Latitude:    lat,
		Longitude:   lon,
		OccurredAt:  earliest.UTC().Format("2006-01-02T15:04:05Z"),
		Attributes: map[string]any{
			"detection_count": len(sorted),
			"satellites":      satellites,
			"max_confidence":  ConfidenceLetter(maxRank),
			"total_frp_mw":    json.Number(frpSum.FloatString(frpScale)),
			"detection_keys":  keys,
		},
	}
}
