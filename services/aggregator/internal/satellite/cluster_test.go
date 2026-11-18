package satellite

import (
	"fmt"
	"math"
	"math/rand"
	"testing"
	"time"
)

func TestNeighborCellsCoverOneKm(t *testing.T) {
	rng := rand.New(rand.NewSource(1)) // fixed seed: deterministic, not flaky
	for i := 0; i < 10000; i++ {
		lat := -11 + rng.Float64()*17 // -11..6, Indonesia's latitude range
		lon := 95 + rng.Float64()*45
		bearing := rng.Float64() * 2 * math.Pi
		distKm := rng.Float64() // 0..1 km, i.e. within the connectivity bound

		// Destination point at distKm along bearing from (lat,lon), via
		// the small-angle approximation (adequate at this scale).
		dLat := (distKm / 111.32) * math.Cos(bearing)
		dLon := (distKm / (111.32 * math.Cos(lat*math.Pi/180))) * math.Sin(bearing)
		lat2, lon2 := lat+dLat, lon+dLon

		clat1, clon1 := Cell(lat, lon)
		clat2, clon2 := Cell(lat2, lon2)
		span := NeighborLonSpan(lat)

		if abs(clat2-clat1) > 1 {
			t.Fatalf("pair %d: latitude cell delta %d exceeds +/-1 (lat=%.5f lon=%.5f -> lat2=%.5f lon2=%.5f, distKm=%.3f)", i, clat2-clat1, lat, lon, lat2, lon2, distKm)
		}
		if abs(clon2-clon1) > span {
			t.Fatalf("pair %d: longitude cell delta %d exceeds neighbor span %d (lat=%.5f lon=%.5f -> lat2=%.5f lon2=%.5f, distKm=%.3f)", i, clon2-clon1, span, lat, lon, lat2, lon2, distKm)
		}
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// gridDetection is a small helper for building synthetic detections in
// cluster_test.go without repeating every field each time.
func gridDetection(sat, acqDate, acqTime string, lat, lon float64, confidence string, at time.Time) Detection {
	return Detection{
		Satellite: sat, AcqDate: acqDate, AcqTimeText: acqTime,
		LatText: fmt.Sprintf("%.5f", lat), LonText: fmt.Sprintf("%.5f", lon),
		Lat: lat, Lon: lon, Confidence: confidence, At: at,
	}
}

func TestComponentsTransitiveAndSingletons(t *testing.T) {
	base := time.Date(2026, 11, 12, 2, 0, 0, 0, time.UTC)

	// A-B-C chain: A-B and B-C are each <=1km, but A-C alone (1.8 km) is
	// not. Transitivity (FIRE.4c) must still put all three in one fire.
	a := gridDetection("N20", "2026-11-12", "200", -2.5000, 113.8000, "n", base)
	b := gridDetection("N20", "2026-11-12", "205", -2.5080, 113.8000, "n", base.Add(5*time.Minute))
	c := gridDetection("N20", "2026-11-12", "210", -2.5160, 113.8000, "n", base.Add(10*time.Minute))
	if DistanceKm(a.Lat, a.Lon, c.Lat, c.Lon) <= 1.0 {
		t.Fatalf("test fixture invalid: A-C must be >1km apart to actually exercise transitivity, got %.3fkm", DistanceKm(a.Lat, a.Lon, c.Lat, c.Lon))
	}

	// Two satellites 0.4 km apart, same fire despite different satellite.
	n20 := gridDetection("N20", "2026-11-12", "300", -3.0000, 114.0000, "h", base)
	n21 := gridDetection("N21", "2026-11-12", "302", -3.0036, 114.0000, "h", base.Add(2*time.Minute))

	// Isolated singleton.
	lone := gridDetection("N20", "2026-11-12", "400", 1.0000, 100.0000, "n", base)

	// A pair 0.5km apart but 7 hours apart in time: both bounds must be
	// satisfied, so this must NOT connect -- two singletons, not a fire.
	timeFar1 := gridDetection("N20", "2026-11-12", "000", 5.0000, 99.0000, "n", base)
	timeFar2 := gridDetection("N20", "2026-11-12", "700", 5.0045, 99.0000, "n", base.Add(7*time.Hour))

	// A low-confidence detection near the chain must never join it.
	low := gridDetection("N20", "2026-11-12", "201", -2.5005, 113.8000, "l", base)

	fires, singletons := Components([]Detection{a, b, c, n20, n21, lone, timeFar1, timeFar2, low})

	if len(fires) != 2 {
		t.Fatalf("got %d fires, want 2 (A-B-C chain, N20-N21 pair); fires=%+v", len(fires), fires)
	}
	if len(fires[0]) != 3 {
		t.Fatalf("fires[0] has %d members, want 3 (A-B-C)", len(fires[0]))
	}
	for _, d := range fires[0] {
		if d.Key() == low.Key() {
			t.Fatalf("low-confidence detection must never join a fire")
		}
	}
	if len(fires[1]) != 2 {
		t.Fatalf("fires[1] has %d members, want 2 (N20-N21)", len(fires[1]))
	}

	wantSingletons := map[string]bool{lone.Key(): true, timeFar1.Key(): true, timeFar2.Key(): true}
	if len(singletons) != len(wantSingletons) {
		t.Fatalf("got %d singletons, want %d; singletons=%+v", len(singletons), len(wantSingletons), singletons)
	}
	for _, s := range singletons {
		if !wantSingletons[s.Key()] {
			t.Errorf("unexpected singleton %q", s.Key())
		}
	}
}

func TestComponentsIsDeterministicAcrossInputOrder(t *testing.T) {
	base := time.Date(2026, 11, 12, 2, 0, 0, 0, time.UTC)
	a := gridDetection("N20", "2026-11-12", "200", -2.5000, 113.8000, "n", base)
	b := gridDetection("N20", "2026-11-12", "205", -2.5080, 113.8000, "n", base.Add(5*time.Minute))

	fires1, singles1 := Components([]Detection{a, b})
	fires2, singles2 := Components([]Detection{b, a})

	if len(fires1) != 1 || len(fires2) != 1 {
		t.Fatalf("expected exactly one fire regardless of input order")
	}
	if fires1[0][0].Key() != fires2[0][0].Key() || fires1[0][1].Key() != fires2[0][1].Key() {
		t.Fatalf("member order within a fire must not depend on input order: %v vs %v", fires1[0], fires2[0])
	}
	if len(singles1) != 0 || len(singles2) != 0 {
		t.Fatalf("no singletons expected")
	}
}
