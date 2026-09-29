package mapper

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestMapSeismicUsesMagnitudeThresholds(t *testing.T) {
	m := New(map[string]VolcanoLocation{})
	cases := []struct {
		magnitude float64
		want      Severity
	}{
		{-0.2, SeverityNormal}, {4.9, SeverityNormal}, {5.0, SeverityWaspada}, {6.49, SeverityWaspada}, {6.5, SeveritySiaga},
	}
	for _, tc := range cases {
		raw := []byte(fmt.Sprintf(`{"event_id":"eq-1","magnitude":%v,"depth_km":10,"epicenter_lat":-6,"epicenter_lon":107,"region_name":"Wilayah Simulasi","occurred_at":"2026-09-28T10:00:00+07:00","potential_tsunami":false}`, tc.magnitude))
		got, err := m.MapSeismic(raw, nil, "hazard-1", time.Date(2026, 9, 28, 10, 1, 2, 0, time.FixedZone("WIB", 7*3600)))
		if err != nil {
			t.Fatalf("magnitude %v: %v", tc.magnitude, err)
		}
		if got.Severity != tc.want {
			t.Errorf("magnitude %v severity=%s, want %s", tc.magnitude, got.Severity, tc.want)
		}
		if got.OccurredAt != "2026-09-28T03:00:00Z" || got.IngestedAt != "2026-09-28T03:01:02Z" {
			t.Errorf("timestamps not normalized to UTC: %+v", got)
		}
	}
}

