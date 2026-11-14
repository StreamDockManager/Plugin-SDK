// Package payload shares process-local payload accounting without importing HTTP
package payload

import "sync/atomic"

var Sent, Received atomic.Uint64

// Read cumulative sent and received payload counters atomically
func Snapshot() (uint64, uint64) { return Sent.Load(), Received.Load() }
