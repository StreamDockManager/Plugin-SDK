package sdk

import (
	"math"
	"testing"
	"time"
)

// Verify measurement freshness rejects expired future and unavailable values
func TestMeasurementFreshnessRejectsExpiredFutureAndUnavailableValues(t *testing.T) {
	now := time.UnixMilli(1700000000000)
	value := 42.0
	m := Measurement{Value: &value, ObservedAt: uint64(now.UnixMilli()), MaxAgeMS: 2500}
	if !m.Fresh(now) || !m.Fresh(now.Add(2500*time.Millisecond)) || m.Fresh(now.Add(2501*time.Millisecond)) || m.Fresh(now.Add(-time.Millisecond)) {
		t.Fatal("incorrect freshness boundary")
	}
	m.ObservedAt, m.MaxAgeMS = math.MaxUint64, math.MaxUint64
	if m.Fresh(now) {
		t.Fatal("overflowing future timestamp appeared fresh")
	}
	m.ObservedAt = uint64(now.UnixMilli())
	errorText := "Unavailable"
	m.Error = &errorText
	if m.Fresh(now) {
		t.Fatal("failed measurement appeared fresh")
	}
	m.Error = nil
	for _, invalid := range []float64{math.NaN(), math.Inf(1), math.Inf(-1)} {
		m.Value = &invalid
		if m.Fresh(now) {
			t.Fatal("nonfinite measurement appeared fresh")
		}
	}
	m.Value = nil
	if m.Fresh(now) {
		t.Fatal("missing measurement appeared fresh")
	}
}
