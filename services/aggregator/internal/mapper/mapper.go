package mapper

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
	"time"
)

type Source string
type HazardType string
type Severity string

const (
	SourceBMKG  Source = "BMKG"
	SourcePVMBG Source = "PVMBG"

	HazardSeismic  HazardType = "SEISMIC"
	HazardVolcanic HazardType = "VOLCANIC"

	SeverityNormal  Severity = "NORMAL"
	SeverityWaspada Severity = "WASPADA"
	SeveritySiaga   Severity = "SIAGA"
	SeverityAwas    Severity = "AWAS"
)

type HazardEvent struct {
	HazardID    string         `json:"hazard_id"`
	Source      Source         `json:"source"`
	SourceRefID string         `json:"source_ref_id"`
	HazardType  HazardType     `json:"hazard_type"`
	Severity    Severity       `json:"severity"`
	AreaName    string         `json:"area_name"`
	Latitude    float64        `json:"latitude"`
	Longitude   float64        `json:"longitude"`
	OccurredAt  string         `json:"occurred_at"`
	IngestedAt  string         `json:"ingested_at"`
	Attributes  map[string]any `json:"attributes"`
}

type VolcanoLocation struct {
	AreaName  string
	Latitude  float64
	Longitude float64
}

type Mapper struct{ volcanoes map[string]VolcanoLocation }

func New(volcanoes map[string]VolcanoLocation) *Mapper {
	copy := make(map[string]VolcanoLocation, len(volcanoes))
	for id, location := range volcanoes {
		copy[id] = location
	}
	return &Mapper{volcanoes: copy}
}

// DefaultVolcanoLocations are explicitly synthetic coordinates used only by the mocks.
func DefaultVolcanoLocations() map[string]VolcanoLocation {
	return map[string]VolcanoLocation{
		"sim-tamriel-red-mountain": {AreaName: "Gunung Simulasi Red Mountain (Tamriel)", Latitude: -7.0, Longitude: 108.0},
		"sim-tamriel-ash-peak":     {AreaName: "Gunung Simulasi Ash Peak (Tamriel)", Latitude: -6.5, Longitude: 107.5},
		"sim-tamriel-north-crag":   {AreaName: "Gunung Simulasi North Crag (Tamriel)", Latitude: -7.5, Longitude: 109.0},
	}
}

