package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

type runRecord struct {
	ID         string            `json:"id"`
	Action     string            `json:"action"`
	Command    string            `json:"command,omitempty"`
	Title      string            `json:"title"`
	Status     string            `json:"status"`
	Started    time.Time         `json:"started"`
	Finished   *time.Time        `json:"finished,omitempty"`
	Revision   string            `json:"revision"`
	Dirty      bool              `json:"dirty"`
	ElapsedMS  int64             `json:"elapsed_ms"`
	Project    string            `json:"project"`
	Profile    map[string]string `json:"profile"`
	Parameters map[string]string `json:"parameters"`
	Error      string            `json:"error,omitempty"`
	Lines      []string          `json:"lines,omitempty"`
	Result     any               `json:"result,omitempty"`
	HasResult  bool              `json:"has_result"`
}

type task struct {
	mu          sync.Mutex
	record      runRecord
	dir         string
	ctx         context.Context
	cancel      context.CancelFunc
	done        chan struct{}
	runner      *runner
	log         *os.File
	startedMono time.Time
	logCapped   bool
}

type runner struct {
	cfg       config
	mu        sync.Mutex
	active    *task
	secretsMu sync.Mutex
	secrets   []string
}

var runIDPattern = regexp.MustCompile(`^[0-9]{8}T[0-9]{6}-[a-f0-9]{12}$`)
var refreshPattern = regexp.MustCompile(`("refresh_token"\s*:\s*")[^"]+`)
var jwtPattern = regexp.MustCompile(`eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+`)

func newRunner(cfg config) *runner {
	r := &runner{cfg: cfg}
	for k, v := range cfg.values {
		if secretPattern.MatchString(k) {
			r.addSecret(v)
		}
	}
	return r
}
func (r *runner) addSecret(secret string) {
	if secret == "" {
		return
	}
	r.secretsMu.Lock()
	defer r.secretsMu.Unlock()
	r.secrets = append(r.secrets, secret)
	sort.Slice(r.secrets, func(i, j int) bool { return len(r.secrets[i]) > len(r.secrets[j]) })
}
func (r *runner) redact(value string) string {
	r.secretsMu.Lock()
	defer r.secretsMu.Unlock()
	for _, secret := range r.secrets {
		value = strings.ReplaceAll(value, secret, "[redacted]")
	}
	value = refreshPattern.ReplaceAllString(value, `${1}[redacted refresh token]`)
	return jwtPattern.ReplaceAllString(value, "[redacted JWT]")
}

func (r *runner) maskedProfile() map[string]string {
	profile := make(map[string]string, len(r.cfg.values))
	for k, v := range r.cfg.values {
		if secretPattern.MatchString(k) {
			profile[k] = "[redacted]"
		} else {
			profile[k] = r.redact(v)
		}
	}
	return profile
}

func (r *runner) command(ctx context.Context, program string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, program, args...)
	cmd.Dir = r.cfg.repo
	cmd.Env = r.cfg.processEnv()
	return cmd
}

func (r *runner) capture(ctx context.Context, program string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	cmd := r.command(ctx, program, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	data, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%s failed: %s", program, r.redact(strings.TrimSpace(stderr.String())))
	}
	return data, nil
}

