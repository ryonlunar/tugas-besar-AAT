package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
	"github.com/ryonlunar/tugas-besar-AAT/pkg/telemetry"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/broker"
)

// ApplyInput commits effects, versions, outbox entries, and a deduplication receipt
// together on this unit. The journal acknowledgement happens afterward.
func (s *Store) ApplyInput(ctx context.Context, input string, source contracts.Source, sequence int64, mutations []contracts.Mutation, correlation string, at time.Time) (failure error) {
	ctx, span := otel.Tracer("aat/store").Start(ctx, "postgres.apply_input")
	span.SetAttributes(attribute.String("source", string(source)))
	defer func() {
		if failure != nil {
			span.SetStatus(codes.Error, "storage operation failed")
		}
		span.End()
	}()
	if input == "" || !source.Valid() || sequence <= 0 {
		return errors.New("input identity required")
	}
	ctx, cancel := s.writeContext(ctx)
	defer cancel()
	db := s.bmkgWriteDB
	if source == contracts.SourcePVMBG {
		db = s.pvmbgWriteDB
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if s.requireFence {
		if err = checkFence(ctx, tx, source); err != nil {
			return err
		}
	}
	// The receipt's unique key serializes duplicate deliveries even when two
	// transactions initially observe no receipt.
	result, err := tx.ExecContext(ctx, `INSERT INTO processed_inputs(source,input_id) VALUES($1,$2) ON CONFLICT DO NOTHING`, source, input)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return tx.Commit()
	}
	for _, m := range mutations {
		if m.Source != source || !sourceIDPattern.MatchString(m.SourceRefID) {
			return errors.New("mutation identity mismatch")
		}
		if _, err = tx.ExecContext(ctx, `INSERT INTO canonical_progress(source,source_ref_id,plan_sequence) VALUES($1,$2,0) ON CONFLICT DO NOTHING`, source, m.SourceRefID); err != nil {
			return err
		}
		var previous int64
		if err = tx.QueryRowContext(ctx, `SELECT plan_sequence FROM canonical_progress WHERE source=$1 AND source_ref_id=$2 FOR UPDATE`, source, m.SourceRefID).Scan(&previous); err != nil {
			return err
		}
		if previous >= sequence {
			continue
		}
		switch m.Kind {
		case contracts.MutationUpsert:
			if m.Event == nil || m.Event.Source != source || m.Event.SourceRefID != m.SourceRefID {
				return errors.New("upsert event identity mismatch")
			}
			err = s.upsertEvent(ctx, tx, *m.Event, correlation, at.UTC())
		case contracts.MutationRetract:
			err = s.retract(ctx, tx, source, m.SourceRefID, correlation, at.UTC())
		default:
			return errors.New("unknown mutation")
		}
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, `UPDATE canonical_progress SET plan_sequence=$3 WHERE source=$1 AND source_ref_id=$2`, source, m.SourceRefID, sequence); err != nil {
			return err
		}
	}
	if _, err = tx.ExecContext(ctx, `SELECT pg_advisory_xact_lock(4031,2)`); err != nil {
		return err
	}
	var pending int64
	if err = tx.QueryRowContext(ctx, `SELECT count(*) FROM outbox WHERE published_at IS NULL`).Scan(&pending); err != nil {
		return err
	}
	if pending > s.outboxLimit {
		return ErrOutboxCapacity
	}
	return tx.Commit()
}

func (s *Store) retract(ctx context.Context, tx *sql.Tx, source contracts.Source, ref, correlation string, at time.Time) error {
	var raw []byte
	var revision int64
	var deleted bool
	err := tx.QueryRowContext(ctx, `SELECT v.data,e.revision,e.retracted FROM hazard_events e JOIN hazard_versions v USING(hazard_id,revision)
 WHERE e.source=$1 AND e.source_ref_id=$2 FOR UPDATE OF e`, source, ref).Scan(&raw, &revision, &deleted)
	if errors.Is(err, sql.ErrNoRows) || deleted {
		return nil
	}
	if err != nil {
		return err
	}
	event, err := decodeHazardEvent(raw)
	if err != nil {
		return err
	}
	event.IngestedAt = at.Format(time.RFC3339Nano)
	revision++
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	id := fmt.Sprintf("%s:%d", event.HazardID, revision)
	envelope, err := json.Marshal(map[string]any{"schema_version": 1, "event_type": "hazard.retracted", "message_id": id, "correlation_id": correlation, "published_at": event.IngestedAt, "producer": "aggregator", "revision": revision, "data": json.RawMessage(data)})
	if err != nil {
		return err
	}
	subject, err := SubjectForSource(source)
	if err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `UPDATE hazard_events SET retracted=true,revision=$2,ingested_at=$3 WHERE hazard_id=$1`, event.HazardID, revision, at); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO hazard_versions(hazard_id,revision,data,recorded_at) VALUES($1,$2,$3,$4)`, event.HazardID, revision, data, at); err != nil {
		return err
	}
	return s.insertRetractionOutbox(ctx, tx, id, event.HazardID, revision, subject, envelope, at)
}

func (s *Store) insertRetractionOutbox(ctx context.Context, tx *sql.Tx, id, hazard string, revision int64, subject string, payload []byte, at time.Time) error {
	var normalized []byte
	if err := tx.QueryRowContext(ctx, `SELECT $1::jsonb`, payload).Scan(&normalized); err != nil {
		return err
	}
	wire, err := compactJSON(normalized)
	if err != nil {
		return err
	}
	if !broker.MessageFitsWithContext(ctx, wire, id, s.maxEnvelopeBytes) {
		return ErrEventTooLarge
	}
	headers := telemetry.TraceHeaders(ctx)
	_, err = tx.ExecContext(ctx, `INSERT INTO outbox(message_id,hazard_id,revision,subject,envelope,created_at,next_attempt_at,traceparent,tracestate) VALUES($1,$2,$3,$4,$5::jsonb,$6,$6,$7,$8)`, id, hazard, revision, subject, normalized, at, headers["traceparent"], headers["tracestate"])
	return err
}
