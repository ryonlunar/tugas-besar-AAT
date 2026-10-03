package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/pkg/jetstreamconsumer"
	"github.com/ryonlunar/tugas-besar-AAT/services/notifier-consumer/internal/store"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)).With("service", "notifier-consumer"))
	port := getenv("NOTIFIER_HTTP_PORT", "8086")
	if len(os.Args) == 2 && os.Args[1] == "inspect" {
		if err := inspect(port, "/internal/v1/notifications"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if len(os.Args) == 2 && os.Args[1] == "healthcheck" {
		client := &http.Client{Timeout: time.Second}
		response, err := client.Get("http://127.0.0.1:" + port + "/ready")
		if err != nil {
			os.Exit(1)
		}
		response.Body.Close()
		if response.StatusCode != http.StatusOK {
			os.Exit(1)
		}
		return
	}
	if err := run(port); err != nil {
		slog.Error("notifier consumer stopped", "reason", err.Error())
		os.Exit(1)
	}
}

func inspect(port, path string) error {
	req, err := http.NewRequest(http.MethodGet, "http://127.0.0.1:"+port+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("X-Demo-Key", os.Getenv("CONSUMER_DEMO_KEY"))
	response, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("inspection returned HTTP %d", response.StatusCode)
	}
	_, err = io.Copy(os.Stdout, response.Body)
	return err
}

func run(port string) error {
	portNumber, err := strconv.Atoi(port)
	if err != nil || portNumber < 1 || portNumber > 65535 {
		return fmt.Errorf("invalid NOTIFIER_HTTP_PORT")
	}
	demoKey := os.Getenv("CONSUMER_DEMO_KEY")
	if demoKey == "" {
		return errors.New("CONSUMER_DEMO_KEY is required")
	}
	config, err := jetstreamconsumer.ConfigFromEnv("NATS_NOTIFIER_DURABLE", "notifier-v1")
	if err != nil {
		return err
	}
	state, err := store.Open(getenv("NOTIFIER_STATE_PATH", "/data/notifier.db"))
	if err != nil {
		return fmt.Errorf("open notifier state: %w", err)
	}
	defer state.Close()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	runner := &jetstreamconsumer.Runner{Config: config, Sink: state}
	server := &http.Server{Addr: ":" + port, Handler: jetstreamconsumer.Handler(runner, state, demoKey, "/internal/v1/notifications"), ReadHeaderTimeout: 2 * time.Second, IdleTimeout: 30 * time.Second, WriteTimeout: 3 * time.Second}
	return jetstreamconsumer.Serve(ctx, runner.Run, server)
}

func getenv(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
