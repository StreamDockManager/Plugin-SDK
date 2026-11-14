// Package discovery coordinates read-only inspector work on an owner event loop
// Start uses a bounded executor supplied by the caller; Complete and Expire run
// on the loop. Cancellation never affects a separately scheduled command
package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"sort"
	"unicode/utf8"
)

const MaxPending = 32
const MaxResponseBytes = 256 << 10
const MaxTime int64 = 9007199254740991

type Request struct {
	Context, Action, Channel, ID, Operation, Scope string
}
type Ticket struct {
	Request  Request
	Deadline int64
	Context  context.Context
	cancel   context.CancelFunc
}
type Reply struct {
	Context, Action string
	Payload         map[string]any
}
type Runner struct {
	pending map[[2]string]*Ticket
	closed  bool
}

// Validate a bounded UTF-8 discovery field
func field(s string, max int, empty bool) bool {
	return (empty || s != "") && len(s) <= max && utf8.ValidString(s)
}

// Validate the request identity and correlation fields
func validate(r Request) error {
	if !field(r.Context, 1024, false) || !field(r.Action, 1024, true) || !field(r.Channel, 128, false) || !field(r.ID, 128, false) || !field(r.Operation, 128, false) || (r.Scope != "action" && r.Scope != "plugin" && r.Scope != "") {
		return fmt.Errorf("invalid discovery request")
	}
	return nil
}

// Admit or replace a discovery request within its deadline
func (r *Runner) Start(ctx context.Context, request Request, now, timeout int64) (*Ticket, error) {
	if err := validate(request); err != nil {
		return nil, err
	}
	if r.closed || now < 0 || now > MaxTime || timeout < 1 || timeout > 120000 || now > MaxTime-timeout {
		return nil, fmt.Errorf("invalid discovery deadline or closed runner")
	}
	key := [2]string{request.Context, request.Channel}
	if r.pending == nil {
		r.pending = map[[2]string]*Ticket{}
	}
	if r.pending[key] == nil && len(r.pending) >= MaxPending {
		return nil, fmt.Errorf("discovery queue full")
	}
	if old := r.pending[key]; old != nil {
		old.cancel()
	}
	if request.Scope == "" {
		request.Scope = "action"
	}
	job, cancel := context.WithCancel(ctx)
	ticket := &Ticket{request, now + timeout, job, cancel}
	r.pending[key] = ticket
	return ticket, nil
}

// Cancel only the matching context, channel and request ID
func (r *Runner) Cancel(context, channel, id string) bool {
	key := [2]string{context, channel}
	t := r.pending[key]
	if t == nil || t.Request.ID != id {
		return false
	}
	t.cancel()
	delete(r.pending, key)
	return true
}

// Cancel all discovery tickets owned by one context
func (r *Runner) Clear(context string) {
	for key, t := range r.pending {
		if key[0] == context {
			t.cancel()
			delete(r.pending, key)
		}
	}
}

// Cancel all pending discovery tickets
func (r *Runner) Close() {
	for _, t := range r.pending {
		t.cancel()
	}
	clear(r.pending)
	r.closed = true
}

// Return the earliest pending discovery deadline
func (r *Runner) NextDeadline() int64 {
	var next int64
	for _, t := range r.pending {
		if next == 0 || t.Deadline < next {
			next = t.Deadline
		}
	}
	return next
}

// Envelope overwrites correlation fields and rejects oversized/non-JSON payloads
// Errors contain fixed public messages; raw worker errors are never exposed
func Envelope(request Request, payload map[string]any, failure string) (Reply, error) {
	if err := validate(request); err != nil {
		return Reply{}, err
	}
	if request.Scope == "" {
		request.Scope = "action"
	}
	result := map[string]any{}
	maps.Copy(result, payload)
	result["op"] = request.Operation
	result["request_id"] = request.ID
	result["scope"] = request.Scope
	result["available"] = failure == ""
	if failure != "" {
		result["error"] = failure
		if _, exists := result["message"]; !exists {
			result["message"] = map[string]string{"timeout": "Discovery timed out", "busy": "Discovery is busy; try again shortly", "failed": "Discovery failed"}[failure]
		}
	}
	data, err := json.Marshal(result)
	if err != nil || len(data) > MaxResponseBytes {
		return Envelope(request, map[string]any{"message": "Discovery response exceeds its supported size"}, "response_too_large")
	}
	var clone map[string]any
	_ = json.Unmarshal(data, &clone)
	return Reply{request.Context, request.Action, clone}, nil
}

// Complete a current ticket and ignore stale results
func (r *Runner) Complete(t *Ticket, payload map[string]any, failure string, now int64) (*Reply, error) {
	if now < 0 || now > MaxTime {
		return nil, fmt.Errorf("invalid discovery time")
	}
	key := [2]string{t.Request.Context, t.Request.Channel}
	if r.pending[key] != t {
		return nil, nil
	}
	if now >= t.Deadline {
		payload = nil
		failure = "timeout"
	} else if t.Context.Err() != nil {
		delete(r.pending, key)
		t.cancel()
		return nil, nil
	}
	reply, err := Envelope(t.Request, payload, failure)
	if err != nil {
		return nil, err
	}
	delete(r.pending, key)
	t.cancel()
	return &reply, nil
}

// Retire overdue tickets and produce timeout replies
func (r *Runner) Expire(now int64) ([]Reply, error) {
	if now < 0 || now > MaxTime {
		return nil, fmt.Errorf("invalid discovery time")
	}
	tickets := []*Ticket{}
	for _, t := range r.pending {
		if t.Deadline <= now {
			tickets = append(tickets, t)
		}
	}
	sort.Slice(tickets, func(i, j int) bool {
		a, b := tickets[i], tickets[j]
		if a.Deadline != b.Deadline {
			return a.Deadline < b.Deadline
		}
		if a.Request.Context != b.Request.Context {
			return a.Request.Context < b.Request.Context
		}
		return a.Request.Channel < b.Request.Channel
	})
	replies := []Reply{}
	for _, t := range tickets {
		reply, err := r.Complete(t, nil, "timeout", now)
		if err != nil {
			return nil, err
		}
		if reply != nil {
			replies = append(replies, *reply)
		}
	}
	return replies, nil
}
