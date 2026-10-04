package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

type commandRequest struct {
	Action     string            `json:"action"`
	Parameters map[string]string `json:"parameters"`
	Script     string            `json:"script"`
}

func readCommand(w http.ResponseWriter, r *http.Request) (commandRequest, action, error) {
	var input commandRequest
	r.Body = http.MaxBytesReader(w, r.Body, 65536)
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&input); err != nil {
		return input, action{}, errors.New("invalid action request")
	}
	if decoder.Decode(new(any)) != io.EOF {
		return input, action{}, errors.New("expected one action request")
	}
	if input.Action == "custom" && len(input.Parameters) == 0 {
		return input, action{ID: "custom", Title: "Command"}, nil
	}
	a, err := validateAction(input.Action, input.Parameters)
	return input, a, err
}

// Preview builds the same shell text that Enter will execute. It starts no process.
func (c *console) previewCommand(w http.ResponseWriter, r *http.Request) {
	input, a, err := readCommand(w, r)
	if err != nil {
		fail(w, 400, err)
		return
	}
	plan, err := c.commandPlan(input.Action, input.Parameters)
	if err != nil {
		fail(w, 400, err)
		return
	}
	respond(w, 200, map[string]string{"command": commandDisplay(plan.script), "script": plan.script, "title": a.Title})
}

func validateInspection(p map[string]string) error {
	allowed := map[string]map[string][]string{
		"load":      {"measurement": {"load", "tcp"}},
		"canonical": {"source": {"BMKG", "PVMBG"}},
		"broker":    {}, "consumer": {"service": {"dashboard-consumer", "notifier-consumer", "third-consumer"}},
		"mapping": {"source": {"BMKG", "PVMBG"}, "ref": nil},
		"api":     {"source": {"BMKG", "PVMBG"}, "identity": {"media", "field-team", "bnpb-internal"}},
		"history": {"source": {"BMKG", "PVMBG"}, "ref": nil, "revisions": {"1", "1,2"}},
		"store":   {}, "events": {}, "containers": {}, "profile": {}, "tracked-config": {},
		"schema": {"name": {"hazard-event", "hazard-envelope", "hazard-summary"}},
		"config": {"service": services},
	}
	fields, ok := allowed[p["kind"]]
	if !ok {
		return errors.New("unknown inspection")
	}
	for k, v := range p {
		if k == "format" {
			if v != "raw" && v != "pretty" {
				return errors.New("invalid format")
			}
			continue
		}
		if k == "kind" {
			continue
		}
		values, ok := fields[k]
		if !ok {
			return errors.New("unexpected inspection parameter")
		}
		if k == "ref" {
			if !refPattern.MatchString(v) {
				return errors.New("invalid source reference")
			}
			continue
		}
		matched := false
		for _, candidate := range values {
			if v == candidate {
				matched = true
			}
		}
		if !matched {
			return fmt.Errorf("invalid %s", k)
		}
	}
	if p["kind"] == "load" && p["measurement"] == "" {
		return errors.New("select a measurement")
	}
	if p["kind"] == "history" && (p["source"] == "" || p["ref"] == "") {
		return errors.New("select a source and read a mapped sample first")
	}
	if (p["kind"] == "config" || p["kind"] == "consumer") && p["service"] == "" {
		return errors.New("select a service")
	}
	return nil
}

// Enter executes the displayed command text unchanged through Bash.
func (c *console) executeCommand(t *task, plan commandPlan) (any, error) {
	current, err := loadConfig(c.cfg.repo, c.cfg.envFile, c.cfg.evidence, c.cfg.specDir)
	if err != nil {
		return nil, err
	}
	if current.fingerprint() != c.cfg.fingerprint() {
		return nil, errors.New("configuration changed; restart the console before running commands")
	}
	for _, target := range plan.targets {
		if err := c.guardHTTP(t.ctx, target); err != nil {
			return nil, err
		}
	}
	sessionDir := filepath.Join(c.cfg.evidence, "sessions")
	if strings.Contains(plan.script, "$AAT_DEMO_SESSION_DIR") {
		if err := os.MkdirAll(sessionDir, 0700); err != nil {
			return nil, err
		}
		t.line("# AAT_DEMO_SESSION_DIR=" + sessionDir)
	}
	output := filepath.Join(t.dir, "command-result.json")
	defer os.Remove(output)
	cmd := c.runner.command(t.ctx, "bash", "--noprofile", "--norc", "-e", "-o", "pipefail", "-c", plan.script)
	cmd.Env = append(cmd.Env, "AAT_DEMO_ENV_FILE="+c.cfg.envFile, "AAT_DEMO_OUTPUT="+output, "AAT_DEMO_RUN_DIR="+t.dir, "AAT_DEMO_SESSION_DIR="+sessionDir)
	prepareProcess(cmd)
	if strings.Contains(plan.script, "$AAT_DEMO_RUN_DIR") || strings.Contains(plan.script, "$AAT_DEMO_OUTPUT") {
		t.line("# AAT_DEMO_RUN_DIR=" + t.dir)
		t.line("# AAT_DEMO_OUTPUT=" + output)
	}
	t.line(commandDisplay(plan.script))
	outputStream := &outputWriter{task: t}
	cmd.Stdout, cmd.Stderr = outputStream, outputStream
	err = cmd.Run()
	outputStream.flush()
	if cmd.ProcessState != nil {
		t.line(fmt.Sprintf("Exit code: %d", cmd.ProcessState.ExitCode()))
	}
	data, readErr := os.ReadFile(output)
	if os.IsNotExist(readErr) {
		return nil, err
	}
	if readErr != nil {
		return nil, errors.Join(err, readErr)
	}
	if !json.Valid(data) {
		return nil, errors.Join(err, errors.New("invalid command result"))
	}
	return json.RawMessage(data), err
}

func commandDisplay(script string) string { return "$ " + strings.ReplaceAll(script, "\n", "\n> ") }

func (c *console) trackedConfig(ctx context.Context) (any, error) {
	files, err := c.runner.capture(ctx, "git", "ls-files", "--", ".env", ".env.example", ".secrets")
	if err != nil {
		return nil, err
	}
	return map[string]any{"tracked_configuration_files": strings.Fields(string(files)), "environment": c.runner.maskedProfile()}, nil
}
