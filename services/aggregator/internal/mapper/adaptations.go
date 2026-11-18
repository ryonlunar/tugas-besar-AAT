package mapper

import (
	"fmt"
	"strings"
)

// VolcanicAdaptations is an explicit, named, opt-in registry of PVMBG schema
// workarounds (D3). The zero value is the strict policy: any incompatible
// change is parked. Each adaptation only recognizes the one narrow shape
// produced by the PVMBG mock's corresponding incompatible mode; anything else
// still parks under the normal schema_incompatible reason.
type VolcanicAdaptations struct {
	// LevelAlias accepts "level" in place of "alert_level" when alert_level is
	// absent (PVMBG mock version 3 / mode "rename_field"). If both are present
	// and disagree, the input still parks with reason "ambiguous_field".
	LevelAlias bool
	// EruptionCountString accepts eruption_count_24h as a decimal string
	// matching ^(0|[1-9][0-9]{0,8})$ (PVMBG mock version 4 / mode
	// "type_change"). Any other string shape (leading zeros, decimals,
	// negative numbers, non-digits) still parks.
	EruptionCountString bool
	// AshHeightOptional treats a missing ash_column_height_m as "the
	// attribute is unknown" rather than a required field (PVMBG mock version
	// 5 / mode "missing_required_field"). The attribute is omitted, not
	// filled with a zero value.
	AshHeightOptional bool
}

var volcanicAdaptationNames = map[string]func(*VolcanicAdaptations){
	"level_alias":           func(a *VolcanicAdaptations) { a.LevelAlias = true },
	"eruption_count_string": func(a *VolcanicAdaptations) { a.EruptionCountString = true },
	"ash_height_optional":   func(a *VolcanicAdaptations) { a.AshHeightOptional = true },
}

// ParseVolcanicAdaptations parses a comma-separated adaptation name list (the
// PVMBG_SCHEMA_ADAPTERS configuration value). An empty/blank string yields the
// strict zero value. An unrecognized name is a startup error, never silently
// ignored.
func ParseVolcanicAdaptations(csv string) (VolcanicAdaptations, error) {
	var a VolcanicAdaptations
	if strings.TrimSpace(csv) == "" {
		return a, nil
	}
	for _, name := range strings.Split(csv, ",") {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		apply, ok := volcanicAdaptationNames[name]
		if !ok {
			return VolcanicAdaptations{}, fmt.Errorf("unknown PVMBG schema adapter %q", name)
		}
		apply(&a)
	}
	return a, nil
}

// WithVolcanicAdaptations returns a copy of m configured with the given
// adaptations. The Mapper itself stays immutable so a shared strict Mapper
// cannot be mutated out from under another caller.
func (m *Mapper) WithVolcanicAdaptations(a VolcanicAdaptations) *Mapper {
	copied := *m
	copied.volcanicAdaptations = a
	return &copied
}

// eruptionCountStringPattern is deliberately narrow (D3): it accepts only the
// canonical decimal rendering strconv.Itoa would produce for a non-negative
// int, never "03", "3.0", "-1", or "" — those stay parked even when the
// adaptation is enabled.
func canonicalEruptionCountString(value string) (int64, bool) {
	if value == "0" {
		return 0, true
	}
	if len(value) == 0 || value[0] < '1' || value[0] > '9' {
		return 0, false
	}
	for i := 1; i < len(value); i++ {
		if value[i] < '0' || value[i] > '9' {
			return 0, false
		}
	}
	if len(value) > 9 {
		return 0, false
	}
	var n int64
	for i := 0; i < len(value); i++ {
		n = n*10 + int64(value[i]-'0')
	}
	return n, true
}
