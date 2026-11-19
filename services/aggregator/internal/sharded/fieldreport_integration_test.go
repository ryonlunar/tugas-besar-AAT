package sharded

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/fieldreport"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/partition"
)

// openDisposable opens the four disposable foundation databases used by
// scripts/m2/check-foundation.py.
func openDisposable(t *testing.T) (*Store, string) {
	t.Helper()
	filename := os.Getenv("AAT_FOUNDATION_DSN_FILE")
	if filename == "" {
		t.Skip("run scripts/m2/check-foundation.py in the development VM")
	}
	raw, err := os.ReadFile(filename)
	var dsns []string
	if err != nil || json.Unmarshal(raw, &dsns) != nil || len(dsns) != 4 {
		t.Fatal("invalid disposable database configuration")
	}
	configs := []Config{}
	for i, dsn := range dsns {
		config, err := pgx.ParseConfig(dsn)
		if err != nil || !strings.HasPrefix(config.Database, "aat_foundation_") || !strings.HasSuffix(config.Database, "_test") {
			t.Fatal("refusing non-disposable database")
		}
		configs = append(configs, Config{fmt.Sprintf("shard-%d", i), dsn})
	}
	file := filepath.Join(t.TempDir(), "shards.json")
	raw, _ = json.Marshal(configs)
	if err := os.WriteFile(file, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return reopen(t, file), file
}

func reopen(t *testing.T, file string) *Store {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	repository, err := Open(ctx, file, Options{ReadConns: 2, BMKGConns: 2, PVMBGConns: 2, RelayConns: 2, ReportConns: 4, ReadTimeout: time.Second, WriteTimeout: 2 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	return repository
}

func TestFieldReportIntakeIsDurableIdempotentAndProcessedOnce(t *testing.T) {
	repository, file := openDisposable(t)
	ctx := context.Background()
	for _, u := range repository.units {
		if _, err := u.readDB.ExecContext(ctx, `TRUNCATE field_reports`); err != nil {
			t.Fatal(err)
		}
	}
	body := []byte(`{"observed_at":"2026-11-12T03:00:00Z","latitude":-0.9492,"longitude":100.3543,"description":"Air naik"}`)
	submit := func(client, key string, payload []byte) (fieldreport.Receipt, error) {
		return repository.AcceptFieldReport(ctx, fieldreport.Submission{ClientID: client, IdempotencyKey: key, CorrelationID: "corr-" + key, Payload: payload})
	}

	t.Run("retry returns first receipt and conflicting reuse is rejected", func(t *testing.T) {
		first, err := submit("field-team", "same-key", body)
		if err != nil || first.Replayed {
			t.Fatalf("first=%+v err=%v", first, err)
		}
		again, err := submit("field-team", "same-key", body)
		if err != nil || !again.Replayed || again.ReportID != first.ReportID {
			t.Fatalf("retry=%+v err=%v", again, err)
		}
		if _, err := submit("field-team", "same-key", []byte(strings.Replace(string(body), "Air naik", "Air surut", 1))); !errors.Is(err, fieldreport.ErrConflict) {
			t.Fatalf("conflicting reuse err=%v", err)
		}
		other, err := submit("other-client", "same-key", body)
		if err != nil || other.ReportID == first.ReportID {
			t.Fatalf("keys must be scoped per client: %+v err=%v", other, err)
		}
	})

	t.Run("concurrent duplicates store one report", func(t *testing.T) {
		var wg sync.WaitGroup
		ids := make([]string, 16)
		for i := range ids {
			wg.Add(1)
			go func() {
				defer wg.Done()
				receipt, err := submit("field-team", "burst-key", body)
				if err != nil {
					t.Error(err)
				}
				ids[i] = receipt.ReportID
			}()
		}
		wg.Wait()
		for _, id := range ids {
			if id != ids[0] {
				t.Fatalf("concurrent retries produced different receipts: %v", ids)
			}
		}
		unit, _ := partition.ReportUnit("field-team", "burst-key")
		var count int
		if err := repository.lookup(unit).readDB.QueryRowContext(ctx, `SELECT count(*) FROM field_reports WHERE idempotency_key='burst-key'`).Scan(&count); err != nil || count != 1 {
			t.Fatalf("stored copies=%d err=%v", count, err)
		}
	})

	const reports = 40
	accepted := map[string]bool{}
	for i := 0; i < reports; i++ {
		receipt, err := submit("field-team", fmt.Sprintf("bulk-%03d", i), body)
		if err != nil {
			t.Fatal(err)
		}
		accepted[receipt.ReportID] = true
	}
	stored := []byte{}
	unit, _ := partition.ReportUnit("field-team", "bulk-000")
	if err := repository.lookup(unit).readDB.QueryRowContext(ctx, `SELECT payload FROM field_reports WHERE idempotency_key='bulk-000'`).Scan(&stored); err != nil || string(stored) != string(body) {
		t.Fatalf("original bytes not preserved: %q err=%v", stored, err)
	}

	t.Run("receipts survive receiver restart before processing", func(t *testing.T) {
		repository.Close()
		repository = reopen(t, file)
		for id := range accepted {
			status, found, err := repository.FieldReport(ctx, "field-team", id)
			if err != nil || !found || status.Status != fieldreport.StatusAccepted {
				t.Fatalf("report %s after restart: %+v found=%v err=%v", id, status, found, err)
			}
		}
		if _, found, err := repository.FieldReport(ctx, "other-client", firstKey(accepted)); err != nil || found {
			t.Fatalf("report visible to another client: found=%v err=%v", found, err)
		}
	})

	t.Run("concurrent workers process each report once", func(t *testing.T) {
		total := 0
		var mu sync.Mutex
		var wg sync.WaitGroup
		for worker := 0; worker < 3; worker++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for {
					n, err := repository.ProcessFieldReports(ctx, fmt.Sprintf("aggregator-%d", worker), 5, fieldreport.Process)
					if err != nil {
						t.Error(err)
						return
					}
					if n == 0 {
						return
					}
					mu.Lock()
					total += n
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		// 40 bulk reports, two same-key/other-client reports, and one burst report.
		want := reports + 3
		if total != want {
			t.Fatalf("processed transitions=%d want %d", total, want)
		}
		if n, err := repository.ProcessFieldReports(ctx, "late-worker", 100, fieldreport.Process); err != nil || n != 0 {
			t.Fatalf("reprocessed %d reports err=%v", n, err)
		}
		for id := range accepted {
			status, found, err := repository.FieldReport(ctx, "field-team", id)
			if err != nil || !found || status.Status != fieldreport.StatusProcessed || status.Attempts != 1 || status.ProcessedAt == nil {
				t.Fatalf("report %s: %+v err=%v", id, status, err)
			}
		}
	})

	t.Run("a crash before commit releases the claim", func(t *testing.T) {
		receipt, err := submit("field-team", "crash-key", body)
		if err != nil {
			t.Fatal(err)
		}
		crashed := func(fieldreport.Pending) fieldreport.Outcome { panic("processor crashed") }
		func() {
			defer func() { _ = recover() }()
			_, _ = repository.ProcessFieldReports(ctx, "crashing", 10, crashed)
		}()
		n, err := repository.ProcessFieldReports(ctx, "recovered", 10, fieldreport.Process)
		status, _, _ := repository.FieldReport(ctx, "field-team", receipt.ReportID)
		if err != nil || n != 1 || status.Status != fieldreport.StatusProcessed || status.Attempts != 1 {
			t.Fatalf("after crash n=%d status=%+v err=%v", n, status, err)
		}
	})
	t.Run("an unstorable queued report is rejected without blocking the unit", func(t *testing.T) {
		// Pick three keys on one unit, queued in this order: a report stored
		// before NUL was rejected at intake, a report the processor approves but
		// PostgreSQL cannot store, and a valid report behind both.
		for {
			if n, err := repository.ProcessFieldReports(ctx, "drain", 100, fieldreport.Process); err != nil {
				t.Fatal(err)
			} else if n == 0 {
				break
			}
		}
		keys := sameUnitKeys(t, 3)
		legacy := []byte(strings.Replace(string(body), "Air naik", `Air\u0000naik`, 1))
		ids := []string{}
		for i, key := range keys {
			payload := body
			if i == 0 {
				payload = legacy
			}
			receipt, err := submit("field-team", key, payload)
			if err != nil {
				t.Fatal(err)
			}
			ids = append(ids, receipt.ReportID)
		}
		unstorable := func(p fieldreport.Pending) fieldreport.Outcome {
			outcome := fieldreport.Process(p)
			if p.ReportID == ids[1] {
				outcome.Report.Description = "a\x00b"
			}
			return outcome
		}
		n, err := repository.ProcessFieldReports(ctx, "poison-worker", 10, unstorable)
		if err != nil || n != 3 {
			t.Fatalf("transitions=%d err=%v; one bad row must not roll back the batch", n, err)
		}
		want := []struct {
			status string
			reason string
		}{
			{fieldreport.StatusRejected, "schema_invalid"},
			{fieldreport.StatusRejected, "storage_incompatible: sqlstate 22"},
			{fieldreport.StatusProcessed, ""},
		}
		for i, id := range ids {
			status, found, err := repository.FieldReport(ctx, "field-team", id)
			if err != nil || !found || status.Status != want[i].status || status.Attempts != 1 {
				t.Fatalf("report %d: %+v err=%v", i, status, err)
			}
			if want[i].reason != "" && (status.Reason == nil || !strings.HasPrefix(*status.Reason, want[i].reason)) {
				t.Fatalf("report %d reason=%v want prefix %q", i, status.Reason, want[i].reason)
			}
		}
	})
	repository.Close()
}

func sameUnitKeys(t *testing.T, n int) []string {
	t.Helper()
	first, _ := partition.ReportUnit("field-team", "poison-0")
	keys := []string{"poison-0"}
	for i := 1; len(keys) < n && i < 10000; i++ {
		key := fmt.Sprintf("poison-%d", i)
		if unit, _ := partition.ReportUnit("field-team", key); unit == first {
			keys = append(keys, key)
		}
	}
	if len(keys) < n {
		t.Fatal("could not find keys on one unit")
	}
	return keys
}

func firstKey(m map[string]bool) string {
	for key := range m {
		return key
	}
	return ""
}
