package main

import (
	"context"
	"math"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/satellite"
	"github.com/ryonlunar/tugas-besar-AAT/services/aggregator/internal/sourcejournal"
)

type satelliteMetrics struct {
	lag, pending prometheus.Gauge
	storage      prometheus.Histogram
}

func newSatelliteMetrics(reg prometheus.Registerer) *satelliteMetrics {
	m := &satelliteMetrics{
		lag: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "aat_satellite_feed_lag_offsets",
			Help: "Offsets between the durable intake cursor and the sampled feed head.",
		}),
		pending: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "aat_satellite_unfinished_batches",
			Help: "Durable satellite batches awaiting canonical processing, including parked batches.",
		}),
		storage: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "aat_satellite_intake_to_storage_seconds",
			Help:    "Time from first input in a satellite batch to canonical commit for batches producing mutations.",
			Buckets: []float64{.1, .25, .5, 1, 2, 3, 5, 8, 15, 30, 45, 60, 90, 120, 300},
		}),
	}
	m.lag.Set(math.NaN())
	m.pending.Set(math.NaN())
	reg.MustRegister(m.lag, m.pending, m.storage)
	return m
}

func (m *satelliteMetrics) observeStored(elapsed time.Duration) {
	m.storage.Observe(elapsed.Seconds())
}

func (m *satelliteMetrics) sample(ctx context.Context, interval time.Duration, client *satellite.Client, journal *sourcejournal.Session, observe func(string, string, error, time.Duration)) {
	defer m.lag.Set(math.NaN())
	defer m.pending.Set(math.NaN())
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for ctx.Err() == nil {
		started := time.Now()
		sampleCtx, cancel := context.WithTimeout(ctx, min(interval, 5*time.Second))
		head, err := client.Session(sampleCtx)
		cancel()
		observe("satellite_session", "SATELIT", err, time.Since(started))
		if err != nil {
			m.lag.Set(math.NaN())
		} else {
			m.lag.Set(satelliteLag(head.LatestOffset, journal.Cursor()))
		}
		batches, err := journal.Unfinished(ctx)
		if err != nil {
			m.pending.Set(math.NaN())
		} else {
			m.pending.Set(float64(batches))
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func satelliteLag(head int64, cursor string) float64 {
	if cursor == "" {
		cursor = "0"
	}
	next, err := strconv.ParseInt(cursor, 10, 64)
	if err != nil || next < 0 || head < 0 {
		return math.NaN()
	}
	return max(0, float64(head)-float64(next)+1)
}
