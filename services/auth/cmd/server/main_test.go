package main

import (
	"testing"
	"time"
)

func TestLoadConfigAllowsConfiguredAccessTokenTTL(t *testing.T) {
	t.Setenv("JWT_PRIVATE_KEY_FILE", "test-private-key.pem")
	t.Setenv("ACCESS_TOKEN_TTL", "45s")
	config, err := loadConfig()
	if err != nil {
		t.Fatal(err)
	}
	if config.accessTTL != 45*time.Second {
		t.Fatalf("access TTL = %s", config.accessTTL)
	}
}
