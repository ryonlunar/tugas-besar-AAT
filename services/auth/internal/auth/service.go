package auth

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/subtle"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var ErrInvalidRefresh = errors.New("invalid refresh token")

type Client struct{ Secret, Scope string }

type Service struct {
	db                    *sql.DB
	privateKey            *rsa.PrivateKey
	clients               map[string]Client
	issuer, audience      string
	accessTTL, refreshTTL time.Duration
	now                   func() time.Time
}

type Tokens struct {
	AccessToken      string `json:"access_token"`
	TokenType        string `json:"token_type"`
	ExpiresIn        int64  `json:"expires_in"`
	RefreshToken     string `json:"refresh_token"`
	RefreshExpiresIn int64  `json:"refresh_expires_in"`
	Scope            string `json:"scope"`
}

func New(db *sql.DB, privateKey *rsa.PrivateKey, clients map[string]Client, issuer, audience string, accessTTL, refreshTTL time.Duration, now func() time.Time) (*Service, error) {
	if db == nil || privateKey == nil || len(clients) != 3 || issuer == "" || audience == "" || accessTTL <= 0 || refreshTTL <= 0 {
		return nil, errors.New("invalid auth service configuration")
	}
	if now == nil {
		now = time.Now
	}
	for _, id := range []string{"media", "field-team", "bnpb-internal"} {
		if clients[id].Secret == "" || clients[id].Scope == "" {
			return nil, errors.New("client credentials are incomplete")
		}
	}
	return &Service{db: db, privateKey: privateKey, clients: clients, issuer: issuer, audience: audience, accessTTL: accessTTL, refreshTTL: refreshTTL, now: now}, nil
}

func Migrate(ctx context.Context, db *sql.DB) error {
	_, err := db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS refresh_tokens (
 token_hash BLOB PRIMARY KEY, family_id TEXT NOT NULL, client_id TEXT NOT NULL,
 expires_at INTEGER NOT NULL, consumed_at INTEGER, revoked_at INTEGER
); CREATE INDEX IF NOT EXISTS refresh_tokens_family_idx ON refresh_tokens(family_id);`)
	return err
}

func (s *Service) Login(ctx context.Context, clientID, clientSecret string) (Tokens, error) {
	client, ok := s.clients[clientID]
	comparisonSecret := client.Secret
	if !ok {
		comparisonSecret = s.clients["media"].Secret
	}
	if !ok || subtle.ConstantTimeCompare([]byte(comparisonSecret), []byte(clientSecret)) != 1 {
		return Tokens{}, ErrInvalidRefresh
	}
	return s.issueNewFamily(ctx, clientID, client.Scope)
}

func (s *Service) Refresh(ctx context.Context, raw string) (Tokens, error) {
	if raw == "" {
		return Tokens{}, ErrInvalidRefresh
	}
	hash := hashToken(raw)
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Tokens{}, err
	}
	defer tx.Rollback()
	var familyID, clientID string
	var expiresAt int64
	var consumedAt, revokedAt sql.NullInt64
	err = tx.QueryRowContext(ctx, `SELECT family_id, client_id, expires_at, consumed_at, revoked_at FROM refresh_tokens WHERE token_hash = ?`, hash).Scan(&familyID, &clientID, &expiresAt, &consumedAt, &revokedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Tokens{}, ErrInvalidRefresh
	}
	if err != nil {
		return Tokens{}, err
	}
	now := s.now().UTC()
	if consumedAt.Valid {
		if _, err := tx.ExecContext(ctx, `UPDATE refresh_tokens SET revoked_at = ? WHERE family_id = ? AND revoked_at IS NULL`, now.Unix(), familyID); err != nil {
			return Tokens{}, err
		}
		if err := tx.Commit(); err != nil {
			return Tokens{}, err
		}
		return Tokens{}, ErrInvalidRefresh
	}
	if revokedAt.Valid || now.Unix() >= expiresAt {
		return Tokens{}, ErrInvalidRefresh
	}
	client, ok := s.clients[clientID]
	if !ok {
		return Tokens{}, fmt.Errorf("stored client is not configured")
	}
	newRaw, newHash, err := newRefreshToken()
	if err != nil {
		return Tokens{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE refresh_tokens SET consumed_at = ? WHERE token_hash = ? AND consumed_at IS NULL`, now.Unix(), hash); err != nil {
		return Tokens{}, err
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO refresh_tokens(token_hash, family_id, client_id, expires_at) VALUES (?, ?, ?, ?)`, newHash, familyID, clientID, expiresAt); err != nil {
		return Tokens{}, err
	}
	if err := tx.Commit(); err != nil {
		return Tokens{}, err
	}
	return s.mint(clientID, client.Scope, newRaw, time.Unix(expiresAt, 0).UTC())
}

func (s *Service) issueNewFamily(ctx context.Context, clientID, scope string) (Tokens, error) {
	raw, hash, err := newRefreshToken()
	if err != nil {
		return Tokens{}, err
	}
	family, err := randomID()
	if err != nil {
		return Tokens{}, err
	}
	expires := s.now().UTC().Add(s.refreshTTL)
	if _, err = s.db.ExecContext(ctx, `INSERT INTO refresh_tokens(token_hash, family_id, client_id, expires_at) VALUES (?, ?, ?, ?)`, hash, family, clientID, expires.Unix()); err != nil {
		return Tokens{}, err
	}
	return s.mint(clientID, scope, raw, expires)
}

func (s *Service) mint(clientID, scope, refresh string, refreshExpiry time.Time) (Tokens, error) {
	now := s.now().UTC()
	expires := now.Add(s.accessTTL)
	claims := jwt.MapClaims{"iss": s.issuer, "aud": s.audience, "sub": clientID, "scope": scope, "iat": now.Unix(), "exp": expires.Unix()}
	access, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(s.privateKey)
	if err != nil {
		return Tokens{}, err
	}
	remaining := int64(refreshExpiry.Sub(now).Seconds())
	if remaining < 0 {
		remaining = 0
	}
	return Tokens{AccessToken: access, TokenType: "Bearer", ExpiresIn: int64(s.accessTTL.Seconds()), RefreshToken: refresh, RefreshExpiresIn: remaining, Scope: scope}, nil
}

func newRefreshToken() (string, []byte, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", nil, err
	}
	raw := base64.RawURLEncoding.EncodeToString(b)
	return raw, hashToken(raw), nil
}
func hashToken(token string) []byte { value := sha256.Sum256([]byte(token)); return value[:] }
func randomID() (string, error) {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}
