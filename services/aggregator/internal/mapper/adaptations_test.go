package mapper

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestParseVolcanicAdaptations(t *testing.T) {
	cases := []struct {
		csv     string
		want    VolcanicAdaptations
		wantErr bool
	}{
		{csv: "", want: VolcanicAdaptations{}},
		{csv: "level_alias", want: VolcanicAdaptations{LevelAlias: true}},
		{csv: "level_alias,eruption_count_string", want: VolcanicAdaptations{LevelAlias: true, EruptionCountString: true}},
		{csv: "level_alias,eruption_count_string,ash_height_optional", want: VolcanicAdaptations{LevelAlias: true, EruptionCountString: true, AshHeightOptional: true}},
		{csv: " level_alias , ash_height_optional ", want: VolcanicAdaptations{LevelAlias: true, AshHeightOptional: true}},
		{csv: "bogus_name", wantErr: true},
		{csv: "level_alias,bogus_name", wantErr: true},
	}
	for _, c := range cases {
		got, err := ParseVolcanicAdaptations(c.csv)
		if c.wantErr {
			if err == nil {
				t.Fatalf("%q: expected error, got %+v", c.csv, got)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%q: %v", c.csv, err)
		}
		if got != c.want {
			t.Fatalf("%q: got %+v, want %+v", c.csv, got, c.want)
		}
	}
}

func volcanicPayload(fields map[string]string, order []string) string {
	parts := make([]string, 0, len(order))
	for _, key := range order {
		parts = append(parts, `"`+key+`":`+fields[key])
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func TestVolcanicAdaptations(t *testing.T) {
	base := map[string]string{
		"report_id":           `"adapt-1"`,
		"volcano_id":          `"sim-tamriel-red-mountain"`,
		"alert_level":         `"Siaga"`,
		"eruption_count_24h":  `3`,
		"ash_column_height_m": `500`,
		"reported_at":         `"2026-11-12T02:00:00Z"`,
		"confidence_level":    `0.75`,
	}
	order := []string{"report_id", "volcano_id", "alert_level", "eruption_count_24h", "ash_column_height_m", "reported_at", "confidence_level"}
	v2 := volcanicPayload(base, order)

	m := New(DefaultVolcanoLocations())
	now := time.Date(2026, 11, 18, 0, 0, 0, 0, time.UTC)

	v2Event, err := m.MapVolcanic([]byte(v2), "hazard-v2", now)
	if err != nil {
		t.Fatalf("baseline v2 mapping failed: %v", err)
	}

	withoutHazardID := func(e HazardEvent) HazardEvent {
		e.HazardID = ""
		e.IngestedAt = ""
		return e
	}

	t.Run("mode3_without_adaptation_parks", func(t *testing.T) {
		mode3Fields := map[string]string{}
		for k, v := range base {
			mode3Fields[k] = v
		}
		delete(mode3Fields, "alert_level")
		mode3Fields["level"] = base["alert_level"]
		mode3Order := []string{"report_id", "volcano_id", "level", "eruption_count_24h", "ash_column_height_m", "reported_at", "confidence_level"}
		payload := volcanicPayload(mode3Fields, mode3Order)
		if _, err := m.MapVolcanic([]byte(payload), "hazard-3", now); err == nil {
			t.Fatal("mode 3 payload must park without level_alias adaptation")
		}
	})

	t.Run("mode3_with_level_alias_matches_v2", func(t *testing.T) {
		mode3Fields := map[string]string{}
		for k, v := range base {
			mode3Fields[k] = v
		}
		delete(mode3Fields, "alert_level")
		mode3Fields["level"] = base["alert_level"]
		mode3Order := []string{"report_id", "volcano_id", "level", "eruption_count_24h", "ash_column_height_m", "reported_at", "confidence_level"}
		payload := volcanicPayload(mode3Fields, mode3Order)
		adapted := m.WithVolcanicAdaptations(VolcanicAdaptations{LevelAlias: true})
		got, err := adapted.MapVolcanic([]byte(payload), "hazard-3", now)
		if err != nil {
			t.Fatalf("mode 3 with level_alias must succeed: %v", err)
		}
		got.SourceRefID = v2Event.SourceRefID // same report_id already; defensive no-op
		if !reflect.DeepEqual(withoutHazardID(got), withoutHazardID(v2Event)) {
			t.Fatalf("mode3+level_alias result differs from v2:\n got=%+v\nwant=%+v", got, v2Event)
		}
	})

	t.Run("mode4_without_adaptation_parks", func(t *testing.T) {
		mode4Fields := map[string]string{}
		for k, v := range base {
			mode4Fields[k] = v
		}
		mode4Fields["eruption_count_24h"] = `"3"`
		payload := volcanicPayload(mode4Fields, order)
		if _, err := m.MapVolcanic([]byte(payload), "hazard-4", now); err == nil {
			t.Fatal("mode 4 payload must park without eruption_count_string adaptation")
		}
	})

	t.Run("mode4_with_eruption_count_string_matches_v2", func(t *testing.T) {
		mode4Fields := map[string]string{}
		for k, v := range base {
			mode4Fields[k] = v
		}
		mode4Fields["eruption_count_24h"] = `"3"`
		payload := volcanicPayload(mode4Fields, order)
		adapted := m.WithVolcanicAdaptations(VolcanicAdaptations{EruptionCountString: true})
		got, err := adapted.MapVolcanic([]byte(payload), "hazard-4", now)
		if err != nil {
			t.Fatalf("mode 4 with eruption_count_string must succeed: %v", err)
		}
		if !reflect.DeepEqual(withoutHazardID(got), withoutHazardID(v2Event)) {
			t.Fatalf("mode4+eruption_count_string result differs from v2:\n got=%+v\nwant=%+v", got, v2Event)
		}
	})

	t.Run("mode4_rejects_non_canonical_strings_even_with_adaptation", func(t *testing.T) {
		adapted := m.WithVolcanicAdaptations(VolcanicAdaptations{EruptionCountString: true})
		for _, bad := range []string{`"03"`, `"3.0"`, `"-1"`, `""`, `"abc"`} {
			fields := map[string]string{}
			for k, v := range base {
				fields[k] = v
			}
			fields["eruption_count_24h"] = bad
			payload := volcanicPayload(fields, order)
			if _, err := adapted.MapVolcanic([]byte(payload), "hazard-4b", now); err == nil {
				t.Fatalf("eruption_count_24h=%s must still be rejected", bad)
			}
		}
	})

	t.Run("mode5_without_adaptation_parks", func(t *testing.T) {
		mode5Fields := map[string]string{}
		for k, v := range base {
			mode5Fields[k] = v
		}
		delete(mode5Fields, "ash_column_height_m")
		mode5Order := []string{"report_id", "volcano_id", "alert_level", "eruption_count_24h", "reported_at", "confidence_level"}
		payload := volcanicPayload(mode5Fields, mode5Order)
		if _, err := m.MapVolcanic([]byte(payload), "hazard-5", now); err == nil {
			t.Fatal("mode 5 payload must park without ash_height_optional adaptation")
		}
	})

	t.Run("mode5_with_ash_height_optional_omits_attribute", func(t *testing.T) {
		mode5Fields := map[string]string{}
		for k, v := range base {
			mode5Fields[k] = v
		}
		delete(mode5Fields, "ash_column_height_m")
		mode5Order := []string{"report_id", "volcano_id", "alert_level", "eruption_count_24h", "reported_at", "confidence_level"}
		payload := volcanicPayload(mode5Fields, mode5Order)
		adapted := m.WithVolcanicAdaptations(VolcanicAdaptations{AshHeightOptional: true})
		got, err := adapted.MapVolcanic([]byte(payload), "hazard-5", now)
		if err != nil {
			t.Fatalf("mode 5 with ash_height_optional must succeed: %v", err)
		}
		if _, ok := got.Attributes["ash_column_height_m"]; ok {
			t.Fatalf("ash_column_height_m attribute must be omitted, not zero-filled: %+v", got.Attributes)
		}
		// Not equal to v2's result: v2 carries the attribute, the adapted mode5 does not.
		if reflect.DeepEqual(withoutHazardID(got), withoutHazardID(v2Event)) {
			t.Fatal("mode5+ash_height_optional must differ from v2 (attribute is missing, not zero)")
		}
	})

	t.Run("ambiguous_level_and_alert_level_disagree", func(t *testing.T) {
		fields := map[string]string{}
		for k, v := range base {
			fields[k] = v
		}
		fields["level"] = `"Awas"` // disagrees with alert_level="Siaga"
		ambiguousOrder := []string{"report_id", "volcano_id", "alert_level", "level", "eruption_count_24h", "ash_column_height_m", "reported_at", "confidence_level"}
		payload := volcanicPayload(fields, ambiguousOrder)
		adapted := m.WithVolcanicAdaptations(VolcanicAdaptations{LevelAlias: true})
		_, err := adapted.MapVolcanic([]byte(payload), "hazard-amb", now)
		if err == nil || !strings.Contains(err.Error(), "ambiguous_field") {
			t.Fatalf("expected ambiguous_field error, got %v", err)
		}
	})

	t.Run("agreeing_level_and_alert_level_is_not_ambiguous", func(t *testing.T) {
		fields := map[string]string{}
		for k, v := range base {
			fields[k] = v
		}
		fields["level"] = base["alert_level"] // agrees
		agreeOrder := []string{"report_id", "volcano_id", "alert_level", "level", "eruption_count_24h", "ash_column_height_m", "reported_at", "confidence_level"}
		payload := volcanicPayload(fields, agreeOrder)
		adapted := m.WithVolcanicAdaptations(VolcanicAdaptations{LevelAlias: true})
		if _, err := adapted.MapVolcanic([]byte(payload), "hazard-agree", now); err != nil {
			t.Fatalf("agreeing level/alert_level must not be rejected: %v", err)
		}
	})
}

func TestWithVolcanicAdaptationsDoesNotMutateOriginal(t *testing.T) {
	m := New(DefaultVolcanoLocations())
	_ = m.WithVolcanicAdaptations(VolcanicAdaptations{LevelAlias: true})
	payload := []byte(`{"report_id":"r1","volcano_id":"sim-tamriel-red-mountain","level":"Siaga","eruption_count_24h":3,"ash_column_height_m":500,"reported_at":"2026-11-12T02:00:00Z"}`)
	if _, err := m.MapVolcanic(payload, "h", time.Now()); err == nil {
		t.Fatal("original Mapper must remain strict after WithVolcanicAdaptations returns a copy")
	}
}
