// Package fieldreport is the durable intake path for Tim Lapangan reports.
//
// A report is acknowledged only after its original bytes, request hash, and
// receipt commit together in the owning storage unit. Processing runs later and
// independently: a slow or stopped processor never delays the 202 response.
package fieldreport

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
)

const (
	StatusAccepted  = "accepted"
	StatusProcessed = "processed"
	StatusRejected  = "rejected"
)

// ErrConflict means an Idempotency-Key was reused with different request bytes.
var ErrConflict = errors.New("idempotency key reused with a different request")

// ErrUnavailable means the owning storage could not be reached.
var ErrUnavailable = errors.New("field report storage unavailable")

type Submission struct {
	ClientID       string
	IdempotencyKey string
	CorrelationID  string
	Payload        []byte
}

type Receipt struct {
	ReportID string `json:"report_id"`
	Status   string `json:"status"`
	Replayed bool   `json:"-"`
}

type Status struct {
	ReportID    string  `json:"report_id"`
	Status      string  `json:"status"`
	ReceivedAt  string  `json:"received_at"`
	ProcessedAt *string `json:"processed_at"`
	Reason      *string `json:"reason"`
	Attempts    int     `json:"attempts"`
	Unit        string  `json:"unit,omitempty"`
}

// Pending is one accepted report claimed by a processor transaction.
type Pending struct {
	ReportID      string
	ClientID      string
	CorrelationID string
	ReceivedAt    time.Time
	Payload       []byte
}

// Outcome is the single state transition recorded for a claimed report.
type Outcome struct {
	Status string
	Reason string
	Report contracts.FieldReport
}

type Processor func(Pending) Outcome

type Repository interface {
	AcceptFieldReport(context.Context, Submission) (Receipt, error)
	FieldReport(ctx context.Context, clientID, reportID string) (Status, bool, error)
	// ProcessFieldReports claims up to limit accepted reports with row locks,
	// applies process, and commits each outcome with the claim. A crash before
	// commit releases the claim; a committed report is never claimed again.
	ProcessFieldReports(ctx context.Context, worker string, limit int, process Processor) (int, error)
}

// RequestHash identifies the exact request bytes bound to an idempotency key.
func RequestHash(payload []byte) string {
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

// NewID returns a random RFC 4122 version 4 UUID.
func NewID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]), nil
}

// Process validates stored bytes again. Client API and the intake endpoint
// already reject invalid bodies, so a rejection here means the contract changed
// after acceptance; the report is kept with its reason rather than dropped.
func Process(report Pending) Outcome {
	parsed, err := contracts.ValidateFieldReport(report.Payload)
	if err != nil {
		return Outcome{Status: StatusRejected, Reason: "schema_invalid: payload does not satisfy field-report.schema.json"}
	}
	return Outcome{Status: StatusProcessed, Report: parsed}
}

// RunWorker drains accepted reports until ctx ends. Several Aggregator
// instances may run it concurrently; row locks keep each report on one worker.
func RunWorker(ctx context.Context, repository Repository, worker string, interval time.Duration, logger *slog.Logger, observe func(string, string, error, time.Duration)) {
	if interval <= 0 {
		interval = 500 * time.Millisecond
	}
	if logger == nil {
		logger = slog.Default()
	}
	for {
		started := time.Now()
		n, err := repository.ProcessFieldReports(ctx, worker, 50, Process)
		if ctx.Err() != nil {
			return
		}
		if observe != nil && (n > 0 || err != nil) {
			observe("field_report_process", "store", err, time.Since(started))
		}
		if err != nil {
			logger.Warn("field report processing incomplete", "service", "aggregator", "operation", "field_report_process", "processed", n, "reason", "STORAGE_UNAVAILABLE")
		} else if n > 0 {
			logger.Info("field reports processed", "service", "aggregator", "operation", "field_report_process", "processed", n, "duration_ms", time.Since(started).Milliseconds())
		}
		if n > 0 && err == nil {
			continue
		}
		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}
