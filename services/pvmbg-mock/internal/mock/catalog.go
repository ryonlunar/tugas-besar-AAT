package mock

import (
	"context"
	"crypto/rand"
	"embed"
	"encoding/hex"
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

type VolcanicReport struct {
	ReportID         string    `json:"report_id"`
	VolcanoID        string    `json:"volcano_id"`
	AlertLevel       string    `json:"alert_level"`
	EruptionCount24h int       `json:"eruption_count_24h"`
	AshColumnHeightM float64   `json:"ash_column_height_m"`
	ReportedAt       time.Time `json:"reported_at"`
	ConfidenceLevel  *float64  `json:"confidence_level,omitempty"`
}

type volcanicSeed struct {
	ID               string  `json:"id"`
	VolcanoID        string  `json:"volcano_id"`
	AlertLevel       string  `json:"alert_level"`
	EruptionCount24h int     `json:"eruption_count_24h"`
	AshColumnHeightM float64 `json:"ash_column_height_m"`
	AgeSeconds       int64   `json:"age_seconds"`
	ConfidenceLevel  float64 `json:"confidence_level"`
}

type PVMBGCatalog struct {
	mu            sync.RWMutex
	runID         string
	reports       []volcanicSeedRecord
	nextID        int
	schemaVersion int
	outage        bool
	minDelay      time.Duration
	maxDelay      time.Duration
	seedCount     int
}

type volcanicSeedRecord struct {
	report     VolcanicReport
	confidence float64
}

func NewPVMBGCatalog(start time.Time, runID string, minDelay, maxDelay time.Duration) (*PVMBGCatalog, error) {
	return NewPVMBGCatalogWithCount(start, runID, 20, minDelay, maxDelay)
}

func NewPVMBGCatalogWithCount(start time.Time, runID string, seedCount int, minDelay, maxDelay time.Duration) (*PVMBGCatalog, error) {
	if start.IsZero() {
		return nil, errors.New("catalog start time is required")
	}
	if !validRunID(runID) {
		return nil, errors.New("run id must contain only ASCII letters, digits, hyphen, or underscore")
	}
	if seedCount < 20 || seedCount > MaxSeedCount {
		return nil, fmt.Errorf("seed count must be between 20 and %d", MaxSeedCount)
	}
	if minDelay < 0 || maxDelay < minDelay {
		return nil, errors.New("delay range must be non-negative and min <= max")
	}
	var seeds []volcanicSeed
	data, err := fs.ReadFile(fixtureFiles, "fixtures/volcanic-reports.json")
	if err != nil {
		return nil, fmt.Errorf("read embedded PVMBG fixture: %w", err)
	}
	if err := json.Unmarshal(data, &seeds); err != nil {
		return nil, fmt.Errorf("decode embedded PVMBG fixture: %w", err)
	}
	if len(seeds) != 20 {
		return nil, fmt.Errorf("PVMBG fixture must contain 20 reports, got %d", len(seeds))
	}
	baseSeeds := append([]volcanicSeed(nil), seeds...)
	for len(seeds) < seedCount {
		sequence := len(seeds) + 1
		extra := baseSeeds[(sequence-1)%len(baseSeeds)]
		extra.ID = fmt.Sprintf("extra-%03d", sequence)
		extra.AgeSeconds = baseSeeds[0].AgeSeconds + int64(sequence-len(baseSeeds))*60
		seeds = append(seeds, extra)
	}
	catalog := &PVMBGCatalog{runID: runID, reports: make([]volcanicSeedRecord, 0, len(seeds)+1), nextID: len(seeds) + 1, schemaVersion: 1, minDelay: minDelay, maxDelay: maxDelay, seedCount: len(seeds)}
	seen := make(map[string]struct{}, len(seeds))
	for i, seed := range seeds {
		if seed.ID == "" || seed.VolcanoID == "" || seed.AgeSeconds < 0 || seed.AlertLevel == "" || seed.EruptionCount24h < 0 || seed.AshColumnHeightM < 0 || seed.ConfidenceLevel < 0 || seed.ConfidenceLevel > 1 {
			return nil, fmt.Errorf("invalid PVMBG seed at index %d", i)
		}
		if _, ok := seen[seed.ID]; ok {
			return nil, fmt.Errorf("duplicate PVMBG fixture id %q", seed.ID)
		}
		seen[seed.ID] = struct{}{}
		if !validAlertLevel(seed.AlertLevel) {
			return nil, fmt.Errorf("invalid alert level %q", seed.AlertLevel)
		}
		report := VolcanicReport{ReportID: fmt.Sprintf("sim-%s-report-%03d", runID, i+1), VolcanoID: seed.VolcanoID, AlertLevel: seed.AlertLevel, EruptionCount24h: seed.EruptionCount24h, AshColumnHeightM: seed.AshColumnHeightM, ReportedAt: start.UTC().Add(-time.Duration(seed.AgeSeconds) * time.Second)}
		catalog.reports = append(catalog.reports, volcanicSeedRecord{report: report, confidence: seed.ConfidenceLevel})
	}
	return catalog, nil
}

func (c *PVMBGCatalog) Snapshot() []VolcanicReport {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result := make([]VolcanicReport, 0, len(c.reports))
	for _, seed := range c.reports {
		report := seed.report
		if c.schemaVersion >= 2 {
			confidence := seed.confidence
			report.ConfidenceLevel = &confidence
		}
		result = append(result, report)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].ReportedAt.Equal(result[j].ReportedAt) {
			return result[i].ReportID < result[j].ReportID
		}
		return result[i].ReportedAt.Before(result[j].ReportedAt)
	})
	return result
}

