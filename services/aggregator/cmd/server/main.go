package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
	"github.com/ryonlunar/tugas-besar-AAT/pkg/telemetry"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/api"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/audit"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/bmkgimport"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/broker"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/fieldreport"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/mapper"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/outbox"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/pipeline"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/poller"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/satellite"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/sharded"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/sourcejournal"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/store"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "migrate-shards" {
		if err := runShardMigration(); err != nil {
			fmt.Fprintln(os.Stderr, "shard migration failed:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		port, err := strconv.Atoi(env("AGGREGATOR_HTTP_PORT", "8083"))
		if err != nil || port < 1 || port > 65535 || probeReadiness(&http.Client{Timeout: time.Second}, "http://127.0.0.1:"+strconv.Itoa(port)) != nil {
			os.Exit(1)
		}
		return
	}
	if len(os.Args) >= 2 && os.Args[1] == "history-audit" {
		if err := runHistoryAuditCommand(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "history audit failed:", err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "store-audit" {
		if err := runStoreAuditCommand(); err != nil {
			fmt.Fprintln(os.Stderr, "store audit failed:", err)
			os.Exit(1)
		}
		return
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("instance", env("POD_NAME", "aggregator"))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("aggregator stopped", "reason", "STARTUP_OR_SERVER_FAILURE")
		os.Exit(1)
	}
}

func run(logger *slog.Logger) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	sensor, err := coastalAdapter(cfg)
	if err != nil {
		return err
	}
	closeTracing, traceErr := telemetry.StartTracing("aggregator")
	if traceErr != nil {
		return traceErr
	}
	defer closeTracing()
	monitor := telemetry.New()
	repository, closeRepository, err := openRepository(cfg, monitor.Registry)
	if err != nil {
		return err
	}
	defer closeRepository()

	publishSubjects := []string{store.SubjectBMKG, store.SubjectPVMBG}
	if cfg.shardConfigFile != "" {
		for _, source := range []contracts.Source{contracts.SourceSatellite, contracts.SourceCoastal} {
			subject, err := store.SubjectForSource(source)
			if err != nil {
				return err
			}
			publishSubjects = append(publishSubjects, subject)
		}
	}
	publisher, err := broker.NewJetStreamPublisher(broker.JetStreamConfig{
		URL: cfg.natsURL, Token: cfg.natsToken, Stream: cfg.natsStream,
		Subjects: publishSubjects, StreamSubjects: []string{store.SubjectFilter},
		MaxAge: cfg.natsMaxAge, MaxBytes: cfg.natsMaxBytes, MaxMsgs: cfg.natsMaxMsgs, MaxMsgSize: int32(cfg.natsMaxMsgSize), DuplicateWindow: cfg.natsDuplicateWindow, ConnectTimeout: time.Second, Replicas: cfg.natsReplicas,
	})
	if err != nil {
		return err
	}
	defer publisher.Close()
	relay := outbox.New(repository, publisher, cfg.outboxBatch, cfg.outboxPoll, cfg.outboxRetryMin, cfg.outboxRetryMax)
	if err := relay.SetPublishTimeout(cfg.natsPublishTimeout); err != nil {
		return fmt.Errorf("configure NATS publish timeout: %w", err)
	}
	relay.SetLogger(logger)
	bmkgClient := poller.NewBMKGClient(cfg.bmkgURL, cfg.bmkgKey, &http.Client{Timeout: cfg.bmkgTimeout, Transport: telemetry.HTTPTransport(http.DefaultTransport)}, cfg.maxResponseBytes)
	pvmbgClient := poller.NewPVMBGClient(cfg.pvmbgURL, cfg.pvmbgToken, &http.Client{Timeout: cfg.pvmbgTimeout, Transport: telemetry.HTTPTransport(http.DefaultTransport)}, cfg.maxResponseBytes)
	pollerService := poller.New(repository, bmkgClient, pvmbgClient,
		mapper.New(mapper.DefaultVolcanoLocations()).WithVolcanicAdaptations(cfg.pvmbgAdapters), logger)
	pollerService.SetMaxEnvelopeBytes(cfg.natsMaxMsgSize)
	pollerService.SetOutboxLimit(cfg.outboxMaxPending)

	if sensor != nil {
		sensor.Observe = monitor.ObserveOperation
	}
	pollerService.Observe = monitor.ObserveOperation
	relay.Observe = monitor.ObserveOperation
	closeMetrics, err := monitor.Listen("http://127.0.0.1" + cfg.listenAddr + "/ready")
	if err != nil {
		return err
	}
	defer closeMetrics()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	waitForWorkers := startBackgroundTasks(
		func() {
			run := func() error { return pollerService.Run(ctx, cfg.pollInterval) }
			if distributed, ok := repository.(*sharded.Store); ok {
				run = func() error {
					return distributed.CoordinateSessions(ctx, configuredSources(cfg, sensor != nil), cfg.sourceIdle, cfg.sourceRetry, logger,
						func(ctx context.Context, source contracts.Source, session *sourcejournal.Session) error {
							if source == contracts.SourceCoastal {
								return pipeline.RunAdapter(ctx, session, source, sensor, sensor.Process, distributed, cfg.natsMaxMsgSize, cfg.processingEnabled, logger, monitor.ObserveOperation)
							}
							if source == contracts.SourceSatellite {
								return runSatelliteSource(ctx, cfg, session, distributed, logger, monitor.ObserveOperation)
							}
							process := pipeline.DefaultProcessor(mapper.New(mapper.DefaultVolcanoLocations()).WithVolcanicAdaptations(cfg.pvmbgAdapters))
							if source == contracts.SourceBMKG && cfg.bmkgHistoricalCatalogFile != "" {
								if err := runBMKGCatalogImport(ctx, cfg, session, distributed, process, logger, monitor.ObserveOperation); err != nil {
									return err
								}
							}
							read := pipeline.Reader(bmkgClient.ReadRecords)
							if source == contracts.SourcePVMBG {
								read = pvmbgClient.ReadRecords
							}
							return pipeline.RunSession(ctx, session, source, read, process, distributed, cfg.pollInterval, cfg.natsMaxMsgSize, cfg.processingEnabled, logger, monitor.ObserveOperation)
						})
				}
			} else if cfg.satelliteEnabled {
				logger.Warn("satellite source configured but repository is not the sharded profile", "reason", "SATELLITE_REQUIRES_SHARDED_PROFILE")
			}
			if err := run(); err != nil && ctx.Err() == nil {
				logger.Error("source polling stopped", "reason", "POLL_LOOP_FAILURE")
			}
		},
		func() { _ = relay.Run(ctx) },
		func() {
			if cfg.fieldReportProcessing {
				fieldreport.RunWorker(ctx, repository, env("POD_NAME", "aggregator"), cfg.fieldReportInterval, logger, monitor.ObserveOperation)
			}
		},
	)
	defer func() {
		stop()
		waitForWorkers()
	}()

	handler := http.NewServeMux()
	handler.Handle("/", api.NewHandlerWithLimit(repository, cfg.internalKey, time.Now, cfg.maxInflight))
	reports := fieldreport.NewHandler(repository, cfg.internalKey, cfg.fieldReportMaxInflight)
	handler.Handle("/internal/v1/field-reports", reports)
	handler.Handle("/internal/v1/field-reports/", reports)
	if distributed, ok := repository.(*sharded.Store); ok {
		ownerAPI := audit.NewHandler(distributed, cfg.auditToken, cfg.internalKey)
		handler.Handle("/internal/audit/", ownerAPI)
		handler.Handle("/internal/v1/failed-messages", ownerAPI)
		handler.Handle("/internal/v1/failed-messages/", ownerAPI)
	}
	server := &http.Server{
		Addr:              cfg.listenAddr,
		Handler:           monitor.Wrap(handler, "/internal/v1/analytics", "/internal/v1/hazards", "/internal/v1/hazards/{id}", "/internal/audit/events", "/internal/audit/checksum", "/internal/audit/location", "/internal/audit/unprocessed", "/internal/v1/failed-messages", "/internal/v1/failed-messages/{message_id}", "/internal/v1/failed-messages/{message_id}/replay", "/internal/v1/field-reports", "/internal/v1/field-reports/{report_id}"),
		ReadHeaderTimeout: cfg.readHeaderTimeout,
		IdleTimeout:       cfg.idleTimeout,
		WriteTimeout:      cfg.writeTimeout,
	}
	serverErr := make(chan error, 1)
	go func() { serverErr <- server.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown HTTP server: %w", err)
		}
		return nil
	case err := <-serverErr:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("listen Aggregator: %w", err)
	}
}

