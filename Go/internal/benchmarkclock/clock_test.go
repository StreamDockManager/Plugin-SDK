package benchmarkclock

import (
	"errors"
	"math"
	"testing"
)

// Verify sub millisecond interval at large counter value
func TestSubMillisecondIntervalAtLargeCounterValue(t *testing.T) {
	// Absolute ticks above float64's exact integer range must not erase a
	// single-tick interval. Windows QPC commonly uses 10 million ticks/second
	const start int64 = 1 << 60
	clock := &Clock{frequency: 10_000_000, read: func() (int64, error) { return start + 1, nil }}
	elapsed, err := clock.ElapsedMS(start)
	if err != nil || elapsed != 0.0001 {
		t.Fatalf("lost measured interval: elapsed=%v err=%v", elapsed, err)
	}
	throughput := float64(9) * 1000 / elapsed
	if throughput != 90_000_000 || math.IsInf(throughput, 0) || math.IsNaN(throughput) {
		t.Fatalf("invalid throughput: %v", throughput)
	}
}

// Verify zero sample cannot become total interval
func TestZeroSampleCannotBecomeTotalInterval(t *testing.T) {
	clock := &Clock{frequency: 10_000_000, read: func() (int64, error) { return 42, nil }}
	if sample, err := clock.SinceMS(42); err != nil || sample != 0 {
		t.Fatalf("changed zero sample: sample=%v err=%v", sample, err)
	}
	if _, err := clock.ElapsedMS(42); err == nil {
		t.Fatal("accepted unmeasurable total interval")
	}
}

// Verify invalid counter intervals
func TestInvalidCounterIntervals(t *testing.T) {
	for _, test := range []struct{ frequency, start, end int64 }{
		{0, 0, 1}, {-1, 0, 1}, {10_000_000, -1, 1}, {10_000_000, 2, 1},
	} {
		clock := &Clock{frequency: test.frequency}
		if _, err := clock.BetweenMS(test.start, test.end); err == nil {
			t.Fatalf("accepted invalid interval: %+v", test)
		}
	}
	clock := &Clock{frequency: 10_000_000}
	if elapsed, err := clock.BetweenMS(0, math.MaxInt64); err != nil || elapsed <= 0 || math.IsInf(elapsed, 0) || math.IsNaN(elapsed) {
		t.Fatalf("overflowed long interval: elapsed=%v err=%v", elapsed, err)
	}
}

// Verify counter read failure
func TestCounterReadFailure(t *testing.T) {
	want := errors.New("counter unavailable")
	clock := &Clock{frequency: 10_000_000, read: func() (int64, error) { return 0, want }}
	if _, err := clock.ElapsedMS(0); !errors.Is(err, want) {
		t.Fatalf("lost counter error: %v", err)
	}
	clock.read = func() (int64, error) { return -1, nil }
	if _, err := clock.Now(); err == nil {
		t.Fatal("accepted negative counter")
	}
}
