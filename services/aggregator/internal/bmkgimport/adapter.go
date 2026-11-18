package bmkgimport

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
)

// Adapter is a FINITE contracts.SourceAdapter that imports a BMKG
// historical-catalog file once, as a phase that runs inside BMKG's own
// ownership session before PollingAdapter's live polling takes over (D11).
// It never advances its durable row cursor past a row the caller's commit
// rejected, and it hands off to polling via Run returning io.EOF with the
// cursor already carrying the "catalog:<sha>:done:<poll>" prefix
// PollingAdapter understands (Task 7.4).
type Adapter struct {
	// Path is the catalog file (CSV: header line, optionally preceded by
	// one or more leading '#' comment lines such as a SYNTHETIC label).
	Path string
	// BatchSize bounds how many rows one RawRecord batch commits
	// together; <=0 defaults to 200.
	BatchSize int
	// CanRead reports backpressure (e.g. journal.Session.Unfinished
	// margin), identical in spirit to the satellite adapter's CanRead. A
	// nil CanRead always allows reading.
	CanRead func(context.Context) (bool, error)
}

// Run implements contracts.SourceAdapter. On success -- the whole file
// imported, or a cursor already marked PhasePolling for this file's
// content hash -- it returns io.EOF so the caller's RunAdapter loop treats
// it as a normal finite-adapter completion and moves on to polling.
func (a *Adapter) Run(ctx context.Context, cursor string, commit func(context.Context, contracts.Batch) error) error {
	data, err := os.ReadFile(a.Path)
	if err != nil {
		return fmt.Errorf("bmkgimport: read catalog file: %w", err)
	}
	sum := sha256.Sum256(data)
	sha12 := hex.EncodeToString(sum[:])[:12]

	phase, startRow, _ := Parse(cursor, sha12)
	if phase == PhasePolling {
		return io.EOF // already finished for this exact file content
	}

	header, lines, err := splitCatalogLines(data)
	if err != nil {
		return fmt.Errorf("bmkgimport: %w", err)
	}

	batchSize := a.BatchSize
	if batchSize <= 0 {
		batchSize = 200
	}

	cur := cursor
	flush := func(ctx context.Context, batch []contracts.RawRecord, newRow int) error {
		if len(batch) == 0 {
			return nil
		}
		next := fmt.Sprintf("catalog:%s:%d", sha12, newRow)
		if err := commit(ctx, contracts.Batch{From: cur, Cursor: next, Records: batch}); err != nil {
			return err
		}
		cur = next
		return nil
	}

	var batch []contracts.RawRecord
	row := 0
	for _, line := range lines {
		row++
		if row <= startRow {
			continue // already imported in a prior run
		}
		if err := a.awaitReadable(ctx); err != nil {
			if flushErr := flush(ctx, batch, row-1); flushErr != nil {
				return flushErr
			}
			return err
		}
		eventID, err := FieldValue(header, line, "event_id")
		if err != nil {
			return fmt.Errorf("bmkgimport: row %d: %w", row, err)
		}
		batch = append(batch, contracts.RawRecord{
			Key:           contracts.RawKey("catalog:"+eventID, line),
			Kind:          "seismic_catalog_row",
			Payload:       append([]byte(nil), line...),
			ReceivedAt:    time.Now().UTC(),
			CorrelationID: "bmkg-catalog-import",
			OrderingKey:   "seismic_event:" + eventID,
			Auxiliary:     header,
		})
		if len(batch) >= batchSize {
			if err := flush(ctx, batch, row); err != nil {
				return err
			}
			batch = nil
		}
	}
	if err := flush(ctx, batch, row); err != nil {
		return err
	}

	doneCursor := fmt.Sprintf("catalog:%s:done:0", sha12)
	if err := commit(ctx, contracts.Batch{From: cur, Cursor: doneCursor, Records: nil}); err != nil {
		return err
	}
	return io.EOF
}

func (a *Adapter) awaitReadable(ctx context.Context) error {
	if a.CanRead == nil {
		return nil
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		allowed, err := a.CanRead(ctx)
		if err != nil {
			return fmt.Errorf("bmkgimport: CanRead: %w", err)
		}
		if allowed {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

// splitCatalogLines separates the catalog file into its header line
// (skipping any leading '#'-prefixed comment lines, e.g. a SYNTHETIC
// label) and every subsequent non-empty data line, trimming a trailing
// '\r' so the file can use either LF or CRLF line endings.
func splitCatalogLines(data []byte) (header []byte, rows [][]byte, err error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	scanner.Buffer(make([]byte, 0, 64*1024), 1<<20)
	headerSeen := false
	for scanner.Scan() {
		line := bytes.TrimRight(scanner.Bytes(), "\r")
		if len(line) == 0 {
			continue
		}
		if !headerSeen {
			if bytes.HasPrefix(line, []byte("#")) {
				continue
			}
			header = append([]byte(nil), line...)
			headerSeen = true
			continue
		}
		rows = append(rows, append([]byte(nil), line...))
	}
	if err := scanner.Err(); err != nil {
		return nil, nil, err
	}
	if !headerSeen {
		return nil, nil, errors.New("catalog file has no header line")
	}
	return header, rows, nil
}
