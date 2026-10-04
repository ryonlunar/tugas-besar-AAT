package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

type action struct {
	ID     string `json:"id"`
	Title  string `json:"title"`
	Impact string `json:"impact"`
}

var actionList = []action{
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
	{"load-trial", "Run 90-second load trial", "P2. Sets PVMBG delay to 3 seconds, runs 60 workers and TCP capture together, then restores configured delay."},
	{"dashboard-stop", "Stop dashboard", "Dashboard stops; producer and notifier continue. Use Start dashboard to recover."},
	{"dashboard-start", "Start dashboard", "Starts the existing dashboard container to process its backlog."},
	{"dashboard-rebuild", "Rebuild dashboard", "Builds and recreates only dashboard. Other services continue running."},
	{"third-start", "Start third consumer", "Starts the demo consumer with its own durable and volume."},
	{"third-stop", "Stop third consumer", "Stops the demo consumer; retains its messages and durable position."},
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
	allowed := map[string][]string{}
	switch id {
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

func (c *console) admin(ctx context.Context, path string, payload any) error {
	status, _, err := c.api(ctx, "POST", c.cfg.endpoint("PVMBG_MOCK_HTTP_PORT", "8082")+"/admin/"+path, payload, map[string]string{"Authorization": "Bearer " + c.cfg.values["PVMBG_TOKEN"]})
	if err != nil {
		return err
	}
	if status != 200 {
		return fmt.Errorf("PVMBG %s: HTTP %d", path, status)
	}
	return nil
}

func (c *console) restoreSource(ctx context.Context) error {
	version, err := strconv.Atoi(c.cfg.value("PVMBG_SCHEMA_VERSION", "1"))
	if err != nil {
		return err
	}
	min, err := strconv.Atoi(c.cfg.value("PVMBG_DELAY_MIN_MS", "500"))
	if err != nil {
		return err
	}
	max, err := strconv.Atoi(c.cfg.value("PVMBG_DELAY_MAX_MS", "3000"))
	if err != nil {
		return err
	}
	outage, err := strconv.ParseBool(c.cfg.value("PVMBG_OUTAGE_ENABLED", "false"))
	if err != nil {
		return err
	}
	return errors.Join(c.admin(ctx, "outage", map[string]bool{"enabled": outage}), c.admin(ctx, "schema-version", map[string]int{"version": version}), c.admin(ctx, "delay", map[string]int{"min_ms": min, "max_ms": max}))
}

func (c *console) execute(t *task, id string, p map[string]string) (result any, err error) {
	switch id {
	case "keys-setup":
		dir := filepath.Join(c.cfg.repo, ".secrets")
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, err
		}
		private, public := filepath.Join(dir, "jwt-private.pem"), filepath.Join(dir, "jwt-public.pem")
		_, privateErr := os.Stat(private)
		_, publicErr := os.Stat(public)
		if os.IsNotExist(privateErr) && os.IsNotExist(publicErr) {
			if err := t.exec(t.ctx, "openssl", "genpkey", "-algorithm", "RSA", "-pkeyopt", "rsa_keygen_bits:2048", "-out", private); err != nil {
				return nil, err
			}
			if err := os.Chmod(private, 0600); err != nil {
				return nil, err
			}
			if err := t.exec(t.ctx, "openssl", "pkey", "-in", private, "-pubout", "-out", public); err != nil {
				return nil, err
			}
		}
		return nil, t.exec(t.ctx, "python3", "scripts/setup-jwt-keys.py")
	case "stack-start":
		return nil, t.compose(t.ctx, "up", "-d", "--wait", "--wait-timeout", "180")
	case "stack-build":
		return nil, t.compose(t.ctx, "up", "--build", "-d", "--wait", "--wait-timeout", "180")
	case "source-schema":
		v, _ := strconv.Atoi(p["version"])
		t.line("POST /admin/schema-version  version=" + p["version"])
		return map[string]string{"version": p["version"]}, c.admin(t.ctx, "schema-version", map[string]int{"version": v})
	case "source-outage":
		enabled := p["enabled"] == "true"
		t.line("POST /admin/outage  enabled=" + p["enabled"])
		return map[string]bool{"outage": enabled}, c.admin(t.ctx, "outage", map[string]bool{"enabled": enabled})
	case "source-delay":
		min, max := 3000, 3000
		if p["preset"] == "default" {
			var err error
			min, err = strconv.Atoi(c.cfg.value("PVMBG_DELAY_MIN_MS", "500"))
			if err != nil {
				return nil, err
			}
			max, err = strconv.Atoi(c.cfg.value("PVMBG_DELAY_MAX_MS", "3000"))
			if err != nil {
				return nil, err
			}
		}
		t.line(fmt.Sprintf("POST /admin/delay  min_ms=%d max_ms=%d", min, max))
		return map[string]int{"min_ms": min, "max_ms": max}, c.admin(t.ctx, "delay", map[string]int{"min_ms": min, "max_ms": max})
	case "restore-defaults":
		t.line("Restoring configured source defaults")
		return nil, c.restoreSource(t.ctx)
	case "dashboard-stop":
		return nil, t.compose(t.ctx, "stop", "dashboard-consumer")
	case "dashboard-start":
		return nil, t.compose(t.ctx, "start", "dashboard-consumer")
	case "third-start":
		return nil, t.compose(t.ctx, "--profile", "demo", "up", "-d", "--no-deps", "--wait", "--wait-timeout", "120", "third-consumer")
	case "third-stop":
		return nil, t.compose(t.ctx, "--profile", "demo", "stop", "third-consumer")
	case "dashboard-rebuild":
		return c.rebuildTrial(t)
	case "access-check":
		return c.accessCheck(t)
	case "load-trial":
		return c.loadTrial(t)
	default:
		scenario := map[string]string{"smoke-trial": "smoke", "token-trial": "tokens", "events-trial": "events"}[id]
		if scenario == "" {
			return nil, errors.New("unimplemented action")
		}
		if scenario == "smoke" {
			defer func() {
				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				t.line("Cleanup: restoring configured source defaults")
				cleanup := c.restoreSource(ctx)
				if cleanup != nil {
					t.line("Cleanup failed: " + cleanup.Error())
				} else {
					t.line("Cleanup complete: configured source defaults restored")
				}
				err = errors.Join(err, cleanup)
			}()
		}
		for _, endpoint := range []string{c.cfg.endpoint("AUTH_HTTP_PORT", "8084"), c.cfg.endpoint("API_HTTP_PORT", "8080"), c.cfg.endpoint("PVMBG_MOCK_HTTP_PORT", "8082"), c.cfg.endpoint("BMKG_MOCK_HTTP_PORT", "8081")} {
			if err := c.guardHTTP(t.ctx, endpoint+"/health"); err != nil {
				return nil, err
			}
		}
		resultPath := filepath.Join(t.dir, "trial.json")
		err = t.exec(t.ctx, "python3", "scripts/verify-integration.py", "--env-file", c.cfg.envFile, "--scenario", scenario, "--output", resultPath)
		data, readErr := os.ReadFile(resultPath)
		if readErr != nil {
			return nil, errors.Join(err, readErr)
		}
		var result any
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		if readErr = decoder.Decode(&result); readErr != nil {
			return nil, errors.Join(err, readErr)
		}
		// Retain only the redacted result.json managed by the runner.
		_ = os.Remove(resultPath)
		return result, err
	}
}

func (c *console) accessCheck(t *task) (any, error) {
	results := []map[string]any{}
	var failures []error
	for _, identity := range []string{"media", "field-team", "bnpb-internal"} {
		token, err := c.login(t.ctx, identity)
		if err != nil {
			return results, err
		}
		view := "full"
		if identity == "media" {
			view = "summary"
		}
		status, data, err := c.api(t.ctx, "GET", c.cfg.endpoint("API_HTTP_PORT", "8080")+"/v1/hazards?source=BMKG&view="+view+"&limit=1", nil, map[string]string{"Authorization": "Bearer " + token})
		if err != nil {
			return results, err
		}
		if status != 200 {
			failures = append(failures, fmt.Errorf("%s HTTP %d", identity, status))
		}
		var response struct {
			Data []map[string]json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(data, &response); err != nil || len(response.Data) == 0 {
			return results, fmt.Errorf("%s returned no readable records", identity)
		}
		if identity == "media" {
			for _, record := range response.Data {
				if len(record) != 7 {
					failures = append(failures, errors.New("Media response does not contain exactly seven summary fields"))
				}
				for _, field := range []string{"hazard_id", "source", "hazard_type", "severity", "area_name", "occurred_at", "ingested_at"} {
					if _, ok := record[field]; !ok {
						failures = append(failures, fmt.Errorf("Media summary is missing %s", field))
					}
				}
			}
		} else if _, ok := response.Data[0]["attributes"]; !ok {
			failures = append(failures, fmt.Errorf("%s full view is missing attributes", identity))
		}
		parts := strings.Split(token, ".")
		var claims any
		if len(parts) == 3 {
			raw, _ := base64.RawURLEncoding.DecodeString(parts[1])
			_ = json.Unmarshal(raw, &claims)
		}
		results = append(results, map[string]any{"identity": identity, "status": status, "view": view, "field_count": len(response.Data[0]), "response": prettyJSON(data), "claims": claims})
		t.line(fmt.Sprintf("%s: HTTP %d (%s)", identity, status, view))
		if identity == "media" {
			for _, query := range []string{"view=full", "fields=attributes"} {
				rejected, _, err := c.api(t.ctx, "GET", c.cfg.endpoint("API_HTTP_PORT", "8080")+"/v1/hazards?"+query, nil, map[string]string{"Authorization": "Bearer " + token})
				if err != nil {
					return results, err
				}
				results = append(results, map[string]any{"check": "Media raw request", "query": query, "status": rejected, "expected": "401 or 403"})
				t.line(fmt.Sprintf("Media %s: HTTP %d", query, rejected))
				if rejected != 401 && rejected != 403 {
					failures = append(failures, errors.New("Media raw request was not rejected"))
				}
			}
		}
	}
	for _, probe := range []struct {
		label, url string
		headers    map[string]string
	}{
		{"PVMBG credential at BMKG", c.cfg.endpoint("BMKG_MOCK_HTTP_PORT", "8081") + "/seismic-events", map[string]string{"X-BMKG-Key": c.cfg.values["PVMBG_TOKEN"]}},
		{"BMKG credential at PVMBG", c.cfg.endpoint("PVMBG_MOCK_HTTP_PORT", "8082") + "/volcanic-reports", map[string]string{"Authorization": "Bearer " + c.cfg.values["BMKG_API_KEY"]}},
	} {
		status, _, err := c.api(t.ctx, "GET", probe.url, nil, probe.headers)
		if err != nil {
			return results, err
		}
		results = append(results, map[string]any{"check": probe.label, "status": status, "expected": "401 or 403"})
		t.line(fmt.Sprintf("%s: HTTP %d", probe.label, status))
		if status != 401 && status != 403 {
			failures = append(failures, errors.New("cross-source credential was not rejected"))
		}
	}
	return results, errors.Join(failures...)
}

func (c *console) rebuildTrial(t *task) (result any, err error) {
	before, err := c.containers(t.ctx)
	if err != nil {
		return nil, err
	}
	defer func() {
		if t.ctx.Err() != nil {
			ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
			defer cancel()
			t.line("Cleanup: restoring dashboard")
			err = errors.Join(err, t.compose(ctx, "up", "-d", "--no-deps", "--wait", "dashboard-consumer"))
		}
	}()
	if err = t.compose(t.ctx, "up", "-d", "--build", "--no-deps", "--force-recreate", "--wait", "dashboard-consumer"); err != nil {
		return nil, err
	}
	after, err := c.containers(t.ctx)
	if err != nil {
		return nil, err
	}
	changed := []string{}
	for _, old := range before {
		for _, now := range after {
			if old.Service == now.Service && (old.ID != now.ID || old.Started != now.Started) {
				changed = append(changed, old.Service)
			}
		}
	}
	if len(changed) != 1 || changed[0] != "dashboard-consumer" {
		return map[string]any{"before": before, "after": after, "changed": changed}, errors.New("unexpected container changes during rebuild")
	}
	return map[string]any{"before": before, "after": after, "changed": changed}, nil
}

func (c *console) loadTrial(t *task) (result any, err error) {
	for _, endpoint := range []string{c.cfg.endpoint("API_HTTP_PORT", "8080"), c.cfg.endpoint("AUTH_HTTP_PORT", "8084")} {
		if err := c.guardHTTP(t.ctx, endpoint+"/health"); err != nil {
			return nil, err
		}
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		min, _ := strconv.Atoi(c.cfg.value("PVMBG_DELAY_MIN_MS", "500"))
		max, _ := strconv.Atoi(c.cfg.value("PVMBG_DELAY_MAX_MS", "3000"))
		t.line("Cleanup: restoring configured PVMBG delay")
		cleanup := c.admin(ctx, "delay", map[string]int{"min_ms": min, "max_ms": max})
		if cleanup != nil {
			t.line("Cleanup failed: " + cleanup.Error())
		}
		err = errors.Join(err, cleanup)
	}()
	if err = c.admin(t.ctx, "delay", map[string]int{"min_ms": 3000, "max_ms": 3000}); err != nil {
		return nil, err
	}
	t.line("PVMBG delay: 3000 ms. Waiting for a polling cycle.")
	wait, err := time.ParseDuration(c.cfg.value("SOURCE_POLL_INTERVAL", "5s"))
	if err != nil {
		return nil, err
	}
	select {
	case <-t.ctx.Done():
		return nil, t.ctx.Err()
	case <-time.After(wait + 3*time.Second):
	}
	started := time.Now()
	status, _, err := c.api(t.ctx, "GET", c.cfg.endpoint("PVMBG_MOCK_HTTP_PORT", "8082")+"/volcanic-reports?since=2050-01-01T00:00:00Z", nil, map[string]string{"Authorization": "Bearer " + c.cfg.values["PVMBG_TOKEN"]})
	latency := time.Since(started).Milliseconds()
	if err != nil || status != 200 {
		return nil, fmt.Errorf("measure PVMBG delay: HTTP %d: %v", status, err)
	}
	t.line(fmt.Sprintf("Observed PVMBG latency: %d ms. Starting load and TCP capture.", latency))
	var wg sync.WaitGroup
	var loadErr, tcpErr error
	wg.Add(2)
	go func() {
		defer wg.Done()
		tcpErr = t.exec(t.ctx, "python3", "scripts/load/capture-tcp-connections.py", "--api-url", c.cfg.endpoint("API_HTTP_PORT", "8080"), "--seconds", "115", "--output", filepath.Join(t.dir, "tcp.json"))
	}()
	go func() {
		defer wg.Done()
		loadErr = t.exec(t.ctx, "python3", "scripts/load/hazards-monotonic.py", "--output", filepath.Join(t.dir, "load.json"))
	}()
	wg.Wait()
	result = map[string]any{"source_latency_ms": latency}
	values := result.(map[string]any)
	for _, name := range []string{"tcp", "load"} {
		data, readErr := os.ReadFile(filepath.Join(t.dir, name+".json"))
		if readErr == nil {
			var v any
			decoder := json.NewDecoder(bytes.NewReader(data))
			decoder.UseNumber()
			if readErr = decoder.Decode(&v); readErr == nil {
				values[name] = v
			}
		}
		err = errors.Join(err, readErr)
		_ = os.Remove(filepath.Join(t.dir, name+".json"))
	}
	if tcp, ok := values["tcp"]; ok {
		data, _ := json.Marshal(tcp)
		var measured struct {
			Interval float64     `json:"sampling_interval_seconds"`
			Samples  []tcpSample `json:"samples"`
		}
		if decodeErr := json.Unmarshal(data, &measured); decodeErr != nil {
			err = errors.Join(err, decodeErr)
		} else {
			span := sustainedTCPSpan(measured.Samples, measured.Interval*2)
			values["sustained_tcp_seconds"] = span
			t.line(fmt.Sprintf("Observed ≥50 TCP connections for %.1f consecutive sampled seconds", span))
			if span < 60 {
				err = errors.Join(err, errors.New("TCP criterion failed: fewer than 50 connections sustained for 60 seconds"))
			}
		}
	}
	return result, errors.Join(err, loadErr, tcpErr)
}

type tcpSample struct {
	Elapsed float64 `json:"elapsed_seconds"`
	Count   int     `json:"established_connections_to_api"`
}

func sustainedTCPSpan(samples []tcpSample, maxGap float64) float64 {
	longest, start, previous := 0.0, -1.0, -1.0
	for _, sample := range samples {
		if sample.Count < 50 || sample.Elapsed < previous || previous >= 0 && sample.Elapsed-previous > maxGap {
			start = -1
		}
		if sample.Count >= 50 {
			if start < 0 {
				start = sample.Elapsed
			}
			if span := sample.Elapsed - start; span > longest {
				longest = span
			}
		}
		previous = sample.Elapsed
	}
	return longest
}

func prettyJSON(raw []byte) string {
	var buf bytes.Buffer
	if json.Indent(&buf, raw, "", "  ") == nil {
		return buf.String()
	}
	return string(raw)
}

var refPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
