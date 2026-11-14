package polling

import (
	"encoding/json"
	"fmt"
	"math"
	"time"
)

const UpdateIntervalKey = "update_interval_ms"

// RefreshPolicy describes periodic reads only, never command execution
type RefreshPolicy struct {
	DefaultMS int64 `json:"default_ms"`
	MinimumMS int64 `json:"minimum_ms"`
	MaximumMS int64 `json:"maximum_ms"`
}

// Resolve a refresh interval from validated settings
func (p RefreshPolicy) Resolve(settings map[string]any) (time.Duration, error) {
	if p.MinimumMS < 1 || p.MaximumMS < p.MinimumMS || p.MaximumMS > 86400000 || (p.DefaultMS != 0 && (p.DefaultMS < p.MinimumMS || p.DefaultMS > p.MaximumMS)) {
		return 0, fmt.Errorf("invalid refresh policy")
	}
	value := p.DefaultMS
	if raw, exists := settings[UpdateIntervalKey]; exists {
		encoded, err := json.Marshal(raw)
		var number float64
		if err != nil || json.Unmarshal(encoded, &number) != nil || string(encoded) == "null" || math.IsNaN(number) || math.IsInf(number, 0) || number != math.Trunc(number) || number < 0 || number > 86400000 {
			return 0, fmt.Errorf("update interval must be an integer number of milliseconds")
		}
		value = int64(number)
	}
	if value != 0 && (value < p.MinimumMS || value > p.MaximumMS) {
		return 0, fmt.Errorf("update interval is outside the supported range")
	}
	return time.Duration(value) * time.Millisecond, nil
}

// Interval preserves a caller's existing default (including adaptive and legacy
// intervals) unless the binding explicitly overrides it. Invalid saved values
// retain that default; editors use Resolve to report errors before saving
func Interval(settings map[string]any, fallback time.Duration) time.Duration {
	policy := RefreshPolicy{DefaultMS: fallback.Milliseconds(), MinimumMS: 1, MaximumMS: 86400000}
	interval, err := policy.Resolve(settings)
	if err != nil {
		return fallback
	}
	return interval
}

// Never is a disabled periodic deadline, distinct from a zero/initial deadline
var Never = time.Date(9999, 1, 1, 0, 0, 0, 0, time.UTC)

// Calculate the next deadline for an enabled interval
func Next(now time.Time, interval time.Duration) time.Time {
	if interval == 0 {
		return Never
	}
	return now.Add(interval)
}
