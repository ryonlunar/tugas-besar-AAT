package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func testConfig(t *testing.T) config {
	t.Helper()
	dir := t.TempDir()
	return config{repo: dir, envFile: filepath.Join(dir, ".env"), evidence: filepath.Join(dir, "runs"), project: "console-test", values: map[string]string{"COMPOSE_PROJECT_NAME": "console-test", "PVMBG_TOKEN": "test-secret-which-must-be-hidden"}}
}

func TestActionValidationRejectsCommandsAndUnexpectedParameters(t *testing.T) {
	for _, test := range []struct {
		id string
		p  map[string]string
	}{
		{"shell", map[string]string{"command": "whoami"}},
		{"dashboard-stop", map[string]string{"service": "postgres"}},
		{"source-outage", map[string]string{"enabled": "true; echo unsafe"}},
		{"source-schema", map[string]string{"version": "3"}},
		{"source-delay", nil},
	} {
		if _, err := validateAction(test.id, test.p); err == nil {
			t.Errorf("accepted unexpected action: %+v", test)
		}
	}
	if _, err := validateAction("source-outage", map[string]string{"enabled": "true"}); err != nil {
		t.Fatal(err)
	}
}

func TestProfilePinsComposeProjectFileAndEnvironment(t *testing.T) {
	t.Setenv("COMPOSE_PROJECT_NAME", "another-project")
	t.Setenv("COMPOSE_PROFILES", "unrelated")
	t.Setenv("COMPOSE_FILE", "/wrong/file.yml")
	cfg := testConfig(t)
	env := map[string]string{}
	for _, line := range cfg.processEnv() {
		k, v, _ := strings.Cut(line, "=")
		env[k] = v
	}
	if env["COMPOSE_PROJECT_NAME"] != cfg.project || env["COMPOSE_FILE"] != filepath.Join(cfg.repo, "docker-compose.yml") || env["COMPOSE_ENV_FILES"] != cfg.envFile {
		t.Fatalf("target is not pinned: %v", env["COMPOSE_PROJECT_NAME"])
	}
	if _, exists := env["COMPOSE_PROFILES"]; exists {
		t.Fatal("inherited profiles can alter the selected project")
	}
	args := strings.Join(cfg.compose("stop", "dashboard-consumer"), " ")
	if !strings.Contains(args, "--project-name console-test") || !strings.Contains(args, "--env-file .env") || !strings.Contains(args, "-f docker-compose.yml") {
		t.Fatal(args)
	}
}

func TestHTTPRejectsOtherHostsOriginsAndMissingToken(t *testing.T) {
	cfg := testConfig(t)
	c := &console{cfg: cfg, runner: newRunner(cfg), port: "8099", token: "console-csrf-token"}
	private := filepath.Join(cfg.repo, "docs/evidence/private")
	if err := os.MkdirAll(private, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(private, "local.json"), []byte(`{"secret":"private evidence"}`), 0600); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct{ host, origin, token, site string }{
		{"evil.example:8099", "", "console-csrf-token", ""},
		{"127.0.0.1:8099", "https://evil.example", "console-csrf-token", ""},
		{"127.0.0.1:8099", "http://127.0.0.1:8099", "", ""},
		{"127.0.0.1:8099", "", "console-csrf-token", "cross-site"},
	} {
		req := httptest.NewRequest("POST", "http://"+test.host+"/api/runs", strings.NewReader(`{"action":"dashboard-stop"}`))
		req.Host = test.host
		req.Header.Set("Origin", test.origin)
		req.Header.Set("X-Console-Token", test.token)
		req.Header.Set("Sec-Fetch-Site", test.site)
		response := httptest.NewRecorder()
		c.handler().ServeHTTP(response, req)
		if response.Code != http.StatusForbidden {
			t.Errorf("request accepted: %+v status=%d", test, response.Code)
		}
	}
	for _, url := range []string{"/api/runs/not-a-run/output.log", "/api/historical?file=../.env", "/api/historical?file=private/local.json", "/api/historical?file=foo/../private/local.json"} {
		req := httptest.NewRequest("GET", "http://127.0.0.1:8099"+url, nil)
		response := httptest.NewRecorder()
		c.handler().ServeHTTP(response, req)
		if response.Code != 404 {
			t.Errorf("unsafe path %s status=%d", url, response.Code)
		}
	}
}

