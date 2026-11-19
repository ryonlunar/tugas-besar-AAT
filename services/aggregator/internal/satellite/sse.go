// Package satellite implements the Feed Satelit transport: a hand-rolled
// text/event-stream (SSE) reader, the feed's detection/update/retract event
// shapes, a session+stream HTTP client, and the contracts.SourceAdapter that
// turns the stream into durable, offset-ordered micro-batches.
package satellite

import (
	"bufio"
	"bytes"
	"errors"
	"io"
)

// ErrEventTooLarge is returned by Reader.Next when a single physical line
// exceeds the configured maxLine, protecting against unbounded buffering of
// a misbehaving or adversarial stream.
var ErrEventTooLarge = errors.New("satellite: SSE line exceeds maximum length")

// Event is one dispatched SSE record: the last id/event field values seen in
// the record, its data lines joined with "\n", and the exact wire bytes from
// the record's first line through its terminating blank line (inclusive).
// Raw is what gets archived verbatim in a micro-batch's RawRecord payload
// (D4), so it must reproduce the source bytes exactly, including any CRLF.
//
// Unlike a browser EventSource, id/event do not carry over between records:
// every dispatched Event is self-contained. The feed is only ever resumed
// via the ?from= query parameter (never Last-Event-ID), so there is no
// reconnect semantic that depends on a persisted "last event id" buffer.
type Event struct {
	ID   string
	Type string
	Data []byte
	Raw  []byte
}

// Reader parses a text/event-stream body into successive Events. It treats
// each run of non-blank lines terminated by a blank line as one record:
// comment lines (leading ':') and unrecognized fields are preserved in Raw
// but never set ID/Type/Data. A record without its terminating blank line
// before the stream ends is reported as io.ErrUnexpectedEOF rather than
// silently dispatched, since its data may be truncated mid-write.
type Reader struct {
	br      *bufio.Reader
	maxLine int
}

// NewReader wraps r. maxLine bounds a single physical line (the bytes read
// up to and including its line terminator); 0 or negative disables the
// bound.
func NewReader(r io.Reader, maxLine int) *Reader {
	return &Reader{br: bufio.NewReaderSize(r, 4096), maxLine: maxLine}
}

// Next reads and returns the next dispatched event. It returns io.EOF once
// the stream ends cleanly at a record boundary (no partial record pending).
func (r *Reader) Next() (Event, error) {
	var (
		id, typ   string
		dataParts [][]byte
		raw       bytes.Buffer
		sawLine   bool
	)
	for {
		lineRaw, content, err := r.nextLine()
		if err != nil {
			if err == io.EOF && sawLine {
				return Event{}, io.ErrUnexpectedEOF
			}
			return Event{}, err
		}
		raw.Write(lineRaw)
		if len(content) == 0 {
			if sawLine {
				return Event{ID: id, Type: typ, Data: joinData(dataParts), Raw: raw.Bytes()}, nil
			}
			continue // leading/extra blank lines before any record don't dispatch
		}
		sawLine = true
		if content[0] == ':' {
			continue // comment: kept in Raw, sets no field
		}
		field, value := splitField(content)
		switch field {
		case "id":
			id = value
		case "event":
			typ = value
		case "data":
			dataParts = append(dataParts, []byte(value))
		default:
			// Unrecognized fields are ignored per the feed's contract.
		}
	}
}

// nextLine reads one physical line, including its terminator, enforcing
// maxLine. err is nil only when a complete terminated line was read; any
// other outcome (EOF, oversized line, or a transport error) yields a nil
// line/content and a non-nil error.
func (r *Reader) nextLine() (lineRaw, content []byte, err error) {
	var buf []byte
	for {
		chunk, rerr := r.br.ReadSlice('\n')
		buf = append(buf, chunk...)
		if r.maxLine > 0 && len(buf) > r.maxLine {
			return nil, nil, ErrEventTooLarge
		}
		switch rerr {
		case nil:
			content = bytes.TrimSuffix(buf, []byte("\n"))
			content = bytes.TrimSuffix(content, []byte("\r"))
			return buf, content, nil
		case bufio.ErrBufferFull:
			continue // delimiter not yet found within the internal buffer; keep accumulating
		default:
			return nil, nil, rerr
		}
	}
}

// splitField parses one content line (terminator already stripped) into an
// SSE field name and value, stripping at most one leading space from the
// value per the wire format (not all leading whitespace).
func splitField(content []byte) (field, value string) {
	idx := bytes.IndexByte(content, ':')
	if idx < 0 {
		return string(content), ""
	}
	field = string(content[:idx])
	val := content[idx+1:]
	val = bytes.TrimPrefix(val, []byte(" "))
	return field, string(val)
}

// joinData concatenates an event's data lines with a single LF, matching
// the feed's multi-line data field convention.
func joinData(parts [][]byte) []byte {
	if len(parts) == 0 {
		return nil
	}
	return bytes.Join(parts, []byte("\n"))
}
