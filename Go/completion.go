package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"
)

type pendingInput struct {
	context  string
	complete func(error)
}

// Cancel all pending inputs belonging to a binding context
func (c *Client) cancelInputs(context string) {
	c.inputMu.Lock()
	callbacks := []func(error){}
	for id, input := range c.inputs {
		if context == "" || input.context == context {
			delete(c.inputs, id)
			callbacks = append(callbacks, input.complete)
		}
	}
	c.inputMu.Unlock()
	for _, complete := range callbacks {
		complete(errors.New("desktop input cancelled"))
	}
}

// Remove and return a matching input completion callback
func (c *Client) takeInput(id, context string) func(error) {
	c.inputMu.Lock()
	defer c.inputMu.Unlock()
	pending, ok := c.inputs[id]
	if !ok || pending.context != context {
		return nil
	}
	delete(c.inputs, id)
	return pending.complete
}

// Register a host input receipt and return its completion function
func (c *Client) trackInput(ctx context.Context, e Event) (func(error), error) {
	noop := func(error) {}
	if e.Event != "desktopInput" || ctx.Value(completionKey{}) == nil {
		return noop, nil
	}
	var payload struct {
		RequestID string `json:"request_id"`
	}
	if json.Unmarshal(e.Payload, &payload) != nil || payload.RequestID == "" {
		return noop, errors.New("desktop input requires a request ID")
	}
	c.inputMu.Lock()
	defer c.inputMu.Unlock()
	if len(c.inputs) >= 128 {
		return noop, errors.New("desktop input completion queue full")
	}
	if c.inputs == nil {
		c.inputs = map[string]pendingInput{}
	}
	if _, exists := c.inputs[payload.RequestID]; exists {
		return noop, errors.New("duplicate desktop input request ID")
	}
	c.inputs[payload.RequestID] = pendingInput{context: e.Context, complete: DeferAction(ctx)}
	return func(err error) {
		if done := c.takeInput(payload.RequestID, e.Context); done != nil {
			done(err)
		}
	}, nil
}

type completionKey struct{}
type actionCompletion struct {
	mu                     sync.Mutex
	pending                int
	returned, failed, sent bool
	send                   func(bool)
}

// DeferAction ties asynchronous work to the current tracked input. Call the
// returned function exactly when that work finishes. The SDK reports success
// only after the handler and every deferred operation have succeeded
func DeferAction(ctx context.Context) func(error) {
	completion, _ := ctx.Value(completionKey{}).(*actionCompletion)
	if completion == nil {
		return func(error) {}
	}
	completion.mu.Lock()
	completion.pending++
	completion.mu.Unlock()
	var once sync.Once
	return func(err error) { once.Do(func() { completion.finish(err, false) }) }
}

// Settle an action once both handler and deferred work permit it
func (a *actionCompletion) finish(err error, returned bool) {
	a.mu.Lock()
	a.failed = a.failed || err != nil
	if returned {
		a.returned = true
	} else {
		a.pending--
	}
	send := a.returned && a.pending == 0 && !a.sent
	success := !a.failed
	if send {
		a.sent = true
	}
	a.mu.Unlock()
	if send {
		a.send(success)
	}
}

// Dispatch an event with cancellation and completion bookkeeping
func handleEvent(ctx context.Context, c *Client, h Handler, e Event) {
	if e.Event == "didReceiveLogSettings" && c.Supports("plugin-logs-v1") {
		var preferences struct {
			DebugNetwork bool `json:"debug_network"`
		}
		if json.Unmarshal(e.Payload, &preferences) == nil {
			c.SetNetworkDebug(preferences.DebugNetwork)
		}
		return
	}
	if e.Event == "willDisappear" {
		c.cancelInputs(e.Context)
	}
	var inputDone func(error)
	var inputResult struct {
		RequestID string `json:"request_id"`
		Error     bool   `json:"error"`
	}
	if e.Event == "desktopInputResult" && json.Unmarshal(e.Payload, &inputResult) == nil {
		inputDone = c.takeInput(inputResult.RequestID, e.Context)
	}
	var completion *actionCompletion
	tracked := e.Surface != nil && e.Surface.RequestID != "" && (c.Supports("action-results-v1"))
	action := ShouldLogAction(e)
	if logging, ok := h.(ActionLogging); ok {
		action = logging.ShouldLogAction(e)
	}
	if tracked || action && c.Supports("plugin-logs-v1") {
		started := time.Now()
		// Send the final receipt and action log only after every deferred completion settles
		completion = &actionCompletion{send: func(success bool) {
			if tracked {
				_ = c.Send(ctx, Message("actionResult", e.Context, map[string]any{"request_id": e.Surface.RequestID, "success": success}))
			}
			if action && c.Supports("plugin-logs-v1") {
				stream, outcome := Stdout, "completed"
				if !success {
					stream, outcome = Stderr, "failed or cancelled"
				}
				_ = c.LogStream(context.WithoutCancel(ctx), stream, fmt.Sprintf("Action %s %s in %d ms", e.Action, outcome, time.Since(started).Milliseconds()))
			}
		}}
		ctx = context.WithValue(ctx, completionKey{}, completion)
	}
	err := h.Handle(ctx, c, e)
	if err == nil && c.Supports("plugin-logs-v1") && (e.Event == "didReceiveSettings" || e.Event == "didReceiveGlobalSettings") {
		_ = c.LogStream(context.WithoutCancel(ctx), Stdout, "Settings updated")
	}
	if inputDone != nil {
		result := err
		if inputResult.Error {
			result = errors.Join(result, errors.New("desktop input failed"))
		}
		inputDone(result)
	}
	if err != nil {
		_ = c.LogStream(context.WithoutCancel(ctx), Stderr, "Action failure: "+err.Error())
		_ = c.Title(ctx, e.Context, "Unavailable")
		_ = c.Send(ctx, Message("showAlert", e.Context, map[string]any{}))
	}
	if completion != nil {
		completion.finish(err, true)
	}
}

// Dispatch runs one event through the same completion/error machinery as Run
// It belongs to the caller's serialized loop; testkit uses it for offline replay
func (c *Client) Dispatch(ctx context.Context, handler Handler, event Event) {
	handleEvent(ctx, c, handler, event)
}
