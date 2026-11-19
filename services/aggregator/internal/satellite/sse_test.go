package satellite

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestSSEParser(t *testing.T) {
	t.Run("dispatches id, event type and data on the terminating blank line", func(t *testing.T) {
		input := "id: 1\nevent: detection\ndata: hello\n\n"
		r := NewReader(strings.NewReader(input), 4096)
		ev, err := r.Next()
		if err != nil {
			t.Fatalf("Next() error = %v, want nil", err)
		}
		if ev.ID != "1" || ev.Type != "detection" || string(ev.Data) != "hello" {
			t.Fatalf("Next() = %+v, want ID=1 Type=detection Data=hello", ev)
		}
		if !bytes.Equal(ev.Raw, []byte(input)) {
			t.Fatalf("Raw = %q, want %q (byte-exact wire capture)", ev.Raw, input)
		}
		if _, err := r.Next(); err != io.EOF {
			t.Fatalf("second Next() error = %v, want io.EOF", err)
		}
	})

	t.Run("joins multiple data lines with a single newline", func(t *testing.T) {
		input := "data: line1\ndata: line2\ndata: line3\n\n"
		r := NewReader(strings.NewReader(input), 4096)
		ev, err := r.Next()
		if err != nil {
			t.Fatalf("Next() error = %v, want nil", err)
		}
		if string(ev.Data) != "line1\nline2\nline3" {
			t.Fatalf("Data = %q, want joined with \\n", ev.Data)
		}
	})

	t.Run("comment lines are ignored as fields but kept in Raw", func(t *testing.T) {
		input := ": this is a comment\nid: 5\ndata: payload\n\n"
		r := NewReader(strings.NewReader(input), 4096)
		ev, err := r.Next()
		if err != nil {
			t.Fatalf("Next() error = %v, want nil", err)
		}
		if ev.ID != "5" || string(ev.Data) != "payload" {
			t.Fatalf("Next() = %+v, want ID=5 Data=payload", ev)
		}
		if !bytes.Equal(ev.Raw, []byte(input)) {
			t.Fatalf("Raw = %q, want %q (comment preserved in wire bytes)", ev.Raw, input)
		}
	})

	t.Run("unrecognized fields are ignored", func(t *testing.T) {
		input := "id: 7\nfoo: bar\ndata: x\n\n"
		r := NewReader(strings.NewReader(input), 4096)
		ev, err := r.Next()
		if err != nil {
			t.Fatalf("Next() error = %v, want nil", err)
		}
		if ev.ID != "7" || ev.Type != "" || string(ev.Data) != "x" {
			t.Fatalf("Next() = %+v, want ID=7 Type=\"\" Data=x (foo ignored)", ev)
		}
	})

	t.Run("supports CRLF line endings and preserves them in Raw", func(t *testing.T) {
		input := "id: 9\r\nevent: retract\r\ndata: z\r\n\r\n"
		r := NewReader(strings.NewReader(input), 4096)
		ev, err := r.Next()
		if err != nil {
			t.Fatalf("Next() error = %v, want nil", err)
		}
		if ev.ID != "9" || ev.Type != "retract" || string(ev.Data) != "z" {
			t.Fatalf("Next() = %+v, want ID=9 Type=retract Data=z", ev)
		}
		if !bytes.Equal(ev.Raw, []byte(input)) {
			t.Fatalf("Raw = %q, want %q (CRLF preserved)", ev.Raw, input)
		}
	})

	t.Run("dispatches multiple consecutive events in order", func(t *testing.T) {
		input := "id: 0\ndata: a\n\nid: 1\ndata: b\n\n"
		r := NewReader(strings.NewReader(input), 4096)
		first, err := r.Next()
		if err != nil {
			t.Fatalf("first Next() error = %v, want nil", err)
		}
		if first.ID != "0" || string(first.Data) != "a" {
			t.Fatalf("first = %+v, want ID=0 Data=a", first)
		}
		second, err := r.Next()
		if err != nil {
			t.Fatalf("second Next() error = %v, want nil", err)
		}
		if second.ID != "1" || string(second.Data) != "b" {
			t.Fatalf("second = %+v, want ID=1 Data=b", second)
		}
		if _, err := r.Next(); err != io.EOF {
			t.Fatalf("third Next() error = %v, want io.EOF", err)
		}
	})

	t.Run("returns ErrEventTooLarge for a line exceeding maxLine", func(t *testing.T) {
		input := "data: " + strings.Repeat("x", 100) + "\n\n"
		r := NewReader(strings.NewReader(input), 20)
		_, err := r.Next()
		if !errors.Is(err, ErrEventTooLarge) {
			t.Fatalf("Next() error = %v, want ErrEventTooLarge", err)
		}
	})

	t.Run("returns io.EOF at a clean end of stream", func(t *testing.T) {
		r := NewReader(strings.NewReader(""), 4096)
		if _, err := r.Next(); err != io.EOF {
			t.Fatalf("Next() error = %v, want io.EOF", err)
		}
	})

	t.Run("returns io.ErrUnexpectedEOF when the stream ends mid-record", func(t *testing.T) {
		input := "id: 1\ndata: partial"
		r := NewReader(strings.NewReader(input), 4096)
		if _, err := r.Next(); err != io.ErrUnexpectedEOF {
			t.Fatalf("Next() error = %v, want io.ErrUnexpectedEOF", err)
		}
	})
}
