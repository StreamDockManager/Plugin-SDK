// Package requests shares in-flight JSON reads without coupling consumer cancellation
// It never retries a write. The last departing consumer cancels the underlying read
package requests

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"
)

const Capacity = 32
const MaxResultBytes = 1 << 20

type flight[T any] struct {
	done              chan struct{}
	ctx               context.Context
	cancel            context.CancelFunc
	consumers         int
	finished, retired bool
	data              []byte
	err               error
}
type cooldown struct {
	until time.Time
	err   error
}
type Group[T any] struct {
	mu        sync.Mutex
	flights   map[string]*flight[T]
	cooldowns map[string]cooldown
	active    int
	closed    bool
}

// Share an idempotent read while preserving subscriber cancellation
func (g *Group[T]) Get(ctx context.Context, key string, work func(context.Context) (T, error), retryAfter func(error) time.Duration) (T, error) {
	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	if key == "" || len(key) > 2048 || work == nil {
		return zero, fmt.Errorf("invalid shared read")
	}
	g.mu.Lock()
	if g.closed {
		g.mu.Unlock()
		return zero, context.Canceled
	}
	now := time.Now()
	for id, entry := range g.cooldowns {
		if !now.Before(entry.until) {
			delete(g.cooldowns, id)
		}
	}
	if entry, ok := g.cooldowns[key]; ok {
		g.mu.Unlock()
		return zero, entry.err
	}
	if g.flights == nil {
		g.flights = map[string]*flight[T]{}
	}
	f := g.flights[key]
	if f == nil {
		if g.active+len(g.cooldowns) >= Capacity {
			g.mu.Unlock()
			return zero, fmt.Errorf("shared read capacity exhausted")
		}
		// Cancellation and deadlines belong to the shared operation, not its first caller
		workctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
		f = &flight[T]{done: make(chan struct{}), ctx: workctx, cancel: cancel}
		g.flights[key] = f
		g.active++
		// One bounded worker serves subscribers without inheriting the first caller cancellation
		go func() {
			var data []byte
			var err error
			var delay time.Duration
			func() {
				// Convert worker panics to shared failures while still releasing capacity
				defer func() {
					if recover() != nil {
						err = fmt.Errorf("shared read panicked")
					}
				}()
				var value T
				value, err = work(workctx)
				if err == nil {
					data, err = json.Marshal(value)
					if len(data) > MaxResultBytes {
						data = nil
						err = fmt.Errorf("shared read exceeds 1 MiB")
					}
				}
				if err != nil && retryAfter != nil {
					delay = retryAfter(err)
				}
			}()
			g.mu.Lock()
			f.finished = true
			if f.consumers == 0 {
				g.active--
			}
			if workctx.Err() != nil {
				err = workctx.Err()
			}
			f.data, f.err = data, err
			if g.flights[key] == f && !g.closed && workctx.Err() == nil && err != nil && delay > 0 {
				if g.cooldowns == nil {
					g.cooldowns = map[string]cooldown{}
				}
				g.cooldowns[key] = cooldown{time.Now().Add(delay), err}
			}
			close(f.done)
			g.mu.Unlock()
			cancel()
		}()
	}
	f.consumers++
	g.mu.Unlock()
	// Cancel abandoned work but keep its capacity charged until the worker exits
	defer func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		f.consumers--
		if f.consumers == 0 {
			if g.flights[key] == f {
				delete(g.flights, key)
			}
			f.cancel()
			if f.finished {
				g.active--
			}
		}
	}()
	select {
	case <-ctx.Done():
		return zero, ctx.Err()
	case <-f.done:
	case <-f.ctx.Done():
		select {
		case <-f.done:
		default:
			return zero, f.ctx.Err()
		}
	}
	if err := ctx.Err(); err != nil {
		return zero, err
	}
	g.mu.Lock()
	retired := f.retired || g.closed
	g.mu.Unlock()
	if retired {
		return zero, context.Canceled
	}
	if f.err != nil {
		return zero, f.err
	}
	var value T
	if err := json.Unmarshal(f.data, &value); err != nil {
		return zero, err
	}
	return value, nil
}

// Cancel outstanding shared reads without reopening a closed group
func (g *Group[T]) CancelPending() {
	g.mu.Lock()
	defer g.mu.Unlock()
	for key, f := range g.flights {
		f.retired = true
		f.cancel()
		delete(g.flights, key)
	}
}

// Close the group and cancel its remaining shared reads
func (g *Group[T]) Close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.closed = true
	for key, f := range g.flights {
		f.retired = true
		f.cancel()
		delete(g.flights, key)
	}
	g.cooldowns = nil
}
