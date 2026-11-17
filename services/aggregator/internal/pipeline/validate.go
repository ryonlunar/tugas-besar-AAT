package pipeline

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/ryonlunar/tugas-besar-AAT/contracts"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/broker"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/sourcejournal"
)

var referencePattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// ValidateProcessor runs before the state/plan transaction commits. Invalid or
// oversized output therefore remains reparsable after the processor is fixed.
func ValidateProcessor(process Processor, maxBytes int) Processor {
	return func(ctx context.Context, tx pgx.Tx, input sourcejournal.Message) ([]contracts.Mutation, error) {
		if tx == nil {
			return nil, errors.New("processor transaction required")
		}
		mutations, err := process(ctx, tx, input)
		if err != nil {
			return nil, err
		}
		seen := map[string]bool{}
		for _, m := range mutations {
			if m.Source != input.Source || !referencePattern.MatchString(m.SourceRefID) || seen[m.SourceRefID] {
				return nil, Park("invalid_mutation_identity")
			}
			seen[m.SourceRefID] = true
			if m.Kind == contracts.MutationRetract {
				if m.Event != nil {
					return nil, Park("retraction_has_event")
				}
				continue
			}
			if m.Kind != contracts.MutationUpsert || m.Event == nil || m.Event.Source != m.Source || m.Event.SourceRefID != m.SourceRefID {
				return nil, Park("invalid_upsert")
			}
			event := *m.Event
			if event.HazardID == "" {
				event.HazardID = "00000000-0000-4000-8000-000000000000"
			}
			// Reserve the largest revision/timestamps instead of accepting an input
			// which fits its first publication but cannot be published after updates.
			event.IngestedAt = "2000-01-01T00:00:00.123456789Z"
			id := event.HazardID + ":9223372036854775807"
			encoded, err := json.Marshal(map[string]any{"schema_version": 1, "event_type": "hazard.updated", "message_id": id, "correlation_id": input.CorrelationID, "published_at": event.IngestedAt, "producer": "aggregator", "revision": int64(9223372036854775807), "data": event})
			if err != nil {
				return nil, Park("invalid_canonical_data")
			}
			subject := "hazard.events.v1." + string(m.Source)
			if _, err := contracts.ParseEnvelope(encoded, subject, id); err != nil {
				return nil, Park("invalid_canonical_data")
			}
			var normalized []byte
			if err := tx.QueryRow(ctx, `SELECT $1::jsonb`, encoded).Scan(&normalized); err != nil {
				var pgError *pgconn.PgError
				if errors.As(err, &pgError) && len(pgError.Code) >= 2 && pgError.Code[:2] == "22" {
					return nil, Park("invalid_canonical_json")
				}
				return nil, err
			}
			var compact bytes.Buffer
			if err := json.Compact(&compact, normalized); err != nil {
				return nil, fmt.Errorf("normalize canonical event: %w", err)
			}
			if !broker.MessageFitsWithContext(ctx, compact.Bytes(), id, maxBytes) {
				return nil, Park("event_too_large")
			}
		}
		return mutations, nil
	}
}
