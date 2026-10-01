package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/services/client-api/internal/api"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		port := env("API_HTTP_PORT", "8080")
		client := http.Client{Timeout: time.Second}
		resp, err := client.Get("http://127.0.0.1:" + port + "/ready")
		if err != nil || resp.StatusCode != 200 {
			os.Exit(1)
		}
		resp.Body.Close()
		return
	}
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	slog.SetDefault(logger)
	if err := run(logger); err != nil {
		logger.Error("client api stopped", "reason", "STARTUP_OR_SERVER_FAILURE")
		os.Exit(1)
	}
}
func run(logger *slog.Logger) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	data, err := os.ReadFile(cfg.publicKeyFile)
	if err != nil {
		return err
	}
	key, err := api.ParsePublicKey(data)
	if err != nil {
		return err
	}
	handler := api.NewHandler(api.Config{PublicKey: key, Issuer: cfg.issuer, Audience: cfg.audience, AggregatorURL: cfg.aggregatorURL, AggregatorKey: cfg.aggregatorKey, Timeout: cfg.timeout, MaxInflight: cfg.maxInflight})
	server := &http.Server{Addr: ":" + cfg.port, Handler: handler, ReadHeaderTimeout: cfg.readHeaderTimeout, IdleTimeout: cfg.idleTimeout, WriteTimeout: cfg.writeTimeout}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case <-ctx.Done():
		shut, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return server.Shutdown(shut)
	case err := <-done:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

type config struct {
	port, publicKeyFile, issuer, audience, aggregatorURL, aggregatorKey string
	timeout, readHeaderTimeout, idleTimeout, writeTimeout               time.Duration
	maxInflight                                                         int
}

func loadConfig() (config, error) {
	var c config
	var err error
	c.port = env("API_HTTP_PORT", "8080")
	c.publicKeyFile = os.Getenv("JWT_PUBLIC_KEY_FILE")
	c.issuer = env("JWT_ISSUER", "aat-auth")
	c.audience = env("JWT_AUDIENCE", "aat-client-api")
	c.aggregatorURL = env("AGGREGATOR_BASE_URL", "http://aggregator:8083")
	c.aggregatorKey = os.Getenv("AGGREGATOR_API_KEY")
	if c.publicKeyFile == "" || c.aggregatorKey == "" {
		return c, errors.New("JWT_PUBLIC_KEY_FILE and AGGREGATOR_API_KEY must be set")
	}
	if c.timeout, err = duration("AGGREGATOR_REQUEST_TIMEOUT", "1s"); err != nil {
		return c, err
	}
	if c.readHeaderTimeout, err = duration("HTTP_READ_HEADER_TIMEOUT", "2s"); err != nil {
		return c, err
	}
	if c.idleTimeout, err = duration("HTTP_IDLE_TIMEOUT", "30s"); err != nil {
		return c, err
	}
	if c.writeTimeout, err = duration("HTTP_WRITE_TIMEOUT", "5s"); err != nil {
		return c, err
	}
	if c.maxInflight, err = intEnv("API_MAX_INFLIGHT", 128, 1, 10000); err != nil {
		return c, err
	}
	if c.timeout <= 0 {
		return c, errors.New("AGGREGATOR_REQUEST_TIMEOUT must be positive")
	}
	return c, nil
}
func env(k, d string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return d
}
func duration(k, d string) (time.Duration, error) { return time.ParseDuration(env(k, d)) }
func intEnv(k string, d, min, max int) (int, error) {
	v := env(k, "")
	if v == "" {
		return d, nil
	}
	n, e := strconv.Atoi(v)
	if e != nil || n < min || n > max {
		return 0, fmt.Errorf("%s is outside its valid range", k)
	}
	return n, nil
}