func (r *runner) start(action action, params map[string]string, execute func(*task) (any, error)) (*task, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active != nil {
		return nil, errors.New("another action is running; finish or cancel it first")
	}
	key := make([]byte, 6)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	id := time.Now().UTC().Format("20060102T150405") + "-" + hex.EncodeToString(key)
	dir := filepath.Join(r.cfg.evidence, id)
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	logFile, err := os.OpenFile(filepath.Join(dir, "output.log"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	revision, _ := r.capture(context.Background(), "git", "rev-parse", "HEAD")
	changes, _ := r.capture(context.Background(), "git", "status", "--porcelain")
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	t := &task{record: runRecord{ID: id, Action: action.ID, Title: action.Title, Status: "running", Started: time.Now().UTC(), Revision: strings.TrimSpace(string(revision)), Dirty: len(changes) > 0, Project: r.cfg.project, Parameters: params}, dir: dir, ctx: ctx, cancel: cancel, done: make(chan struct{}), runner: r, log: logFile, startedMono: time.Now()}
	t.record.Profile = r.maskedProfile()
	if err := t.persist(); err != nil {
		cancel()
		logFile.Close()
		return nil, err
	}
	r.active = t
	go func() {
		defer close(t.done)
		defer cancel()
		t.line("# " + action.Title)
		result, err := execute(t)
		if result != nil {
			err = errors.Join(err, r.writeJSON(filepath.Join(dir, "result.json"), result))
		}
		resultData, _ := os.ReadFile(filepath.Join(dir, "result.json"))
		finished := time.Now().UTC()
		t.mu.Lock()
		t.record.Finished = &finished
		t.record.ElapsedMS = time.Since(t.startedMono).Milliseconds()
		if json.Valid(resultData) {
			t.record.Result = json.RawMessage(resultData)
			t.record.HasResult = true
		}
		t.record.Status = "passed"
		if err != nil {
			t.record.Status = "failed"
			t.record.Error = r.redact(err.Error())
		}
		if ctx.Err() != nil {
			t.record.Status = "cancelled"
			if err == nil {
				t.record.Error = "Run cancelled"
			}
		}
		t.mu.Unlock()
		if err != nil {
			t.line("Error: " + err.Error())
		}
		t.line("Finished: " + t.snapshot().Status)
		if err := t.persist(); err != nil {
			t.line("Could not save run metadata: " + err.Error())
		}
		logFile.Close()
		r.mu.Lock()
		r.active = nil
		r.mu.Unlock()
	}()
	return t, nil
}

func (t *task) line(value string) {
	value = t.runner.redact(value)
	t.mu.Lock()
	defer t.mu.Unlock()
	t.record.Lines = append(t.record.Lines, value)
	if len(t.record.Lines) > 400 {
		t.record.Lines = t.record.Lines[len(t.record.Lines)-400:]
	}
	if info, err := t.log.Stat(); err == nil && info.Size() < 4<<20 {
		_, _ = fmt.Fprintln(t.log, t.record.Lines[len(t.record.Lines)-1])
	} else if err == nil && !t.logCapped {
		_, _ = fmt.Fprintln(t.log, "[Output log reached 4 MiB; further output is omitted.]")
		t.logCapped = true
	}
}
func (t *task) snapshot() runRecord {
	t.mu.Lock()
	defer t.mu.Unlock()
	v := t.record
	if v.Status == "running" {
		v.ElapsedMS = time.Since(t.startedMono).Milliseconds()
	}
	v.Lines = append([]string(nil), v.Lines...)
	return v
}
func (t *task) persist() error {
	v := t.snapshot()
	v.Lines = nil
	v.Result = nil
	return t.runner.writeJSON(filepath.Join(t.dir, "run.json"), v)
}

func (r *runner) redactedJSON(value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var decoded any
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&decoded); err != nil {
		return nil, err
	}
	var scrub func(any) any
	scrub = func(v any) any {
		switch v := v.(type) {
		case string:
			return r.redact(v)
		case []any:
			for i := range v {
				v[i] = scrub(v[i])
			}
		case map[string]any:
			for k, item := range v {
				if secretPattern.MatchString(k) {
					if _, ok := item.(string); ok {
						v[k] = "[redacted]"
						continue
					}
				}
				v[k] = scrub(item)
			}
		}
		return v
	}
	data, err = json.MarshalIndent(scrub(decoded), "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

func (r *runner) writeJSON(path string, value any) error {
	data, err := r.redactedJSON(value)
	if err != nil {
		return err
	}
	if err := os.WriteFile(path+".tmp", data, 0600); err != nil {
		return err
	}
	return os.Rename(path+".tmp", path)
}

// Buffer complete lines so a secret split across process writes is still redacted.
type outputWriter struct {
	task    *task
	mu      sync.Mutex
	pending []byte
	discard bool
}

func (w *outputWriter) Write(data []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(data)
	for len(data) > 0 {
		part, rest, found := bytes.Cut(data, []byte("\n"))
		if !w.discard {
			w.pending = append(w.pending, part...)
			if len(w.pending) > 128<<10 {
				w.pending = nil
				w.discard = true
			}
		}
		if found {
			if w.discard {
				w.task.line("[Output line omitted: exceeded 128 KiB]")
			} else {
				w.task.line(strings.TrimSuffix(string(w.pending), "\r"))
			}
			w.pending = nil
			w.discard = false
			data = rest
		} else {
			break
		}
	}
	return n, nil
}
func (w *outputWriter) flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.discard {
		w.task.line("[Output line omitted: exceeded 128 KiB]")
	} else if len(w.pending) > 0 {
		w.task.line(string(w.pending))
	}
	w.pending = nil
}

func (t *task) exec(ctx context.Context, program string, args ...string) error {
	t.line(formatCommand(program, args))
	cmd := t.runner.command(ctx, program, args...)
	prepareProcess(cmd)
	out, stderr := &outputWriter{task: t}, &outputWriter{task: t}
	cmd.Stdout = out
	cmd.Stderr = stderr
	err := cmd.Run()
	out.flush()
	stderr.flush()
	if err != nil {
		return fmt.Errorf("%s: %w", program, err)
	}
	t.line("Exit code: 0")
	return nil
}
func (t *task) compose(ctx context.Context, args ...string) error {
	return t.exec(ctx, "docker", t.runner.cfg.compose(args...)...)
}
func (r *runner) cancelActive() {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active != nil {
		r.active.cancel()
	}
}
func (r *runner) wait(ctx context.Context) {
	r.mu.Lock()
	t := r.active
	r.mu.Unlock()
	if t != nil {
		select {
		case <-t.done:
		case <-ctx.Done():
		}
	}
}

func (c *console) startRun(w http.ResponseWriter, r *http.Request) {
	input, a, err := readCommand(w, r)
	if err != nil {
		fail(w, 400, err)
		return
	}
	if strings.TrimSpace(input.Script) == "" || strings.ContainsRune(input.Script, '\x00') || len(input.Script) > 32768 {
		fail(w, 400, errors.New("enter a command of at most 32 KiB"))
		return
	}
	plan := commandPlan{script: input.Script}
	if a.ID != "custom" {
		original, err := c.commandPlan(input.Action, input.Parameters)
		if err != nil {
			fail(w, 400, err)
			return
		}
		if original.script == input.Script {
			plan = original
		} else {
			a = action{ID: "custom", Title: "Edited: " + a.Title}
			input.Parameters = nil
		}
	}
	t, err := c.runner.start(a, input.Parameters, func(t *task) (any, error) {
		t.mu.Lock()
		t.record.Command = plan.script
		t.mu.Unlock()
		if err := t.persist(); err != nil {
			return nil, err
		}
		return c.executeCommand(t, plan)
	})
	if err != nil {
		fail(w, 409, err)
		return
	}
	respond(w, 202, t.snapshot())
}
func (c *console) cancelRun(w http.ResponseWriter, r *http.Request) {
	c.runner.mu.Lock()
	defer c.runner.mu.Unlock()
	t := c.runner.active
	if t == nil || t.record.ID != r.PathValue("id") {
		fail(w, 409, errors.New("run is no longer active"))
		return
	}
	t.cancel()
	respond(w, 202, map[string]string{"status": "cancelling"})
}
func (c *console) getRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !runIDPattern.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	c.runner.mu.Lock()
	t := c.runner.active
	c.runner.mu.Unlock()
	if t != nil && t.record.ID == id {
		respond(w, 200, t.snapshot())
		return
	}
	data, err := os.ReadFile(filepath.Join(c.cfg.evidence, id, "run.json"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	var v runRecord
	if json.Unmarshal(data, &v) != nil {
		fail(w, 500, errors.New("invalid run metadata"))
		return
	}
	v = savedRunState(v, "")
	logData, _ := os.ReadFile(filepath.Join(c.cfg.evidence, id, "output.log"))
	v.Lines = strings.Split(strings.TrimSpace(string(logData)), "\n")
	if len(v.Lines) > 400 {
		v.Lines = v.Lines[len(v.Lines)-400:]
	}
	result, _ := os.ReadFile(filepath.Join(c.cfg.evidence, id, "result.json"))
	if json.Valid(result) {
		v.Result = json.RawMessage(result)
		v.HasResult = true
	}
	respond(w, 200, v)
}
func (c *console) listRuns(w http.ResponseWriter, r *http.Request) {
	c.runner.mu.Lock()
	active := ""
	if c.runner.active != nil {
		active = c.runner.active.record.ID
	}
	c.runner.mu.Unlock()
	entries, _ := os.ReadDir(c.cfg.evidence)
	result := []runRecord{}
	for i := len(entries) - 1; i >= 0 && len(result) < 100; i-- {
		if !runIDPattern.MatchString(entries[i].Name()) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(c.cfg.evidence, entries[i].Name(), "run.json"))
		var v runRecord
		if err == nil && json.Unmarshal(data, &v) == nil {
			_, resultErr := os.Stat(filepath.Join(c.cfg.evidence, entries[i].Name(), "result.json"))
			v.HasResult = resultErr == nil
			result = append(result, savedRunState(v, active))
		}
	}
	respond(w, 200, map[string]any{"runs": result, "active": active})
}

// A saved start record alone cannot prove either completion or cleanup.
func savedRunState(v runRecord, active string) runRecord {
	if v.Status == "running" && v.ID != active {
		v.Status = "unfinished"
		v.Error = "No completion record. Check service state and restore defaults before retrying."
	}
	return v
}
func (c *console) runFile(w http.ResponseWriter, r *http.Request) {
	id, file := r.PathValue("id"), r.PathValue("file")
	if !runIDPattern.MatchString(id) || (file != "run.json" && file != "result.json" && file != "output.log") {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+file+`"`)
	http.ServeFile(w, r, filepath.Join(c.cfg.evidence, id, file))
}

var _ io.Writer = (*outputWriter)(nil)
var shellWord = regexp.MustCompile(`^[A-Za-z0-9_./:@%+,=-]+$`)

// Quote for a readable, copyable command display. Execution still uses argv.
func formatCommand(program string, args []string) string {
	words := append([]string{program}, args...)
	for i, word := range words {
		if !shellWord.MatchString(word) {
			words[i] = "'" + strings.ReplaceAll(word, "'", "'\"'\"'") + "'"
		}
	}
	return "$ " + strings.Join(words, " ")
}
