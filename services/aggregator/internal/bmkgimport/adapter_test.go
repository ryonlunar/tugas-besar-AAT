package bmkgimport

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
)

func writeTestCatalog(t *testing.T, rows int) string {
	t.Helper()
	content := "# SYNTHETIC — test fixture\n" + testHeader + "\n"
	for i := 1; i <= rows; i++ {
		content += fmt.Sprintf("synthetic-eq-%05d,2023-11-14T22:15:37Z,-0.73,100.23,10.6,4.5,Sumatera Barat,false\n", i)
	}
	path := filepath.Join(t.TempDir(), "catalog.csv")
	if err := os.WriteFile(path, []byte(content), 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func fileSHA12(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])[:12]
}

func TestCatalogAdapterImportsAllRowsThenHandsOffToPolling(t *testing.T) {
	path := writeTestCatalog(t, 10)
	adapter := &Adapter{Path: path, BatchSize: 4}

	var allKeys []string
	var lastCursor string
	err := adapter.Run(context.Background(), "", func(_ context.Context, b contracts.Batch) error {
		for _, r := range b.Records {
			allKeys = append(allKeys, r.Key)
			if r.Kind != "seismic_catalog_row" {
				t.Fatalf("Kind = %q, want seismic_catalog_row", r.Kind)
			}
			if string(r.Auxiliary) != testHeader {
				t.Fatalf("Auxiliary = %q, want header", r.Auxiliary)
			}
		}
		lastCursor = b.Cursor
		return nil
	})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("Run error = %v, want io.EOF on a completed import", err)
	}
	if len(allKeys) != 10 {
		t.Fatalf("got %d records, want 10", len(allKeys))
	}
	if lastCursor == "" {
		t.Fatal("no commit ever happened")
	}

	phase, _, poll := Parse(lastCursor, fileSHA12(t, path))
	if phase != PhasePolling || poll != "0" {
		t.Fatalf("final cursor %q: phase=%v poll=%q, want PhasePolling poll=0", lastCursor, phase, poll)
	}

	// Running again with the final (done) cursor must immediately hand
	// off to polling without re-importing anything.
	called := false
	err2 := adapter.Run(context.Background(), lastCursor, func(context.Context, contracts.Batch) error {
		called = true
		return nil
	})
	if called {
		t.Fatal("re-running with a done cursor must not commit anything")
	}
	if !errors.Is(err2, io.EOF) {
		t.Fatalf("re-running with a done cursor: err = %v, want io.EOF", err2)
	}
}

func TestCatalogAdapterResumesFromRow(t *testing.T) {
	path := writeTestCatalog(t, 10)
	adapter := &Adapter{Path: path, BatchSize: 4}

	// First run: fail the second batch's commit (simulating a crash) --
	// Run must stop and return that error, with the cursor durably
	// advanced only past the first successfully-committed batch (4 rows).
	batchNum := 0
	var survivedCursor string
	firstErr := adapter.Run(context.Background(), "", func(_ context.Context, b contracts.Batch) error {
		batchNum++
		if batchNum == 2 {
			return errors.New("simulated commit failure")
		}
		survivedCursor = b.Cursor
		return nil
	})
	if firstErr == nil || errors.Is(firstErr, io.EOF) {
		t.Fatalf("first run should have failed at the second batch, got %v", firstErr)
	}
	if survivedCursor == "" {
		t.Fatal("the first batch must have committed before the simulated failure")
	}

	// Second run: resume from the surviving cursor. No row already
	// committed in the first run must be committed again.
	var secondRunKeys []string
	err := adapter.Run(context.Background(), survivedCursor, func(_ context.Context, b contracts.Batch) error {
		secondRunKeys = append(secondRunKeys, keysOf(b.Records)...)
		return nil
	})
	if !errors.Is(err, io.EOF) {
		t.Fatalf("resumed run error = %v, want io.EOF", err)
	}
	// 10 rows total, first batch of 4 already committed before the
	// simulated failure, so the resumed run must deliver exactly the
	// remaining 6, with no duplicates.
	if len(secondRunKeys) != 6 {
		t.Fatalf("resumed run delivered %d records, want 6 (10 total - 4 already committed)", len(secondRunKeys))
	}
}

func keysOf(records []contracts.RawRecord) []string {
	out := make([]string, len(records))
	for i, r := range records {
		out[i] = r.Key
	}
	return out
}