func startBackgroundTasks(tasks ...func()) func() {
	var wg sync.WaitGroup
	wg.Add(len(tasks))
	for _, task := range tasks {
		go func(task func()) {
			defer wg.Done()
			task()
		}(task)
	}
	return func() { wg.Wait() }
}

func configuredSources(cfg config, coastalEnabled bool) []contracts.Source {
	sources := []contracts.Source{contracts.SourceBMKG, contracts.SourcePVMBG}
	if cfg.satelliteEnabled {
		sources = append(sources, contracts.SourceSatellite)
	}
	if coastalEnabled {
		sources = append(sources, contracts.SourceCoastal)
	}
	return sources
}

// runSatelliteSource wires a satellite.Client/Adapter/Recognizer into
// pipeline.RunAdapter for the SATELIT source. CanRead backs off once the
// journal has 9000+ unfinished inputs (ahead of RunAdapter's own
// PollingAdapter-only 10000 cap, which does not apply to a non-polling
// adapter) so a slow processor never lets the satellite source run away
// unbounded (D4/P4.4d).
func runSatelliteSource(ctx context.Context, cfg config, session *sourcejournal.Session, distributed *sharded.Store, logger *slog.Logger, observe func(string, string, error, time.Duration)) error {
	client, err := satellite.NewClient(cfg.satelliteBaseURL, cfg.satelliteToken, cfg.satelliteRequestTimeout)
	if err != nil {
		return err
	}
	adapter := &satellite.Adapter{
		Client:       client,
		MaxEvents:    cfg.satelliteBatchMaxEvents,
		MaxBytes:     cfg.satelliteBatchMaxBytes,
		MaxWait:      cfg.satelliteBatchMaxWait,
		IdleTimeout:  cfg.satelliteIdleTimeout,
		ReconnectMin: cfg.satelliteReconnectMin,
		ReconnectMax: cfg.satelliteReconnectMax,
		CanRead: func(ctx context.Context) (bool, error) {
			n, err := session.Unfinished(ctx)
			return n < 9000, err
		},
		Observe: func(outcome, reason string, err error, elapsed time.Duration) {
			observe("satellite_adapter_"+outcome, reason, err, elapsed)
		},
	}
	recognizer := satellite.NewRecognizer(cfg.satelliteLateness)
	return pipeline.RunAdapter(ctx, session, contracts.SourceSatellite, adapter, satellite.Processor(recognizer), distributed, cfg.natsMaxMsgSize, cfg.processingEnabled, logger, observe)
}

