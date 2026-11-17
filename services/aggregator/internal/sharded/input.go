package sharded

import (
	"context"
	"errors"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/partition"
)

// Partial cross-unit commits are safe to retry: each unit commits its receipt
// with its own effects. Acknowledgement waits until every affected unit succeeds.
func (s *Store) ApplyInput(ctx context.Context, input string, source contracts.Source, sequence int64, mutations []contracts.Mutation, correlation string, at time.Time) error {
	groups := map[string][]contracts.Mutation{}
	for _, m := range mutations {
		if m.Source != source {
			return errors.New("input source mismatch")
		}
		name, err := partition.Unit(source, m.SourceRefID)
		if err != nil {
			return err
		}
		groups[name] = append(groups[name], m)
	}
	for _, u := range s.units {
		if group := groups[u.name]; len(group) > 0 {
			if err := u.store.ApplyInput(ctx, input, source, sequence, group, correlation, at); err != nil {
				return err
			}
		}
	}
	return nil
}
