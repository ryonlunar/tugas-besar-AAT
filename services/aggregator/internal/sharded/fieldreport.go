package sharded

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/fieldreport"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/partition"
)

// AcceptFieldReport writes to the unit selected by the idempotency scope. That
// unit's synchronous standby must acknowledge the commit before 202 is sent,
// so the receipt survives loss of the node running its primary.
func (s *Store) AcceptFieldReport(ctx context.Context, submission fieldreport.Submission) (fieldreport.Receipt, error) {
	name, err := partition.ReportUnit(submission.ClientID, submission.IdempotencyKey)
	if err != nil {
		return fieldreport.Receipt{}, err
	}
	return s.lookup(name).store.AcceptFieldReport(ctx, submission)
}

// FieldReport searches every unit because report IDs are random. Absence is
// reported only when every unit answered; otherwise the caller gets an error
// instead of a misleading 404.
func (s *Store) FieldReport(ctx context.Context, clientID, reportID string) (fieldreport.Status, bool, error) {
	type answer struct {
		status fieldreport.Status
		found  bool
		err    error
	}
	answers := make([]answer, len(s.units))
	var wg sync.WaitGroup
	for i, u := range s.units {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, found, err := u.store.FieldReport(ctx, clientID, reportID)
			answers[i] = answer{status, found, err}
		}()
	}
	wg.Wait()
	var failed error
	for i, a := range answers {
		if a.err == nil && a.found {
			return a.status, true, nil
		}
		if a.err != nil {
			failed = errors.Join(failed, fmt.Errorf("%s: %w", s.units[i].name, a.err))
		}
	}
	if failed != nil {
		return fieldreport.Status{}, false, fmt.Errorf("%w: %v", fieldreport.ErrUnavailable, failed)
	}
	return fieldreport.Status{}, false, nil
}

// ProcessFieldReports drains each unit independently; one unavailable unit
// does not stop processing on the others.
func (s *Store) ProcessFieldReports(ctx context.Context, worker string, limit int, process fieldreport.Processor) (int, error) {
	total := 0
	var failed error
	for _, u := range s.units {
		n, err := u.store.ProcessFieldReports(ctx, worker, limit, process)
		total += n
		if err != nil {
			failed = errors.Join(failed, fmt.Errorf("%s: %w", u.name, err))
		}
	}
	return total, failed
}
