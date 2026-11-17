package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/fieldreport"
)

// AcceptFieldReport stores the original bytes, request hash, and receipt in one
// statement. The UNIQUE(client_id, idempotency_key) constraint makes a retried
// request return the first receipt, including after a crash following commit.
func (s *Store) AcceptFieldReport(ctx context.Context, submission fieldreport.Submission) (fieldreport.Receipt, error) {
	id, err := fieldreport.NewID()
	if err != nil {
		return fieldreport.Receipt{}, err
	}
	hash := fieldreport.RequestHash(submission.Payload)
	ctx, cancel := s.writeContext(ctx)
	defer cancel()
	var stored string
	err = s.reportDB.QueryRowContext(ctx, `INSERT INTO field_reports(report_id,client_id,idempotency_key,request_sha256,payload,correlation_id)
 VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT (client_id,idempotency_key) DO NOTHING RETURNING report_id::text`,
		id, submission.ClientID, submission.IdempotencyKey, hash, submission.Payload, submission.CorrelationID).Scan(&stored)
	if err == nil {
		return fieldreport.Receipt{ReportID: stored, Status: fieldreport.StatusAccepted}, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fieldreport.Receipt{}, fmt.Errorf("%w: %v", fieldreport.ErrUnavailable, err)
	}
	var existingHash string
	err = s.reportDB.QueryRowContext(ctx, `SELECT report_id::text,request_sha256 FROM field_reports WHERE client_id=$1 AND idempotency_key=$2`,
		submission.ClientID, submission.IdempotencyKey).Scan(&stored, &existingHash)
	if err != nil {
		return fieldreport.Receipt{}, fmt.Errorf("%w: %v", fieldreport.ErrUnavailable, err)
	}
	if existingHash != hash {
		return fieldreport.Receipt{}, fieldreport.ErrConflict
	}
	return fieldreport.Receipt{ReportID: stored, Status: fieldreport.StatusAccepted, Replayed: true}, nil
}

func (s *Store) FieldReport(ctx context.Context, clientID, reportID string) (fieldreport.Status, bool, error) {
	ctx, cancel := s.readContext(ctx)
	defer cancel()
	var status fieldreport.Status
	var received time.Time
	var processed sql.NullTime
	var reason sql.NullString
	err := s.readDB.QueryRowContext(ctx, `SELECT report_id::text,status,received_at,processed_at,reason,attempts
 FROM field_reports WHERE report_id=$1 AND client_id=$2`, reportID, clientID).
		Scan(&status.ReportID, &status.Status, &received, &processed, &reason, &status.Attempts)
	if errors.Is(err, sql.ErrNoRows) {
		return status, false, nil
	}
	if err != nil {
		return status, false, err
	}
	status.ReceivedAt = received.UTC().Format(time.RFC3339Nano)
	if processed.Valid {
		value := processed.Time.UTC().Format(time.RFC3339Nano)
		status.ProcessedAt = &value
	}
	if reason.Valid {
		status.Reason = &reason.String
	}
	status.Unit = s.unit
	return status, true, nil
}

// ProcessFieldReports commits each report's outcome in the transaction that
// claimed it. FOR UPDATE SKIP LOCKED lets concurrent Aggregators share the
// queue without claiming the same row; the status predicate on UPDATE makes a
// second transition impossible even if a claim were somehow repeated.
func (s *Store) ProcessFieldReports(ctx context.Context, worker string, limit int, process fieldreport.Processor) (int, error) {
	if limit < 1 {
		limit = 50
	}
	ctx, cancel := s.writeContext(ctx)
	defer cancel()
	tx, err := s.reportDB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	rows, err := tx.QueryContext(ctx, `SELECT report_id::text,client_id,correlation_id,received_at,payload FROM field_reports
 WHERE status='accepted' ORDER BY received_at,report_id LIMIT $1 FOR UPDATE SKIP LOCKED`, limit)
	if err != nil {
		return 0, err
	}
	pending := []fieldreport.Pending{}
	for rows.Next() {
		var p fieldreport.Pending
		if err := rows.Scan(&p.ReportID, &p.ClientID, &p.CorrelationID, &p.ReceivedAt, &p.Payload); err != nil {
			rows.Close()
			return 0, err
		}
		pending = append(pending, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}
	processed := 0
	for _, p := range pending {
		outcome := process(p)
		var result sql.Result
		if outcome.Status == fieldreport.StatusProcessed {
			observed, parseErr := time.Parse(time.RFC3339Nano, outcome.Report.ObservedAt)
			if parseErr != nil {
				outcome = fieldreport.Outcome{Status: fieldreport.StatusRejected, Reason: "schema_invalid: observed_at"}
			} else {
				result, err = tx.ExecContext(ctx, `UPDATE field_reports SET status='processed',attempts=attempts+1,processed_at=now(),processed_by=$2,
 observed_at=$3,latitude=$4,longitude=$5,description=$6 WHERE report_id=$1 AND status='accepted'`,
					p.ReportID, worker, observed.UTC(), outcome.Report.Latitude, outcome.Report.Longitude, outcome.Report.Description)
			}
		}
		if outcome.Status != fieldreport.StatusProcessed {
			result, err = tx.ExecContext(ctx, `UPDATE field_reports SET status='rejected',attempts=attempts+1,processed_at=now(),processed_by=$2,reason=$3
 WHERE report_id=$1 AND status='accepted'`, p.ReportID, worker, outcome.Reason)
		}
		if err != nil {
			return 0, err
		}
		if n, _ := result.RowsAffected(); n == 1 {
			processed++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return processed, nil
}
