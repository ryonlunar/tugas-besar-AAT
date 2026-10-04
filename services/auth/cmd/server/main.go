package main

import (
	"context"
	"crypto/rsa"
	"crypto/x509"
	"database/sql"
	"encoding/pem"
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

	"github.com/ryonlunar/tugas-besar-AAT/services/auth/internal/auth"
	_ "modernc.org/sqlite"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		port := env("AUTH_HTTP_PORT", "8084")
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
		logger.Error("auth stopped", "reason", "STARTUP_OR_SERVER_FAILURE")
		os.Exit(1)
	}
}
func run(logger *slog.Logger) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	key, err := loadPrivateKey(cfg.privateKeyFile)
	if err != nil {
		return err
	}
	db, err := sql.Open("sqlite", cfg.statePath)
	if err != nil {
		return err
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := auth.Migrate(ctx, db); err != nil {
		return fmt.Errorf("migrate auth state: %w", err)
	}
	if err := db.PingContext(ctx); err != nil {
		return err
	}
	service, err := auth.New(db, key, cfg.clients, cfg.issuer, cfg.audience, cfg.accessTTL, cfg.refreshTTL, time.Now)
	if err != nil {
		return err
	}
	server := newHTTPServer(cfg, auth.NewHandler(service, cfg.maxBody, cfg.maxInflight))
	stopCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	done := make(chan error, 1)
	go func() { done <- server.ListenAndServe() }()
	select {
	case <-stopCtx.Done():
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
	port, statePath, privateKeyFile, issuer, audience                                string
	clients                                                                          map[string]auth.Client
	accessTTL, refreshTTL, readTimeout, readHeaderTimeout, idleTimeout, writeTimeout time.Duration
	maxBody                                                                          int64
	maxInflight                                                                      int
}

func loadConfig() (config, error) {
	var c config
	var err error
	c.port = env("AUTH_HTTP_PORT", "8084")
	c.statePath = env("AUTH_STATE_PATH", "/data/auth.db")
	c.privateKeyFile = os.Getenv("JWT_PRIVATE_KEY_FILE")
	c.issuer = env("JWT_ISSUER", "aat-auth")
	c.audience = env("JWT_AUDIENCE", "aat-client-api")
	if c.privateKeyFile == "" {
		return c, errors.New("JWT_PRIVATE_KEY_FILE must be set")
	}
	c.clients = map[string]auth.Client{"media": {Secret: os.Getenv("MEDIA_CLIENT_SECRET"), Scope: "hazards:summary"}, "field-team": {Secret: os.Getenv("FIELD_TEAM_CLIENT_SECRET"), Scope: "hazards:summary hazards:raw"}, "bnpb-internal": {Secret: os.Getenv("BNPB_INTERNAL_CLIENT_SECRET"), Scope: "hazards:summary hazards:raw"}}
	if c.accessTTL, err = duration("ACCESS_TOKEN_TTL", "60s"); err != nil {
		return c, err
	}
	if c.refreshTTL, err = duration("REFRESH_TOKEN_TTL", "24h"); err != nil {
		return c, err
	}
	if c.readHeaderTimeout, err = duration("HTTP_READ_HEADER_TIMEOUT", "2s"); err != nil {
		return c, err
	}
	if c.readTimeout, err = duration("AUTH_READ_TIMEOUT", "2s"); err != nil {
		return c, err
	}
	if c.readTimeout <= 0 {
		return c, errors.New("AUTH_READ_TIMEOUT must be positive")
	}
	if c.idleTimeout, err = duration("HTTP_IDLE_TIMEOUT", "30s"); err != nil {
		return c, err
	}
	if c.writeTimeout, err = duration("HTTP_WRITE_TIMEOUT", "5s"); err != nil {
		return c, err
	}
	if c.maxBody, err = int64Env("AUTH_BODY_MAX_BYTES", 8192, 1, 1048576); err != nil {
		return c, err
	}
	if c.maxInflight, err = intEnv("AUTH_MAX_INFLIGHT", 16, 1, 10000); err != nil {
		return c, err
	}
	if c.accessTTL <= 0 || c.refreshTTL <= 0 {
		return c, errors.New("token TTL values must be positive")
	}
	return c, nil
}
func newHTTPServer(cfg config, handler http.Handler) *http.Server {
	return &http.Server{Addr: ":" + cfg.port, Handler: handler, ReadTimeout: cfg.readTimeout, ReadHeaderTimeout: cfg.readHeaderTimeout, IdleTimeout: cfg.idleTimeout, WriteTimeout: cfg.writeTimeout}
}
func loadPrivateKey(path string) (*rsa.PrivateKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("JWT private key is not PEM")
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		if key.N.BitLen() < 2048 {
			return nil, errors.New("RSA key must be at least 2048 bits")
		}
		return key, nil
	}
	keyAny, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, errors.New("JWT private key is invalid")
	}
	key, ok := keyAny.(*rsa.PrivateKey)
	if !ok || key.N.BitLen() < 2048 {
		return nil, errors.New("RSA key must be at least 2048 bits")
	}
	return key, nil
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
func int64Env(k string, d, min, max int64) (int64, error) {
	v := env(k, "")
	if v == "" {
		return d, nil
	}
	n, e := strconv.ParseInt(v, 10, 64)
	if e != nil || n < min || n > max {
		return 0, fmt.Errorf("%s is outside its valid range", k)
	}
	return n, nil
}
