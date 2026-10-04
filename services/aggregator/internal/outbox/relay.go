package outbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/store"
)

type Queue interface {
	PendingOutbox(context.Context, int, time.Time) ([]store.OutboxMessage, error)
	MarkPublished(context.Context, string, time.Time) error
	RecordPublishFailure(context.Context, string, string, time.Time) error
}
type Publisher interface {
	Publish(context.Context, string, []byte, string) error
}

type Relay struct {
	queue          Queue
	publisher      Publisher
	batchSize      int
	interval       time.Duration
	publishTimeout time.Duration
	retryMin       time.Duration
	retryMax       time.Duration
	now            func() time.Time
	logger         *slog.Logger
	retryMu        sync.Mutex
	localRetryAt   map[string]time.Time
}

func New(queue Queue, publisher Publisher, batchSize int, interval, retryMin, retryMax time.Duration) *Relay {
	publishTimeout := 2 * time.Second
	if batchSize <= 0 {
		batchSize = 20
	}
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	if publishTimeout <= 0 {
		publishTimeout = 2 * time.Second
	}
	if retryMin <= 0 {
		retryMin = time.Second
	}
	if retryMax < retryMin {
		retryMax = 30 * time.Second
	}
	return &Relay{queue: queue, publisher: publisher, batchSize: batchSize, interval: interval, publishTimeout: publishTimeout, retryMin: retryMin, retryMax: retryMax, now: time.Now, logger: slog.Default(), localRetryAt: make(map[string]time.Time)}
}

func (r *Relay) SetPublishTimeout(timeout time.Duration) error {
	if timeout <= 0 {
		return errors.New("publish timeout must be positive")
	}
	r.publishTimeout = timeout
	return nil
}

func (r *Relay) SetClock(now func() time.Time) {
	if now != nil {
		r.now = now
	}
}
func (r *Relay) SetLogger(logger *slog.Logger) {
	if logger != nil {
		r.logger = logger
	}
}

func (r *Relay) DrainOnce(ctx context.Context) error {
	if r.queue == nil || r.publisher == nil {
		return errors.New("outbox relay is not configured")
	}
	now := r.now().UTC()
	readStarted := time.Now()
	messages, err := r.queue.PendingOutbox(ctx, r.batchSize, now)
	r.logger.InfoContext(ctx, "outbox pending batch read", "operation", "outbox_read_pending", "duration_ms", time.Since(readStarted).Milliseconds(), "status", statusFor(err))
	if err != nil {
		return fmt.Errorf("read pending outbox: %w", err)
	}
	var failures []error
	blockedHazards := make(map[string]struct{})
	for _, message := range messages {
		if _, blocked := blockedHazards[message.HazardID]; message.HazardID != "" && blocked {
			continue
		}
		if r.isLocallyDeferred(message.MessageID, now) {
			if message.HazardID != "" {
				blockedHazards[message.HazardID] = struct{}{}
			}
			continue
		}
		correlationID := envelopeCorrelationID(message.Envelope)
		publishCtx, cancel := context.WithTimeout(ctx, r.publishTimeout)
		publishStarted := time.Now()
		err := r.publisher.Publish(publishCtx, message.Subject, message.Envelope, message.MessageID)
		cancel()
		publishAttrs := []any{"operation", "jetstream_publish", "message_id", message.MessageID, "subject", message.Subject, "correlation_id", correlationID, "duration_ms", time.Since(publishStarted).Milliseconds(), "status", statusFor(err)}
		if err != nil {
			if message.HazardID != "" {
				blockedHazards[message.HazardID] = struct{}{}
			}
			r.logger.WarnContext(ctx, "JetStream publish failed", append(publishAttrs, "reason", "NATS_PUBLISH_FAILED")...)
			next := r.now().UTC().Add(retryDelay(message.Attempts+1, r.retryMin, r.retryMax))
			failureStarted := time.Now()
			if recordErr := r.queue.RecordPublishFailure(ctx, message.MessageID, "NATS_PUBLISH_FAILED", next); recordErr != nil {
				r.deferLocally(message.MessageID, next)
				r.logger.ErrorContext(ctx, "outbox retry scheduling failed", "operation", "outbox_record_failure", "message_id", message.MessageID, "correlation_id", correlationID, "duration_ms", time.Since(failureStarted).Milliseconds(), "status", "failed", "reason", "STORE_FAILURE")
				failures = append(failures, fmt.Errorf("record publish failure: %w", recordErr))
			} else {
				r.clearLocalRetry(message.MessageID)
				r.logger.InfoContext(ctx, "outbox retry scheduled", "operation", "outbox_record_failure", "message_id", message.MessageID, "correlation_id", correlationID, "duration_ms", time.Since(failureStarted).Milliseconds(), "status", "success")
				failures = append(failures, errors.New("NATS publish failed; message remains pending"))
			}
			continue
		}
		r.logger.InfoContext(ctx, "JetStream publish acknowledged", publishAttrs...)
		markStarted := time.Now()
		if err := r.queue.MarkPublished(ctx, message.MessageID, r.now().UTC()); err != nil {
			if message.HazardID != "" {
				blockedHazards[message.HazardID] = struct{}{}
			}
			r.logger.ErrorContext(ctx, "outbox acknowledgement persistence failed", "operation", "outbox_mark_published", "message_id", message.MessageID, "correlation_id", correlationID, "duration_ms", time.Since(markStarted).Milliseconds(), "status", "failed", "reason", "STORE_FAILURE")
			failures = append(failures, fmt.Errorf("mark outbox message published: %w", err))
			next := r.now().UTC().Add(retryDelay(message.Attempts+1, r.retryMin, r.retryMax))
			failureStarted := time.Now()
			if recordErr := r.queue.RecordPublishFailure(ctx, message.MessageID, "ACK_PERSIST_FAILED", next); recordErr != nil {
				r.deferLocally(message.MessageID, next)
				r.logger.ErrorContext(ctx, "outbox retry scheduling failed after acknowledgement error", "operation", "outbox_record_failure", "message_id", message.MessageID, "correlation_id", correlationID, "duration_ms", time.Since(failureStarted).Milliseconds(), "status", "failed", "reason", "STORE_FAILURE")
				failures = append(failures, fmt.Errorf("schedule retry after acknowledgement persistence failure: %w", recordErr))
			} else {
				r.clearLocalRetry(message.MessageID)
				r.logger.InfoContext(ctx, "outbox retry scheduled after acknowledgement error", "operation", "outbox_record_failure", "message_id", message.MessageID, "correlation_id", correlationID, "duration_ms", time.Since(failureStarted).Milliseconds(), "status", "success")
			}
		} else {
			r.clearLocalRetry(message.MessageID)
			r.logger.InfoContext(ctx, "outbox publish acknowledgement persisted", "operation", "outbox_mark_published", "message_id", message.MessageID, "correlation_id", correlationID, "duration_ms", time.Since(markStarted).Milliseconds(), "status", "success")
		}
	}
	return errors.Join(failures...)
}

