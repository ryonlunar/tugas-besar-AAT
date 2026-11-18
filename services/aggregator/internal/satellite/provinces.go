package satellite

import (
	"embed"
	"encoding/json"
	"fmt"
	"sort"
)

//go:embed provinces.json
var provincesFile embed.FS

// Province is one entry of the static reference table used to assign a
// fire's area_name (MAP.3). Source/method document exactly how each
// coordinate was obtained, since this table is a project assumption, not an
// official BNPB reference (open question K4 in the plan): it currently uses
// each province's capital-city coordinates as a placeholder centroid, not a
// true administrative-polygon centroid.
type Province struct {
	Name      string
	Latitude  float64
	Longitude float64
	Source    string
	Method    string
}

var provinces = loadProvinces()

func loadProvinces() []Province {
	raw, err := provincesFile.ReadFile("provinces.json")
	if err != nil {
		panic(fmt.Errorf("satellite: embedded provinces.json: %w", err))
	}
	var entries []struct {
		Name      string  `json:"name"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
		Source    string  `json:"source"`
		Method    string  `json:"method"`
	}
	if err := json.Unmarshal(raw, &entries); err != nil {
		panic(fmt.Errorf("satellite: decode embedded provinces.json: %w", err))
	}
	out := make([]Province, 0, len(entries))
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		if seen[e.Name] {
			panic(fmt.Errorf("satellite: duplicate province name %q in provinces.json", e.Name))
		}
		seen[e.Name] = true
		out = append(out, Province{Name: e.Name, Latitude: e.Latitude, Longitude: e.Longitude, Source: e.Source, Method: e.Method})
	}
	return out
}

// NearestProvince returns the province whose reference point is closest to
// (lat, lon) by great-circle distance (MAP.3). Ties -- which the current
// table never actually produces, since no two entries share a coordinate --
// are broken by the lexicographically smaller province name, for full
// determinism regardless of table iteration order.
func NearestProvince(lat, lon float64) Province {
	best := provinces[0]
	bestDist := DistanceKm(lat, lon, best.Latitude, best.Longitude)
	for _, p := range provinces[1:] {
		d := DistanceKm(lat, lon, p.Latitude, p.Longitude)
		if d < bestDist || (d == bestDist && p.Name < best.Name) {
			best, bestDist = p, d
		}
	}
	return best
}

// ProvinceNames returns every table entry's name, sorted, for tests that
// need to assert on the table's shape without duplicating its contents.
func ProvinceNames() []string {
	names := make([]string, len(provinces))
	for i, p := range provinces {
		names[i] = p.Name
	}
	sort.Strings(names)
	return names
}
