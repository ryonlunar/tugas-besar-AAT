package store

import (
	"database/sql"
	"testing"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/prometheus/client_golang/prometheus"
)

func TestMetricsOmitStateOnStorageFailure(t *testing.T) {
	db, err := sql.Open("pgx", "postgres://unused:unused@127.0.0.1:1/unused")
	if err != nil {
		t.Fatal(err)
	}
	// A closed pool fails immediately without contacting a database.
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	registry := prometheus.NewRegistry()
	registry.MustRegister(New(db, time.Second).MetricsCollector())
	families, err := registry.Gather()
	if err != nil {
		t.Fatal(err)
	}
	if len(families) != 1 || families[0].GetName() != "aat_store_collection_success" || families[0].Metric[0].Gauge.GetValue() != 0 {
		t.Fatal("failed storage collection reported queue or source state")
	}
}