func TestMapSeismicUsesHighestRelatedWarningAndPreservesExtras(t *testing.T) {
	m := New(map[string]VolcanoLocation{})
	event := []byte(`{"event_id":"eq-1","magnitude":4.9,"depth_km":10,"epicenter_lat":-6,"epicenter_lon":107,"region_name":"Wilayah Simulasi","occurred_at":"2026-09-28T10:00:00Z","potential_tsunami":true,"tsunami_warnings":{"legacy":"keep"},"nested_extra":{"enabled":true,"level":2}}`)
	warnings := [][]byte{
		[]byte(`{"warning_id":"w-b","related_event_id":"eq-1","threat_level":"Waspada","affected_zones":["A"],"estimated_arrival":"2026-09-28T11:00:00Z","issuer_extension":"preserve"}`),
		[]byte(`{"warning_id":"w-a","related_event_id":"eq-1","threat_level":"Awas","affected_zones":["B"],"estimated_arrival":"2026-09-28T11:05:00Z"}`),
		[]byte(`{"warning_id":"w-other","related_event_id":"another-event","threat_level":"Awas","affected_zones":["C"],"estimated_arrival":"2026-09-28T11:10:00Z"}`),
	}
	got, err := m.MapSeismic(event, warnings, "hazard-1", time.Date(2026, 9, 28, 10, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if got.Severity != SeverityAwas {
		t.Fatalf("severity=%s, want AWAS", got.Severity)
	}
	list, ok := got.Attributes["tsunami_warnings"].([]any)
	if !ok || len(list) != 2 {
		t.Fatalf("related warnings not preserved: %#v", got.Attributes["tsunami_warnings"])
	}
	first := list[0].(map[string]any)
	if first["warning_id"] != "w-a" {
		t.Fatalf("warnings not sorted: %#v", list)
	}
	extras, ok := got.Attributes["source_extras"].(map[string]any)
	if !ok {
		t.Fatalf("collision extras missing: %#v", got.Attributes["source_extras"])
	}
	if _, ok := extras["tsunami_warnings"]; !ok {
		t.Fatalf("colliding source field lost: %#v", extras)
	}
	if _, ok := got.Attributes["nested_extra"].(map[string]any); !ok {
		t.Fatalf("nested extra lost: %#v", got.Attributes)
	}
	second := list[1].(map[string]any)
	issuer := second["issuer_extension"]
	if issuer != "preserve" {
		t.Fatalf("warning extension lost: %#v", issuer)
	}
	strongEvent := []byte(`{"event_id":"eq-1","magnitude":7.0,"depth_km":10,"epicenter_lat":-6,"epicenter_lon":107,"region_name":"Wilayah Simulasi","occurred_at":"2026-09-28T10:00:00Z","potential_tsunami":true}`)
	got, err = m.MapSeismic(strongEvent, warnings[:1], "hazard-1", time.Date(2026, 9, 28, 10, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if got.Severity != SeverityWaspada {
		t.Fatalf("warning must determine severity when present: got %s, want WASPADA", got.Severity)
	}
}

func TestMapVolcanicPreservesV1V2AndDynamicFields(t *testing.T) {
	m := New(DefaultVolcanoLocations())
	v1 := []byte(`{"report_id":"pv-1","volcano_id":"sim-tamriel-red-mountain","alert_level":"Siaga","eruption_count_24h":2,"ash_column_height_m":500,"reported_at":"2026-09-28T10:00:00+07:00","nested_extension":{"confidence":"unknown"}}`)
	got, err := m.MapVolcanic(v1, "hazard-2", time.Date(2026, 9, 28, 3, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if got.Severity != SeveritySiaga || got.AreaName != "Gunung Simulasi Red Mountain (Tamriel)" || got.Latitude != -7 || got.Longitude != 108 {
		t.Fatalf("bad mapping: %+v", got)
	}
	if _, ok := got.Attributes["confidence_level"]; ok {
		t.Fatal("v1 must not invent confidence_level")
	}
	if _, ok := got.Attributes["nested_extension"].(map[string]any); !ok {
		t.Fatalf("nested dynamic field lost: %#v", got.Attributes)
	}

	v2 := []byte(`{"report_id":"pv-1","volcano_id":"sim-tamriel-red-mountain","alert_level":"Siaga","eruption_count_24h":2,"ash_column_height_m":500,"reported_at":"2026-09-28T10:00:00Z","confidence_level":0.9}`)
	got, err = m.MapVolcanic(v2, "hazard-2", time.Date(2026, 9, 28, 3, 1, 0, 0, time.UTC))
	if err != nil {
		t.Fatal(err)
	}
	if got.Attributes["confidence_level"] != json.Number("0.9") {
		t.Fatalf("confidence not preserved numerically: %#v", got.Attributes["confidence_level"])
	}
}

func TestDefaultVolcanoLocationsAreSyntheticTamrielFixtures(t *testing.T) {
	locations := DefaultVolcanoLocations()
	for _, id := range []string{"sim-tamriel-red-mountain", "sim-tamriel-ash-peak", "sim-tamriel-north-crag"} {
		location, ok := locations[id]
		if !ok || !strings.Contains(location.AreaName, "Tamriel") {
			t.Fatalf("default location %q must be a labeled Tamriel fixture: %+v", id, location)
		}
	}
}

func TestMapperRejectsUnknownVolcanoInvalidEnumsAndDuplicateKeys(t *testing.T) {
	m := New(map[string]VolcanoLocation{})
	volcanic := []byte(`{"report_id":"pv-1","volcano_id":"unknown","alert_level":"Siaga","eruption_count_24h":2,"ash_column_height_m":500,"reported_at":"2026-09-28T10:00:00Z"}`)
	if _, err := m.MapVolcanic(volcanic, "h", time.Now()); err == nil {
		t.Fatal("unknown volcano id must be rejected")
	}
	badEnum := []byte(`{"report_id":"pv-1","volcano_id":"known","alert_level":"SIAGA","eruption_count_24h":2,"ash_column_height_m":500,"reported_at":"2026-09-28T10:00:00Z"}`)
	m = New(map[string]VolcanoLocation{"known": {AreaName: "Simulasi", Latitude: 1, Longitude: 2}})
	if _, err := m.MapVolcanic(badEnum, "h", time.Now()); err == nil {
		t.Fatal("unknown enum must be rejected")
	}
	duplicate := []byte(`{"event_id":"first","event_id":"second","magnitude":5,"depth_km":10,"epicenter_lat":-6,"epicenter_lon":107,"region_name":"Simulasi","occurred_at":"2026-09-28T10:00:00Z","potential_tsunami":false}`)
	if _, err := m.MapSeismic(duplicate, nil, "h", time.Now()); err == nil {
		t.Fatal("duplicate JSON key must be rejected")
	}
}

func TestMapperRejectsTimestampWithoutTimezone(t *testing.T) {
	m := New(map[string]VolcanoLocation{})
	raw := []byte(`{"event_id":"eq-1","magnitude":5,"depth_km":10,"epicenter_lat":-6,"epicenter_lon":107,"region_name":"Simulasi","occurred_at":"2026-09-28T10:00:00","potential_tsunami":false}`)
	if _, err := m.MapSeismic(raw, nil, "h", time.Now()); err == nil {
		t.Fatal("timestamp without timezone must be rejected")
	}
}
