package main

import (
	"context"
	"fmt"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/api"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/outbox"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/poller"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/sharded"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/store"
)

type runtimeRepository interface {
	api.Repository
	poller.Repository
	outbox.Queue
}

func openRepository(cfg config, registerer prometheus.Registerer) (runtimeRepository, func(), error) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if cfg.shardConfigFile != "" {
		repository, err := sharded.Open(ctx, cfg.shardConfigFile, sharded.Options{
			ReadConns: cfg.readConns, BMKGConns: cfg.bmkgWriteConns, PVMBGConns: cfg.pvmbgWriteConns, RelayConns: cfg.relayConns,
			ReadTimeout: cfg.dbReadTimeout, WriteTimeout: cfg.dbWriteTimeout, StaleAfter: cfg.staleAfter,
			OutboxLimit: cfg.outboxMaxPending, MaxEnvelopeBytes: cfg.natsMaxMsgSize,
		})
		if err != nil {
			return nil, nil, err
		}
		repository.RegisterMetrics(registerer)
		return repository, repository.Close, nil
	}
	pools := []interface{ Close() error }{}
	cleanup := func() {
		for _, db := range pools {
			_ = db.Close()
		}
	}
	complete := false
	defer func() {
		if !complete {
			cleanup()
		}
	}()
	readDB, err := openDB(cfg.databaseURL, cfg.readConns)
	if err != nil {
		return nil, nil, err
	}
	pools = append(pools, readDB)
	bmkgDB, err := openDB(cfg.databaseURL, cfg.bmkgWriteConns)
	if err != nil {
		return nil, nil, err
	}
	pools = append(pools, bmkgDB)
	pvmbgDB, err := openDB(cfg.databaseURL, cfg.pvmbgWriteConns)
	if err != nil {
		return nil, nil, err
	}
	pools = append(pools, pvmbgDB)
	relayDB, err := openDB(cfg.databaseURL, cfg.relayConns)
	if err != nil {
		return nil, nil, err
	}
	pools = append(pools, relayDB)

	startupCtx, startupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer startupCancel()
	if err := readDB.PingContext(startupCtx); err != nil {
		return nil, nil, fmt.Errorf("PostgreSQL is unavailable")
	}
	repository := store.NewWithPools(readDB, bmkgDB, pvmbgDB, relayDB, cfg.staleAfter)
	repository.SetTimeouts(cfg.dbReadTimeout, cfg.dbWriteTimeout)
	repository.SetOutboxLimit(cfg.outboxMaxPending)
	repository.SetMaxEnvelopeBytes(cfg.natsMaxMsgSize)
	if err := repository.Migrate(startupCtx); err != nil {
		return nil, nil, fmt.Errorf("apply Aggregator migrations: %w", err)
	}

	registerer.MustRegister(repository.MetricsCollector())
	complete = true
	return repository, cleanup, nil
}

func runShardMigration() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if cfg.shardConfigFile == "" {
		return fmt.Errorf("SHARD_CONFIG_FILE is required")
	}
	registerer := prometheus.NewRegistry()
	repository, closeRepository, err := openRepository(cfg, registerer)
	if err != nil {
		return err
	}
	defer closeRepository()
	legacy, err := openDB(cfg.databaseURL, 2)
	if err != nil {
		return err
	}
	defer legacy.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	if err := repository.(*sharded.Store).ImportLegacy(ctx, legacy); err != nil {
		return err
	}
	fmt.Println("Canonical records, history, source state, and outbox copied into four logical units.")
	return nil
}
