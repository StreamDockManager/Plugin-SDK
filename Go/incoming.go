package sdk

import (
	"context"
	"errors"
	"sync"
)

// MaxInputBytes charges encoded envelopes until dispatch (or dial batching)
// releases them. The socket reader may additionally hold one bounded envelope
const MaxInputBytes = 8 * MaxMessageBytes
const MaxInputItems = 128

type incomingBudget struct {
	mu           sync.Mutex
	bytes, items int
	changed      chan struct{}
}

type incomingLease struct {
	budget *incomingBudget
	bytes  int
	once   sync.Once
}

// Wait for both incoming-byte and item capacity
func (b *incomingBudget) acquire(ctx context.Context, size int) (*incomingLease, error) {
	if size < 0 || size > MaxMessageBytes {
		return nil, errors.New("SDK incoming message exceeds limit")
	}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		b.mu.Lock()
		if b.changed == nil {
			b.changed = make(chan struct{})
		}
		if b.items < MaxInputItems && b.bytes+size <= MaxInputBytes {
			b.items++
			b.bytes += size
			b.mu.Unlock()
			return &incomingLease{budget: b, bytes: size}, nil
		}
		changed := b.changed
		b.mu.Unlock()
		select {
		case <-changed:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}

// Release an incoming envelope reservation exactly once
func (lease *incomingLease) release() {
	if lease == nil {
		return
	}
	// Competing dispatch and cancellation paths must release each lease only once
	lease.once.Do(func() {
		b := lease.budget
		b.mu.Lock()
		defer b.mu.Unlock()
		b.bytes -= lease.bytes
		b.items--
		close(b.changed)
		b.changed = make(chan struct{})
	})
}
