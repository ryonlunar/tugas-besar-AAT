package sourcejournal

import (
	"bytes"
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/ryonlunar/tugas-besar-AAT/contracts"
)

// Processor may change source-local recognition state through tx. It must not
// commit the transaction or perform network side effects. Its state changes and
// normalized mutations commit together, before canonical delivery begins.
type Processor func(context.Context, pgx.Tx, Message) ([]contracts.Mutation, error)
type Plan struct {
	Sequence  int64
	Mutations []contracts.Mutation
}

func (s *Session) Prepare(ctx context.Context, message Message, process Processor) (Plan, error) {
	var plan Plan
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	tx, err := s.conn.Begin(ctx)
	if err != nil {
		return plan, err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = tx.Rollback(ctx)
	}()
	if _, err = tx.Exec(ctx, `SET LOCAL statement_timeout='2s'; SET LOCAL idle_in_transaction_session_timeout='2s'`); err != nil {
		return plan, err
	}
	var raw []byte
	var sequence *int64
	err = tx.QueryRow(ctx, `SELECT normalized_plan,plan_sequence FROM ingest_records
 WHERE source=$1 AND record_key=$2 AND EXISTS(SELECT 1 FROM ingest_sources WHERE source=$1 AND generation=$3) FOR UPDATE`, s.source, message.Key, s.generation).Scan(&raw, &sequence)
	if err != nil {
		return plan, err
	}
	if sequence != nil {
		plan.Sequence = *sequence
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.UseNumber()
		if err = decoder.Decode(&plan.Mutations); err != nil {
			return plan, err
		}
	} else {
		plan.Mutations, err = process(ctx, tx, message)
		if err != nil {
			return plan, err
		}
		raw, err = json.Marshal(plan.Mutations)
		if err != nil {
			return plan, err
		}
		err = tx.QueryRow(ctx, `UPDATE ingest_records SET normalized_plan=$3::jsonb,plan_sequence=nextval('ingest_plan_sequence') WHERE source=$1 AND record_key=$2 RETURNING plan_sequence`, s.source, message.Key, raw).Scan(&plan.Sequence)
		if err != nil {
			return plan, err
		}
	}
	return plan, tx.Commit(ctx)
}
