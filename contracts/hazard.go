package contracts

type Source string
type HazardType string
type Severity string

const (
	SourceBMKG      Source = "BMKG"
	SourcePVMBG     Source = "PVMBG"
	SourceSatellite Source = "SATELIT"
	SourceCoastal   Source = "SENSOR_PANTAI"

	HazardSeismic  HazardType = "SEISMIC"
	HazardVolcanic HazardType = "VOLCANIC"
	HazardWildfire HazardType = "WILDFIRE"
	HazardCoastal  HazardType = "COASTAL"

	SeverityNormal  Severity = "NORMAL"
	SeverityWaspada Severity = "WASPADA"
	SeveritySiaga   Severity = "SIAGA"
	SeverityAwas    Severity = "AWAS"
)

func (s Source) Valid() bool {
	switch s {
	case SourceBMKG, SourcePVMBG, SourceSatellite, SourceCoastal:
		return true
	default:
		return false
	}
}

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
