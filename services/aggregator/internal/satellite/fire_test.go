package satellite

import (
	"math/big"
	"testing"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
)

func TestSeverityThresholds(t *testing.T) {
	cases := []struct {
		name  string
		count int
		frp   string // decimal text, parsed via big.Rat
		want  contracts.Severity
	}{
		{"19 detections and 99.99 MW is SIAGA, not AWAS", 19, "99.99", contracts.SeveritySiaga},
		{"exactly 20 detections is AWAS regardless of FRP", 20, "0", contracts.SeverityAwas},
		{"2 detections but 100 MW is AWAS", 2, "100", contracts.SeverityAwas},
		{"5 detections and 29.99 MW is WASPADA", 5, "29.99", contracts.SeverityWaspada},
		{"exactly 6 detections is SIAGA regardless of FRP", 6, "0", contracts.SeveritySiaga},
		{"0 detections and 0 MW is WASPADA", 0, "0", contracts.SeverityWaspada},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			frp := new(big.Rat)
			if _, ok := frp.SetString(c.frp); !ok {
				t.Fatalf("test fixture: %q is not a valid decimal", c.frp)
			}
			if got := Severity(c.count, frp); got != c.want {
				t.Errorf("Severity(%d, %s) = %v, want %v", c.count, c.frp, got, c.want)
			}
		})
	}
}

func TestBuildFireEvent(t *testing.T) {
	base := time.Date(2026, 11, 12, 2, 43, 0, 0, time.UTC)
	members := []Detection{
		{Satellite: "N20", AcqDate: "2026-11-12", AcqTimeText: "243", LatText: "-2.51230", LonText: "113.80440", Lat: -2.51230, Lon: 113.80440, Confidence: "h", FRPText: "10.2", At: base},
		{Satellite: "N20", AcqDate: "2026-11-12", AcqTimeText: "245", LatText: "-2.51231", LonText: "113.80441", Lat: -2.51231, Lon: 113.80441, Confidence: "n", FRPText: "2.3", At: base.Add(2 * time.Minute)},
	}

	event := BuildEvent("fire-fixture-1", members)

	if event.Source != contracts.SourceSatellite {
		t.Errorf("Source = %v, want SATELIT", event.Source)
	}
	if event.HazardType != contracts.HazardWildfire {
		t.Errorf("HazardType = %v, want WILDFIRE", event.HazardType)
	}
	if event.SourceRefID != "fire-fixture-1" {
		t.Errorf("SourceRefID = %v, want fire-fixture-1", event.SourceRefID)
	}
	if event.OccurredAt != "2026-11-12T02:43:00Z" {
		t.Errorf("OccurredAt = %v, want earliest member's detection time", event.OccurredAt)
	}
	if event.Severity != contracts.SeverityWaspada {
		t.Errorf("Severity = %v, want WASPADA (2 detections, 12.5 MW)", event.Severity)
	}
	wantLat, wantLon := -2.512305, 113.804405
	if event.Latitude != wantLat || event.Longitude != wantLon {
		t.Errorf("centroid = (%v,%v), want (%v,%v)", event.Latitude, event.Longitude, wantLat, wantLon)
	}
	attrs := event.Attributes
	if attrs["detection_count"] != 2 {
		t.Errorf("detection_count = %v, want 2", attrs["detection_count"])
	}
	if attrs["max_confidence"] != "h" {
		t.Errorf("max_confidence = %v, want h (highest of h,n)", attrs["max_confidence"])
	}
	if got, ok := attrs["total_frp_mw"].(interface{ String() string }); ok {
		_ = got
	}
	gotFRP := attrs["total_frp_mw"]
	if gotFRP == nil {
		t.Fatalf("total_frp_mw missing")
	}
	if s, ok := gotFRP.(interface{ String() string }); !ok || s.String() != "12.5" {
		t.Errorf("total_frp_mw = %v, want 12.5 (json.Number)", gotFRP)
	}
	keys, ok := attrs["detection_keys"].([]string)
	if !ok || len(keys) != 2 {
		t.Fatalf("detection_keys = %v, want 2 string entries", attrs["detection_keys"])
	}
	wantKeys := []string{"N20|2026-11-12|243|-2.51230|113.80440", "N20|2026-11-12|245|-2.51231|113.80441"}
	for i, want := range wantKeys {
		if keys[i] != want {
			t.Errorf("detection_keys[%d] = %q, want %q", i, keys[i], want)
		}
	}
}

func TestBuildFireEventOrderIndependence(t *testing.T) {
	base := time.Date(2026, 11, 12, 2, 43, 0, 0, time.UTC)
	a := Detection{Satellite: "N20", AcqDate: "2026-11-12", AcqTimeText: "243", LatText: "-2.51230", LonText: "113.80440", Lat: -2.51230, Lon: 113.80440, Confidence: "h", FRPText: "10.2", At: base}
	b := Detection{Satellite: "N21", AcqDate: "2026-11-12", AcqTimeText: "244", LatText: "-2.51235", LonText: "113.80445", Lat: -2.51235, Lon: 113.80445, Confidence: "n", FRPText: "3.1", At: base.Add(time.Minute)}
	c := Detection{Satellite: "N20", AcqDate: "2026-11-12", AcqTimeText: "245", LatText: "-2.51231", LonText: "113.80441", Lat: -2.51231, Lon: 113.80441, Confidence: "l", FRPText: "1.0", At: base.Add(2 * time.Minute)}

	e1 := BuildEvent("fire-x", []Detection{a, b, c})
	e2 := BuildEvent("fire-x", []Detection{c, a, b})
	e3 := BuildEvent("fire-x", []Detection{b, c, a})

	for _, pair := range [][2]contracts.HazardEvent{{e1, e2}, {e1, e3}} {
		if pair[0].Latitude != pair[1].Latitude || pair[0].Longitude != pair[1].Longitude {
			t.Errorf("centroid depends on member order: %v vs %v", pair[0], pair[1])
		}
		k1 := pair[0].Attributes["detection_keys"].([]string)
		k2 := pair[1].Attributes["detection_keys"].([]string)
		for i := range k1 {
			if k1[i] != k2[i] {
				t.Errorf("detection_keys order depends on member order: %v vs %v", k1, k2)
			}
		}
	}
}
