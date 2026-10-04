package outbox

import (
	"testing"
	"time"
)

func TestRetryDelaySaturatesBeforeDurationOverflow(t *testing.T) {
	for _, test := range []struct {
		attempt int
		want    time.Duration
	}{
		{0, 900 * time.Millisecond},
		{1, 900 * time.Millisecond},
		{2, 2 * time.Second},
		{3, 4400 * time.Millisecond},
		{4, 9600 * time.Millisecond},
		{5, 12800 * time.Millisecond},
		{6, 27 * time.Second},
		{10, 24 * time.Second},
		{34, 30 * time.Second},
		{35, 24 * time.Second},
		{40, 24 * time.Second},
		{100, 24 * time.Second},
		{1025, 24 * time.Second},
	} {
		got := retryDelay(test.attempt, time.Second, 30*time.Second)
		if difference := got - test.want; difference < -time.Nanosecond || difference > time.Nanosecond {
			t.Errorf("attempt %d: got %s, want %s", test.attempt, got, test.want)
		}
	}
	if got := retryDelay(int(^uint(0)>>1), time.Second, 30*time.Second); got < 24*time.Second || got > 30*time.Second {
		t.Fatalf("largest attempt count reset the delay: %s", got)
	}
}

func TestRetryDelayCapsJitterBeforeDurationConversion(t *testing.T) {
	const max = time.Duration(1<<63 - 1)
	min := max / 2
	// Attempt 4 applies a 1.2 multiplier to the capped base, which used to
	// overflow during conversion and fall back to min/2.
	if got := retryDelay(4, min, max); got != max {
		t.Fatalf("large duration overflowed: got %s, want %s", got, max)
	}
	if got := retryDelay(5, min, max); got < min || got > max {
		t.Fatalf("sub-unit jitter escaped its bounds: %s", got)
	}
}
