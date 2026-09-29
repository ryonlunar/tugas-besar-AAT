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

	"github.com/ryonlunar/tugas-besar-AAT/services/bmkg-mock/internal/mock"
)

func main() {
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		if err := healthcheck(); err != nil {
			os.Exit(1)
		}
		return
	}
	if err := run(); err != nil {
		slog.Error("service stopped", "service", "bmkg-mock", "error", err)
		os.Exit(1)
	}
}

func healthcheck() error {
	port, err := intEnv("BMKG_MOCK_HTTP_PORT", 8081)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("BMKG mock healthcheck has invalid port")
	}
	return probeHealth(&http.Client{Timeout: time.Second}, "http://127.0.0.1:"+strconv.Itoa(port))
}

func probeHealth(client *http.Client, baseURL string) error {
	if client == nil {
		client = &http.Client{Timeout: time.Second}
	}
	request, err := http.NewRequest(http.MethodGet, strings.TrimRight(baseURL, "/")+"/health", nil)
	if err != nil {
		return errors.New("health probe request could not be created")
	}
	response, err := client.Do(request)
	if err != nil {
		return errors.New("health endpoint is unavailable")
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return errors.New("health endpoint is not live")
	}
	return nil
}

func run() error {
	key := os.Getenv("BMKG_API_KEY")
	if key == "" {
		return errors.New("BMKG_API_KEY is required")
	}
	port, err := intEnv("BMKG_MOCK_HTTP_PORT", 8081)
	if err != nil || port < 1 || port > 65535 {
		return errors.New("BMKG_MOCK_HTTP_PORT must be between 1 and 65535")
	}
	delayMS, err := intEnv("BMKG_DELAY_MS", 100)
	if err != nil || delayMS < 50 || delayMS > 150 {
		return errors.New("BMKG_DELAY_MS must be between 50 and 150")
	}
	interval, err := durationEnv("MOCK_EVENT_INTERVAL", 10*time.Second)
	if err != nil || interval <= 0 {
		return errors.New("MOCK_EVENT_INTERVAL must be a positive duration")
	}
	seedCount, err := intEnv("MOCK_SEED_COUNT", 20)
	if err != nil || seedCount < 20 || seedCount > mock.MaxSeedCount {
		return fmt.Errorf("MOCK_SEED_COUNT must be between 20 and %d", mock.MaxSeedCount)
	}
	runID, err := mock.RunID()
	if err != nil {
		return fmt.Errorf("generate run id: %w", err)
	}
	catalog, err := mock.NewBMKGCatalogWithCount(time.Now().UTC(), runID, seedCount)
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		if err := catalog.Run(ctx, interval); err != nil && !errors.Is(err, context.Canceled) {
			slog.Error("fixture generator failed", "service", "bmkg-mock", "error", err)
		}
	}()

	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	server := &http.Server{Addr: fmt.Sprintf(":%d", port), Handler: mock.WithCorrelationID(mock.NewBMKGHandler(mock.BMKGConfig{APIKey: key, Delay: time.Duration(delayMS) * time.Millisecond}, catalog, nil)), ReadHeaderTimeout: 2 * time.Second, IdleTimeout: 30 * time.Second, WriteTimeout: 5 * time.Second}
	return serve(ctx, server, "bmkg-mock", seedCount)
}

func intEnv(key string, fallback int) (int, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(value)
	return parsed, err
}
func durationEnv(key string, fallback time.Duration) (time.Duration, error) {
	value := os.Getenv(key)
	if value == "" {
		return fallback, nil
	}
	return time.ParseDuration(value)
}
func serve(ctx context.Context, server *http.Server, name string, seedCount int) error {
	errorsCh := make(chan error, 1)
	go func() {
		slog.Info("service listening", "service", name, "address", server.Addr, "seed_records", seedCount)
		errorsCh <- server.ListenAndServe()
	}()
	select {
	case err := <-errorsCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("shutdown %s: %w", name, err)
		}
		err := <-errorsCh
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}
