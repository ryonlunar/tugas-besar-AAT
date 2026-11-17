package sourcejournal

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/ryonlunar/tugas-besar-AAT/contracts"
)

// Message remains in the journal until its canonical effects commit. Sequence
// orders versions of an entity; unrelated entities can bypass a parked input.
type Message struct {
	contracts.RawRecord
	Source                  contracts.Source
	Sequence                int64
	Attempts                int
	TraceParent, TraceState string
}

func (s *Session) Next(ctx context.Context) (*Message, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	var m Message
	m.Source = s.source
	err := s.conn.QueryRow(ctx, `UPDATE ingest_records SET attempts=attempts+1,replay_requested=false WHERE sequence=(
 SELECT r.sequence FROM ingest_records r WHERE r.source=$1 AND r.status='retrying' AND r.next_attempt_at<=now()
 AND EXISTS(SELECT 1 FROM ingest_sources WHERE source=$1 AND generation=$2)
 AND NOT EXISTS(SELECT 1 FROM ingest_records p WHERE p.source=r.source AND p.sequence<r.sequence
 AND p.status<>'done' AND p.ordering_key<>'' AND p.ordering_key=r.ordering_key)
 ORDER BY r.sequence LIMIT 1)
 RETURNING record_key,kind,payload,received_at,correlation_id,ordering_key,auxiliary,sequence,attempts,traceparent,tracestate`, s.source, s.generation).
		Scan(&m.Key, &m.Kind, &m.Payload, &m.ReceivedAt, &m.CorrelationID, &m.OrderingKey, &m.Auxiliary, &m.Sequence, &m.Attempts, &m.TraceParent, &m.TraceState)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	return &m, err
}

// Finish uses the original ownership connection. A crash before Finish leaves
// the input retryable; a canonical receipt prevents duplicate effects on retry.
func (s *Session) Finish(ctx context.Context, key, reason string, retryable bool, delay time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	status := "done"
	if reason != "" {
		status = "parked"
		if retryable {
			status = "retrying"
		}
	}
	result, err := s.conn.Exec(ctx, `UPDATE ingest_records SET status=CASE WHEN replay_requested AND $3='parked' THEN 'retrying' ELSE $3 END,reason=$4,replay_requested=CASE WHEN $3='done' THEN false ELSE replay_requested END,next_attempt_at=now()+$5::bigint*interval '1 millisecond'
 WHERE source=$1 AND record_key=$2 AND EXISTS(SELECT 1 FROM ingest_sources WHERE source=$1 AND generation=$6)`, s.source, key, status, reason, delay.Milliseconds(), s.generation)
	if err == nil && result.RowsAffected() != 1 {
		return ErrCursorConflict
	}
	return err
}

func (s *Session) Unfinished(ctx context.Context) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	err := s.conn.QueryRow(ctx, `SELECT count(*) FROM ingest_records WHERE source=$1 AND status<>'done'`, s.source).Scan(&n)
	return n, err
}
