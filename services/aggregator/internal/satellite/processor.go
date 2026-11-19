package satellite

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/ryonlunar/tugas-besar-AAT/contracts"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/pipeline"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/sourcejournal"
)

// satelliteBatchKind is the only RawRecord.Kind the satellite adapter ever
// produces (D4): one micro-batch's archived SSE bytes.
const satelliteBatchKind = "sse_batch"

// Processor adapts a Recognizer into a sourcejournal.Processor, wiring a
// pgx.Tx-backed State so its recognition state and the returned Mutations
// commit together with the journal's own bookkeeping (Prepare's plan
// caching -- see sourcejournal/plan.go -- is what makes this safe to run
// more than once for the same message: a cached plan never re-invokes this
// function, so state is never double-applied).
func Processor(r *Recognizer) sourcejournal.Processor {
	return func(ctx context.Context, tx pgx.Tx, msg sourcejournal.Message) ([]contracts.Mutation, error) {
		if msg.Kind != satelliteBatchKind {
			return nil, pipeline.Park("invalid_source_response: unexpected kind " + msg.Kind)
		}
		st := NewPGState(tx)
		mutations, err := r.ProcessBatch(ctx, st, msg.Key, msg.Payload)
		if err != nil {
			return nil, pipeline.Park("invalid_source_response: " + err.Error())
		}
		return mutations, nil
	}
}
