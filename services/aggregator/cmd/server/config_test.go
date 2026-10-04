package main

import (
	"testing"
	"time"
)

func setValidConfigEnvironment(t *testing.T) {
	t.Helper()
	values := map[string]string{
		"AGGREGATOR_HTTP_PORT":      "8083",
		"BMKG_BASE_URL":             "http://bmkg-mock:8081",
		"PVMBG_BASE_URL":            "http://pvmbg-mock:8082",
		"BMKG_API_KEY":              "synthetic-bmkg-key",
		"PVMBG_TOKEN":               "synthetic-pvmbg-token",
		"AGGREGATOR_API_KEY":        "synthetic-aggregator-key",
		"POSTGRES_HOST":             "localhost",
		"POSTGRES_PORT":             "5432",
		"POSTGRES_DB":               "aat_test",
		"POSTGRES_USER":             "aat_test",
		"POSTGRES_PASSWORD":         "synthetic-test-password",
		"POSTGRES_SSLMODE":          "disable",
		"DB_READ_MAX_CONNS":         "10",
		"DB_BMKG_WRITE_MAX_CONNS":   "2",
		"DB_PVMBG_WRITE_MAX_CONNS":  "2",
		"DB_RELAY_MAX_CONNS":        "2",
		"DB_READ_TIMEOUT":           "200ms",
		"DB_WRITE_TIMEOUT":          "2s",
		"SOURCE_POLL_INTERVAL":      "5s",
		"BMKG_REQUEST_TIMEOUT":      "1s",
		"PVMBG_REQUEST_TIMEOUT":     "4s",
		"SOURCE_STALE_AFTER":        "15s",
		"SOURCE_RESPONSE_MAX_BYTES": "1048576",
		"AGGREGATOR_MAX_INFLIGHT":   "128",
		"HTTP_READ_HEADER_TIMEOUT":  "2s",
		"HTTP_IDLE_TIMEOUT":         "30s",
		"HTTP_WRITE_TIMEOUT":        "5s",
		"NATS_URL":                  "nats://localhost:4222",
		"NATS_TOKEN":                "synthetic-nats-token",
		"NATS_STREAM_NAME":          "HAZARD_EVENTS",
		"NATS_MAX_AGE":              "24h",
		"NATS_DUPLICATE_WINDOW":     "2m",
		"NATS_MAX_BYTES":            "268435456",
		"NATS_MAX_MSGS":             "100000",
		"NATS_MAX_MSG_SIZE":         "65536",
		"OUTBOX_POLL_INTERVAL":      "500ms",
		"OUTBOX_RETRY_MIN":          "1s",
		"OUTBOX_RETRY_MAX":          "30s",
		"OUTBOX_BATCH_SIZE":         "20",
		"OUTBOX_MAX_PENDING":        "10000",
	}
	for name, value := range values {
		t.Setenv(name, value)
	}
}

func TestLoadConfigReadsNATSPublishTimeout(t *testing.T) {
	setValidConfigEnvironment(t)
	t.Setenv("NATS_PUBLISH_TIMEOUT", "1500ms")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.natsPublishTimeout != 1500*time.Millisecond {
		t.Fatalf("NATS publish timeout=%s, want 1.5s", cfg.natsPublishTimeout)
	}
}

func TestLoadConfigRejectsNonPositiveNATSPublishTimeout(t *testing.T) {
	setValidConfigEnvironment(t)
	t.Setenv("NATS_PUBLISH_TIMEOUT", "0s")
	if _, err := loadConfig(); err == nil {
		t.Fatal("non-positive NATS publish timeout must be rejected")
	}
}

func TestLoadConfigRejectsDuplicateWindowBelowBrokerMinimum(t *testing.T) {
	setValidConfigEnvironment(t)
	for _, value := range []string{"0s", "-1s", "1ns", "99ms"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("NATS_DUPLICATE_WINDOW", value)
			if _, err := loadConfig(); err == nil || err.Error() != "NATS_DUPLICATE_WINDOW must be at least 100ms" {
				t.Fatalf("invalid duplicate window accepted: %v", err)
			}
		})
	}
	t.Setenv("NATS_DUPLICATE_WINDOW", "")
	cfg, err := loadConfig()
	if err != nil || cfg.natsDuplicateWindow != 2*time.Minute {
		t.Fatalf("default duplicate window=%s error=%v", cfg.natsDuplicateWindow, err)
	}
}
