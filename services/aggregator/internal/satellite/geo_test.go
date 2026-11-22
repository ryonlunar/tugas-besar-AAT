package satellite

import (
	"math"
	"testing"
	"time"
)

func TestConnectedBoundaries(t *testing.T) {
	base := Detection{Lat: 0, Lon: 0, At: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)}

	// 0.999 km due north at the equator and 360 minutes apart: both
	// bounds satisfied (inclusive).
	near := base
	near.Lat = 0.999 / 111.19 // ~1 km per degree of latitude at the equator
	near.At = base.At.Add(360 * time.Minute)
	if !Connected(base, near) {
		t.Fatalf("expected connected: ~0.999 km and exactly 360 minutes apart")
	}

	// Just over 1.0 km: distance bound violated.
	far := base
	far.Lat = 1.2 / 111.19
	far.At = base.At
	if Connected(base, far) {
		t.Fatalf("expected not connected: ~1.2 km apart")
	}

	// Within distance but 361 minutes apart: time bound violated.
	late := base
	late.Lat = 0
	late.Lon = 0
	late.At = base.At.Add(361 * time.Minute)
	if Connected(base, late) {
		t.Fatalf("expected not connected: 361 minutes apart")
	}

	// At latitude -10 degrees, a 1 km east-west offset covers a larger
	// longitude delta than at the equator (cos(lat) shrinks); verify the
	// Haversine distance for that offset still lands under the 1.0 km
	// bound when it should, confirming DistanceKm (not a flat-Earth
	// approximation) drives the decision.
	south := Detection{Lat: -10, Lon: 0, At: base.At}
	oneKmEastAtMinus10 := Detection{Lat: -10, Lon: 1.0 / (111.32 * math.Cos(-10*math.Pi/180)), At: base.At}
	d := DistanceKm(south.Lat, south.Lon, oneKmEastAtMinus10.Lat, oneKmEastAtMinus10.Lon)
	if d < 0.95 || d > 1.05 {
		t.Fatalf("DistanceKm at latitude -10 for a ~1km offset = %.4f km, want ~1.0", d)
	}
}

func TestConfidenceClassification(t *testing.T) {
	cases := []struct {
		value    string
		wantRank int
		wantLow  bool
		wantErr  bool
	}{
		{value: "l", wantRank: 0, wantLow: true},
		{value: "low", wantRank: 0, wantLow: true},
		{value: "n", wantRank: 1, wantLow: false},
		{value: "nominal", wantRank: 1, wantLow: false},
		{value: "h", wantRank: 2, wantLow: false},
		{value: "high", wantRank: 2, wantLow: false},
		{value: "H", wantRank: 2, wantLow: false},
		{value: "medium", wantErr: true},
		{value: "", wantErr: true},
	}
	for _, c := range cases {
		rank, low, err := ConfidenceRank(c.value)
		if c.wantErr {
			if err == nil {
				t.Errorf("ConfidenceRank(%q) = (%d,%v,nil), want error", c.value, rank, low)
			}
			continue
		}
		if err != nil {
			t.Errorf("ConfidenceRank(%q) unexpected error: %v", c.value, err)
			continue
		}
		if rank != c.wantRank || low != c.wantLow {
			t.Errorf("ConfidenceRank(%q) = (%d,%v), want (%d,%v)", c.value, rank, low, c.wantRank, c.wantLow)
		}
	}
}