func (m *Mapper) MapSeismic(eventJSON []byte, warningJSON [][]byte, hazardID string, ingestedAt time.Time) (HazardEvent, error) {
	fields, err := decodeObject(eventJSON)
	if err != nil {
		return HazardEvent{}, fmt.Errorf("decode seismic event: %w", err)
	}
	eventID, err := requiredString(fields, "event_id")
	if err != nil {
		return HazardEvent{}, err
	}
	magnitude, err := requiredFloat(fields, "magnitude")
	if err != nil {
		return HazardEvent{}, err
	}
	depth, err := requiredFloat(fields, "depth_km")
	if err != nil {
		return HazardEvent{}, err
	}
	latitude, err := requiredFloat(fields, "epicenter_lat")
	if err != nil {
		return HazardEvent{}, err
	}
	longitude, err := requiredFloat(fields, "epicenter_lon")
	if err != nil {
		return HazardEvent{}, err
	}
	region, err := requiredString(fields, "region_name")
	if err != nil {
		return HazardEvent{}, err
	}
	occurredAt, err := requiredTime(fields, "occurred_at")
	if err != nil {
		return HazardEvent{}, err
	}
	potentialTsunami, err := requiredBool(fields, "potential_tsunami")
	if err != nil {
		return HazardEvent{}, err
	}
	if latitude < -90 || latitude > 90 || longitude < -180 || longitude > 180 {
		return HazardEvent{}, errors.New("seismic coordinates are outside valid ranges")
	}
	if depth < 0 {
		return HazardEvent{}, errors.New("depth must be non-negative")
	}
	if hazardID == "" || ingestedAt.IsZero() {
		return HazardEvent{}, errors.New("hazard id and ingestion time are required")
	}

	attrs := map[string]any{"magnitude": magnitude, "depth_km": depth, "potential_tsunami": potentialTsunami}
	warnings := make([]any, 0)
	severity := SeverityNormal
	hasRelatedWarning := false
	if potentialTsunami {
		for _, warningRaw := range warningJSON {
			warningFields, err := decodeObject(warningRaw)
			if err != nil {
				return HazardEvent{}, fmt.Errorf("decode tsunami warning: %w", err)
			}
			relatedID, err := requiredString(warningFields, "related_event_id")
			if err != nil {
				return HazardEvent{}, err
			}
			if relatedID != eventID {
				continue
			}
			warning, threat, err := mapWarning(warningFields)
			if err != nil {
				return HazardEvent{}, err
			}
			warnings = append(warnings, warning)
			if !hasRelatedWarning || severityRank(threat) > severityRank(severity) {
				severity = threat
			}
			hasRelatedWarning = true
		}
	}
	if !hasRelatedWarning {
		severity = severityForMagnitude(magnitude)
	}
	sort.Slice(warnings, func(i, j int) bool {
		left := warnings[i].(map[string]any)
		right := warnings[j].(map[string]any)
		return left["warning_id"].(string) < right["warning_id"].(string)
	})
	if len(warnings) > 0 {
		attrs["tsunami_warnings"] = warnings
	}
	if err := addExtras(attrs, fields, setOf("event_id", "magnitude", "depth_km", "epicenter_lat", "epicenter_lon", "region_name", "occurred_at", "potential_tsunami")); err != nil {
		return HazardEvent{}, err
	}

	return HazardEvent{HazardID: hazardID, Source: SourceBMKG, SourceRefID: eventID, HazardType: HazardSeismic, Severity: severity, AreaName: region, Latitude: latitude, Longitude: longitude, OccurredAt: occurredAt, IngestedAt: ingestedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), Attributes: attrs}, nil
}

