package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Stream the saved log, using byte offsets to resume without losing fast output.
func (c *console) streamRun(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !runIDPattern.MatchString(id) {
		http.NotFound(w, r)
		return
	}
	c.runner.mu.Lock()
	t := c.runner.active
	c.runner.mu.Unlock()
	if t != nil && t.record.ID != id {
		t = nil
	}
	var saved runRecord
	if t == nil {
		data, err := os.ReadFile(filepath.Join(c.cfg.evidence, id, "run.json"))
		if err != nil || json.Unmarshal(data, &saved) != nil {
			http.NotFound(w, r)
			return
		}
		saved = savedRunState(saved, "")
	}
	file, err := os.Open(filepath.Join(c.cfg.evidence, id, "output.log"))
	if err != nil {
		http.NotFound(w, r)
		return
	}
	defer file.Close()
	controller := http.NewResponseController(w)
	if controller.SetWriteDeadline(time.Time{}) != nil {
		http.Error(w, "streaming unavailable", 500)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("X-Accel-Buffering", "no")
	send := func(event string, offset int64, value any) bool {
		if controller.SetWriteDeadline(time.Now().Add(10*time.Second)) != nil {
			return false
		}
		data, err := json.Marshal(value)
		if err != nil {
			return false
		}
		if event == "output" {
			_, err = fmt.Fprintf(w, "id: %d\n", offset)
		}
		if err == nil {
			_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data)
		}
		return err == nil && controller.Flush() == nil
	}
	cursor, _ := strconv.ParseInt(r.Header.Get("Last-Event-ID"), 10, 64)
	info, err := file.Stat()
	if err != nil {
		return
	}
	if cursor < 0 || cursor > info.Size() {
		cursor = 0
	}
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		snapshot := saved
		if t != nil {
			// The log writer holds this mutex until a complete redacted line is saved.
			t.mu.Lock()
			snapshot = t.record
			snapshot.Lines = nil
			info, err = file.Stat()
			t.mu.Unlock()
		} else {
			info, err = file.Stat()
		}
		if err != nil {
			return
		}
		end := info.Size()
		if end > cursor {
			data, err := io.ReadAll(io.NewSectionReader(file, cursor, end-cursor))
			if err != nil || !send("output", end, map[string]string{"text": string(data)}) {
				return
			}
			cursor = end
		}
		if snapshot.Status != "running" {
			send("complete", end, snapshot)
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
		}
	}
}
