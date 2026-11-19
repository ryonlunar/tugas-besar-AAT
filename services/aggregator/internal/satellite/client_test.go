package satellite

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSessionDecode(t *testing.T) {
	t.Run("sends the bearer token and decodes every documented field", func(t *testing.T) {
		var gotAuth, gotPath string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotAuth = r.Header.Get("Authorization")
			gotPath = r.URL.Path
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"profile":"practice","speed":60,"group":"dikejardeadline","latest_offset":0,"virtual_now":"2026-11-21T00:00:00Z"}`))
		}))
		defer server.Close()

		client, err := NewClient(server.URL, "s3cr3t", time.Second)
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}
		session, err := client.Session(context.Background())
		if err != nil {
			t.Fatalf("Session() error = %v", err)
		}
		if gotAuth != "Bearer s3cr3t" {
			t.Errorf("Authorization header = %q, want %q", gotAuth, "Bearer s3cr3t")
		}
		if gotPath != "/v1/session" {
			t.Errorf("path = %q, want /v1/session", gotPath)
		}
		if session.Profile != "practice" || session.Speed != 60 || session.Group != "dikejardeadline" || session.LatestOffset != 0 {
			t.Fatalf("Session() = %+v, want profile=practice speed=60 group=dikejardeadline latest_offset=0", session)
		}
		wantTime := time.Date(2026, 11, 21, 0, 0, 0, 0, time.UTC)
		if !session.VirtualNow.Equal(wantTime) {
			t.Fatalf("VirtualNow = %v, want %v", session.VirtualNow, wantTime)
		}
	})

	t.Run("missing token is rejected before any request is made", func(t *testing.T) {
		if _, err := NewClient("https://example.test", "", time.Second); err == nil {
			t.Fatal("NewClient() error = nil, want an error for an empty token")
		}
	})
}

func TestFeedErrorClassification(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   ErrorClass
	}{
		{"400 is permanent", http.StatusBadRequest, ClassPermanent},
		{"401 is auth", http.StatusUnauthorized, ClassAuth},
		{"429 is busy", http.StatusTooManyRequests, ClassBusy},
		{"503 is transient", http.StatusServiceUnavailable, ClassTransient},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.status)
				_, _ = w.Write([]byte(`{"error":{"code":"some_code","message":"some message"}}`))
			}))
			defer server.Close()

			client, err := NewClient(server.URL, "token", time.Second)
			if err != nil {
				t.Fatalf("NewClient() error = %v", err)
			}
			_, err = client.Session(context.Background())
			if err == nil {
				t.Fatal("Session() error = nil, want a *FeedError")
			}
			var feedErr *FeedError
			if !errors.As(err, &feedErr) {
				t.Fatalf("error type = %T, want *FeedError", err)
			}
			if feedErr.Status != c.status {
				t.Errorf("Status = %d, want %d", feedErr.Status, c.status)
			}
			if feedErr.Code != "some_code" {
				t.Errorf("Code = %q, want some_code", feedErr.Code)
			}
			if got := ClassifyFeedError(err); got != c.want {
				t.Errorf("ClassifyFeedError() = %v, want %v", got, c.want)
			}
		})
	}

	t.Run("a transport-level failure classifies as transient", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
		server.Close() // closed: any request now fails at the transport level, no HTTP status at all

		client, err := NewClient(server.URL, "token", 200*time.Millisecond)
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}
		_, err = client.Session(context.Background())
		if err == nil {
			t.Fatal("Session() error = nil, want a transport error")
		}
		var feedErr *FeedError
		if errors.As(err, &feedErr) {
			t.Fatalf("got a *FeedError %+v, want a raw transport error", feedErr)
		}
		if got := ClassifyFeedError(err); got != ClassTransient {
			t.Errorf("ClassifyFeedError() = %v, want %v", got, ClassTransient)
		}
	})
}

func TestOpenStreamUsesCursor(t *testing.T) {
	t.Run("resumes via the ?from= query parameter, never Last-Event-ID", func(t *testing.T) {
		var gotFrom, gotAccept, gotCacheControl, gotAuth string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotFrom = r.URL.Query().Get("from")
			gotAccept = r.Header.Get("Accept")
			gotCacheControl = r.Header.Get("Cache-Control")
			gotAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("id: 42\ndata: {}\n\n"))
		}))
		defer server.Close()

		client, err := NewClient(server.URL, "s3cr3t", time.Second)
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}
		body, err := client.OpenStream(context.Background(), "42")
		if err != nil {
			t.Fatalf("OpenStream() error = %v", err)
		}
		defer body.Close()
		if gotFrom != "42" {
			t.Errorf("from = %q, want 42", gotFrom)
		}
		if gotAccept != "text/event-stream" {
			t.Errorf("Accept = %q, want text/event-stream", gotAccept)
		}
		if gotCacheControl != "no-cache" {
			t.Errorf("Cache-Control = %q, want no-cache", gotCacheControl)
		}
		if gotAuth != "Bearer s3cr3t" {
			t.Errorf("Authorization = %q, want Bearer s3cr3t", gotAuth)
		}
	})

	t.Run("an empty cursor resumes from offset 0", func(t *testing.T) {
		var gotFrom string
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			gotFrom = r.URL.Query().Get("from")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte("\n"))
		}))
		defer server.Close()

		client, err := NewClient(server.URL, "token", time.Second)
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}
		body, err := client.OpenStream(context.Background(), "")
		if err != nil {
			t.Fatalf("OpenStream() error = %v", err)
		}
		defer body.Close()
		if gotFrom != "0" {
			t.Errorf("from = %q, want 0", gotFrom)
		}
	})

	t.Run("a non-200 response yields a *FeedError and no body leak", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = w.Write([]byte(`{"error":{"code":"unavailable","message":"try later"}}`))
		}))
		defer server.Close()

		client, err := NewClient(server.URL, "token", time.Second)
		if err != nil {
			t.Fatalf("NewClient() error = %v", err)
		}
		_, err = client.OpenStream(context.Background(), "0")
		var feedErr *FeedError
		if !errors.As(err, &feedErr) {
			t.Fatalf("error type = %T, want *FeedError", err)
		}
		if feedErr.Status != http.StatusServiceUnavailable || feedErr.Code != "unavailable" {
			t.Errorf("FeedError = %+v, want status=503 code=unavailable", feedErr)
		}
	})
}

var _ io.ReadCloser // keep io imported for readers of this file who extend it
