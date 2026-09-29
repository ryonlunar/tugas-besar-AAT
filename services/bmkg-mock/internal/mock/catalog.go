package mock

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"sort"
	"sync"
	"time"
)

//go:embed fixtures/*.json
var fixtureFiles embed.FS

const (
	// MaxSeedCount keeps startup snapshots within the response budget.
	MaxSeedCount = 1000
	// MaxGeneratedRecords bounds the generated tail while preserving seed fixtures.
	MaxGeneratedRecords = 1000
)

type SeismicEvent struct {
	EventID          string    `json:"event_id"`
	Magnitude        float64   `json:"magnitude"`
	DepthKM          float64   `json:"depth_km"`
	EpicenterLat     float64   `json:"epicenter_lat"`
	EpicenterLon     float64   `json:"epicenter_lon"`
	RegionName       string    `json:"region_name"`
	OccurredAt       time.Time `json:"occurred_at"`
	PotentialTsunami bool      `json:"potential_tsunami"`
}

type TsunamiWarning struct {
	WarningID        string    `json:"warning_id"`
	RelatedEventID   string    `json:"related_event_id"`
	ThreatLevel      string    `json:"threat_level"`
	AffectedZones    []string  `json:"affected_zones"`
	EstimatedArrival time.Time `json:"estimated_arrival"`
	PublishedAt      time.Time `json:"-"`
}

type seismicSeed struct {
	ID               string  `json:"id"`
	Magnitude        float64 `json:"magnitude"`
	DepthKM          float64 `json:"depth_km"`
	Latitude         float64 `json:"epicenter_lat"`
	Longitude        float64 `json:"epicenter_lon"`
	RegionName       string  `json:"region_name"`
	PotentialTsunami bool    `json:"potential_tsunami"`
	AgeSeconds       int64   `json:"age_seconds"`
}

type warningSeed struct {
	ID                  string   `json:"id"`
	EventIndex          int      `json:"event_index"`
	ThreatLevel         string   `json:"threat_level"`
	AffectedZones       []string `json:"affected_zones"`
	ArrivalDelayMinutes int      `json:"arrival_delay_minutes"`
}

type BMKGCatalog struct {
	mu        sync.RWMutex
	runID     string
	events    []SeismicEvent
	warnings  []TsunamiWarning
	nextID    int
	seedCount int
}

func NewBMKGCatalog(start time.Time, runID string) (*BMKGCatalog, error) {
	return NewBMKGCatalogWithCount(start, runID, 20)
}

