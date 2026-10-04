package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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

	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/api"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/broker"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/mapper"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/outbox"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/poller"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/store"
)

func main() {
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
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
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
	readDB, err := openDB(cfg.databaseURL, cfg.readConns)
	if err != nil {
		return err
	}
	defer readDB.Close()
	bmkgDB, err := openDB(cfg.databaseURL, cfg.bmkgWriteConns)
	if err != nil {
		return err
	}
	defer bmkgDB.Close()
	pvmbgDB, err := openDB(cfg.databaseURL, cfg.pvmbgWriteConns)
	if err != nil {
		return err
	}
	defer pvmbgDB.Close()
	relayDB, err := openDB(cfg.databaseURL, cfg.relayConns)
	if err != nil {
		return err
	}
	defer relayDB.Close()

	startupCtx, startupCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer startupCancel()
	if err := readDB.PingContext(startupCtx); err != nil {
		return fmt.Errorf("PostgreSQL is unavailable")
	}
	repository := store.NewWithPools(readDB, bmkgDB, pvmbgDB, relayDB, cfg.staleAfter)
	repository.SetTimeouts(cfg.dbReadTimeout, cfg.dbWriteTimeout)
	repository.SetOutboxLimit(cfg.outboxMaxPending)
	repository.SetMaxEnvelopeBytes(cfg.natsMaxMsgSize)
	if err := repository.Migrate(startupCtx); err != nil {
		return fmt.Errorf("apply Aggregator migrations: %w", err)
	}

	publisher, err := broker.NewJetStreamPublisher(broker.JetStreamConfig{
		URL: cfg.natsURL, Token: cfg.natsToken, Stream: cfg.natsStream,
		Subjects: []string{store.SubjectBMKG, store.SubjectPVMBG}, StreamSubjects: []string{store.SubjectFilter},
		MaxAge: cfg.natsMaxAge, MaxBytes: cfg.natsMaxBytes, MaxMsgs: cfg.natsMaxMsgs, MaxMsgSize: int32(cfg.natsMaxMsgSize), DuplicateWindow: cfg.natsDuplicateWindow, ConnectTimeout: time.Second,
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
	pollerService := poller.New(repository,
		poller.NewBMKGClient(cfg.bmkgURL, cfg.bmkgKey, &http.Client{Timeout: cfg.bmkgTimeout}, cfg.maxResponseBytes),
		poller.NewPVMBGClient(cfg.pvmbgURL, cfg.pvmbgToken, &http.Client{Timeout: cfg.pvmbgTimeout}, cfg.maxResponseBytes),
		mapper.New(mapper.DefaultVolcanoLocations()), logger)
	pollerService.SetMaxEnvelopeBytes(cfg.natsMaxMsgSize)
	pollerService.SetOutboxLimit(cfg.outboxMaxPending)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	waitForWorkers := startBackgroundTasks(
		func() {
			if err := pollerService.Run(ctx, cfg.pollInterval); err != nil && ctx.Err() == nil {
				logger.Error("source polling stopped", "reason", "POLL_LOOP_FAILURE")
			}
		},
		func() { _ = relay.Run(ctx) },
	)
	defer func() {
		stop()
		waitForWorkers()
	}()

	server := &http.Server{
		Addr:              cfg.listenAddr,
		Handler:           api.NewHandlerWithLimit(repository, cfg.internalKey, time.Now, cfg.maxInflight),
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
	listenAddr                                             string
	bmkgURL, pvmbgURL, bmkgKey, pvmbgToken, internalKey    string
	databaseURL                                            string
	readConns, bmkgWriteConns, pvmbgWriteConns, relayConns int
	dbReadTimeout, dbWriteTimeout                          time.Duration
	pollInterval, bmkgTimeout, pvmbgTimeout, staleAfter    time.Duration
	maxResponseBytes                                       int64
	maxInflight                                            int
	readHeaderTimeout, idleTimeout, writeTimeout           time.Duration
	natsURL, natsToken, natsStream                         string
	natsMaxAge, natsDuplicateWindow, natsPublishTimeout    time.Duration
	natsMaxBytes, natsMaxMsgs                              int64
	natsMaxMsgSize                                         int
	outboxPoll, outboxRetryMin, outboxRetryMax             time.Duration
	outboxBatch                                            int
	outboxMaxPending                                       int64
}

func loadConfig() (config, error) {
	var c config
	var err error
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
	if password == "" {
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
	if c.natsMaxMsgSize, err = envInt("NATS_MAX_MSG_SIZE", 65536, 1, 65536); err != nil {
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
	return c, nil
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
