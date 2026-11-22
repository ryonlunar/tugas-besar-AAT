package main

import (
	"math"
	"testing"
)

func TestSatelliteLagFromDurableCursor(t *testing.T) {
	for _, tt := range []struct {
		head   int64
		cursor string
		want   float64
	}{{19, "", 20}, {19, "10", 10}, {19, "20", 0}, {19, "21", 0}} {
		if got := satelliteLag(tt.head, tt.cursor); got != tt.want {
			t.Errorf("head=%d cursor=%q: got %v want %v", tt.head, tt.cursor, got, tt.want)
		}
	}
	if !math.IsNaN(satelliteLag(10, "broken")) {
		t.Fatal("invalid cursor must not appear caught up")
	}
}
