package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
)

type action struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Impact string `json:"impact"`
}

var actionList = []action{
	{"source-read", "Read source", "Checks one source with the selected credential; prints HTTP status and response."},
	{"client-login", "Log in client", "Saves a private session for subsequent reads. Does not run any API query."},
	{"client-read", "Read client API", "Uses the saved session without logging in or refreshing."},
	{"token-wait", "Wait for expiry", "Waits for the saved Field Team token to expire naturally."},
	{"token-refresh", "Refresh Field Team", "Rotates the saved refresh token, preserving the old access token for a separate rejection check."},
	{"token-old-read", "Read with old token", "Uses the access token saved before refresh."},
	{"inspect", "Inspect", "Reads the selected source or service and prints JSON."},
	{"service-start", "Start service", "Starts only the selected service."},
	{"service-stop", "Stop service", "Stops only the selected service."},
	{"service-rebuild", "Rebuild service", "Rebuilds and recreates only the selected service."},
	{"service-logs", "Follow service logs", "Follows actual container logs until stopped."},
	{"keys-setup", "Prepare JWT keys", "Creates a local RSA pair if absent and initializes this project's private/public key volumes. Existing key files are preserved."},
	{"stack-start", "Start stack", "Starts the nine default services in this Compose project."},
	{"stack-build", "Build and start", "Builds and recreates changed services in this Compose project."},
	{"source-schema", "Change PVMBG schema", "Changes live PVMBG reports. The Aggregator picks up changes on its next poll."},
	{"source-outage", "Change PVMBG outage", "Changes PVMBG availability; other services keep running."},
	{"source-delay", "Change PVMBG delay", "Changes live PVMBG response delay."},
	{"restore-defaults", "Restore source defaults", "Restores the schema, outage and delay values in the selected configuration."},
	{"access-check", "Check client permissions", "Reads the API as all three clients and verifies cross-source credential rejection."},
	{"token-trial", "Expiry and refresh trial", "P3. Temporarily stops Auth, waits for natural expiry, then checks refresh and replay rejection."},
	{"smoke-trial", "Mapping and access trial", "P1, P2, P3, P4. Changes schema/outage and restores configured source defaults."},
	{"events-trial", "Delivery and rebuild trial", "P4, P5. Stops/rebuilds dashboard, adds a fresh durable, then restores dashboard and the default third consumer."},
	{"load-run", "Record sustained workload", "Runs the workload and TCP collector concurrently; saves their measurements for separate reads."},
	{"latency-trial", "Compare source latency", "Runs simultaneous source-specific API requests with the current source delay."},
	{"load-trial", "Run 90-second load trial", "P2. Sets PVMBG delay to 3 seconds, runs 60 workers and TCP capture together, then restores configured delay."},
	{"dashboard-stop", "Stop dashboard", "Dashboard stops; producer and notifier continue. Use Start dashboard to recover."},
	{"dashboard-start", "Start dashboard", "Starts the existing dashboard container to process its backlog."},
	{"dashboard-rebuild", "Rebuild dashboard", "Builds and recreates only dashboard. Other services continue running."},
	{"third-start", "Start third consumer", "Starts the demo consumer with its own durable and volume."},
	{"third-stop", "Stop third consumer", "Stops the demo consumer; retains its messages and durable position."},
}

var services = []string{"bmkg-mock", "pvmbg-mock", "aggregator", "postgres", "nats", "client-api", "auth", "dashboard-consumer", "notifier-consumer", "third-consumer"}

func validService(service string) bool {
	for _, s := range services {
		if s == service {
			return true
		}
	}
	return false
}

func validateAction(id string, p map[string]string) (action, error) {
	var a action
	for _, candidate := range actionList {
		if candidate.ID == id {
			a = candidate
			break
		}
	}
	if a.ID == "" {
		return a, errors.New("unknown action")
	}
	// Formatting is explicit in the recipe, and optional for callers.
	if value, ok := p["format"]; ok {
		if value != "raw" && value != "pretty" {
			return a, errors.New("invalid format")
		}
		copy := make(map[string]string, len(p)-1)
		for key, value := range p {
			if key != "format" {
				copy[key] = value
			}
		}
		p = copy
	}
	allowed := map[string][]string{}
	switch id {
	case "source-read":
		allowed["source"] = []string{"BMKG", "PVMBG"}
		allowed["credential"] = []string{"BMKG", "PVMBG"}
	case "client-login":
		allowed["identity"] = []string{"media", "field-team", "bnpb-internal"}
	case "client-read":
		allowed["identity"] = []string{"media", "field-team", "bnpb-internal"}
		allowed["source"] = []string{"BMKG", "PVMBG"}
		allowed["view"] = []string{"summary", "full", "attributes"}
	case "service-start", "service-stop", "service-rebuild", "service-logs":
		allowed["service"] = services
	case "inspect":
		return a, validateInspection(p)
	case "source-schema":
		allowed["version"] = []string{"1", "2"}
	case "source-outage":
		allowed["enabled"] = []string{"true", "false"}
	case "source-delay":
		allowed["preset"] = []string{"slow", "default"}
	}
	if len(p) != len(allowed) {
		return a, errors.New("invalid action parameters")
	}
	for key, values := range allowed {
		valid := false
		for _, v := range values {
			if p[key] == v {
				valid = true
			}
		}
		if !valid {
			return a, fmt.Errorf("invalid %s", key)
		}
	}
	return a, nil
}

func (c *console) api(ctx context.Context, method, url string, payload any, headers map[string]string) (int, []byte, error) {
	if err := c.guardHTTP(ctx, url); err != nil {
		return 0, nil, err
	}
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return 0, nil, err
		}
		body = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("X-Correlation-ID", "demo-console")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	response, err := c.client.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer response.Body.Close()
	data, err := io.ReadAll(io.LimitReader(response.Body, 2<<20))

	return response.StatusCode, data, err
}

func (c *console) login(ctx context.Context, identity string) (string, error) {
	key := map[string]string{"media": "MEDIA_CLIENT_SECRET", "field-team": "FIELD_TEAM_CLIENT_SECRET", "bnpb-internal": "BNPB_INTERNAL_CLIENT_SECRET"}[identity]
	status, data, err := c.api(ctx, "POST", c.cfg.endpoint("AUTH_HTTP_PORT", "8084")+"/v1/auth/login", map[string]string{"client_id": identity, "client_secret": c.cfg.values[key]}, nil)
	if err != nil {
		return "", err
	}
	if status != 200 {
		return "", fmt.Errorf("Auth login: HTTP %d", status)
	}
	var result struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return "", err
	}
	if result.Access == "" {
		return "", errors.New("Auth returned no access token")
	}
	c.runner.addSecret(result.Access)
	c.runner.addSecret(result.Refresh)
	return result.Access, nil
}

func prettyJSON(raw []byte) string {
	var buf bytes.Buffer
	if json.Indent(&buf, raw, "", "  ") == nil {
		return buf.String()
	}
	return string(raw)
}

var refPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
