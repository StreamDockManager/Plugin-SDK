// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"fmt"
	sdk "github.com/StreamDockManager/Plugin-SDK/Go"
	"os"
	"os/signal"
	"time"
)

type result struct {
	context string
	done    func(error)
	value   string
}
type advanced struct {
	pool    *sdk.ProbePool
	results chan result
	wake    chan struct{}
	cancel  map[string]context.CancelFunc
	ticks   int
}

// Handle example inputs and launch bounded deferred work
func (h *advanced) Handle(ctx context.Context, c *sdk.Client, e sdk.Event) error {
	switch e.Event {
	case "willAppear":
		if c.Supports("telemetry-v1") {
			return c.SubscribeTelemetry(ctx, []string{"cpu"})
		}
		return c.Title(ctx, e.Context, "Telemetry unavailable on this host")
	case "willDisappear":
		if cancel := h.cancel[e.Context]; cancel != nil {
			cancel()
			delete(h.cancel, e.Context)
		}
	case "dialRotate":
		h.ticks += e.Data().Ticks
		return c.Title(ctx, e.Context, fmt.Sprint(h.ticks))
	case "touchTap":
		return c.Title(ctx, e.Context, "Touched")
	case "keyUp":
		work, cancel := context.WithCancel(ctx)
		h.cancel[e.Context] = cancel
		done := sdk.DeferAction(ctx)
		if !h.pool.Submit(work, func() {
			value := "Background result"
			if work.Err() != nil {
				done(work.Err())
				return
			}
			select {
			case h.results <- result{e.Context, done, value}:
				select {
				case h.wake <- struct{}{}:
				default:
				}
			case <-work.Done():
				done(work.Err())
			}
		}) {
			cancel()
			done(fmt.Errorf("probe queue full"))
			return fmt.Errorf("probe queue full")
		}
	}
	return nil
}

// Apply completed example work on the owner loop
func (h *advanced) Poll(ctx context.Context, c *sdk.Client) error {
	for {
		select {
		case r := <-h.results:
			r.done(c.Title(ctx, r.context, r.value))
		default:
			return nil
		}
	}
}

// Keep the example idle until an explicit wake
func (*advanced) NextPoll(time.Time) time.Time { return time.Time{} }

// Expose the example background-work wake channel
func (h *advanced) Wake() <-chan struct{} { return h.wake }

// Allow coalescing of example dial rotations
func (*advanced) BatchDial(string) bool { return true }

// Cancel outstanding example work during shutdown
func (h *advanced) Close() error {
	for _, cancel := range h.cancel {
		cancel()
	}
	h.pool.Close()
	return nil
}

// Launch the advanced plugin example using host arguments
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	h := &advanced{pool: sdk.NewProbePool(), results: make(chan result, 36), wake: make(chan struct{}, 1), cancel: map[string]context.CancelFunc{}}
	if err := sdk.Run(ctx, os.Args[1:], h); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
