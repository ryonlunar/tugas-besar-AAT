package sharded

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/audit"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/partition"
)

func (s *Store) InspectFailed(ctx context.Context, id string) (audit.Detail, bool, error) {
	var d audit.Detail
	source, key, err := parseMessageID(id)
	if err != nil {
		return d, false, err
	}
	home, _ := partition.JournalUnit(source)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	var at time.Time
	err = s.lookup(home).readDB.QueryRowContext(ctx, `SELECT source,received_at,reason,attempts,status,kind,ordering_key,correlation_id,payload,auxiliary
 FROM ingest_records WHERE source=$1 AND record_key=$2 AND status<>'done'`, source, key).
		Scan(&d.Source, &at, &d.Reason, &d.Attempts, &d.Status, &d.Kind, &d.OrderingKey, &d.CorrelationID, &d.Payload, &d.Auxiliary)
	if errors.Is(err, sql.ErrNoRows) {
		return d, false, nil
	}
	if err != nil {
		return d, false, err
	}
	d.MessageID = id
	d.ReceivedAt = at.UTC().Format(time.RFC3339Nano)
	d.Retryable = d.Status == "retrying"
	return d, true, nil
}

func (s *Store) ReplayFailed(ctx context.Context, id string) (audit.Replay, bool, error) {
	result := audit.Replay{MessageID: id, Status: "queued"}
	source, key, err := parseMessageID(id)
	if err != nil {
		return result, false, err
	}
	home, _ := partition.JournalUnit(source)
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	tx, err := s.lookup(home).readDB.BeginTx(ctx, nil)
	if err != nil {
		return result, false, err
	}
	defer tx.Rollback()
	var status string
	err = tx.QueryRowContext(ctx, `SELECT status FROM ingest_records WHERE source=$1 AND record_key=$2 FOR UPDATE`, source, key).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return result, false, nil
	}
	if err != nil {
		return result, false, err
	}
	if status == "done" {
		result.Status = "already_processed"
	} else {
		_, err = tx.ExecContext(ctx, `UPDATE ingest_records SET replay_requested=true,status='retrying',next_attempt_at=now() WHERE source=$1 AND record_key=$2`, source, key)
		if err != nil {
			return result, false, err
		}
	}
	err = tx.Commit()
	return result, true, err
}
