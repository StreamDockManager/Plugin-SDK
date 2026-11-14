package sdk

import (
	"context"
	"sync"
	"time"
)

// Scheduler explicitly enables timed polling. Handlers without Scheduler never poll
// A zero deadline suspends idle polling until an event or wake
// Methods run on the handler event loop and must not block
type Scheduler interface {
	NextPoll(time.Time) time.Time
	Wake() <-chan struct{}
}

// AdditiveDial opts in only actions whose rotation operation is additive
type AdditiveDial interface{ BatchDial(action string) bool }

// ProbePool bounds blocking probes to four workers and 32 pending immutable jobs
// Callbacks must deliver immutable results; handlers apply them on their event loop
type probeWork struct {
	ctx context.Context
	run func()
}

type ProbePool struct {
	mu      sync.Mutex
	pending chan probeWork
	done    chan struct{}
	closed  bool
}

// Workers and their queue are allocated only when the first probe is submitted
func NewProbePool() *ProbePool { return &ProbePool{} }

// Submit background work without exceeding pool capacity
func (p *ProbePool) Submit(ctx context.Context, job func()) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed || ctx.Err() != nil {
		return false
	}
	if p.pending == nil {
		p.pending = make(chan probeWork, 32)
		p.done = make(chan struct{})
		for range 4 {
			go p.worker(p.pending, p.done)
		}
	}
	select {
	case p.pending <- probeWork{ctx, job}:
		return true
	default:
		return false
	}
}

// Execute queued probes until shutdown
func (p *ProbePool) worker(pending <-chan probeWork, done <-chan struct{}) {
	for {
		select {
		case <-done:
			return
		case job := <-pending:
			select {
			case <-done:
				return
			default:
			}
			if job.ctx.Err() == nil {
				job.run()
			}
		}
	}
}

// Stop accepting probes and signal worker shutdown
func (p *ProbePool) Close() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return
	}
	p.closed = true
	if p.done == nil {
		return
	}
	close(p.done)
	// Drop closures and their immutable snapshots even if a worker is still exiting
	for {
		select {
		case <-p.pending:
		default:
			return
		}
	}
}
