package sdk

import (
	"context"
	"testing"
	"time"
)

// Verify probe pool bounds running and pending work
func TestProbePoolBoundsRunningAndPendingWork(t *testing.T) {
	pool := NewProbePool()
	defer pool.Close()
	started := make(chan struct{}, 4)
	release := make(chan struct{})
	defer close(release)
	for range 4 {
		if !pool.Submit(context.Background(), func() { started <- struct{}{}; <-release }) {
			t.Fatal("worker rejected")
		}
	}
	for range 4 {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("four workers did not start")
		}
	}
	for range 32 {
		if !pool.Submit(context.Background(), func() {}) {
			t.Fatal("pending queue too small")
		}
	}
	if pool.Submit(context.Background(), func() {}) {
		t.Fatal("pending queue exceeded 32")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if pool.Submit(cancelled, func() {}) {
		t.Fatal("accepted cancelled work")
	}
	pool.Close()
	if pool.Submit(context.Background(), func() {}) {
		t.Fatal("accepted work after close")
	}
}

// Verify probe pool skips canceled queued work and releases queue on close
func TestProbePoolSkipsCanceledQueuedWorkAndReleasesQueueOnClose(t *testing.T) {
	pool := NewProbePool()
	if pool.pending != nil {
		t.Fatal("idle pool allocated a worker queue")
	}
	release := make(chan struct{})
	started := make(chan struct{}, 4)
	for range 4 {
		if !pool.Submit(context.Background(), func() { started <- struct{}{}; <-release }) {
			t.Fatal("submit")
		}
	}
	for range 4 {
		<-started
	}
	ctx, cancel := context.WithCancel(context.Background())
	ran := make(chan struct{}, 1)
	pool.Submit(ctx, func() { ran <- struct{}{} })
	cancel()
	drained := make(chan struct{})
	pool.Submit(context.Background(), func() { close(drained) })
	close(release)
	select {
	case <-drained:
	case <-time.After(time.Second):
		t.Fatal("queue did not drain")
	}
	select {
	case <-ran:
		t.Fatal("canceled probe executed")
	default:
	}
	pool.Close()
	if len(pool.pending) != 0 {
		t.Fatal("shutdown retained queued snapshots")
	}
}
