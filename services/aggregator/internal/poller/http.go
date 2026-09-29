package poller

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

type correlationIDContextKey struct{}

func withCorrelationID(ctx context.Context, correlationID string) context.Context {
	return context.WithValue(ctx, correlationIDContextKey{}, correlationID)
}

type BMKGClient struct {
	baseURL string
	key     string
	client  *http.Client
	maxBody int64
}

func NewBMKGClient(baseURL, key string, client *http.Client, maxBody int64) *BMKGClient {
	return &BMKGClient{baseURL: strings.TrimRight(baseURL, "/"), key: key, client: clientOrDefault(client), maxBody: maxBody}
}

func (c *BMKGClient) Fetch(ctx context.Context) ([][]byte, [][]byte, error) {
	if strings.TrimSpace(c.key) == "" || c.maxBody <= 0 {
		return nil, nil, errors.New("BMKG client is not configured")
	}
	if err := validateBaseURL(c.baseURL); err != nil {
		return nil, nil, fmt.Errorf("BMKG client is not configured: %w", err)
	}
	return c.fetchBMKGPair(ctx)
}

func (c *BMKGClient) fetchBMKGPair(ctx context.Context) ([][]byte, [][]byte, error) {
	type result struct {
		path  string
		items [][]byte
		err   error
	}
	results := make(chan result, 2)
	for _, path := range []string{"/seismic-events", "/tsunami-warnings"} {
		path := path
		go func() {
			items, err := c.getArray(ctx, path, func(req *http.Request) { req.Header.Set("X-BMKG-Key", c.key) })
			results <- result{path: path, items: items, err: err}
		}()
	}
	var events, warnings [][]byte
	for range 2 {
		res := <-results
		if res.err != nil {
			return nil, nil, res.err
		}
		if res.path == "/seismic-events" {
			events = res.items
		} else {
			warnings = res.items
		}
	}
	return events, warnings, nil
}

type PVMBGClient struct {
	baseURL string
	token   string
	client  *http.Client
	maxBody int64
}

func NewPVMBGClient(baseURL, token string, client *http.Client, maxBody int64) *PVMBGClient {
	return &PVMBGClient{baseURL: strings.TrimRight(baseURL, "/"), token: token, client: clientOrDefault(client), maxBody: maxBody}
}

func (c *PVMBGClient) Fetch(ctx context.Context) ([][]byte, error) {
	if strings.TrimSpace(c.token) == "" || c.maxBody <= 0 {
		return nil, errors.New("PVMBG client is not configured")
	}
	if err := validateBaseURL(c.baseURL); err != nil {
		return nil, fmt.Errorf("PVMBG client is not configured: %w", err)
	}
	return c.getArray(ctx, "/volcanic-reports", func(req *http.Request) { req.Header.Set("Authorization", "Bearer "+c.token) })
}

func (c *BMKGClient) getArray(ctx context.Context, path string, auth func(*http.Request)) ([][]byte, error) {
	return fetchArray(ctx, c.client, c.baseURL+path, c.maxBody, auth, path)
}
func (c *PVMBGClient) getArray(ctx context.Context, path string, auth func(*http.Request)) ([][]byte, error) {
	return fetchArray(ctx, c.client, c.baseURL+path, c.maxBody, auth, path)
}

func fetchArray(ctx context.Context, client *http.Client, target string, maxBody int64, auth func(*http.Request), label string) ([][]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, fmt.Errorf("build source request %s: %w", label, err)
	}
	req.Header.Set("Accept", "application/json")
	if correlationID, ok := ctx.Value(correlationIDContextKey{}).(string); ok && sourceIDPattern.MatchString(correlationID) {
		req.Header.Set("X-Correlation-ID", correlationID)
	}
	auth(req)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("source request %s failed", label)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("source request %s returned HTTP %d", label, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if err != nil {
		return nil, fmt.Errorf("read source response %s failed", label)
	}
	if int64(len(body)) > maxBody {
		return nil, fmt.Errorf("source response %s exceeds configured limit", label)
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	var raw []json.RawMessage
	if err := dec.Decode(&raw); err != nil || raw == nil {
		return nil, fmt.Errorf("source response %s is not a JSON array", label)
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, fmt.Errorf("source response %s has trailing JSON data", label)
	}
	items := make([][]byte, len(raw))
	for i, item := range raw {
		items[i] = append([]byte(nil), item...)
	}
	return items, nil
}

func clientOrDefault(client *http.Client) *http.Client {
	var safeClient http.Client
	if client != nil {
		safeClient = *client
	}
	// Source credentials are bound to the configured origin. Do not let any
	// redirect cause a credential-bearing request to be sent elsewhere.
	safeClient.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &safeClient
}

func validateBaseURL(value string) error {
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return errors.New("base URL must be an absolute HTTP(S) URL without userinfo, query, or fragment")
	}
	return nil
}