// runBMKGCatalogImport runs the historical-catalog import phase (P1.1a,
// D11) to completion before the caller falls through to BMKG's live
// polling. It treats the import's own completion (io.EOF, from either
// finishing the file or finding a cursor already marked done for this
// exact file content) as success; any other error -- including ctx
// cancellation -- propagates so CoordinateSessions retries from the
// durable row cursor rather than silently skipping ahead to live polling
// with a half-imported catalog.
func runBMKGCatalogImport(ctx context.Context, cfg config, session *sourcejournal.Session, distributed *sharded.Store, process pipeline.Processor, logger *slog.Logger, observe func(string, string, error, time.Duration)) error {
	adapter := &bmkgimport.Adapter{
		Path: cfg.bmkgHistoricalCatalogFile,
		CanRead: func(ctx context.Context) (bool, error) {
			n, err := session.Unfinished(ctx)
			return n < 9000, err
		},
	}
	err := pipeline.RunAdapter(ctx, session, contracts.SourceBMKG, adapter, process, distributed, cfg.natsMaxMsgSize, cfg.processingEnabled, logger, observe)
	if err != nil && !errors.Is(err, io.EOF) {
		logger.Error("bmkg historical catalog import stopped", "reason", "CATALOG_IMPORT_FAILURE")
		return err
	}
	return nil
}