func (m *Mapper) MapVolcanic(reportJSON []byte, hazardID string, ingestedAt time.Time) (HazardEvent, error) {
	fields, err := decodeObject(reportJSON)
	if err != nil {
		return HazardEvent{}, fmt.Errorf("decode volcanic report: %w", err)
	}
	reportID, err := requiredString(fields, "report_id")
	if err != nil {
		return HazardEvent{}, err
	}
	volcanoID, err := requiredString(fields, "volcano_id")
	if err != nil {
		return HazardEvent{}, err
	}
	alert, err := requiredString(fields, "alert_level")
	if err != nil {
		return HazardEvent{}, err
	}
	eruptions, err := requiredInt(fields, "eruption_count_24h")
	if err != nil {
		return HazardEvent{}, err
	}
	ashHeight, err := requiredFloat(fields, "ash_column_height_m")
	if err != nil {
		return HazardEvent{}, err
	}
	occurredAt, err := requiredTime(fields, "reported_at")
	if err != nil {
		return HazardEvent{}, err
	}
	if hazardID == "" || ingestedAt.IsZero() {
		return HazardEvent{}, errors.New("hazard id and ingestion time are required")
	}
	if eruptions < 0 || ashHeight < 0 {
		return HazardEvent{}, errors.New("eruption count and ash height must be non-negative")
	}
	location, ok := m.volcanoes[volcanoID]
	if !ok {
		return HazardEvent{}, fmt.Errorf("unknown synthetic volcano id %q", volcanoID)
	}
	if location.AreaName == "" || math.IsNaN(location.Latitude) || math.IsInf(location.Latitude, 0) || location.Latitude < -90 || location.Latitude > 90 || math.IsNaN(location.Longitude) || math.IsInf(location.Longitude, 0) || location.Longitude < -180 || location.Longitude > 180 {
		return HazardEvent{}, fmt.Errorf("invalid reference location for volcano %q", volcanoID)
	}
	severity, err := mapAlertLevel(alert)
	if err != nil {
		return HazardEvent{}, err
	}
	attrs := map[string]any{"volcano_id": volcanoID, "eruption_count_24h": eruptions, "ash_column_height_m": ashHeight}
	if raw, ok := fields["confidence_level"]; ok {
		if string(raw) == "null" {
			return HazardEvent{}, errors.New("confidence_level must be a number in [0,1]")
		}
		confidence, err := requiredFloat(fields, "confidence_level")
		if err != nil || confidence < 0 || confidence > 1 {
			return HazardEvent{}, errors.New("confidence_level must be a number in [0,1]")
		}
		attrs["confidence_level"] = json.Number(string(raw))
	}
	if err := addExtras(attrs, fields, setOf("report_id", "volcano_id", "alert_level", "eruption_count_24h", "ash_column_height_m", "reported_at", "confidence_level")); err != nil {
		return HazardEvent{}, err
	}
	return HazardEvent{HazardID: hazardID, Source: SourcePVMBG, SourceRefID: reportID, HazardType: HazardVolcanic, Severity: severity, AreaName: location.AreaName, Latitude: location.Latitude, Longitude: location.Longitude, OccurredAt: occurredAt, IngestedAt: ingestedAt.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"), Attributes: attrs}, nil
}

func mapWarning(fields map[string]json.RawMessage) (map[string]any, Severity, error) {
	if _, err := requiredString(fields, "warning_id"); err != nil {
		return nil, "", err
	}
	threatRaw, err := requiredString(fields, "threat_level")
	if err != nil {
		return nil, "", err
	}
	threat, err := mapAlertLevel(threatRaw)
	if err != nil || threat == SeverityNormal {
		return nil, "", fmt.Errorf("invalid tsunami threat_level %q", threatRaw)
	}
	zonesRaw, ok := fields["affected_zones"]
	if !ok || string(zonesRaw) == "null" {
		return nil, "", errors.New("affected_zones must be an array")
	}
	var zones []string
	if err := json.Unmarshal(zonesRaw, &zones); err != nil || zones == nil {
		return nil, "", errors.New("affected_zones must be an array of strings")
	}
	if _, err := requiredTime(fields, "estimated_arrival"); err != nil {
		return nil, "", err
	}
	result := make(map[string]any, len(fields))
	for key, raw := range fields {
		value, err := decodeValue(raw)
		if err != nil {
			return nil, "", fmt.Errorf("warning field %q: %w", key, err)
		}
		result[key] = value
	}
	return result, threat, nil
}

func mapAlertLevel(value string) (Severity, error) {
	switch value {
	case "Normal":
		return SeverityNormal, nil
	case "Waspada":
		return SeverityWaspada, nil
	case "Siaga":
		return SeveritySiaga, nil
	case "Awas":
		return SeverityAwas, nil
	default:
		return "", fmt.Errorf("unknown alert level %q", value)
	}
}

func severityForMagnitude(magnitude float64) Severity {
	if magnitude < 5 {
		return SeverityNormal
	}
	if magnitude < 6.5 {
		return SeverityWaspada
	}
	return SeveritySiaga
}

func severityRank(value Severity) int {
	switch value {
	case SeverityNormal:
		return 0
	case SeverityWaspada:
		return 1
	case SeveritySiaga:
		return 2
	case SeverityAwas:
		return 3
	default:
		return -1
	}
}

func addExtras(target map[string]any, fields map[string]json.RawMessage, known map[string]struct{}) error {
	extras := make(map[string]any)
	for key, raw := range fields {
		if _, ok := known[key]; ok {
			continue
		}
		value, err := decodeValue(raw)
		if err != nil {
			return fmt.Errorf("decode source extension %q: %w", key, err)
		}
		if _, collision := target[key]; collision || key == "source_extras" {
			extras[key] = value
			continue
		}
		target[key] = value
	}
	if len(extras) > 0 {
		target["source_extras"] = extras
	}
	return nil
}

func setOf(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}
