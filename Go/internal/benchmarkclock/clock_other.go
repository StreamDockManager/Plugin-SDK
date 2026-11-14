//go:build !windows

package benchmarkclock

import "time"

// Create a monotonic benchmark clock for this platform
func platformClock() (*Clock, error) {
	origin := time.Now()
	return &Clock{frequency: int64(time.Second), read: func() (int64, error) {
		return time.Since(origin).Nanoseconds(), nil
	}}, nil
}
