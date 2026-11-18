package pipeline

import (
	"context"
	"testing"
	"time"

	"github.com/ryonlunar/tugas-besar-AAT/contracts"
)

// TestPollingAdapterPreservesCursorPrefix is the shared-file half of
// bmkgimport's cursor handoff (D11, Task 7.4): once the historical-catalog
// import finishes and hands the cursor to PollingAdapter as
// "catalog:<sha>:done:<n>", PollingAdapter must keep incrementing only the
// trailing counter and preserve the "catalog:<sha>:done:" prefix verbatim
// -- never reparse or drop it. A plain M1-style numeric cursor (no prefix
// at all) must keep behaving exactly as before.
func TestPollingAdapterPreservesCursorPrefix(t *testing.T) {
	t.Run("catalog prefix is preserved across polls", func(t *testing.T) {
		var gotFrom, gotCursor string
		p := &PollingAdapter{
			Read:     func(context.Context) ([]contracts.RawRecord, error) { return nil, nil },
			Interval: time.Millisecond,
			Source:   contracts.SourceBMKG,
		}
		ctx, cancel := context.WithCancel(context.Background())
		commits := 0
		err := p.Run(ctx, "catalog:abc123:done:5", func(_ context.Context, b contracts.Batch) error {
			commits++
			gotFrom, gotCursor = b.From, b.Cursor
			cancel()
			return nil
		})
		if err != context.Canceled {
			t.Fatalf("Run error = %v, want context.Canceled", err)
		}
		if commits != 1 {
			t.Fatalf("commits = %d, want 1", commits)
		}
		if gotFrom != "catalog:abc123:done:5" {
			t.Fatalf("From = %q, want catalog:abc123:done:5 (unchanged)", gotFrom)
		}
		if gotCursor != "catalog:abc123:done:6" {
			t.Fatalf("Cursor = %q, want catalog:abc123:done:6", gotCursor)
		}
	})

	t.Run("plain M1-style cursor is unaffected", func(t *testing.T) {
		var gotCursor string
		p := &PollingAdapter{
			Read:     func(context.Context) ([]contracts.RawRecord, error) { return nil, nil },
			Interval: time.Millisecond,
			Source:   contracts.SourceBMKG,
		}
		ctx, cancel := context.WithCancel(context.Background())
		err := p.Run(ctx, "5", func(_ context.Context, b contracts.Batch) error {
			gotCursor = b.Cursor
			cancel()
			return nil
		})
		if err != context.Canceled {
			t.Fatalf("Run error = %v, want context.Canceled", err)
		}
		if gotCursor != "6" {
			t.Fatalf("Cursor = %q, want 6 (M1-compatible)", gotCursor)
		}
	})

	t.Run("empty cursor starts at prefix-less 0 -> 1", func(t *testing.T) {
		var gotCursor string
		p := &PollingAdapter{
			Read:     func(context.Context) ([]contracts.RawRecord, error) { return nil, nil },
			Interval: time.Millisecond,
			Source:   contracts.SourceBMKG,
		}
		ctx, cancel := context.WithCancel(context.Background())
		err := p.Run(ctx, "", func(_ context.Context, b contracts.Batch) error {
			gotCursor = b.Cursor
			cancel()
			return nil
		})
		if err != context.Canceled {
			t.Fatalf("Run error = %v, want context.Canceled", err)
		}
		if gotCursor != "1" {
			t.Fatalf("Cursor = %q, want 1", gotCursor)
		}
	})
}