func (c *PVMBGCatalog) SetSchemaVersion(version int) error {
	if version != 1 && version != 2 {
		return errors.New("schema version must be 1 or 2")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.schemaVersion = version
	return nil
}

func (c *PVMBGCatalog) SetOutage(enabled bool) { c.mu.Lock(); defer c.mu.Unlock(); c.outage = enabled }
func (c *PVMBGCatalog) IsOutage() bool         { c.mu.RLock(); defer c.mu.RUnlock(); return c.outage }

func (c *PVMBGCatalog) SetDelayRange(minDelay, maxDelay time.Duration) error {
	if minDelay < 0 || maxDelay < minDelay {
		return errors.New("delay range must be non-negative and min <= max")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.minDelay, c.maxDelay = minDelay, maxDelay
	return nil
}

func (c *PVMBGCatalog) DelayRange() (time.Duration, time.Duration) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.minDelay, c.maxDelay
}

func (c *PVMBGCatalog) AddGenerated(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.reports)-c.seedCount >= MaxGeneratedRecords {
		c.reports = append(c.reports[:c.seedCount], c.reports[c.seedCount+1:]...)
	}
	id := c.nextID
	c.nextID++
	volcanoes := []string{"sim-tamriel-red-mountain", "sim-tamriel-ash-peak", "sim-tamriel-north-crag"}
	levels := []string{"Normal", "Waspada", "Siaga", "Awas"}
	confidence := 0.65 + float64(id%30)/100
	report := VolcanicReport{ReportID: fmt.Sprintf("sim-%s-report-%03d", c.runID, id), VolcanoID: volcanoes[(id-1)%len(volcanoes)], AlertLevel: levels[(id-1)%len(levels)], EruptionCount24h: id % 5, AshColumnHeightM: float64((id % 8) * 125), ReportedAt: at.UTC()}
	c.reports = append(c.reports, volcanicSeedRecord{report: report, confidence: confidence})
}

func (c *PVMBGCatalog) Run(ctx context.Context, interval time.Duration) error {
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

func validAlertLevel(value string) bool {
	return value == "Normal" || value == "Waspada" || value == "Siaga" || value == "Awas"
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
func randomRunID() (string, error) {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
