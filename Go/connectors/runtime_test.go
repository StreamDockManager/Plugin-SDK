package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	sdk "github.com/StreamDockManager/Plugin-SDK/Go"
	engine "github.com/StreamDockManager/Plugin-SDK/Go/authoring"
	"github.com/StreamDockManager/Plugin-SDK/Go/testkit"
	testutil "github.com/StreamDockManager/Plugin-SDK/Go/testkit/peers"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type fixtureDriver struct {
	mu                        sync.Mutex
	reads, closed, discovered int
	commands                  []int
	failure                   bool
	gate                      chan struct{}
	entered                   chan string
	active, peak              int
}

// Choose the fake connector resource from its player setting
func (*fixtureDriver) Key(s map[string]any) string { return String(s, "player", "one") }

// Read the fake backend value or injected read failure
func (d *fixtureDriver) Read(ctx context.Context, s map[string]any) (any, error) {
	d.mu.Lock()
	d.reads++
	d.active++
	d.peak = max(d.active, d.peak)
	gate, failure := d.gate, d.failure
	d.mu.Unlock()
	defer func() { d.mu.Lock(); d.active--; d.mu.Unlock() }()
	if d.entered != nil {
		d.entered <- String(s, "player", "one")
	}
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if failure {
		return nil, fmt.Errorf("fixture unavailable")
	}
	return String(s, "player", "one"), nil
}

// Record the fake connector command and its tick count
func (d *fixtureDriver) Command(ctx context.Context, _ string, _ map[string]any, ticks int) (Outcome, error) {
	d.mu.Lock()
	d.commands = append(d.commands, ticks)
	d.active++
	d.peak = max(d.active, d.peak)
	gate := d.gate
	d.mu.Unlock()
	defer func() { d.mu.Lock(); d.active--; d.mu.Unlock() }()
	if gate != nil {
		select {
		case <-gate:
		case <-ctx.Done():
			return Outcome{}, ctx.Err()
		}
	}
	return Outcome{Message: "Delivered"}, nil
}

// Return the fake connector discovery choices
func (d *fixtureDriver) Discover(context.Context, map[string]any) ([]Choice, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.discovered++
	return nil, nil
}

// Convert the fixture resource value to display text
func (*fixtureDriver) Display(value any, _ string, _ map[string]any) string { return value.(string) }

// Perform the fixture shutdown behavior expected by the test
func (d *fixtureDriver) Close() { d.mu.Lock(); d.closed++; d.mu.Unlock() }

// Create a connector engine with a recorder and controlled clock
func runtimeFixture(t *testing.T, driver *fixtureDriver) (*engine.Engine, *testkit.Recorder, *testkit.Clock) {
	t.Helper()
	def := engine.Definition{Slug: "fixture", Actions: []string{"status", "volume"}}
	visuals := map[string]engine.Visual{}
	e := engine.New(def, filepath.Join(t.TempDir(), "state.json"), Factory(Options{Definition: def, Visuals: visuals, Interval: time.Minute, Dial: true, New: func(chan<- struct{}) Driver { return driver }}))
	clock := testkit.NewClock(time.Now())
	e.Now = clock.Now
	e.Feature.Render = func(context.Context, *sdk.Client, string) error { return nil }
	t.Cleanup(func() { _ = e.Close() })
	return e, testkit.New(sdk.HostInfo{}), clock
}

// Process pending engine results through the test recorder
func drain(t *testing.T, e *engine.Engine, r *testkit.Recorder) {
	t.Helper()
	testutil.Eventually(t, func() bool {
		if err := e.DrainJobs(context.Background(), r.Client); err != nil {
			t.Fatal(err)
		}
		return len(e.Jobs) == 0
	})
}