func NewBMKGCatalogWithCount(start time.Time, runID string, seedCount int) (*BMKGCatalog, error) {
	if start.IsZero() {
		return nil, errors.New("catalog start time is required")
	}
	if !validRunID(runID) {
		return nil, errors.New("run id must contain only ASCII letters, digits, hyphen, or underscore")
	}
	if seedCount < 20 || seedCount > MaxSeedCount {
		return nil, fmt.Errorf("seed count must be between 20 and %d", MaxSeedCount)
	}
	var seeds []seismicSeed
	if err := readFixture("fixtures/seismic-events.json", &seeds); err != nil {
		return nil, err
	}
	var warningSeeds []warningSeed
	if err := readFixture("fixtures/tsunami-warnings.json", &warningSeeds); err != nil {
		return nil, err
	}
	if len(seeds) != 20 {
		return nil, fmt.Errorf("BMKG fixture must contain 20 events, got %d", len(seeds))
	}
	baseSeeds := append([]seismicSeed(nil), seeds...)
	for len(seeds) < seedCount {
		sequence := len(seeds) + 1
		extra := baseSeeds[(sequence-1)%len(baseSeeds)]
		extra.ID = fmt.Sprintf("extra-%03d", sequence)
		extra.RegionName = fmt.Sprintf("Wilayah Simulasi Tambahan %03d", sequence)
		extra.AgeSeconds = baseSeeds[0].AgeSeconds + int64(sequence-len(baseSeeds))*60
		extra.PotentialTsunami = false
		seeds = append(seeds, extra)
	}
	catalog := &BMKGCatalog{runID: runID, events: make([]SeismicEvent, 0, len(seeds)+1), warnings: make([]TsunamiWarning, 0, len(warningSeeds)), nextID: len(seeds) + 1, seedCount: len(seeds)}
	seen := make(map[string]struct{}, len(seeds))
	for i, seed := range seeds {
		if seed.ID == "" || seed.RegionName == "" || seed.AgeSeconds < 0 || seed.Magnitude < 0 || seed.DepthKM < 0 || seed.Latitude < -90 || seed.Latitude > 90 || seed.Longitude < -180 || seed.Longitude > 180 {
			return nil, fmt.Errorf("invalid BMKG seed at index %d", i)
		}
		if _, ok := seen[seed.ID]; ok {
			return nil, fmt.Errorf("duplicate BMKG fixture id %q", seed.ID)
		}
		seen[seed.ID] = struct{}{}
		catalog.events = append(catalog.events, SeismicEvent{EventID: fmt.Sprintf("sim-%s-event-%03d", runID, i+1), Magnitude: seed.Magnitude, DepthKM: seed.DepthKM, EpicenterLat: seed.Latitude, EpicenterLon: seed.Longitude, RegionName: seed.RegionName, OccurredAt: start.UTC().Add(-time.Duration(seed.AgeSeconds) * time.Second), PotentialTsunami: seed.PotentialTsunami})
	}
	for i, seed := range warningSeeds {
		if seed.EventIndex < 1 || seed.EventIndex > len(catalog.events) || seed.ArrivalDelayMinutes < 0 || seed.ID == "" || len(seed.AffectedZones) == 0 {
			return nil, fmt.Errorf("invalid BMKG warning fixture %q", seed.ID)
		}
		event := catalog.events[seed.EventIndex-1]
		if !event.PotentialTsunami {
			return nil, fmt.Errorf("warning %q references event without tsunami potential", seed.ID)
		}
		if !validThreatLevel(seed.ThreatLevel) {
			return nil, fmt.Errorf("invalid threat level %q", seed.ThreatLevel)
		}
		catalog.warnings = append(catalog.warnings, TsunamiWarning{WarningID: fmt.Sprintf("sim-%s-warning-%03d", runID, i+1), RelatedEventID: event.EventID, ThreatLevel: seed.ThreatLevel, AffectedZones: append([]string(nil), seed.AffectedZones...), EstimatedArrival: event.OccurredAt.Add(time.Duration(seed.ArrivalDelayMinutes) * time.Minute), PublishedAt: event.OccurredAt})
	}
	return catalog, nil
}

func (c *BMKGCatalog) Snapshot() ([]SeismicEvent, []TsunamiWarning) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	events := append([]SeismicEvent(nil), c.events...)
	warnings := make([]TsunamiWarning, len(c.warnings))
	for i, warning := range c.warnings {
		warnings[i] = warning
		warnings[i].AffectedZones = append([]string(nil), warning.AffectedZones...)
	}
	sort.Slice(events, func(i, j int) bool {
		if events[i].OccurredAt.Equal(events[j].OccurredAt) {
			return events[i].EventID < events[j].EventID
		}
		return events[i].OccurredAt.Before(events[j].OccurredAt)
	})
	sort.Slice(warnings, func(i, j int) bool { return warnings[i].WarningID < warnings[j].WarningID })
	return events, warnings
}

func (c *BMKGCatalog) AddGenerated(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.events)-c.seedCount >= MaxGeneratedRecords {
		c.events = append(c.events[:c.seedCount], c.events[c.seedCount+1:]...)
	}
	id := c.nextID
	c.nextID++
	c.events = append(c.events, SeismicEvent{EventID: fmt.Sprintf("sim-%s-event-%03d", c.runID, id), Magnitude: 4.5 + float64(id%30)/10, DepthKM: float64(8 + id%20), EpicenterLat: -8.0 + float64(id%10)/10, EpicenterLon: 106.0 + float64(id%20)/10, RegionName: fmt.Sprintf("Wilayah Simulasi Baru %03d", id), OccurredAt: at.UTC(), PotentialTsunami: false})
}

func (c *BMKGCatalog) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return errors.New("event interval must be positive")
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case at := <-ticker.C:
			c.AddGenerated(at)
		}
	}
}

func validThreatLevel(value string) bool {
	return value == "Waspada" || value == "Siaga" || value == "Awas"
}

func validRunID(value string) bool {
	if value == "" {
		return false
	}
	for _, r := range value {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

func readFixture(name string, target any) error {
	data, err := fs.ReadFile(fixtureFiles, name)
	if err != nil {
		return fmt.Errorf("read embedded fixture %s: %w", name, err)
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode embedded fixture %s: %w", name, err)
	}
	return nil
}

func RunID() (string, error) {
	return randomRunID()
}
