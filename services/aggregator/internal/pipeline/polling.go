package pipeline

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/store"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
)

// PollingAdapter turns snapshots into durable batches. Streaming sources instead
// implement contracts.SourceAdapter with their native resume offset/cursor.
type PollingAdapter struct {
	Read      Reader
	Interval  time.Duration
	Source    contracts.Source
	Observe   func(string, string, error, time.Duration)
	onFailure func(context.Context) error
	canRead   func(context.Context) (bool, error)
}

func (p *PollingAdapter) Run(ctx context.Context, cursor string, commit func(context.Context, contracts.Batch) error) error {
	if p.Read == nil || p.Interval <= 0 {
		return errors.New("reader and positive poll interval required")
	}
	position := uint64(0)
	if cursor != "" {
		var err error
		position, err = strconv.ParseUint(cursor, 10, 64)
		if err != nil {
			return fmt.Errorf("polling cursor: %w", err)
		}
	}
	for ctx.Err() == nil {
		if err := store.CheckOwnership(ctx); err != nil {
			return err
		}
		if p.canRead != nil {
			allowed, err := p.canRead(ctx)
			if err != nil {
				return err
			}
			if !allowed {
				if !pause(ctx, p.Interval) {
					break
				}
				continue
			}
		}
		started := time.Now()
		readCtx, span := otel.Tracer("aat/ingestion").Start(ctx, "source.poll")
		span.SetAttributes(attribute.String("source", string(p.Source)))
		records, err := p.Read(readCtx)
		if err == nil {
			if position == ^uint64(0) {
				span.End()
				return errors.New("polling cursor exhausted")
			}
			next := strconv.FormatUint(position+1, 10)
			if err = commit(readCtx, contracts.Batch{From: cursor, Cursor: next, Records: records}); err != nil {
				span.End()
				return err
			}
			cursor = next
			position++
		} else if p.onFailure != nil {
			if failure := p.onFailure(ctx); failure != nil {
				span.End()
				return failure
			}
		}
		span.End()
		if p.Observe != nil {
			p.Observe("source_poll", string(p.Source), err, time.Since(started))
		}
		if !pause(ctx, p.Interval) {
			break
		}
	}
	return ctx.Err()
}
