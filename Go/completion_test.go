package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type completionHandler func(context.Context, *Client, Event) error

// Delegate completion-test events to the supplied callback
func (h completionHandler) Handle(ctx context.Context, c *Client, e Event) error { return h(ctx, c, e) }

// Keep the fixture event-driven without periodic work
func (h completionHandler) Poll(context.Context, *Client) error { return nil }

// Close the completion fixture without additional cleanup
func (h completionHandler) Close() error { return nil }

// Create a fixture event requiring an action receipt
func tracked() Event {
	return Event{Event: "keyDown", Context: "binding", Surface: &Surface{RequestID: "run-1"}}
}

// Assert the action receipt matches the expected success state
func result(t *testing.T, c *Client, success bool) {
	t.Helper()
	for len(c.out) > 0 {
		e := <-c.out
		if e.Event != "actionResult" {
			continue
		}
		var p struct {
			RequestID string `json:"request_id"`
			Success   bool   `json:"success"`
		}
		if err := json.Unmarshal(e.Payload, &p); err != nil {
			t.Fatal(err)
		}
		if e.Context != "binding" || p.RequestID != "run-1" || p.Success != success {
			t.Fatalf("unexpected completion: %+v %+v", e, p)
		}
		return
	}
	t.Fatal("missing correlated action result")
}

// Verify tracked actions wait for every deferred operation and preserve failures
func TestTrackedActionsWaitForEveryDeferredOperationAndPreserveFailures(t *testing.T) {
	for _, failure := range []bool{false, true} {
		c := &Client{host: completionTestHost(t), out: make(chan Event, 32)}
		var first, second func(error)
		h := completionHandler(func(ctx context.Context, _ *Client, _ Event) error {
			first, second = DeferAction(ctx), DeferAction(ctx)
			return nil
		})
		handleEvent(context.Background(), c, h, tracked())
		if len(c.out) != 0 {
			t.Fatal("handler returned before deferred work but reported success")
		}
		first(nil)
		first(errors.New("duplicate"))
		if len(c.out) != 0 {
			t.Fatal("did not wait for all work")
		}
		var err error
		if failure {
			err = errors.New("network request failed")
		}
		second(err)
		result(t, c, !failure)
		second(nil)
		if len(c.out) != 0 {
			t.Fatal("duplicate completion")
		}
	}
}

// Verify tracked actions report handler failure and leave legacy inputs unchanged
func TestTrackedActionsReportHandlerFailureAndLeaveLegacyInputsUnchanged(t *testing.T) {
	c := &Client{host: completionTestHost(t), out: make(chan Event, 32)}
	h := completionHandler(func(context.Context, *Client, Event) error { return errors.New("failed") })
	handleEvent(context.Background(), c, h, tracked())
	result(t, c, false)
	h = completionHandler(func(context.Context, *Client, Event) error { return nil })
	handleEvent(context.Background(), c, h, Event{Event: "keyDown", Context: "binding"})
	if len(c.out) != 0 {
		t.Fatal("legacy input unexpectedly acknowledged")
	}
}

// Verify tracked desktop input waits for the host and uses the host outcome
func TestTrackedDesktopInputWaitsForTheHostAndUsesTheHostOutcome(t *testing.T) {
	for _, failure := range []bool{false, true} {
		c := &Client{host: completionTestHost(t), out: make(chan Event, 32)}
		h := completionHandler(func(ctx context.Context, c *Client, e Event) error {
			if e.Event == "keyDown" {
				return c.Send(ctx, Message("desktopInput", e.Context, map[string]any{"request_id": "input-1", "op": "shortcut"}))
			}
			return nil
		})
		handleEvent(context.Background(), c, h, tracked())
		if e := <-c.out; e.Event != "desktopInput" {
			t.Fatal(e)
		}
		if len(c.out) != 0 {
			t.Fatal("queued desktop input counted as success")
		}
		handleEvent(context.Background(), c, h, Message("desktopInputResult", "other", map[string]any{"request_id": "input-1"}))
		if len(c.out) != 0 {
			t.Fatal("unrelated input result counted")
		}
		handleEvent(context.Background(), c, h, Message("desktopInputResult", "binding", map[string]any{"request_id": "input-1", "error": failure}))
		result(t, c, !failure)
	}
}

// Verify tracked desktop input is cancelled when its binding disappears
func TestTrackedDesktopInputIsCancelledWhenItsBindingDisappears(t *testing.T) {
	c := &Client{host: completionTestHost(t), out: make(chan Event, 32)}
	h := completionHandler(func(ctx context.Context, c *Client, e Event) error {
		if e.Event == "keyDown" {
			return c.Send(ctx, Message("desktopInput", e.Context, map[string]any{"request_id": "input-1", "op": "shortcut"}))
		}
		return nil
	})
	handleEvent(context.Background(), c, h, tracked())
	<-c.out
	handleEvent(context.Background(), c, h, Event{Event: "willDisappear", Context: "binding"})
	result(t, c, false)
	if len(c.inputs) != 0 {
		t.Fatal("retired input completion retained")
	}
	handleEvent(context.Background(), c, h, Message("desktopInputResult", "binding", map[string]any{"request_id": "input-1"}))
	if len(c.out) != 0 {
		t.Fatal("late input result produced another completion")
	}
}

// Create host metadata supporting action-completion tests
func completionTestHost(t *testing.T) *HostInfo {
	t.Helper()
	host, err := ParseHostInfo([]byte(`{"streamDockManager":{"contract_version":1,"capabilities":["action-results-v1","desktop-input-v1"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	return &host
}
