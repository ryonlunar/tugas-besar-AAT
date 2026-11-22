package coastal

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/jackc/pgx/v5"
	"github.com/ryonlunar/tugas-besar-AAT/contracts"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/pipeline"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/sourcejournal"
)

var referenceID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

type station struct {
	ID        string   `json:"station_id"`
	Name      string   `json:"name"`
	Province  string   `json:"province"`
	Latitude  *float64 `json:"latitude"`
	Longitude *float64 `json:"longitude"`
}
type observation struct {
	ID         string    `json:"observation_id"`
	Station    string    `json:"station_id"`
	ObservedAt time.Time `json:"observed_at"`
	Anomaly    *float64  `json:"sea_level_anomaly_m"`
	Wave       *float64  `json:"wave_height_m"`
	Quality    string    `json:"quality"`
	Critical   *bool     `json:"critical"`
}

func decodeObservation(raw []byte) (observation, error) {
	var o observation
	if !utf8.Valid(raw) || json.Unmarshal(raw, &o) != nil {
		return o, errors.New("coastal_observation_invalid_json")
	}
	if !referenceID.MatchString(o.ID) || strings.TrimSpace(o.Station) == "" || o.ObservedAt.IsZero() || o.Anomaly == nil || o.Wave == nil || *o.Wave < 0 || strings.TrimSpace(o.Quality) == "" || o.Critical == nil {
		return o, errors.New("coastal_observation_invalid_fields")
	}
	return o, nil
}

func decodeStation(stationRaw []byte, id string) (station, bool) {
	var s station
	if !utf8.Valid(stationRaw) || json.Unmarshal(stationRaw, &s) != nil || s.ID != id || strings.TrimSpace(s.Name) == "" || strings.TrimSpace(s.Province) == "" || s.Latitude == nil || s.Longitude == nil || *s.Latitude < -90 || *s.Latitude > 90 || *s.Longitude < -180 || *s.Longitude > 180 {
		return s, false
	}
	return s, true
}

func (a *Adapter) Process(ctx context.Context, _ pgx.Tx, m sourcejournal.Message) ([]contracts.Mutation, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if m.Kind != observationKind {
		return nil, pipeline.Park("coastal_response_invalid")
	}
	o, err := decodeObservation(m.Payload)
	if err != nil {
		return nil, pipeline.Park(err.Error())
	}
	s, known := decodeStation(m.Auxiliary, o.Station)
	if !known {
		// Metadata can arrive after the observation. Use the refreshed catalog
		// on retry, including after acquiring ownership on a different replica.
		s, known = decodeStation(a.stationMetadata(o.Station), o.Station)
	}
	if !known {
		// With the worker's 2–32 second backoff, allow roughly two minutes
		// for catalog recovery before parking an unresolved station reference.
		if m.Attempts >= 8 {
			return nil, pipeline.Park("coastal_station_unknown_or_invalid")
		}
		return nil, pipeline.Retry("coastal_station_metadata_unavailable")
	}
	// The sensor supplies the critical decision. The canonical severity is our
	// presentation mapping, with no inferred thresholds from practice data.
	severity := contracts.SeverityWaspada
	if *o.Critical {
		severity = contracts.SeverityAwas
	}
	event := contracts.HazardEvent{
		Source: contracts.SourceCoastal, SourceRefID: o.ID, HazardType: contracts.HazardCoastal, Severity: severity,
		AreaName: s.Province, Latitude: *s.Latitude, Longitude: *s.Longitude, OccurredAt: o.ObservedAt.UTC().Format(time.RFC3339Nano),
		Attributes: map[string]any{"station_id": o.Station, "sea_level_anomaly_m": *o.Anomaly, "wave_height_m": *o.Wave, "quality": o.Quality, "critical": *o.Critical, "received_at": m.ReceivedAt.UTC().Format(time.RFC3339Nano)},
	}
	return []contracts.Mutation{{Kind: contracts.MutationUpsert, Source: event.Source, SourceRefID: event.SourceRefID, Event: &event}}, nil
}
