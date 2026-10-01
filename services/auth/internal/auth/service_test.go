package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"database/sql"
	"errors"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func TestRefreshRotationRejectsReplayAndRevokesFamily(t *testing.T) {
	db, err := sql.Open("sqlite", "file:auth_rotation?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	svc, err := New(db, key, testClients(), "aat-auth", "aat-client-api", time.Minute, 24*time.Hour, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	initial, err := svc.Login(context.Background(), "field-team", "field-secret")
	if err != nil {
		t.Fatal(err)
	}
	rotated, err := svc.Refresh(context.Background(), initial.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.RefreshToken == initial.RefreshToken || rotated.RefreshExpiresIn != initial.RefreshExpiresIn {
		t.Fatal("refresh must rotate without extending family expiry")
	}
	if _, err := svc.Refresh(context.Background(), initial.RefreshToken); !errors.Is(err, ErrInvalidRefresh) {
		t.Fatalf("replay error = %v", err)
	}
	if _, err := svc.Refresh(context.Background(), rotated.RefreshToken); !errors.Is(err, ErrInvalidRefresh) {
		t.Fatalf("rotated token after family revocation = %v", err)
	}
}

func TestLoginUsesServerSideScope(t *testing.T) {
	db, err := sql.Open("sqlite", "file:auth_scope?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	if err := Migrate(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	key, _ := rsa.GenerateKey(rand.Reader, 2048)
	svc, err := New(db, key, testClients(), "aat-auth", "aat-client-api", time.Minute, time.Hour, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	tokens, err := svc.Login(context.Background(), "media", "media-secret")
	if err != nil {
		t.Fatal(err)
	}
	if tokens.Scope != "hazards:summary" {
		t.Fatalf("media scope = %q", tokens.Scope)
	}
	if _, err := svc.Login(context.Background(), "media", "wrong"); !errors.Is(err, ErrInvalidRefresh) {
		t.Fatalf("wrong credential error = %v", err)
	}
}

func testClients() map[string]Client {
	return map[string]Client{"media": {Secret: "media-secret", Scope: "hazards:summary"}, "field-team": {Secret: "field-secret", Scope: "hazards:summary hazards:raw"}, "bnpb-internal": {Secret: "bnpb-secret", Scope: "hazards:summary hazards:raw"}}
}
