package pipeline

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"github.com/jackc/pgx/v5"
	"unicode/utf8"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/bmkgimport"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/mapper"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/sourcejournal"
)

// DefaultProcessor covers the existing BMKG/PVMBG inputs. New adapters supply a
// processor through the same interface, without changing persistence or replay.
func DefaultProcessor(m *mapper.Mapper) Processor {
	if m == nil {
		m = mapper.New(mapper.DefaultVolcanoLocations())
	}
	return func(ctx context.Context, _ pgx.Tx, input sourcejournal.Message) ([]contracts.Mutation, error) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if len(input.Payload) > 1<<20 {
			return nil, Park("record_too_large")
		}
		if !utf8.Valid(input.Payload) {
			return nil, Park("invalid_utf8")
		}
		if excessiveDepth(input.Payload, 128) {
			return nil, Park("json_nesting_too_deep")
		}
		var event contracts.HazardEvent
		var err error
		var id [16]byte
		if _, err = rand.Read(id[:]); err != nil {
			return nil, err
		}
		id[6] = (id[6] & 15) | 64
		id[8] = (id[8] & 63) | 128
		hazardID := fmt.Sprintf("%x-%x-%x-%x-%x", id[:4], id[4:6], id[6:8], id[8:10], id[10:])
		switch input.Kind {
		case "seismic_event":
			var related []json.RawMessage
			if len(input.Auxiliary) > 0 {
				if json.Unmarshal(input.Auxiliary, &related) != nil {
					return nil, Park("invalid_related_warnings")
				}
			}
			warnings := make([][]byte, len(related))
			for i := range related {
				warnings[i] = related[i]
			}
			event, err = m.MapSeismic(input.Payload, warnings, hazardID, input.ReceivedAt)
		case "seismic_catalog_row":
			// Historical-catalog import (D11): Auxiliary carries the
			// catalog's header line, Payload one data row. No related
			// tsunami warnings exist in a one-time bulk import.
			var raw []byte
			raw, err = bmkgimport.ToSeismicJSON(input.Auxiliary, input.Payload)
			if err == nil {
				event, err = m.MapSeismic(raw, nil, hazardID, input.ReceivedAt)
			}
		case "volcanic_report":
			event, err = m.MapVolcanic(input.Payload, hazardID, input.ReceivedAt)
		case "tsunami_warning":
			if err = mapper.ValidateTsunamiWarning(input.Payload); err == nil {
				return nil, nil
			}
		default:
			return nil, Park("invalid_source_response: expected a JSON array of records")
		}
		if err != nil {
			return nil, Park("schema_incompatible: " + err.Error())
		}
		return []contracts.Mutation{{Kind: contracts.MutationUpsert, Source: event.Source, SourceRefID: event.SourceRefID, Event: &event}}, nil
	}
}

func excessiveDepth(raw []byte, limit int) bool {
	depth := 0
	quoted, escape := false, false
	for _, c := range raw {
		if quoted {
			if escape {
				escape = false
			} else if c == '\\' {
				escape = true
			} else if c == '"' {
				quoted = false
			}
			continue
		}
		switch c {
		case '"':
			quoted = true
		case '{', '[':
			depth++
			if depth > limit {
				return true
			}
		case '}', ']':
			depth--
		}
	}
	return false
}
