package sharded

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/partition"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/sourcejournal"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/store"
	"golang.org/x/sync/errgroup"
)

// Coordinate runs one source loop per acquired server-side session. Takeover
// installs the generation on every canonical unit before making upstream reads.
func (s *Store) Coordinate(ctx context.Context, idle, retry time.Duration, logger *slog.Logger, run func(context.Context, contracts.Source) error) error {
	if idle < time.Second || retry <= 0 || run == nil {
		return errors.New("invalid source ownership configuration")
	}
	var group errgroup.Group
	for _, source := range []contracts.Source{contracts.SourceBMKG, contracts.SourcePVMBG} {
		group.Go(func() error {
			home, _ := partition.JournalUnit(source)
			for ctx.Err() == nil {
				session, owned, err := sourcejournal.Acquire(ctx, s.lookup(home).dsn, source, idle)
				if err == nil && owned {
					logger.InfoContext(ctx, "source ownership acquired", "source", source, "generation", session.Generation())
					err = session.WithHeartbeat(ctx, func(workCtx context.Context) error {
						if err := s.installFences(workCtx, source, session.Generation()); err != nil {
							return err
						}
						workCtx = store.WithFence(workCtx, source, session.Generation(), session.Heartbeat)
						return run(workCtx, source)
					})
					_ = session.Close(context.Background())
					if ctx.Err() == nil {
						logger.WarnContext(ctx, "source ownership released", "source", source, "reason", "OWNERSHIP_REACQUIRE")
					}
				} else if err != nil && ctx.Err() == nil {
					logger.DebugContext(ctx, "source ownership unavailable", "source", source)
				}
				timer := time.NewTimer(retry + time.Duration(rand.Int64N(int64(retry/2)+1)))
				select {
				case <-ctx.Done():
					timer.Stop()
					return nil
				case <-timer.C:
				}
			}
			return nil
		})
	}
	return group.Wait()
}
