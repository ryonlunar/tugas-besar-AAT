// Package pipeline connects durable source intake, normalization, and canonical effects.
package pipeline

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"sync/atomic"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
	"github.com/ryonlunar/tugas-besar-AAT/pkg/telemetry"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/sourcejournal"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/store"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"golang.org/x/sync/errgroup"
)

type Reader func(context.Context) ([]contracts.RawRecord, error)
type Processor = sourcejournal.Processor
type Repository interface {
	ApplyInput(context.Context, string, contracts.Source, int64, []contracts.Mutation, string, time.Time) error
	ApplyCycle(context.Context, contracts.Source, []contracts.HazardEvent, string, bool, string, time.Time, time.Time) error
}

type PermanentError struct{ Reason string }

func (e *PermanentError) Error() string { return e.Reason }
func Park(reason string) error {
	if len(reason) > 512 {
		reason = strings.ToValidUTF8(reason[:512], "")
	}
	return &PermanentError{Reason: reason}
}

// RunSession separates polling from processing. Ownership/fencing is supplied by
// the coordinator; intake acknowledges only the journal commit, never mapping.
func RunSession(ctx context.Context, session *sourcejournal.Session, source contracts.Source, read Reader, process Processor, repository Repository, interval time.Duration, maxEnvelopeBytes int, processingEnabled bool, logger *slog.Logger, observe func(string, string, error, time.Duration)) error {
	return RunAdapter(ctx, session, source, &PollingAdapter{Read: read, Interval: interval, Source: source, Observe: observe}, process, repository, maxEnvelopeBytes, processingEnabled, logger, observe)
}

// RunAdapter uses opaque adapter cursors unchanged. The coordinator must supply
// a live session and a fenced context. Polling and streaming share the worker.
func RunAdapter(ctx context.Context, session *sourcejournal.Session, source contracts.Source, adapter contracts.SourceAdapter, process Processor, repository Repository, maxEnvelopeBytes int, processingEnabled bool, logger *slog.Logger, observe func(string, string, error, time.Duration)) error {
	if adapter == nil || process == nil || maxEnvelopeBytes <= 0 || maxEnvelopeBytes > 64<<10 {
		return errors.New("source adapter and processor and valid envelope limit required")
	}
	var health atomic.Value
	health.Store("SOURCE_FETCH_FAILED")
	refresh := func(ctx context.Context, correlation string) error {
		pending, err := session.Unfinished(ctx)
		if err != nil {
			return err
		}
		code := health.Load().(string)
		if code == "" && pending > 0 && source != contracts.SourceCoastal {
			code = "UNPROCESSED_INPUT"
		}
		success := code == ""
		now := time.Now().UTC()
		return repository.ApplyCycle(ctx, source, nil, correlation, success, code, now, now)
	}
	group, ctx := errgroup.WithContext(ctx)
	if polling, ok := adapter.(*PollingAdapter); ok {
		polling.onFailure = func(ctx context.Context) error {
			health.Store("SOURCE_FETCH_FAILED")
			logger.WarnContext(ctx, "source fetch failed", "source", source, "reason", "SOURCE_FETCH_FAILED")
			return refresh(ctx, "source-intake")
		}
		polling.canRead = func(ctx context.Context) (bool, error) {
			pending, err := session.Unfinished(ctx)
			return pending < 10000, err
		}
	}
	group.Go(func() error {
		err := adapter.Run(ctx, session.Cursor(), func(batchCtx context.Context, batch contracts.Batch) error {
			if err := store.CheckOwnership(batchCtx); err != nil {
				return err
			}
			if len(batch.Records) > 0 || batch.From != batch.Cursor {
				if err := session.Append(batchCtx, batch); err != nil {
					return err
				}
			}
			health.Store(batch.DegradedReason)
			return refresh(batchCtx, "source-intake")
		})
		if err == nil {
			return io.EOF
		}
		return err
	})
	group.Go(func() error {
		if !processingEnabled {
			<-ctx.Done()
			return ctx.Err()
		}
		changed := false
		for ctx.Err() == nil {
			message, err := session.Next(ctx)
			if err != nil {
				return err
			}
			if message == nil {
				if changed {
					if err := refresh(ctx, "source-processing"); err != nil {
						return err
					}
					changed = false
				}
				if !pause(ctx, 250*time.Millisecond) {
					break
				}
				continue
			}
			started := time.Now()
			workCtx := telemetry.RestoreTrace(ctx, message.TraceParent, message.TraceState)
			workCtx, span := otel.Tracer("aat/ingestion").Start(workCtx, "input.process")
			span.SetAttributes(attribute.String("source", string(source)))
			plan, processErr := session.Prepare(workCtx, *message, ValidateProcessor(process, maxEnvelopeBytes))
			if processErr == nil {
				processErr = repository.ApplyInput(workCtx, message.Key, source, plan.Sequence, plan.Mutations, message.CorrelationID, time.Now().UTC())
			}
			if processErr != nil {
				span.SetStatus(codes.Error, "input processing failed")
			}
			span.End()
			if observe != nil {
				observe("input_process", "store", processErr, time.Since(started))
			}
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if errors.Is(processErr, store.ErrFenced) {
				return processErr
			}
			reason := ""
			retryable := true
			if processErr != nil {
				reason = "canonical_write_failed"
				var permanent *PermanentError
				if errors.As(processErr, &permanent) {
					reason = permanent.Reason
					retryable = false
				}
				if errors.Is(processErr, store.ErrEventTooLarge) {
					reason = "event_too_large"
					retryable = false
				}
				logger.WarnContext(workCtx, "input processing failed", "source", source, "correlation_id", message.CorrelationID, "message_id", message.Key, "reason", reason, "retryable", retryable)
			} else {
				logger.InfoContext(workCtx, "input processed", "source", source, "correlation_id", message.CorrelationID, "message_id", message.Key, "trace_id", telemetry.TraceID(workCtx))
			}
			delay := time.Second * time.Duration(1<<min(message.Attempts, 5))
			if err := session.Finish(ctx, message.Key, reason, retryable, delay); err != nil {
				return err
			}
			changed = true
		}
		return ctx.Err()
	})
	return group.Wait()
}
func pause(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
