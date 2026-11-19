package poller

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
)

func TestRawIntakePreservesUndecodableResponse(t *testing.T) {
	for _, body := range [][]byte{[]byte(`[{"report_id":`), {'[', '{', '"', 'x', '"', ':', '"', 0xff, '"', '}', ']'}} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
		client := NewPVMBGClient(server.URL, "token", server.Client(), 1<<20)
		records, err := client.ReadRecords(context.Background())
		server.Close()
		if err != nil || len(records) != 1 || !bytes.Equal(records[0].Payload, body) && !bytes.Equal(records[0].Payload, body[1:len(body)-1]) {
			t.Fatalf("raw bytes changed before persistence: %+v %v", records, err)
		}
	}
}

// TestPVMBGMalformedResponses is the P3.3a-d matrix (Task 1.13): malformed
// source bytes must become exactly one inspectable failed input per response,
// never a panic, and must never advance the cursor silently.
func TestPVMBGMalformedResponses(t *testing.T) {
	serve := func(body []byte) *httptest.Server {
		return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(body) }))
	}

	t.Run("truncated_json_wraps_whole_body_as_one_record", func(t *testing.T) {
		body := []byte(`[{"report_id":"r1","alert_level":`)
		server := serve(body)
		defer server.Close()
		client := NewPVMBGClient(server.URL, "token", server.Client(), 1<<20)
		records, err := client.ReadRecords(context.Background())
		if err != nil || len(records) != 1 {
			t.Fatalf("records=%d err=%v", len(records), err)
		}
		if records[0].Kind != "invalid_volcanic_report_response" {
			t.Fatalf("kind=%q, want invalid_volcanic_report_response", records[0].Kind)
		}
		if !bytes.Equal(records[0].Payload, body) {
			t.Fatalf("payload bytes changed: %q, want %q", records[0].Payload, body)
		}
	})

	t.Run("ten_thousand_open_brackets_does_not_panic", func(t *testing.T) {
		body := bytes.Repeat([]byte("["), 10000)
		server := serve(body)
		defer server.Close()
		client := NewPVMBGClient(server.URL, "token", server.Client(), 1<<20)
		var records []contracts.RawRecord
		var err error
		func() {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panicked on deeply nested/truncated array input: %v", r)
				}
			}()
			records, err = client.ReadRecords(context.Background())
		}()
		if err != nil || len(records) != 1 || records[0].Kind != "invalid_volcanic_report_response" {
			t.Fatalf("records=%+v err=%v", records, err)
		}
		if !bytes.Equal(records[0].Payload, body) {
			t.Fatal("deeply nested payload bytes changed before persistence")
		}
	})

	t.Run("invalid_utf8_in_string_preserved_whole", func(t *testing.T) {
		// Go's encoding/json tokenizes a quoted string without validating
		// UTF-8, so the array-splitting layer here legitimately succeeds and
		// keeps this as an ordinary volcanic_report record; UTF-8 validity is
		// enforced later, in pipeline.DefaultProcessor (invalid_utf8 park
		// reason). What this layer must guarantee is just: exactly one
		// record, bytes preserved verbatim, no panic.
		body := []byte{'[', '{', '"', 'r', '"', ':', '"', 0xff, '"', '}', ']'}
		server := serve(body)
		defer server.Close()
		client := NewPVMBGClient(server.URL, "token", server.Client(), 1<<20)
		records, err := client.ReadRecords(context.Background())
		if err != nil || len(records) != 1 {
			t.Fatalf("records=%+v err=%v", records, err)
		}
		if !bytes.Equal(records[0].Payload, body) && !bytes.Equal(records[0].Payload, body[1:len(body)-1]) {
			t.Fatalf("invalid UTF-8 payload bytes changed before persistence: %q", records[0].Payload)
		}
	})

	t.Run("oversized_body_fails_fetch_without_any_record", func(t *testing.T) {
		body := bytes.Repeat([]byte("x"), 200)
		server := serve(body)
		defer server.Close()
		client := NewPVMBGClient(server.URL, "token", server.Client(), 100) // maxBody < len(body)
		records, err := client.ReadRecords(context.Background())
		if err == nil {
			t.Fatal("oversized response must fail the fetch, not be admitted")
		}
		if len(records) != 0 {
			t.Fatalf("oversized response must not advance any record: %+v", records)
		}
	})
}
