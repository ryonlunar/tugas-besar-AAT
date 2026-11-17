package store

import (
	"context"
	"database/sql"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/partition"
)

// ReadPage reads a single unit using the same stable key as the fan-out merger.
func (s *Store) ReadPage(ctx context.Context, source contracts.Source, after *partition.Position, limit int) (result []contracts.HazardEvent, failure error) {
	ctx, span := otel.Tracer("aat/store").Start(ctx, "postgres.read_page")
	defer func() {
		if failure != nil {
			span.SetStatus(codes.Error, "storage read failed")
		}
		span.End()
	}()
	span.SetAttributes(attribute.String("unit", s.unit))
	ctx, cancel := s.readContext(ctx)
	defer cancel()
	var at sql.NullTime
	var id string
	if after != nil {
		at = sql.NullTime{Time: after.OccurredAt, Valid: true}
		id = after.HazardID
	}
	rows, err := s.readDB.QueryContext(ctx, `SELECT v.data FROM hazard_events e
		JOIN hazard_versions v ON v.hazard_id=e.hazard_id AND v.revision=e.revision
		WHERE ($1='' OR e.source=$1) AND ($2::timestamptz IS NULL OR e.occurred_at<$2 OR (e.occurred_at=$2 AND e.hazard_id::text>$3))
		ORDER BY e.occurred_at DESC,e.hazard_id ASC LIMIT $4`, source, at, id, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []contracts.HazardEvent{}
	for rows.Next() {
		var raw []byte
		if err := rows.Scan(&raw); err != nil {
			return nil, err
		}
		event, err := decodeHazardEvent(raw)
		if err != nil {
			return nil, err
		}
		items = append(items, event)
	}
	return items, rows.Err()
}
