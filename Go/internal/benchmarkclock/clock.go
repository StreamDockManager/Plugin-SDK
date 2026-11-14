// Package benchmarkclock measures benchmark intervals without changing runtime
// timers or the system timer resolution
package benchmarkclock

import (
	"errors"
	"fmt"
)

// Clock keeps raw monotonic counter ticks until an interval is measured
type Clock struct {
	frequency int64
	read      func() (int64, error)
}

// Create the platform benchmark clock
func New() (*Clock, error) {
	clock, err := platformClock()
	if err != nil {
		return nil, fmt.Errorf("benchmark clock: %w", err)
	}
	if clock.frequency <= 0 {
		return nil, errors.New("benchmark clock: invalid counter frequency")
	}
	return clock, nil
}

// Read the benchmark clock counter
func (c *Clock) Now() (int64, error) {
	ticks, err := c.read()
	if err != nil {
		return 0, fmt.Errorf("benchmark clock: %w", err)
	}
	if ticks < 0 {
		return 0, errors.New("benchmark clock: negative counter value")
	}
	return ticks, nil
}

// Convert two ordered counter values to milliseconds
func (c *Clock) BetweenMS(start, end int64) (float64, error) {
	if c.frequency <= 0 || start < 0 || end < start {
		return 0, errors.New("benchmark clock: invalid frequency or backward interval")
	}
	// Subtract integers first: converting large absolute counter values to
	// float64 would lose short intervals. Convert before multiplication to
	// avoid overflowing an integer tick-to-millisecond conversion
	return float64(end-start) * 1000 / float64(c.frequency), nil
}

// SinceMS allows a zero sample when both readings fall on the same counter tick
func (c *Clock) SinceMS(start int64) (float64, error) {
	end, err := c.Now()
	if err != nil {
		return 0, err
	}
	return c.BetweenMS(start, end)
}

// ElapsedMS requires a measurable total interval. A zero total cannot support
// a throughput measurement; report the failure instead of inventing a duration
func (c *Clock) ElapsedMS(start int64) (float64, error) {
	elapsed, err := c.SinceMS(start)
	if err != nil {
		return 0, err
	}
	if elapsed <= 0 {
		return 0, errors.New("benchmark clock: total interval did not advance")
	}
	return elapsed, nil
}
