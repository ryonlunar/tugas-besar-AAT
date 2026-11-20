package main

import (
	"github.com/ryonlunar/tugas-besar-AAT/contracts"
	"testing"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/mapper"
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

func TestLoadConfigPVMBGAdapters(t *testing.T) {
	setValidConfigEnvironment(t)
	t.Setenv("PVMBG_SCHEMA_ADAPTERS", "")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.pvmbgAdapters != (mapper.VolcanicAdaptations{}) {
		t.Fatalf("empty PVMBG_SCHEMA_ADAPTERS must mean the strict policy: %+v", cfg.pvmbgAdapters)
	}

	t.Setenv("PVMBG_SCHEMA_ADAPTERS", "level_alias,eruption_count_string")
	cfg, err = loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	want := mapper.VolcanicAdaptations{LevelAlias: true, EruptionCountString: true}
	if cfg.pvmbgAdapters != want {
		t.Fatalf("adapters=%+v, want %+v", cfg.pvmbgAdapters, want)
	}

	t.Setenv("PVMBG_SCHEMA_ADAPTERS", "level_alias,not_a_real_adapter")
	if _, err := loadConfig(); err == nil {
		t.Fatal("unknown PVMBG_SCHEMA_ADAPTERS entry must fail startup, not be ignored")
	}
}

func TestLoadConfigSatellite(t *testing.T) {
	setValidConfigEnvironment(t)

	// Both empty: satellite stays disabled, startup still succeeds.
	t.Setenv("SATELLITE_BASE_URL", "")
	t.Setenv("SATELLITE_TOKEN", "")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("both empty should be a valid (disabled) configuration: %v", err)
	}
	if cfg.satelliteEnabled {
		t.Fatal("satellite must be disabled when both SATELLITE_BASE_URL and SATELLITE_TOKEN are empty")
	}

	// Only one set: startup error, never silently half-enabled.
	t.Setenv("SATELLITE_BASE_URL", "https://satellite.example")
	t.Setenv("SATELLITE_TOKEN", "")
	if _, err := loadConfig(); err == nil {
		t.Fatal("SATELLITE_BASE_URL without SATELLITE_TOKEN must be rejected")
	}
	t.Setenv("SATELLITE_BASE_URL", "")
	t.Setenv("SATELLITE_TOKEN", "synthetic-satellite-token")
	if _, err := loadConfig(); err == nil {
		t.Fatal("SATELLITE_TOKEN without SATELLITE_BASE_URL must be rejected")
	}

	// A non-https URL for a real external host is rejected.
	t.Setenv("SATELLITE_BASE_URL", "http://satellite.example")
	t.Setenv("SATELLITE_TOKEN", "synthetic-satellite-token")
	if _, err := loadConfig(); err == nil {
		t.Fatal("a non-https URL for an external host must be rejected")
	}

	// http://127.0.0.1 and a bare service name are allowed (dev/test).
	for _, url := range []string{"http://127.0.0.1:9443", "http://satellite-mock:9443"} {
		t.Setenv("SATELLITE_BASE_URL", url)
		cfg, err := loadConfig()
		if err != nil {
			t.Fatalf("%s should be accepted for dev/test: %v", url, err)
		}
		if !cfg.satelliteEnabled {
			t.Fatalf("%s should enable the satellite source", url)
		}
	}

	// A valid https configuration picks up every documented default.
	t.Setenv("SATELLITE_BASE_URL", "https://satellite.naufarrel.dev")
	for _, name := range []string{"SATELLITE_ALLOWED_LATENESS", "SATELLITE_BATCH_MAX_EVENTS", "SATELLITE_BATCH_MAX_BYTES", "SATELLITE_BATCH_MAX_WAIT", "SATELLITE_IDLE_TIMEOUT", "SATELLITE_RECONNECT_MIN", "SATELLITE_RECONNECT_MAX", "SATELLITE_REQUEST_TIMEOUT", "SATELLITE_SESSION_POLL_INTERVAL"} {
		t.Setenv(name, "")
	}
	cfg, err = loadConfig()
	if err != nil {
		t.Fatalf("valid https configuration with default durations: %v", err)
	}
	if !cfg.satelliteEnabled || cfg.satelliteLateness != 12*time.Hour || cfg.satelliteBatchMaxEvents != 200 ||
		cfg.satelliteBatchMaxBytes != 262144 || cfg.satelliteBatchMaxWait != 250*time.Millisecond ||
		cfg.satelliteIdleTimeout != 30*time.Second || cfg.satelliteReconnectMin != 500*time.Millisecond ||
		cfg.satelliteReconnectMax != 30*time.Second || cfg.satelliteRequestTimeout != 5*time.Second ||
		cfg.satelliteSessionPollInterval != 5*time.Second {
		t.Fatalf("satellite defaults not as documented: %+v", cfg)
	}

	// An out-of-range duration fails startup.
	t.Setenv("SATELLITE_RECONNECT_MAX", "100ms")
	t.Setenv("SATELLITE_RECONNECT_MIN", "500ms")
	if _, err := loadConfig(); err == nil {
		t.Fatal("SATELLITE_RECONNECT_MAX below SATELLITE_RECONNECT_MIN must be rejected")
	}
}

func TestConfiguredSourcesAddsSatelliteOnlyWhenEnabled(t *testing.T) {
	disabled := configuredSources(config{}, false)
	if len(disabled) != 2 {
		t.Fatalf("disabled sources = %v, want exactly BMKG+PVMBG", disabled)
	}
	enabled := configuredSources(config{satelliteEnabled: true}, false)
	if len(enabled) != 3 {
		t.Fatalf("enabled sources = %v, want BMKG+PVMBG+SATELIT", enabled)
	}
	both := configuredSources(config{satelliteEnabled: true}, true)
	if len(both) != 4 || both[3] != contracts.SourceCoastal {
		t.Fatalf("enabled sources = %v, want both satellite and coastal", both)
	}
}

func TestLoadConfigBMKGHistoricalCatalogFile(t *testing.T) {
	setValidConfigEnvironment(t)

	// Unset (M1/dev/CI default): catalog import stays off, startup
	// succeeds exactly as before this feature existed.
	t.Setenv("BMKG_HISTORICAL_CATALOG_FILE", "")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("empty BMKG_HISTORICAL_CATALOG_FILE should be valid (disabled): %v", err)
	}
	if cfg.bmkgHistoricalCatalogFile != "" {
		t.Fatalf("bmkgHistoricalCatalogFile = %q, want empty", cfg.bmkgHistoricalCatalogFile)
	}

	// Set: the path is passed through untouched (existence is checked
	// only when the import actually runs, not at config load time).
	t.Setenv("BMKG_HISTORICAL_CATALOG_FILE", " /data/synthetic-catalog.csv ")
	cfg, err = loadConfig()
	if err != nil {
		t.Fatalf("a configured catalog path should be valid: %v", err)
	}
	if cfg.bmkgHistoricalCatalogFile != "/data/synthetic-catalog.csv" {
		t.Fatalf("bmkgHistoricalCatalogFile = %q, want trimmed path", cfg.bmkgHistoricalCatalogFile)
	}
}
