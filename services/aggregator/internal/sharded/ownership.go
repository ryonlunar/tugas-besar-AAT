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
	if run == nil {
		return errors.New("source callback required")
	}
	return s.CoordinateSessions(ctx, []contracts.Source{contracts.SourceBMKG, contracts.SourcePVMBG}, idle, retry, logger, func(ctx context.Context, source contracts.Source, _ *sourcejournal.Session) error {
		return run(ctx, source)
	})
}

// CoordinateSessions is the extension point for additional configured sources.
func (s *Store) CoordinateSessions(ctx context.Context, sources []contracts.Source, idle, retry time.Duration, logger *slog.Logger, run func(context.Context, contracts.Source, *sourcejournal.Session) error) error {
	seen := map[contracts.Source]bool{}
	for _, source := range sources {
		if !source.Valid() || seen[source] {
			return errors.New("invalid or duplicate source")
		}
		seen[source] = true
	}
	if idle < time.Second || retry <= 0 || run == nil || len(sources) == 0 {
		return errors.New("invalid source ownership configuration")
	}
	var group errgroup.Group
	for _, source := range sources {
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
						return run(workCtx, source, session)
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