func (r *Relay) isLocallyDeferred(messageID string, now time.Time) bool {
	if messageID == "" {
		return false
	}
	r.retryMu.Lock()
	defer r.retryMu.Unlock()
	next, ok := r.localRetryAt[messageID]
	if !ok {
		return false
	}
	if !now.Before(next) {
		delete(r.localRetryAt, messageID)
		return false
	}
	return true
}

func (r *Relay) deferLocally(messageID string, next time.Time) {
	if messageID == "" {
		return
	}
	r.retryMu.Lock()
	defer r.retryMu.Unlock()
	r.localRetryAt[messageID] = next
}

func (r *Relay) clearLocalRetry(messageID string) {
	r.retryMu.Lock()
	defer r.retryMu.Unlock()
	delete(r.localRetryAt, messageID)
}

func (r *Relay) Run(ctx context.Context) error {
	if r.queue == nil || r.publisher == nil {
		return errors.New("outbox relay is not configured")
	}
	ticker := time.NewTicker(r.interval)
	defer ticker.Stop()
	for {
		if err := r.DrainOnce(ctx); err != nil && ctx.Err() == nil {
			r.logger.Warn("outbox relay drain failed", "reason", "PUBLISH_OR_STORAGE_FAILURE")
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}

func envelopeCorrelationID(payload []byte) string {
	var envelope struct {
		CorrelationID string `json:"correlation_id"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil || envelope.CorrelationID == "" || len(envelope.CorrelationID) > 64 {
		return "unavailable"
	}
	for _, r := range envelope.CorrelationID {
		if !(r >= 'A' && r <= 'Z') && !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '.' && r != '_' && r != '-' {
			return "unavailable"
		}
	}
	return envelope.CorrelationID
}

func statusFor(err error) string {
	if err != nil {
		return "failed"
	}
	return "success"
}

func retryDelay(attempt int, min, max time.Duration) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	base := min
	for i := 1; i < attempt && base < max; i++ {
		if base > max/2 {
			base = max
			break
		}
		base *= 2
	}
	if base > max {
		base = max
	}
	// Keep the existing deterministic variation between attempts.
	jitter := 0.8 + float64(attempt%5)*0.1
	scaled := float64(base) * jitter
	// Cap before conversion, which can overflow even when base is bounded.
	if scaled >= float64(max) {
		return max
	}
	result := time.Duration(scaled)
	if result < min/2 {
		result = min / 2
	}
	return result
}