func probeReadiness(client *http.Client, baseURL string) error {
	if client == nil {
		client = &http.Client{Timeout: time.Second}
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(baseURL, "/")+"/ready", nil)
	if err != nil {
		return errors.New("readiness probe request could not be created")
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("readiness endpoint is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("readiness endpoint is not ready")
	}
	return nil
}

func openDB(dsn string, maxConns int) (*sql.DB, error) {
	db, err := sql.Open("pgx", dsn)
	if err != nil {
		return nil, fmt.Errorf("configure PostgreSQL pool: %w", err)
	}
	db.SetMaxOpenConns(maxConns)
	db.SetMaxIdleConns(maxConns)
	db.SetConnMaxIdleTime(5 * time.Minute)
	return db, nil
}

type config struct {
	processingEnabled bool

	fieldReportProcessing               bool
	fieldReportInterval                 time.Duration
	fieldReportMaxInflight, reportConns int

	auditToken                                                                                          string
	shardConfigFile                                                                                     string
	sourceIdle, sourceRetry                                                                             time.Duration
	natsReplicas                                                                                        int
	listenAddr                                                                                          string
	bmkgURL, pvmbgURL, bmkgKey, pvmbgToken, internalKey                                                 string
	pvmbgAdapters                                                                                       mapper.VolcanicAdaptations
	databaseURL                                                                                         string
	readConns, bmkgWriteConns, pvmbgWriteConns, relayConns                                              int
	dbReadTimeout, dbWriteTimeout                                                                       time.Duration
	pollInterval, bmkgTimeout, pvmbgTimeout, staleAfter                                                 time.Duration
	maxResponseBytes                                                                                    int64
	maxInflight                                                                                         int
	readHeaderTimeout, idleTimeout, writeTimeout                                                        time.Duration
	natsURL, natsToken, natsStream                                                                      string
	natsMaxAge, natsDuplicateWindow, natsPublishTimeout                                                 time.Duration
	natsMaxBytes, natsMaxMsgs                                                                           int64
	natsMaxMsgSize                                                                                      int
	outboxPoll, outboxRetryMin, outboxRetryMax                                                          time.Duration
	outboxBatch                                                                                         int
	outboxMaxPending                                                                                    int64
	satelliteEnabled                                                                                    bool
	satelliteBaseURL, satelliteToken                                                                    string
	satelliteLateness, satelliteBatchMaxWait, satelliteIdleTimeout                                      time.Duration
	satelliteReconnectMin, satelliteReconnectMax, satelliteRequestTimeout, satelliteSessionPollInterval time.Duration
	satelliteBatchMaxEvents, satelliteBatchMaxBytes                                                     int
	bmkgHistoricalCatalogFile                                                                           string
}

func loadConfig() (config, error) {
	var c config
	var err error
	c.processingEnabled, err = strconv.ParseBool(env("INGEST_PROCESSING_ENABLED", "true"))
	if err != nil {
		return c, errors.New("INGEST_PROCESSING_ENABLED must be boolean")
	}
	c.fieldReportProcessing, err = strconv.ParseBool(env("FIELD_REPORT_PROCESSING_ENABLED", "true"))
	if err != nil {
		return c, errors.New("FIELD_REPORT_PROCESSING_ENABLED must be boolean")
	}
	if c.fieldReportInterval, err = envDuration("FIELD_REPORT_PROCESS_INTERVAL", "500ms"); err != nil || c.fieldReportInterval <= 0 {
		return c, errors.New("FIELD_REPORT_PROCESS_INTERVAL must be a positive duration")
	}
	if c.fieldReportMaxInflight, err = envInt("FIELD_REPORT_MAX_INFLIGHT", 64, 1, 10000); err != nil {
		return c, err
	}
	if c.reportConns, err = envInt("DB_REPORT_MAX_CONNS", 4, 1, 50); err != nil {
		return c, err
	}
	c.auditToken = os.Getenv("AUDIT_TOKEN")
	c.listenAddr = ":" + env("AGGREGATOR_HTTP_PORT", "8083")
	c.bmkgURL = env("BMKG_BASE_URL", "http://bmkg-mock:8081")
	c.pvmbgURL = env("PVMBG_BASE_URL", "http://pvmbg-mock:8082")
	c.bmkgKey = os.Getenv("BMKG_API_KEY")
	c.pvmbgToken = os.Getenv("PVMBG_TOKEN")
	c.internalKey = os.Getenv("AGGREGATOR_API_KEY")
	if c.bmkgKey == "" || c.pvmbgToken == "" || c.internalKey == "" {
		return config{}, errors.New("source and internal credentials must be set")
	}

	host := env("POSTGRES_HOST", "postgres")
	port := env("POSTGRES_PORT", "5432")
	database := env("POSTGRES_DB", "aat")
	user := env("POSTGRES_USER", "aat_aggregator")
	password := os.Getenv("POSTGRES_PASSWORD")
	c.shardConfigFile = strings.TrimSpace(os.Getenv("SHARD_CONFIG_FILE"))
	if password == "" && c.shardConfigFile == "" {
		return config{}, errors.New("Aggregator PostgreSQL password must be set")
	}
	sslmode := env("POSTGRES_SSLMODE", "disable")
	u := url.URL{Scheme: "postgres", User: url.UserPassword(user, password), Host: net.JoinHostPort(host, port), Path: "/" + database}
	q := u.Query()
	q.Set("sslmode", sslmode)
	u.RawQuery = q.Encode()
	c.databaseURL = u.String()

	if c.readConns, err = envInt("DB_READ_MAX_CONNS", 10, 1, 100); err != nil {
		return config{}, err
	}
	if c.bmkgWriteConns, err = envInt("DB_BMKG_WRITE_MAX_CONNS", 2, 1, 20); err != nil {
		return config{}, err
	}
	if c.pvmbgWriteConns, err = envInt("DB_PVMBG_WRITE_MAX_CONNS", 2, 1, 20); err != nil {
		return config{}, err
	}
	if c.relayConns, err = envInt("DB_RELAY_MAX_CONNS", 2, 1, 20); err != nil {
		return config{}, err
	}
	if c.dbReadTimeout, err = envDuration("DB_READ_TIMEOUT", "200ms"); err != nil {
		return config{}, err
	}
	if c.dbWriteTimeout, err = envDuration("DB_WRITE_TIMEOUT", "2s"); err != nil {
		return config{}, err
	}
	if c.pollInterval, err = envDuration("SOURCE_POLL_INTERVAL", "5s"); err != nil {
		return config{}, err
	}
	if c.bmkgTimeout, err = envDuration("BMKG_REQUEST_TIMEOUT", "1s"); err != nil {
		return config{}, err
	}
	if c.pvmbgTimeout, err = envDuration("PVMBG_REQUEST_TIMEOUT", "4s"); err != nil {
		return config{}, err
	}
	if c.staleAfter, err = envDuration("SOURCE_STALE_AFTER", "15s"); err != nil {
		return config{}, err
	}
	if c.maxResponseBytes, err = envInt64("SOURCE_RESPONSE_MAX_BYTES", 1048576, 1, 64*1024*1024); err != nil {
		return config{}, err
	}
	if c.pollInterval <= 0 || c.bmkgTimeout <= 0 || c.pvmbgTimeout <= 0 || c.staleAfter <= 0 {
		return config{}, errors.New("source intervals and timeouts must be positive")
	}
	if c.pvmbgTimeout <= 3*time.Second {
		return config{}, errors.New("PVMBG_REQUEST_TIMEOUT must exceed the maximum normal PVMBG delay")
	}

	if c.maxInflight, err = envInt("AGGREGATOR_MAX_INFLIGHT", 128, 1, 10000); err != nil {
		return config{}, err
	}
	if c.readHeaderTimeout, err = envDuration("HTTP_READ_HEADER_TIMEOUT", "2s"); err != nil {
		return config{}, err
	}
	if c.idleTimeout, err = envDuration("HTTP_IDLE_TIMEOUT", "30s"); err != nil {
		return config{}, err
	}
	if c.writeTimeout, err = envDuration("HTTP_WRITE_TIMEOUT", "5s"); err != nil {
		return config{}, err
	}

	c.natsURL = env("NATS_URL", "nats://nats:4222")
	c.natsToken = os.Getenv("NATS_TOKEN")
	c.natsStream = env("NATS_STREAM_NAME", "HAZARD_EVENTS")
	if c.natsToken == "" {
		return config{}, errors.New("NATS_TOKEN must be set")
	}
	if c.natsReplicas, err = envInt("NATS_STREAM_REPLICAS", 1, 1, 5); err != nil {
		return config{}, err
	}
	if c.natsReplicas != 1 && c.natsReplicas != 3 && c.natsReplicas != 5 {
		return config{}, errors.New("NATS_STREAM_REPLICAS must be 1, 3, or 5")
	}
	if c.sourceIdle, err = envDuration("SOURCE_SESSION_IDLE_TIMEOUT", "10s"); err != nil {
		return config{}, err
	}
	if c.sourceRetry, err = envDuration("SOURCE_RETRY_INTERVAL", "2s"); err != nil {
		return config{}, err
	}
	if c.sourceIdle < time.Second || c.sourceRetry <= 0 {
		return config{}, errors.New("invalid source ownership timeouts")
	}
	if c.natsPublishTimeout, err = envDuration("NATS_PUBLISH_TIMEOUT", "2s"); err != nil {
		return config{}, err
	}
	if c.natsPublishTimeout <= 0 {
		return config{}, errors.New("NATS_PUBLISH_TIMEOUT must be positive")
	}
	if c.natsMaxAge, err = envDuration("NATS_MAX_AGE", "24h"); err != nil {
		return config{}, err
	}
	if c.natsDuplicateWindow, err = envDuration("NATS_DUPLICATE_WINDOW", "2m"); err != nil {
		return config{}, err
	}
	if c.natsMaxBytes, err = envInt64("NATS_MAX_BYTES", 268435456, 1, 1<<40); err != nil {
		return config{}, err
	}
	if c.natsMaxMsgs, err = envInt64("NATS_MAX_MSGS", 100000, 1, 1<<40); err != nil {
		return config{}, err
	}
	defaultEnvelopeBytes := broker.DefaultEnvelopeBytes
	if c.shardConfigFile != "" {
		defaultEnvelopeBytes = broker.MaxEnvelopeBytes
	}
	if c.natsMaxMsgSize, err = envInt("NATS_MAX_MSG_SIZE", defaultEnvelopeBytes, 1, broker.MaxEnvelopeBytes); err != nil {
		return config{}, err
	}
	if c.natsMaxMsgSize > int(^uint32(0)>>1) {
		return config{}, errors.New("NATS_MAX_MSG_SIZE exceeds supported range")
	}
	if c.natsMaxAge <= 0 {
		return config{}, errors.New("NATS_MAX_AGE must be positive")
	}
	if c.natsDuplicateWindow < broker.MinDuplicateWindow {
		return config{}, fmt.Errorf("NATS_DUPLICATE_WINDOW must be at least %s", broker.MinDuplicateWindow)
	}
	if c.outboxPoll, err = envDuration("OUTBOX_POLL_INTERVAL", "500ms"); err != nil {
		return config{}, err
	}
	if c.outboxRetryMin, err = envDuration("OUTBOX_RETRY_MIN", "1s"); err != nil {
		return config{}, err
	}
	if c.outboxRetryMax, err = envDuration("OUTBOX_RETRY_MAX", "30s"); err != nil {
		return config{}, err
	}
	if c.outboxBatch, err = envInt("OUTBOX_BATCH_SIZE", 20, 1, 1000); err != nil {
		return config{}, err
	}
	if c.outboxMaxPending, err = envInt64("OUTBOX_MAX_PENDING", store.DefaultOutboxLimit, 1, 1_000_000_000); err != nil {
		return config{}, err
	}
	if c.outboxPoll <= 0 || c.outboxRetryMin <= 0 || c.outboxRetryMax < c.outboxRetryMin {
		return config{}, errors.New("outbox retry configuration is invalid")
	}
	if c.pvmbgAdapters, err = mapper.ParseVolcanicAdaptations(os.Getenv("PVMBG_SCHEMA_ADAPTERS")); err != nil {
		return config{}, err
	}
	if err := c.loadSatelliteConfig(); err != nil {
		return config{}, err
	}
	c.bmkgHistoricalCatalogFile = strings.TrimSpace(os.Getenv("BMKG_HISTORICAL_CATALOG_FILE"))
	return c, nil
}

// loadSatelliteConfig reads Feed Satelit settings (SAT.1, SAT.2). Both
// SATELLITE_BASE_URL and SATELLITE_TOKEN empty means the source is simply
// disabled -- this keeps dev/CI/M1-profile startup working unchanged.
// Exactly one set is a startup error: a half-configured source is never
// silently ignored.
func (c *config) loadSatelliteConfig() error {
	c.satelliteBaseURL = strings.TrimSpace(os.Getenv("SATELLITE_BASE_URL"))
	c.satelliteToken = os.Getenv("SATELLITE_TOKEN")
	switch {
	case c.satelliteBaseURL == "" && c.satelliteToken == "":
		c.satelliteEnabled = false
		return nil
	case c.satelliteBaseURL == "" || c.satelliteToken == "":
		return errors.New("SATELLITE_BASE_URL and SATELLITE_TOKEN must both be set, or both left empty")
	}
	parsed, err := url.Parse(c.satelliteBaseURL)
	if err != nil {
		return errors.New("SATELLITE_BASE_URL is not a valid URL")
	}
	host := parsed.Hostname()
	localOrBareService := host == "127.0.0.1" || host == "localhost" || !strings.Contains(host, ".")
	if parsed.Scheme != "https" && !(parsed.Scheme == "http" && localOrBareService) {
		return errors.New("SATELLITE_BASE_URL must use https:// (http:// only for 127.0.0.1/localhost or a bare service name)")
	}
	c.satelliteEnabled = true
	var err2 error
	if c.satelliteLateness, err2 = envDuration("SATELLITE_ALLOWED_LATENESS", "12h"); err2 != nil {
		return err2
	}
	if c.satelliteBatchMaxEvents, err2 = envInt("SATELLITE_BATCH_MAX_EVENTS", 200, 1, 10000); err2 != nil {
		return err2
	}
	if c.satelliteBatchMaxBytes, err2 = envInt("SATELLITE_BATCH_MAX_BYTES", 262144, 1, 1<<20); err2 != nil {
		return err2
	}
	if c.satelliteBatchMaxWait, err2 = envDuration("SATELLITE_BATCH_MAX_WAIT", "250ms"); err2 != nil {
		return err2
	}
	if c.satelliteIdleTimeout, err2 = envDuration("SATELLITE_IDLE_TIMEOUT", "30s"); err2 != nil {
		return err2
	}
	if c.satelliteReconnectMin, err2 = envDuration("SATELLITE_RECONNECT_MIN", "500ms"); err2 != nil {
		return err2
	}
	if c.satelliteReconnectMax, err2 = envDuration("SATELLITE_RECONNECT_MAX", "30s"); err2 != nil {
		return err2
	}
	if c.satelliteRequestTimeout, err2 = envDuration("SATELLITE_REQUEST_TIMEOUT", "5s"); err2 != nil {
		return err2
	}
	if c.satelliteSessionPollInterval, err2 = envDuration("SATELLITE_SESSION_POLL_INTERVAL", "5s"); err2 != nil {
		return err2
	}
	if c.satelliteLateness <= 0 || c.satelliteBatchMaxWait <= 0 || c.satelliteIdleTimeout <= 0 ||
		c.satelliteReconnectMin <= 0 || c.satelliteReconnectMax < c.satelliteReconnectMin ||
		c.satelliteRequestTimeout <= 0 || c.satelliteSessionPollInterval <= 0 {
		return errors.New("satellite durations must be positive, and SATELLITE_RECONNECT_MAX must be at least SATELLITE_RECONNECT_MIN")
	}
	return nil
}

func env(name, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	return fallback
}
func envInt(name string, fallback, min, max int) (int, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil || parsed < min || parsed > max {
		return 0, fmt.Errorf("%s is outside its valid range", name)
	}
	return parsed, nil
}
func envInt64(name string, fallback, min, max int64) (int64, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(value, 10, 64)
	if err != nil || parsed < min || parsed > max {
		return 0, fmt.Errorf("%s is outside its valid range", name)
	}
	return parsed, nil
}
func envDuration(name, fallback string) (time.Duration, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		value = fallback
	}
	parsed, err := time.ParseDuration(value)
	if err != nil {
		return 0, fmt.Errorf("%s must be a valid duration", name)
	}
	return parsed, nil
}
