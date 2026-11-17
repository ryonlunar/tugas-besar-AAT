// Package sharded connects the canonical store to four replicated logical units.
package sharded

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/ryonlunar/tugas-besar-AAT/contracts"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/mapper"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/partition"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/sourcejournal"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/store"
	"golang.org/x/sync/errgroup"
)

type Config struct {
	Unit string `json:"unit"`
	DSN  string `json:"dsn"`
}

type Options struct {
	ReadConns, BMKGConns, PVMBGConns, RelayConns int
	ReadTimeout, WriteTimeout, StaleAfter        time.Duration
	OutboxLimit                                  int64
	MaxEnvelopeBytes                             int
}

type unit struct {
	name, dsn string
	store     *store.Store
}
type Store struct {
	units   []unit
	pools   []*sql.DB
	timeout time.Duration
}

func Open(ctx context.Context, filename string, options Options) (*Store, error) {
	raw, err := os.ReadFile(filename)
	if err != nil {
		return nil, errors.New("cannot read shard configuration")
	}
	var configs []Config
	if json.Unmarshal(raw, &configs) != nil || len(configs) != partition.UnitCount {
		return nil, errors.New("four logical units are required")
	}
	byName := map[string]string{}
	for _, c := range configs {
		if c.DSN == "" || byName[c.Unit] != "" {
			return nil, errors.New("invalid shard configuration")
		}
		byName[c.Unit] = c.DSN
	}
	s := &Store{timeout: options.ReadTimeout}
	if s.timeout <= 0 {
		s.timeout = 200 * time.Millisecond
	}
	complete := false
	defer func() {
		if !complete {
			s.Close()
		}
	}()
	for i := 0; i < partition.UnitCount; i++ {
		name := fmt.Sprintf("shard-%d", i)
		dsn := byName[name]
		if dsn == "" {
			return nil, errors.New("unknown or missing logical unit")
		}
		pools := []*sql.DB{}
		for _, size := range []int{options.ReadConns, options.BMKGConns, options.PVMBGConns, options.RelayConns} {
			if size < 1 {
				return nil, errors.New("positive connection pool sizes are required")
			}
			db, err := sql.Open("pgx", dsn)
			if err != nil {
				return nil, errors.New("cannot configure shard connection")
			}
			db.SetMaxOpenConns(size)
			db.SetMaxIdleConns(size)
			db.SetConnMaxIdleTime(5 * time.Minute)
			pools = append(pools, db)
			s.pools = append(s.pools, db)
		}
		repository := store.NewWithPools(pools[0], pools[1], pools[2], pools[3], options.StaleAfter)
		repository.SetUnit(name)
		repository.SetTimeouts(s.timeout, options.WriteTimeout)
		limit := options.OutboxLimit
		if limit <= 0 {
			limit = store.DefaultOutboxLimit
		}
		if limit < partition.UnitCount {
			return nil, errors.New("sharded outbox limit must be at least four")
		}
		quota := limit / int64(partition.UnitCount)
		if int64(i) < limit%int64(partition.UnitCount) {
			quota++
		}
		repository.SetOutboxLimit(quota)
		repository.SetMaxEnvelopeBytes(options.MaxEnvelopeBytes)
		repository.RequireFence()
		s.units = append(s.units, unit{name, dsn, repository})
	}
	group, groupCtx := errgroup.WithContext(ctx)
	for _, u := range s.units {
		group.Go(func() error {
			for {
				probeCtx, cancel := context.WithTimeout(groupCtx, 2*time.Second)
				err := u.store.Ping(probeCtx)
				cancel()
				if err == nil {
					break
				}
				timer := time.NewTimer(time.Second)
				select {
				case <-groupCtx.Done():
					timer.Stop()
					return fmt.Errorf("%s storage startup deadline exceeded", u.name)
				case <-timer.C:
				}
			}
			if err := u.store.Migrate(groupCtx); err != nil {
				return fmt.Errorf("migrate %s: %w", u.name, err)
			}
			conn, err := pgx.Connect(groupCtx, u.dsn)
			if err != nil {
				return fmt.Errorf("connect journal %s", u.name)
			}
			defer conn.Close(context.Background())
			// Serialize first-time CREATE TABLE across concurrently starting instances.
			if _, err = conn.Exec(groupCtx, `SELECT pg_advisory_lock(4032,0)`); err != nil {
				return err
			}
			return sourcejournal.Install(groupCtx, conn)
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	complete = true
	return s, nil
}

func (s *Store) Close() {
	for _, db := range s.pools {
		_ = db.Close()
	}
}

// A ready router can serve healthy units during a storage outage. Each read
// reports missing units; readiness must not remove every router at that moment.
func (s *Store) Ping(ctx context.Context) error { return ctx.Err() }

func (s *Store) RegisterMetrics(registerer prometheus.Registerer) {
	for _, u := range s.units {
		prometheus.WrapRegistererWith(prometheus.Labels{"unit": u.name}, registerer).MustRegister(u.store.MetricsCollector())
	}
}

func (s *Store) lookup(name string) unit {
	for _, u := range s.units {
		if u.name == name {
			return u
		}
	}
	panic("invalid internal unit routing")
}

func (s *Store) FindHazardID(ctx context.Context, source mapper.Source, ref string) (string, bool, error) {
	name, err := partition.Unit(source, ref)
	if err != nil {
		return "", false, err
	}
	return s.lookup(name).store.FindHazardID(ctx, source, ref)
}

func (s *Store) Page(ctx context.Context, source, cursor string, limit, offset int) (partition.Page, error) {
	// ReadPage owns the fan-out timeout and classifies slow units as partial.
	// Only legacy offset traversal needs an additional overall scan budget.
	if offset > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, 2*s.timeout)
		defer cancel()
	}
	readers := make([]partition.Reader, 0, len(s.units))
	for _, u := range s.units {
		readers = append(readers, partition.Reader{Unit: u.name, Read: u.store.ReadPage})
	}
	for {
		size := limit
		if offset > 0 {
			size = min(offset, 1000)
		}
		page, err := partition.ReadPage(ctx, readers, partition.Query{Source: contracts.Source(source), Cursor: cursor, Limit: size}, s.timeout)
		if err != nil {
			return page, err
		}
		if offset == 0 {
			return page, nil
		}
		if page.Partial {
			page.Items = []contracts.HazardEvent{}
			return page, nil
		}
		offset -= len(page.Items)
		if page.NextCursor == nil {
			page.Items = []contracts.HazardEvent{}
			return page, nil
		}
		cursor = *page.NextCursor
	}
}

func (s *Store) List(ctx context.Context, source string, limit, offset int) ([]mapper.HazardEvent, bool, error) {
	page, err := s.Page(ctx, source, "", limit, offset)
	return page.Items, page.NextCursor != nil, err
}

func (s *Store) Get(ctx context.Context, id string) (mapper.HazardEvent, bool, error) {
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	type result struct {
		event mapper.HazardEvent
		found bool
		err   error
	}
	results := make(chan result, len(s.units))
	for _, u := range s.units {
		go func(u unit) { e, f, err := u.store.Get(ctx, id); results <- result{e, f, err} }(u)
	}
	unavailable := false
	for range s.units {
		select {
		case <-ctx.Done():
			return mapper.HazardEvent{}, false, ctx.Err()
		case r := <-results:
			if r.err != nil {
				unavailable = true
			} else if r.found {
				return r.event, true, nil
			}
		}
	}
	if unavailable {
		return mapper.HazardEvent{}, false, errors.New("hazard may be on an unavailable unit")
	}
	return mapper.HazardEvent{}, false, nil
}

func (s *Store) SourceStates(ctx context.Context, sources []mapper.Source, now time.Time) (map[mapper.Source]store.SourceState, error) {
	type result struct {
		name   string
		states map[mapper.Source]store.SourceState
		err    error
	}
	ctx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()
	results := make(chan result, len(s.units))
	for _, u := range s.units {
		go func(u unit) { st, err := u.store.SourceStates(ctx, sources, now); results <- result{u.name, st, err} }(u)
	}
	known := map[string]map[mapper.Source]store.SourceState{}
	for range s.units {
		select {
		case <-ctx.Done(): // Preserve completed results; the page carries availability.
			return mergeStates(known, sources), nil
		case r := <-results:
			if r.err == nil {
				known[r.name] = r.states
			}
		}
	}
	return mergeStates(known, sources), nil
}

func mergeStates(known map[string]map[mapper.Source]store.SourceState, sources []mapper.Source) map[mapper.Source]store.SourceState {
	states := map[mapper.Source]store.SourceState{}
	for _, source := range sources {
		home, _ := partition.JournalUnit(source)
		state := known[home][source]
		if state.Status == "" {
			state.Status = "unavailable"
		}
		if state.Status == "unavailable" {
			for _, st := range known {
				if st[source].Status != "" && st[source].Status != "unavailable" {
					state = st[source]
					state.Status = "stale"
					break
				}
			}
		}
		states[source] = state
	}
	return states
}

func (s *Store) ApplyCycle(ctx context.Context, source mapper.Source, events []mapper.HazardEvent, correlationID string, success bool, code string, attempt, accepted time.Time) error {
	groups := map[string][]mapper.HazardEvent{}
	for _, event := range events {
		if event.Source != source {
			return errors.New("event source mismatch")
		}
		name, err := partition.Unit(source, event.SourceRefID)
		if err != nil {
			return err
		}
		groups[name] = append(groups[name], event)
	}
	home, err := partition.JournalUnit(source)
	if err != nil {
		return err
	}
	group, groupCtx := errgroup.WithContext(ctx)
	for _, u := range s.units {
		if u.name != home {
			group.Go(func() error {
				return u.store.ApplyCycle(groupCtx, source, groups[u.name], correlationID, success, code, attempt, accepted)
			})
		}
	}
	if err := group.Wait(); err != nil {
		return err
	}
	// The source's authoritative freshness advances only after the other units.
	return s.lookup(home).store.ApplyCycle(ctx, source, groups[home], correlationID, success, code, attempt, accepted)
}

func (s *Store) installFences(ctx context.Context, source contracts.Source, generation int64) error {
	var group errgroup.Group
	for _, u := range s.units {
		group.Go(func() error { return u.store.InstallFence(ctx, source, generation) })
	}
	return group.Wait()
}

func (s *Store) PendingOutboxCount(ctx context.Context) (int64, error) {
	var total int64
	var mu sync.Mutex
	var group errgroup.Group
	for _, u := range s.units {
		group.Go(func() error {
			n, err := u.store.PendingOutboxCount(ctx)
			if err == nil {
				mu.Lock()
				total += n
				mu.Unlock()
			}
			return err
		})
	}
	err := group.Wait()
	return total, err
}
