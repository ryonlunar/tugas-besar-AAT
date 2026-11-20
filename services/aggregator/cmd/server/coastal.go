package main

import (
	"errors"
	"net/http"
	"os"

	"github.com/ryonlunar/tugas-besar-AAT/pkg/telemetry"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/coastal"
)

func coastalAdapter(cfg config) (*coastal.Adapter, error) {
	base := os.Getenv("PANTAI_BASE_URL")
	if base == "" {
		return nil, nil
	}
	if cfg.shardConfigFile == "" {
		return nil, errors.New("Sensor Pantai requires the replicated profile")
	}
	interval, err := envDuration("PANTAI_POLL_INTERVAL", "500ms")
	if err != nil {
		return nil, err
	}
	maxAge, err := envDuration("PANTAI_MAX_OBSERVATION_AGE", "2m")
	if err != nil {
		return nil, err
	}
	return coastal.New(base, os.Getenv("PANTAI_AUTH_TOKEN"), telemetry.HTTPTransport(http.DefaultTransport), interval, maxAge)
}
