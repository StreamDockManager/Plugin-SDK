// Package testkit runs real SDK handlers without sockets or a Manager checkout
package testkit

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/StreamDockManager/Plugin-SDK/Go"
	"reflect"
	"sync"
	"time"
)

type Clock struct {
	mu  sync.Mutex
	now time.Time
}

// Create a controllable clock at the supplied instant
func NewClock(at time.Time) *Clock { return &Clock{now: at} }

// Read the current simulated time
func (c *Clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.now }

// Advance the simulated clock atomically
func (c *Clock) Advance(by time.Duration) {
	if by < 0 {
		panic("clock cannot go backwards")
	}
	c.mu.Lock()
	c.now = c.now.Add(by)
	c.mu.Unlock()
}

type Recorder struct {
	Client  *sdk.Client
	events  <-chan sdk.Event
	History []sdk.Event
}

// Create an in-process host event recorder
func New(info sdk.HostInfo) *Recorder {
	client, events := sdk.NewLocalClient(info, "test-token")
	return &Recorder{Client: client, events: events}
}

// Drain the currently captured outbound events
func (r *Recorder) Drain() []sdk.Event {
	out := []sdk.Event{}
	for {
		select {
		case event := <-r.events:
			out = append(out, event)
			r.History = append(r.History, event)
		default:
			return out
		}
	}
}

// Replay input events through a plugin handler
func (r *Recorder) Replay(ctx context.Context, handler sdk.Handler, events []sdk.Event) error {
	for _, event := range events {
		if err := ctx.Err(); err != nil {
			return err
		}
		r.Client.Dispatch(ctx, handler, event)
	}
	return nil
}

// Invoke one handler poll using the recorder client
func (r *Recorder) Poll(ctx context.Context, handler sdk.Handler) error {
	return handler.Poll(ctx, r.Client)
}

// Compare captured events with the expected sequence
func (r *Recorder) Expect(expected []sdk.Event) error {
	actual := r.Drain()
	normalize := func(value any) any {
		data, _ := json.Marshal(value)
		var out any
		_ = json.Unmarshal(data, &out)
		return out
	}
	if !reflect.DeepEqual(normalize(actual), normalize(expected)) {
		return fmt.Errorf("recorded events differ: got %v; want %v", normalize(actual), normalize(expected))
	}
	return nil
}

// Reject outbound events addressed outside the allowed contexts
func (r *Recorder) AssertContexts(allowed ...string) error {
	for _, event := range r.History {
		ok := false
		for _, context := range allowed {
			if event.Context == context {
				ok = true
			}
		}
		if !ok {
			return fmt.Errorf("event escaped allowed contexts: %s", event.Event)
		}
	}
	return nil
}
