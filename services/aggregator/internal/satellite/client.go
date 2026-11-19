package satellite

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Session is the feed's /v1/session response: the practice profile, its
// playback speed multiplier, the owning group, the latest offset the feed
// has produced so far, and the feed's current virtual (event-time) clock.
type Session struct {
	Profile      string
	Speed        float64
	Group        string
	LatestOffset int64
	VirtualNow   time.Time
}

// Client talks to the Feed Satelit HTTP API: a short session lookup and the
// long-lived event stream, both authenticated with a bearer token.
type Client struct {
	baseURL string
	token   string
	http    *http.Client
}

// NewClient builds a Client against baseURL using token as the bearer
// credential. requestTimeout bounds how long a request may wait for
// response headers (Transport.ResponseHeaderTimeout) -- it deliberately
// does not become http.Client.Timeout, which would also cut short a long
// SSE stream's body read.
func NewClient(baseURL, token string, requestTimeout time.Duration) (*Client, error) {
	if err := validateBaseURL(baseURL); err != nil {
		return nil, fmt.Errorf("satellite client base URL: %w", err)
	}
	if strings.TrimSpace(token) == "" {
		return nil, errors.New("satellite client requires a non-empty token")
	}
	if requestTimeout <= 0 {
		return nil, errors.New("satellite client requires a positive request timeout")
	}
	transport := &http.Transport{ResponseHeaderTimeout: requestTimeout}
	client := &http.Client{
		Transport: transport,
		// Credentials are bound to the configured origin; never let a
		// redirect carry the bearer token to another host.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	return &Client{baseURL: strings.TrimRight(baseURL, "/"), token: token, http: client}, nil
}

func validateBaseURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("base URL must be an absolute HTTP(S) URL without userinfo, query, or fragment")
	}
	return nil
}

// Session fetches GET /v1/session. A non-2xx response yields a *FeedError;
// any other failure (transport, decode) is returned as-is.
func (c *Client) Session(ctx context.Context) (Session, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/v1/session", nil)
	if err != nil {
		return Session{}, fmt.Errorf("build session request: %w", err)
	}
	c.authorize(req)
	req.Header.Set("Accept", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return Session{}, fmt.Errorf("satellite session request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Session{}, readFeedError(resp)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return Session{}, fmt.Errorf("read session response: %w", err)
	}
	var raw struct {
		Profile      string  `json:"profile"`
		Speed        float64 `json:"speed"`
		Group        string  `json:"group"`
		LatestOffset int64   `json:"latest_offset"`
		VirtualNow   string  `json:"virtual_now"`
	}
	if err := json.Unmarshal(body, &raw); err != nil {
		return Session{}, fmt.Errorf("decode session response: %w", err)
	}
	virtualNow, err := time.Parse(time.RFC3339, raw.VirtualNow)
	if err != nil {
		return Session{}, fmt.Errorf("decode session virtual_now: %w", err)
	}
	return Session{
		Profile:      raw.Profile,
		Speed:        raw.Speed,
		Group:        raw.Group,
		LatestOffset: raw.LatestOffset,
		VirtualNow:   virtualNow,
	}, nil
}

// OpenStream opens GET /v1/stream?from=<cursor>. Resume is always by query
// parameter -- never by a Last-Event-ID header -- because the feed defines
// ?from=N as inclusive of offset N while Last-Event-ID is exclusive; mixing
// the two would silently reintroduce an off-by-one (see
// docs/m2-satellite-feed-notes.md section 2). An empty cursor requests from
// offset 0. The caller owns the returned body and must Close it; a non-200
// response's body is drained into a *FeedError and closed here instead.
func (c *Client) OpenStream(ctx context.Context, cursor string) (io.ReadCloser, error) {
	if cursor == "" {
		cursor = "0"
	}
	target := c.baseURL + "/v1/stream?from=" + url.QueryEscape(cursor)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("build stream request: %w", err)
	}
	c.authorize(req)
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("Cache-Control", "no-cache")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("satellite stream request failed: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		defer resp.Body.Close()
		return nil, readFeedError(resp)
	}
	return resp.Body, nil
}

func (c *Client) authorize(req *http.Request) {
	req.Header.Set("Authorization", "Bearer "+c.token)
}

// FeedError is the feed's documented error shape:
// {"error":{"code":"...","message":"..."}}, carrying the HTTP status that
// produced it. A body that doesn't match the shape still yields a FeedError
// with an empty Code/Message -- the status alone is enough to classify it.
type FeedError struct {
	Status  int
	Code    string
	Message string
}

func (e *FeedError) Error() string {
	return fmt.Sprintf("satellite feed error: HTTP %d %s: %s", e.Status, e.Code, e.Message)
}

func readFeedError(resp *http.Response) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	var parsed struct {
		Error struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		} `json:"error"`
	}
	_ = json.Unmarshal(body, &parsed) // tolerant: an unexpected body shape just leaves Code/Message empty
	return &FeedError{Status: resp.StatusCode, Code: parsed.Error.Code, Message: parsed.Error.Message}
}

// ErrorClass is how the adapter decides to back off after a failed request.
type ErrorClass string

const (
	// ClassPermanent (HTTP 400, or any other 4xx but 401/429): retrying the
	// same request will not help; it indicates a bug in what we sent.
	ClassPermanent ErrorClass = "permanent"
	// ClassAuth (HTTP 401): the token is invalid; back off for a long,
	// fixed interval rather than retrying quickly.
	ClassAuth ErrorClass = "auth"
	// ClassBusy (HTTP 429): the feed is rate-limiting us, plausibly because
	// a prior owner's connection has not yet been torn down server-side
	// after a takeover; back off with jitter, floor >=5s.
	ClassBusy ErrorClass = "busy"
	// ClassTransient (HTTP 5xx, or any transport-level failure with no
	// HTTP status at all): retry with exponential backoff.
	ClassTransient ErrorClass = "transient"
)

// Class classifies a FeedError by its HTTP status.
func (e *FeedError) Class() ErrorClass {
	switch e.Status {
	case http.StatusBadRequest:
		return ClassPermanent
	case http.StatusUnauthorized:
		return ClassAuth
	case http.StatusTooManyRequests:
		return ClassBusy
	}
	if e.Status >= 500 {
		return ClassTransient
	}
	return ClassPermanent
}

// ClassifyFeedError classifies any error returned by Session or OpenStream.
// A *FeedError classifies by its status; anything else (connection refused,
// timeout, TLS failure, ...) has no HTTP status at all and is treated as
// transient, since it says nothing about whether the same request would
// fail again.
func ClassifyFeedError(err error) ErrorClass {
	var feedErr *FeedError
	if errors.As(err, &feedErr) {
		return feedErr.Class()
	}
	return ClassTransient
}