func TestOutputRedactsSecretsSplitAcrossWritesAndPreservesJSONNumbers(t *testing.T) {
	cfg := testConfig(t)
	r := newRunner(cfg)
	if err := os.MkdirAll(cfg.evidence, 0700); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(cfg.evidence, "log"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	task := &task{runner: r, log: f}
	writer := &outputWriter{task: task}
	secret := cfg.values["PVMBG_TOKEN"]
	writer.Write([]byte("header=" + secret[:12]))
	writer.Write([]byte(secret[12:] + "\n"))
	writer.flush()
	if strings.Contains(strings.Join(task.snapshot().Lines, "\n"), secret) || !strings.Contains(strings.Join(task.snapshot().Lines, "\n"), "[redacted]") {
		t.Fatal("secret leaked from a split write")
	}
	path := filepath.Join(cfg.evidence, "result.json")
	if err := r.writeJSON(path, map[string]any{"count": json.Number("9007199254740993"), "secret": secret}); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	if strings.Contains(string(data), secret) || !strings.Contains(string(data), "9007199254740993") {
		t.Fatal(string(data))
	}
}

func TestSavedRunDoesNotClaimCompletionAfterAbruptStop(t *testing.T) {
	v := runRecord{ID: "saved-run", Status: "running"}
	if got := savedRunState(v, ""); got.Status != "unfinished" || got.Error == "" {
		t.Fatal(got)
	}
	if got := savedRunState(v, v.ID); got.Status != "running" {
		t.Fatal(got)
	}
}

func TestProfileMasksSecretsEmbeddedInOtherValues(t *testing.T) {
	cfg := testConfig(t)
	cfg.values["EXAMPLE_URL"] = "https://example.test/?credential=" + cfg.values["PVMBG_TOKEN"]
	data, err := json.Marshal(newRunner(cfg).maskedProfile())
	if err != nil || strings.Contains(string(data), cfg.values["PVMBG_TOKEN"]) {
		t.Fatalf("unsafe profile: %s, %v", data, err)
	}
}

func TestTokenDurationsDoNotRedactHazardFieldNames(t *testing.T) {
	cfg := testConfig(t)
	cfg.values["REFRESH_TOKEN_TTL"] = "24h"
	cfg.values["ACCESS_TOKEN_TTL"] = "60s"
	r := newRunner(cfg)
	data, err := r.redactedJSON(map[string]any{"eruption_count_24h": 4, "token": "sensitive"})
	if err != nil || !strings.Contains(string(data), `"eruption_count_24h": 4`) || strings.Contains(string(data), "sensitive") {
		t.Fatalf("%s: %v", data, err)
	}
	if r.maskedProfile()["REFRESH_TOKEN_TTL"] != "24h" {
		t.Fatal("duration is hidden as a credential")
	}
}

func TestAPIInspectionCannotAddTrafficDuringLoadMeasurement(t *testing.T) {
	cfg := testConfig(t)
	c := &console{cfg: cfg, runner: newRunner(cfg), port: "8099"}
	c.runner.active = &task{record: runRecord{Action: "load-trial"}}
	for _, kind := range []string{"api", "mapping"} {
		req := httptest.NewRequest("GET", "http://127.0.0.1:8099/api/inspect/"+kind, nil)
		w := httptest.NewRecorder()
		c.handler().ServeHTTP(w, req)
		if w.Code != http.StatusConflict {
			t.Fatalf("kind=%s status=%d", kind, w.Code)
		}
	}
}

func TestActionRequestRejectsTrailingJSONBeforeExecution(t *testing.T) {
	cfg := testConfig(t)
	c := &console{cfg: cfg, runner: newRunner(cfg), port: "8099", token: "local-token"}
	req := httptest.NewRequest("POST", "http://127.0.0.1:8099/api/runs", strings.NewReader(`{"action":"stack-start"} {}`))
	req.Header.Set("X-Console-Token", c.token)
	w := httptest.NewRecorder()
	c.handler().ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest || c.runner.active != nil {
		t.Fatalf("status=%d, active=%v", w.Code, c.runner.active)
	}
}

func TestCommandPreviewDoesNotExecute(t *testing.T) {
	cfg := testConfig(t)
	c := &console{cfg: cfg, runner: newRunner(cfg), port: "8099", token: "local-token"}
	for _, test := range []struct {
		body   string
		status int
	}{
		{`{"action":"service-stop","parameters":{"service":"postgres"}}`, 200},
		{`{"action":"service-stop","parameters":{"service":"unrelated"}}`, 400},
		{`{"action":"stack-start"} {}`, 400},
		{`{"action":"stack-start","shell":"anything"}`, 400},
	} {
		req := httptest.NewRequest("POST", "http://127.0.0.1:8099/api/commands/preview", strings.NewReader(test.body))
		req.Header.Set("X-Console-Token", c.token)
		w := httptest.NewRecorder()
		c.handler().ServeHTTP(w, req)
		if w.Code != test.status || c.runner.active != nil {
			t.Fatalf("status=%d, active=%v", w.Code, c.runner.active)
		}
		if test.status == 200 {
			var response map[string]string
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			want := commandDisplay(composeCommand(cfg, "--profile", "demo", "stop", "postgres"))
			if response["command"] != want {
				t.Fatalf("preview differs from execution: %q", response["command"])
			}
		}
	}
	if _, err := os.Stat(cfg.evidence); !os.IsNotExist(err) {
		t.Fatalf("preview created run files: %v", err)
	}
	req := httptest.NewRequest("POST", "http://127.0.0.1:8099/api/commands/preview", strings.NewReader(`{"action":"stack-start"}`))
	w := httptest.NewRecorder()
	c.handler().ServeHTTP(w, req)
	if w.Code != 403 {
		t.Fatalf("preview accepted without console token: %d", w.Code)
	}
}

func TestCommandRequestRejectsEmptyOrInvalidShellText(t *testing.T) {
	cfg := testConfig(t)
	c := &console{cfg: cfg, runner: newRunner(cfg), port: "8099", token: "local-token"}
	for _, script := range []string{"", "bad\x00command", strings.Repeat("x", 32769)} {
		body, _ := json.Marshal(commandRequest{Action: "inspect", Parameters: map[string]string{"kind": "schema"}, Script: script})
		req := httptest.NewRequest("POST", "http://127.0.0.1:8099/api/runs", strings.NewReader(string(body)))
		req.Header.Set("X-Console-Token", c.token)
		w := httptest.NewRecorder()
		c.handler().ServeHTTP(w, req)
		if w.Code != 400 || c.runner.active != nil {
			t.Fatalf("status=%d, active=%v", w.Code, c.runner.active)
		}
	}
	if _, err := os.Stat(cfg.evidence); !os.IsNotExist(err) {
		t.Fatalf("invalid command created a run: %v", err)
	}
}

func TestDisplayedDockerCommandExecutesItsActualArguments(t *testing.T) {
	cfg := runnableTestConfig(t)
	bin := filepath.Join(cfg.repo, "bin")
	if err := os.Mkdir(bin, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte("#!/bin/sh\nprintf 'actual argv: <%s>\\n' \"$@\"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	c := &console{cfg: cfg, runner: newRunner(cfg)}
	plan, err := c.commandPlan("service-stop", map[string]string{"service": "postgres"})
	if err != nil {
		t.Fatal(err)
	}
	job, err := c.runner.start(action{ID: "service-stop", Title: "Stop postgres"}, nil, func(job *task) (any, error) { return c.executeCommand(job, plan) })
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-job.done:
	case <-time.After(5 * time.Second):
		job.cancel()
		t.Fatal("command did not finish")
	}
	if job.snapshot().Status != "passed" {
		t.Fatal(job.snapshot().Error)
	}
	data, err := os.ReadFile(filepath.Join(job.dir, "output.log"))
	if err != nil {
		t.Fatal(err)
	}
	log := string(data)
	if !strings.Contains(log, commandDisplay(plan.script)) || !strings.Contains(log, "actual argv: <console-test>") || !strings.Contains(log, "actual argv: <stop>\nactual argv: <postgres>") || strings.Contains(log, "--command") {
		t.Fatalf("command or output differs: %s", log)
	}
	if _, err := os.Stat(filepath.Join(job.dir, "result.json")); !os.IsNotExist(err) {
		t.Fatalf("invented a JSON result for a Docker process: %v", err)
	}
}

func TestRecipesHaveValidBashSyntax(t *testing.T) {
	cfg := testConfig(t)
	c := &console{cfg: cfg, runner: newRunner(cfg)}
	for _, a := range actionList {
		params := map[string]string{}
		switch a.ID {
		case "source-read":
			params["source"], params["credential"] = "PVMBG", "BMKG"
		case "client-login":
			params["identity"] = "field-team"
		case "client-read":
			params["identity"], params["source"], params["view"] = "media", "BMKG", "attributes"
		case "service-start", "service-stop", "service-rebuild", "service-logs":
			params["service"] = "postgres"
		case "source-schema":
			params["version"] = "2"
		case "source-outage":
			params["enabled"] = "true"
		case "source-delay":
			params["preset"] = "default"
		case "inspect":
			params["kind"] = "mapping"
			params["source"] = "PVMBG"
		}
		plan, err := c.commandPlan(a.ID, params)
		if err != nil {
			t.Fatal(a.ID, err)
		}
		output, err := c.runner.command(context.Background(), "bash", "-n", "-c", plan.script).CombinedOutput()
		if err != nil {
			t.Fatalf("%s: %v %s", a.ID, err, output)
		}
	}
}

func TestSourceDefaultsUseJSONNumbersAndBooleans(t *testing.T) {
	cfg := testConfig(t)
	cfg.values["PVMBG_OUTAGE_ENABLED"] = "1"
	cfg.values["PVMBG_DELAY_MIN_MS"] = "0500"
	cfg.values["PVMBG_DELAY_MAX_MS"] = "+3000"
	c := &console{cfg: cfg}
	plan, err := c.commandPlan("restore-defaults", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(plan.script, `'{"enabled":true}'`) || !strings.Contains(plan.script, `'{"max_ms":3000,"min_ms":500}'`) {
		t.Fatal(plan.script)
	}
}

func TestCancellationRunsSubprocessFinallyAndPreventsConcurrentActions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("subprocess interruption requires Linux/WSL")
	}
	cfg := testConfig(t)
	r := newRunner(cfg)
	ready := filepath.Join(cfg.repo, "ready")
	cleaned := filepath.Join(cfg.repo, "cleaned")
	script := `import pathlib,time
try:
 pathlib.Path('ready').write_text('ready')
 time.sleep(60)
finally:
 pathlib.Path('cleaned').write_text('cleaned')
`
	job, err := r.start(action{ID: "test", Title: "Cancellation test"}, nil, func(t *task) (any, error) { return nil, t.exec(t.ctx, "python3", "-c", script) })
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(ready); err != nil {
		job.cancel()
		t.Fatal("subprocess did not start")
	}
	if _, err := r.start(action{ID: "second"}, nil, func(*task) (any, error) { return nil, nil }); err == nil {
		t.Fatal("allowed concurrent actions")
	}
	r.cancelActive()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	r.wait(ctx)
	if _, err := os.Stat(cleaned); err != nil {
		t.Fatal("cancellation skipped subprocess cleanup")
	}
	if job.snapshot().Status != "cancelled" {
		t.Fatalf("status: %+v", job.snapshot())
	}
	data, err := os.ReadFile(filepath.Join(job.dir, "run.json"))
	if err != nil {
		t.Fatal(err)
	}
	var saved runRecord
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.Status != "cancelled" {
		t.Fatal(saved.Status)
	}
}

func TestTCPRecipeRejectsCountDipsAndSamplingGaps(t *testing.T) {
	for _, shape := range []string{"continuous", "dip", "gap"} {
		cfg := testConfig(t)
		if err := os.MkdirAll(cfg.evidence, 0700); err != nil {
			t.Fatal(err)
		}
		samples := []map[string]any{}
		for i := 0; i <= 60; i++ {
			count := 50
			if shape == "dip" && i == 30 {
				count = 49
			}
			samples = append(samples, map[string]any{"elapsed_seconds": i, "established_connections_to_api": count})
		}
		if shape == "gap" {
			samples = []map[string]any{{"elapsed_seconds": 0, "established_connections_to_api": 50}, {"elapsed_seconds": 65, "established_connections_to_api": 50}}
		}
		r := newRunner(cfg)
		if err := r.writeJSON(filepath.Join(cfg.evidence, "tcp.json"), map[string]any{"sampling_interval_seconds": 1, "samples": samples}); err != nil {
			t.Fatal(err)
		}
		if err := r.writeJSON(filepath.Join(cfg.evidence, "load.json"), map[string]any{"passed": true}); err != nil {
			t.Fatal(err)
		}
		cmd := r.command(context.Background(), "python3", "-c", tcpCheck)
		cmd.Env = append(cmd.Env, "AAT_DEMO_RUN_DIR="+cfg.evidence, "AAT_DEMO_OUTPUT="+filepath.Join(cfg.evidence, "combined.json"))
		output, err := cmd.CombinedOutput()
		if (err == nil) != (shape == "continuous") {
			t.Fatalf("%s: %v %s", shape, err, output)
		}
	}
}

func TestStreamResumesLiveOutputAndRedactsSecrets(t *testing.T) {
	cfg := testConfig(t)
	c := &console{cfg: cfg, runner: newRunner(cfg)}
	ready, release := make(chan struct{}), make(chan struct{})
	job, err := c.runner.start(action{ID: "test", Title: "Stream test"}, nil, func(t *task) (any, error) {
		t.line("before cursor")
		close(ready)
		select {
		case <-release:
		case <-t.ctx.Done():
		}
		return map[string]any{"number": json.Number("9007199254740993")}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer job.cancel()
	<-ready
	job.line("resumed output " + cfg.values["PVMBG_TOKEN"])
	server := httptest.NewServer(c.handler())
	defer func() { job.cancel(); <-job.done; server.Close() }()
	_, c.port, _ = net.SplitHostPort(server.Listener.Addr().String())
	req, _ := http.NewRequest("GET", server.URL+"/api/runs/"+job.record.ID+"/stream", nil)
	req.Header.Set("Last-Event-ID", "28")
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	close(release)
	data, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	if strings.Contains(text, "before cursor") || strings.Contains(text, cfg.values["PVMBG_TOKEN"]) || !strings.Contains(text, "resumed output [redacted]") || !strings.Contains(text, "event: complete") || !strings.Contains(text, "9007199254740993") {
		t.Fatal(text)
	}
}

func TestInspectionAndServiceCommandsRejectUnexpectedInputs(t *testing.T) {
	for _, params := range []map[string]string{
		{"kind": "shell"}, {"kind": "store", "sql": "DELETE FROM hazard_events"},
		{"kind": "config", "service": "outside-project"}, {"kind": "history", "source": "PVMBG", "ref": "../.env"},
	} {
		if _, err := validateAction("inspect", params); err == nil {
			t.Fatal(params)
		}
	}
	if _, err := validateAction("service-stop", map[string]string{"service": "postgres; docker system prune"}); err == nil {
		t.Fatal("accepted a shell string")
	}
}

func TestStreamKeepsFastOutputBeyondTheMemoryTail(t *testing.T) {
	cfg := testConfig(t)
	c := &console{cfg: cfg, runner: newRunner(cfg)}
	ready, release := make(chan struct{}), make(chan struct{})
	job, err := c.runner.start(action{ID: "test", Title: "Burst test"}, nil, func(t *task) (any, error) {
		for i := 0; i < 700; i++ {
			t.line(fmt.Sprintf("burst %03d", i))
		}
		close(ready)
		select {
		case <-release:
		case <-t.ctx.Done():
		}
		return nil, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	defer job.cancel()
	<-ready
	server := httptest.NewServer(c.handler())
	defer func() { job.cancel(); <-job.done; server.Close() }()
	_, c.port, _ = net.SplitHostPort(server.Listener.Addr().String())
	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(server.URL + "/api/runs/" + job.record.ID + "/stream")
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	close(release)
	data, err := io.ReadAll(response.Body)
	if err != nil || strings.Count(string(data), "burst ") != 700 {
		t.Fatalf("stream lost output: count=%d err=%v", strings.Count(string(data), "burst "), err)
	}
}

func runnableTestConfig(t *testing.T) config {
	cfg := testConfig(t)
	for _, key := range []string{"BMKG_API_KEY", "AGGREGATOR_API_KEY", "CONSUMER_DEMO_KEY", "POSTGRES_PASSWORD", "NATS_TOKEN", "MEDIA_CLIENT_SECRET", "FIELD_TEAM_CLIENT_SECRET", "BNPB_INTERNAL_CLIENT_SECRET"} {
		cfg.values[key] = "private-" + key
	}
	var environment strings.Builder
	for key, value := range cfg.values {
		fmt.Fprintf(&environment, "%s=%s\n", key, value)
	}
	if err := os.WriteFile(cfg.envFile, []byte(environment.String()), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cfg.repo, "docker-compose.yml"), []byte("services: {}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestEditedAndTypedCommandsExecuteTheirActualText(t *testing.T) {
	for _, actionID := range []string{"inspect", "custom"} {
		cfg := runnableTestConfig(t)
		c := &console{cfg: cfg, runner: newRunner(cfg), port: "8099", token: "local-token"}
		input := commandRequest{Action: actionID, Script: "printf 'edited number: %s\\n' 7"}
		if actionID == "inspect" {
			input.Parameters = map[string]string{"kind": "schema"}
		}
		body, _ := json.Marshal(input)
		req := httptest.NewRequest("POST", "http://127.0.0.1:8099/api/runs", strings.NewReader(string(body)))
		req.Header.Set("X-Console-Token", c.token)
		w := httptest.NewRecorder()
		c.handler().ServeHTTP(w, req)
		if w.Code != 202 {
			t.Fatal(w.Code, w.Body.String())
		}
		var record runRecord
		if err := json.Unmarshal(w.Body.Bytes(), &record); err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		c.runner.wait(ctx)
		cancel()
		data, err := os.ReadFile(filepath.Join(cfg.evidence, record.ID, "output.log"))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(data), "edited number: 7\n") || strings.Contains(string(data), "cat contracts") {
			t.Fatalf("edited text was not executed: %s", data)
		}
		data, _ = os.ReadFile(filepath.Join(cfg.evidence, record.ID, "run.json"))
		if err := json.Unmarshal(data, &record); err != nil {
			t.Fatal(err)
		}
		if record.Status != "passed" || record.Command != input.Script || record.Action != "custom" {
			t.Fatalf("incorrect edited-command metadata: %+v", record)
		}
	}
}

func TestJSONFormattingKeepsHTTPStatusOutsideBody(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"code":"UNAUTHORIZED"}}`)
	}))
	defer server.Close()
	c := &console{cfg: testConfig(t)}
	for _, format := range []string{"raw", "pretty"} {
		cmd := newRunner(c.cfg).command(context.Background(), "bash", "-e", "-o", "pipefail", "-c", jsonRead(server.URL, "X-BMKG-Key: invalid", format))
		var stderr strings.Builder
		cmd.Stderr = &stderr
		body, err := cmd.Output()
		if err != nil || !json.Valid(body) || !strings.Contains(stderr.String(), "HTTP 401") {
			t.Fatalf("%s: invalid body/status: %v %s %s", format, err, body, stderr.String())
		}
		if (strings.Count(string(body), "\n") > 1) != (format == "pretty") {
			t.Fatalf("%s formatting: %s", format, body)
		}
	}
}

func TestSessionTokensAreMaskedWithoutBreakingJSON(t *testing.T) {
	r := newRunner(testConfig(t))
	for _, raw := range []string{`{"refresh_token":"opaque-token","expires_in":60}`, "{\n  \"refresh_token\": \"opaque-token\",\n  \"expires_in\": 60\n}"} {
		masked := r.redact(raw)
		if strings.Contains(masked, "opaque-token") || !json.Valid([]byte(masked)) {
			t.Fatalf("invalid masked session: %s", masked)
		}
	}
}
