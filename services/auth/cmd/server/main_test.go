package main

import (
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/services/auth/internal/auth"
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

func TestLoadConfigRejectsUnboundedBodyRead(t *testing.T) {
	t.Setenv("JWT_PRIVATE_KEY_FILE", "test-private-key.pem")
	for _, value := range []string{"0s", "-1s", "invalid"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("AUTH_READ_TIMEOUT", value)
			if _, err := loadConfig(); err == nil {
				t.Fatal("invalid body-read timeout accepted")
			}
		})
	}
}

func TestSlowBodyReleasesAuthAdmission(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := newHTTPServer(config{readTimeout: 200 * time.Millisecond, writeTimeout: time.Second}, auth.NewHandler(nil, 8192, 1))
	defer server.Close()
	go server.Serve(listener)
	slow, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer slow.Close()
	if _, err := fmt.Fprintf(slow, "POST /v1/auth/login HTTP/1.1\r\nHost: localhost\r\nContent-Length: 100\r\nContent-Type: application/json\r\n\r\n{"); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: time.Second}
	defer client.CloseIdleConnections()
	blocked := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		// An unknown field exercises body decoding without invoking the nil service.
		resp, err := client.Post("http://"+listener.Addr().String()+"/v1/auth/login", "application/json", strings.NewReader(`{"unknown":true}`))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode == http.StatusTooManyRequests {
			blocked = true
		} else if blocked && resp.StatusCode == http.StatusBadRequest {
			return // Slot released while the slow client still has its connection open.
		} else if resp.StatusCode != http.StatusBadRequest {
			t.Fatalf("unexpected response: %d", resp.StatusCode)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("admission did not recover after incomplete body; observed blocking=%v", blocked)
}