// Verify shared sample stale retention backoff and read only inspector
func TestSharedSampleStaleRetentionBackoffAndReadOnlyInspector(t *testing.T) {
	d := &fixtureDriver{}
	e, r, clock := runtimeFixture(t, d)
	ctx := context.Background()
	for _, id := range []string{"a", "b"} {
		e.Bindings[id] = engine.Binding{Action: "app.streamdockmanager.plugins.fixture.status", Settings: map[string]any{"player": "one"}}
		e.Feature.DemandChanged()
	}
	if err := e.Poll(ctx, r.Client); err != nil {
		t.Fatal(err)
	}
	drain(t, e, r)
	d.mu.Lock()
	reads := d.reads
	d.failure = true
	d.mu.Unlock()
	if reads != 1 {
		t.Fatal("same resource fetched per binding", reads)
	}
	clock.Advance(time.Minute)
	_ = e.Poll(ctx, r.Client)
	drain(t, e, r)
	status := e.Feature.Status(ctx, "b", e.Bindings["b"])
	if status.Text != "Stale\none" {
		t.Fatal("last successful sample lost", status)
	}
	if next := e.NextPoll(clock.Now()); !next.After(clock.Now()) {
		t.Fatal("failed reads spin without backoff", next)
	}
	for _, op := range []string{"test", "discover"} {
		payload := []byte(`{"op":"` + op + `","request_id":"picker-1","settings":{"player":"one"}}`)
		if err := e.Handle(ctx, r.Client, sdk.Event{Event: "sendToPlugin", Context: "inspector", Payload: payload}); err != nil {
			t.Fatal(err)
		}
		drain(t, e, r)
		found := false
		for _, event := range r.Drain() {
			if event.Event == "sendToPropertyInspector" {
				var payload map[string]any
				if err := json.Unmarshal(event.Payload, &payload); err != nil {
					t.Fatal(err)
				}
				if payload["request_id"] == "picker-1" {
					found = true
				}
			}
		}
		if !found {
			t.Fatal("missing discovery request correlation")
		}
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.commands) != 0 || d.discovered != 1 {
		t.Fatal("read-only inspector mutated service", d.commands, d.discovered)
	}
}

// Verify settings cancel obsolete samples and last unbind closes demand
func TestSettingsCancelObsoleteSamplesAndLastUnbindClosesDemand(t *testing.T) {
	d := &fixtureDriver{gate: make(chan struct{}), entered: make(chan string, 8)}
	e, r, _ := runtimeFixture(t, d)
	ctx := context.Background()
	action := "app.streamdockmanager.plugins.fixture.status"
	e.Bindings["a"] = engine.Binding{Action: action, Settings: map[string]any{"player": "old"}}
	e.Feature.DemandChanged()
	_ = e.Poll(ctx, r.Client)
	select {
	case <-d.entered:
	case <-time.After(time.Second):
		t.Fatal("sample not started")
	}
	if err := e.Handle(ctx, r.Client, sdk.Event{Event: "didReceiveSettings", Context: "a", Action: action, Payload: []byte(`{"settings":{"player":"new"}}`)}); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	d.gate = nil
	d.mu.Unlock()
	_ = e.Poll(ctx, r.Client)
	drain(t, e, r)
	if status := e.Feature.Status(ctx, "a", e.Bindings["a"]); status.Text != "new" || strings.Contains(status.Text, "old") {
		t.Fatal("obsolete result displayed", status)
	}
	if err := e.Handle(ctx, r.Client, sdk.Event{Event: "willDisappear", Context: "a", Action: action}); err != nil {
		t.Fatal(err)
	}
	if !e.NextPoll(time.Now()).IsZero() {
		t.Fatal("no-demand runtime keeps polling")
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.closed == 0 || d.peak != 1 {
		t.Fatal("connection lifecycle or serialization", d.closed, d.peak)
	}
}

// Verify queued dial commands are serialized and not replayed
func TestQueuedDialCommandsAreSerializedAndNotReplayed(t *testing.T) {
	gate := make(chan struct{})
	d := &fixtureDriver{gate: gate}
	e, r, _ := runtimeFixture(t, d)
	ctx := context.Background()
	action := "app.streamdockmanager.plugins.fixture.volume"
	e.Bindings["dial"] = engine.Binding{Action: action}
	e.Feature.DemandChanged()
	for _, ticks := range []int{1, 2, -1} {
		payload := fmt.Appendf(nil, `{"ticks":%d}`, ticks)
		if err := e.Handle(ctx, r.Client, sdk.Event{Event: "dialRotate", Context: "dial", Action: action, Payload: payload}); err != nil {
			t.Fatal(err)
		}
	}
	close(gate)
	for range 3 {
		drain(t, e, r)
		_ = e.Poll(ctx, r.Client)
	}
	drain(t, e, r)
	d.mu.Lock()
	defer d.mu.Unlock()
	if fmt.Sprint(d.commands) != "[1 2 -1]" || d.peak != 1 {
		t.Fatal(d.commands, d.peak)
	}
}
