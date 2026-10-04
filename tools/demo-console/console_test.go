package main

import (
	"context"
	"encoding/json"
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
	if !strings.Contains(args, "--project-name console-test") || !strings.Contains(args, "--env-file "+cfg.envFile) {
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

func TestTCPSpanRequiresContinuousSamplesAboveThreshold(t *testing.T) {
	samples := []tcpSample{}
	for i := 0; i <= 60; i++ {
		samples = append(samples, tcpSample{Elapsed: float64(i), Count: 50})
	}
	if got := sustainedTCPSpan(samples, 2); got != 60 {
		t.Fatal(got)
	}
	samples[30].Count = 49
	if got := sustainedTCPSpan(samples, 2); got >= 60 {
		t.Fatal("count dip was ignored")
	}
	if got := sustainedTCPSpan([]tcpSample{{0, 50}, {65, 50}}, 2); got != 0 {
		t.Fatal("unsampled gap counted as sustained connections")
	}
}
