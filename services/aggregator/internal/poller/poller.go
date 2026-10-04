package poller

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sync"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/broker"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/mapper"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/store"
)

type BMKGSource interface {
	Fetch(context.Context) (events [][]byte, warnings [][]byte, err error)
}
type PVMBGSource interface {
	Fetch(context.Context) (reports [][]byte, err error)
}
type Repository interface {
	FindHazardID(context.Context, mapper.Source, string) (id string, found bool, err error)
	PendingOutboxCount(context.Context) (int64, error)
	ApplyCycle(context.Context, mapper.Source, []mapper.HazardEvent, string, bool, string, time.Time, time.Time) error
}

type Poller struct {
	repository       Repository
	bmkg             BMKGSource
	pvmbg            PVMBGSource
	mapper           *mapper.Mapper
	logger           *slog.Logger
	now              func() time.Time
	maxEnvelopeBytes int
	outboxLimit      int64
}

func New(repository Repository, bmkg BMKGSource, pvmbg PVMBGSource, sourceMapper *mapper.Mapper, logger *slog.Logger) *Poller {
	if sourceMapper == nil {
		sourceMapper = mapper.New(mapper.DefaultVolcanoLocations())
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Poller{repository: repository, bmkg: bmkg, pvmbg: pvmbg, mapper: sourceMapper, logger: logger, now: time.Now, maxEnvelopeBytes: 64 << 10, outboxLimit: store.DefaultOutboxLimit}
}

func (p *Poller) SetMaxEnvelopeBytes(limit int) {
	if limit <= 0 || limit > 64<<10 {
		limit = 64 << 10
	}
	p.maxEnvelopeBytes = limit
}

func (p *Poller) SetOutboxLimit(limit int64) {
	if limit <= 0 {
		limit = store.DefaultOutboxLimit
	}
	p.outboxLimit = limit
}

func (p *Poller) checkOutboxCapacity(ctx context.Context, source mapper.Source) (bool, error) {
	pending, err := p.repository.PendingOutboxCount(ctx)
	if err != nil {
		p.logger.Error("outbox capacity check failed", "source", source, "reason", "OUTBOX_CAPACITY_CHECK_FAILED")
		return false, fmt.Errorf("check pending outbox capacity: %w", err)
	}
	if pending >= p.outboxLimit {
		p.logger.Warn("source ingestion paused", "source", source, "pending", pending, "limit", p.outboxLimit, "reason", "OUTBOX_CAPACITY")
		return false, nil
	}
	return true, nil
}

func (p *Poller) PollBMKG(ctx context.Context) error {
	if p.repository == nil || p.bmkg == nil {
		return errors.New("BMKG poller is not configured")
	}
	allowed, err := p.checkOutboxCapacity(ctx, mapper.SourceBMKG)
	if err != nil || !allowed {
		return err
	}
	correlationID, err := newCorrelationID()
	if err != nil {
		return err
	}
	attemptAt := p.now().UTC()
	sourceCtx := withCorrelationID(ctx, correlationID)
	events, warnings, fetchErr := p.bmkg.Fetch(sourceCtx)
	if fetchErr != nil {
		failureAt := p.now().UTC()
		if saveErr := p.repository.ApplyCycle(ctx, mapper.SourceBMKG, nil, correlationID, false, "SOURCE_FETCH_FAILED", attemptAt, failureAt); saveErr != nil {
			return errors.Join(fetchErr, fmt.Errorf("persist BMKG failure state: %w", saveErr))
		}
		return fetchErr
	}
	acceptedAt := p.now().UTC()
	invalid := 0
	warningsByEvent := make(map[string][][]byte)
	for index, raw := range warnings {
		id, err := rawStringField(raw, "related_event_id")
		if err != nil {
			invalid++
			p.logRejectedRecord(mapper.SourceBMKG, correlationID, "tsunami_warning", index, "", "related_event_id", "INVALID_RECORD")
			continue
		}
		if err := mapper.ValidateTsunamiWarning(raw); err != nil {
			invalid++
			p.logRejectedRecord(mapper.SourceBMKG, correlationID, "tsunami_warning", index, id, "", "INVALID_RECORD")
			continue
		}
		warningsByEvent[id] = append(warningsByEvent[id], raw)
	}
	mapped := make([]mapper.HazardEvent, 0, len(events))
	oversized := 0
	for index, raw := range events {
		refID, err := rawStringField(raw, "event_id")
		if err != nil {
			invalid++
			p.logRejectedRecord(mapper.SourceBMKG, correlationID, "seismic_event", index, "", "event_id", "INVALID_RECORD")
			continue
		}
		hazardID, err := p.hazardID(ctx, mapper.SourceBMKG, refID)
		if err != nil {
			return err
		}
		entity, err := p.mapper.MapSeismic(raw, warningsByEvent[refID], hazardID, acceptedAt)
		if err != nil {
			invalid++
			p.logRejectedRecord(mapper.SourceBMKG, correlationID, "seismic_event", index, refID, "", "INVALID_RECORD")
			continue
		}
		fits, err := eventFitsEnvelope(entity, correlationID, acceptedAt, p.maxEnvelopeBytes)
		if err != nil || !fits {
			oversized++
			p.logRejectedRecord(mapper.SourceBMKG, correlationID, "seismic_event", index, refID, "", "EVENT_TOO_LARGE")
			continue
		}
		mapped = append(mapped, entity)
	}
	code := ""
	if invalid > 0 {
		code = "INVALID_RECORD"
	}
	if oversized > 0 {
		code = "EVENT_TOO_LARGE"
	}
	success := invalid == 0 && oversized == 0
	if err := p.repository.ApplyCycle(ctx, mapper.SourceBMKG, mapped, correlationID, success, code, attemptAt, acceptedAt); err != nil {
		if errors.Is(err, store.ErrOutboxCapacity) {
			p.logger.Warn("source ingestion paused", "source", mapper.SourceBMKG, "limit", p.outboxLimit, "reason", "OUTBOX_CAPACITY")
		}
		return fmt.Errorf("persist BMKG snapshot: %w", err)
	}
	return nil
}

func (p *Poller) PollPVMBG(ctx context.Context) error {
	if p.repository == nil || p.pvmbg == nil {
		return errors.New("PVMBG poller is not configured")
	}
	allowed, err := p.checkOutboxCapacity(ctx, mapper.SourcePVMBG)
	if err != nil || !allowed {
		return err
	}
	correlationID, err := newCorrelationID()
	if err != nil {
		return err
	}
	attemptAt := p.now().UTC()
	sourceCtx := withCorrelationID(ctx, correlationID)
	reports, fetchErr := p.pvmbg.Fetch(sourceCtx)
	if fetchErr != nil {
		failureAt := p.now().UTC()
		if saveErr := p.repository.ApplyCycle(ctx, mapper.SourcePVMBG, nil, correlationID, false, "SOURCE_FETCH_FAILED", attemptAt, failureAt); saveErr != nil {
			return errors.Join(fetchErr, fmt.Errorf("persist PVMBG failure state: %w", saveErr))
		}
		return fetchErr
	}
	acceptedAt := p.now().UTC()
	mapped := make([]mapper.HazardEvent, 0, len(reports))
	invalid, oversized := 0, 0
	for index, raw := range reports {
		refID, err := rawStringField(raw, "report_id")
		if err != nil {
			invalid++
			p.logRejectedRecord(mapper.SourcePVMBG, correlationID, "volcanic_report", index, "", "report_id", "INVALID_RECORD")
			continue
		}
		hazardID, err := p.hazardID(ctx, mapper.SourcePVMBG, refID)
		if err != nil {
			return err
		}
		entity, err := p.mapper.MapVolcanic(raw, hazardID, acceptedAt)
		if err != nil {
			invalid++
			p.logRejectedRecord(mapper.SourcePVMBG, correlationID, "volcanic_report", index, refID, "", "INVALID_RECORD")
			continue
		}
		fits, err := eventFitsEnvelope(entity, correlationID, acceptedAt, p.maxEnvelopeBytes)
		if err != nil || !fits {
			oversized++
			p.logRejectedRecord(mapper.SourcePVMBG, correlationID, "volcanic_report", index, refID, "", "EVENT_TOO_LARGE")
			continue
		}
		mapped = append(mapped, entity)
	}
	code := ""
	if invalid > 0 {
		code = "INVALID_RECORD"
	}
	if oversized > 0 {
		code = "EVENT_TOO_LARGE"
	}
	success := invalid == 0 && oversized == 0
	if err := p.repository.ApplyCycle(ctx, mapper.SourcePVMBG, mapped, correlationID, success, code, attemptAt, acceptedAt); err != nil {
		if errors.Is(err, store.ErrOutboxCapacity) {
			p.logger.Warn("source ingestion paused", "source", mapper.SourcePVMBG, "limit", p.outboxLimit, "reason", "OUTBOX_CAPACITY")
		}
		return fmt.Errorf("persist PVMBG snapshot: %w", err)
	}
	return nil
}

func (p *Poller) logRejectedRecord(source mapper.Source, correlationID, kind string, index int, sourceRefID, field, reason string) {
	attrs := []any{"source", source, "correlation_id", correlationID, "record_kind", kind, "record_index", index, "reason", reason}
	if sourceRefID != "" {
		attrs = append(attrs, "source_ref_id", sourceRefID)
	}
	if field != "" {
		attrs = append(attrs, "field", field)
	}
	p.logger.Warn("source record rejected", attrs...)
}

func eventFitsEnvelope(event mapper.HazardEvent, correlationID string, at time.Time, maxBytes int) (bool, error) {
	if maxBytes <= 0 || maxBytes > 64<<10 {
		maxBytes = 64 << 10
	}
	messageID := event.HazardID + ":9223372036854775807"
	envelope := map[string]any{
		"schema_version": 1,
		"event_type":     "hazard.updated",
		"message_id":     messageID,
		"correlation_id": correlationID,
		"published_at":   at.UTC().Format(time.RFC3339Nano),
		"producer":       "aggregator",
		"revision":       int64(9223372036854775807),
		"data":           event,
	}
	encoded, err := json.Marshal(envelope)
	if err != nil {
		return false, err
	}
	return broker.MessageFits(encoded, messageID, maxBytes), nil
}

func (p *Poller) hazardID(ctx context.Context, source mapper.Source, refID string) (string, error) {
	id, found, err := p.repository.FindHazardID(ctx, source, refID)
	if err != nil {
		return "", fmt.Errorf("look up existing hazard identity: %w", err)
	}
	if found {
		return id, nil
	}
	return newUUID()
}

func (p *Poller) Run(ctx context.Context, interval time.Duration) error {
	if interval <= 0 {
		return errors.New("poll interval must be positive")
	}
	if p.bmkg == nil || p.pvmbg == nil || p.repository == nil {
		return errors.New("poller requires both source clients and a repository")
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go p.runLoop(ctx, &wg, "BMKG", interval, p.PollBMKG)
	go p.runLoop(ctx, &wg, "PVMBG", interval, p.PollPVMBG)
	<-ctx.Done()
	wg.Wait()
	return nil
}

func (p *Poller) runLoop(ctx context.Context, wg *sync.WaitGroup, source string, interval time.Duration, poll func(context.Context) error) {
	defer wg.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if ctx.Err() != nil {
			return
		}
		if err := poll(ctx); err != nil && ctx.Err() == nil {
			p.logger.Warn("source poll failed", "source", source, "reason", "POLL_FAILED")
		}
		if ctx.Err() != nil {
			return
		}
		select {
		case <-ticker.C:
		default:
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

var sourceIDPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

func rawStringField(raw []byte, key string) (string, error) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
		return "", errors.New("record is not a JSON object")
	}
	var value string
	if err := json.Unmarshal(fields[key], &value); err != nil || !sourceIDPattern.MatchString(value) {
		return "", fmt.Errorf("field %s is missing or invalid", key)
	}
	return value, nil
}

func newUUID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	value[6] = (value[6] & 0x0f) | 0x40
	value[8] = (value[8] & 0x3f) | 0x80
	encoded := hex.EncodeToString(value[:])
	return encoded[:8] + "-" + encoded[8:12] + "-" + encoded[12:16] + "-" + encoded[16:20] + "-" + encoded[20:], nil
}

func newCorrelationID() (string, error) {
	return newUUID()
}
