// Package connectors adapts independent integrations to the SDK lifecycle
// It contains no feature imports, so executables retain dependency isolation
package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	discovery "github.com/StreamDockManager/Plugin-SDK/Go/discovery"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	sdk "github.com/StreamDockManager/Plugin-SDK/Go"
	engine "github.com/StreamDockManager/Plugin-SDK/Go/authoring"
	"github.com/StreamDockManager/Plugin-SDK/Go/polling"
	"github.com/StreamDockManager/Plugin-SDK/Go/settings"
)

type Choice struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Kind string `json:"kind"`
}
type Outcome struct {
	Message string
	Value   any
}
type Driver interface {
	Key(map[string]any) string
	Read(context.Context, map[string]any) (any, error)
	Command(context.Context, string, map[string]any, int) (Outcome, error)
	Discover(context.Context, map[string]any) ([]Choice, error)
	Display(any, string, map[string]any) string
	Close()
}

// ClientReader is an optional connector read hook for authenticated host RPC.
type ClientReader interface {
	ReadWithClient(context.Context, *sdk.Client, string, map[string]any) (any, error)
}

func readDriver(ctx context.Context, client *sdk.Client, driver Driver, action string, settings map[string]any) (any, error) {
	if reader, ok := driver.(ClientReader); ok && client != nil {
		return reader.ReadWithClient(ctx, client, action, settings)
	}
	return driver.Read(ctx, settings)
}

// An explicit host authorization failure is not a transient provider failure.
func discardManagedSample(err error) bool {
	var rpc *sdk.RPCError
	if !errors.As(err, &rpc) {
		return false
	}
	switch rpc.Code {
	case "needs_setup", "vault_locked", "permission_denied", "not_declared", "operation_unavailable", "unsupported", "stale_revision":
		return true
	default:
		return false
	}
}

type hostClientKey struct{}

// HostClient returns the authenticated client for the current connector operation.
func HostClient(ctx context.Context) *sdk.Client {
	client, _ := ctx.Value(hostClientKey{}).(*sdk.Client)
	return client
}

type Options struct {
	UsesVault        bool
	ReadTimeout      time.Duration
	CommandTimeout   time.Duration
	AuthorizeTimeout time.Duration
	Definition       engine.Definition
	Interval         time.Duration
	Tick             time.Duration
	Visuals          map[string]engine.Visual
	New              func(chan<- struct{}) Driver
	Dial             bool
	DialActions      []string
	SharedKeys       []string
}
type request struct {
	ctx            context.Context
	id, action, op string
	actionUUID     string
	requestID      string
	scope          string
	settings       map[string]any
	ticks          int
	complete       func(error)
}
type result struct {
	request    request
	key        string
	generation uint64
	discard    bool
	value      any
	choices    []Choice
	outcome    Outcome
}
type Runtime struct {
	*engine.Engine
	options        Options
	driver         Driver
	resources      *polling.Resources
	queue          []request
	messages       map[string]string
	busy           bool
	nextTick       time.Time
	activeComplete func(error)
	activeID       string
	workMu         sync.Mutex
}

// Construct the authoring factory for a service connector
func Factory(options Options) engine.Factory {
	return func(e *engine.Engine) engine.Feature {
		r := &Runtime{Engine: e, options: options, resources: polling.NewResources(), messages: map[string]string{}}
		r.driver = options.New(e.WakeChannel)
		return engine.Feature{Action: r.action, Inspector: r.inspector, Before: r.beforeSettings, Unbound: r.unbound, Close: r.close, After: r.after,
			Status: r.status, Poll: r.poll, Deadline: r.deadline, Visuals: options.Visuals,
			AdditiveDial: func(a string) bool {
				return r.dialAction(engine.Action(a))
			},
			Snapshot: func() any { return map[string]any{} }, DemandChanged: r.watch,
		}
	}
}

// Synchronize resource watchers with current bindings
func (r *Runtime) watch() {
	var demand []map[string]any
	for id, b := range r.Bindings {
		s := r.bindingSettings(b)
		demand = append(demand, s)
		_ = r.resources.Watch(id, r.driver.Key(s), polling.Interval(b.Settings, r.options.Interval))
	}
	if driver, ok := r.driver.(interface{ Demand([]map[string]any) }); ok {
		driver.Demand(demand)
	}
}

// Build display status from the current resource snapshot
func (r *Runtime) status(_ context.Context, id string, b engine.Binding) engine.DisplayStatus {
	text := r.messages[id]
	visualState := 0
	var content *engine.Presentation
	if text == "" {
		if state, ok := r.resources.Snapshot(id); ok && state.Available {
			text = r.driver.Display(state.LastValue, engine.Action(b.Action), r.bindingSettings(b))
			if driver, ok := r.driver.(interface {
				Presentation(any, string, map[string]any) engine.Presentation
			}); ok {
				value := driver.Presentation(state.LastValue, engine.Action(b.Action), r.bindingSettings(b))
				content = &value
			}
			if driver, ok := r.driver.(interface{ VisualState(any, string) int }); ok {
				visualState = driver.VisualState(state.LastValue, engine.Action(b.Action))
			}
			if state.Stale {
				text = "Stale\n" + text
			}
		}
	}
	if text == "" {
		text = "Waiting for connection"
	}
	return engine.DisplayStatus{Text: text, Content: content, State: visualState, Unavailable: strings.HasPrefix(text, "Unavailable")}
}

// Dispatch an activating binding input to its connector operation
func (r *Runtime) action(ctx context.Context, c *sdk.Client, ev sdk.Event, b engine.Binding) error {
	if !engine.Active(ev) && ev.Event != "dialRotate" {
		return nil
	}
	a := engine.Action(b.Action)
	if ev.Event == "dialRotate" && !r.dialAction(a) {
		return nil
	}
	ticks := ev.Data().Ticks
	if ev.Event != "dialRotate" {
		ticks = 0
	}
	return r.enqueue(c, request{ctx: ctx, id: ev.Context, action: a, actionUUID: ev.Action, settings: r.bindingSettings(b), ticks: ticks})
}

// Route inspector discovery and connector-specific requests
func (r *Runtime) inspector(ctx context.Context, c *sdk.Client, ev sdk.Event) (bool, error) {
	var p struct {
		Op        string         `json:"op"`
		RequestID string         `json:"request_id"`
		Settings  map[string]any `json:"settings"`
		Scope     string         `json:"scope"`
	}
	if json.Unmarshal(ev.Payload, &p) != nil {
		return false, nil
	}
	if p.Op != "test" && p.Op != "discover" && p.Op != "authorize" && p.Op != "disconnect" {
		return false, nil
	}
	if len(p.RequestID) > 128 {
		return true, fmt.Errorf("discovery request ID exceeds 128 bytes")
	}
	s := r.MergedSettings(p.Settings)
	a := engine.Action(ev.Action)
	s["_action"] = a
	if p.Scope != "plugin" {
		r.sharedSettings(s)
	}
	return true, r.enqueue(c, request{ctx: ctx, id: ev.Context, action: a, actionUUID: ev.Action, op: p.Op, requestID: p.RequestID, scope: p.Scope, settings: s})
}

// Queue a bounded connector request
func (r *Runtime) enqueue(c *sdk.Client, q request) error {
	if len(r.queue) >= 32 {
		payload := map[string]any{"error": "Command queue full; retry after it drains"}
		if q.requestID != "" {
			payload["request_id"] = q.requestID
			payload["available"] = false
		}
		reply := sdk.Message("sendToPropertyInspector", q.id, payload)
		reply.Action = q.actionUUID
		return c.Send(q.ctx, reply)
	}
	q.complete = sdk.DeferAction(q.ctx)
	q.settings = engine.CloneSettings(q.settings)
	r.queue = append(r.queue, q)
	return r.start(q.ctx, c)
}

// Initialize connector work for the active bindings
func (r *Runtime) start(ctx context.Context, c *sdk.Client) error {
	if r.busy {
		return nil
	}
	for len(r.queue) > 0 {
		q := r.queue[0]
		r.queue[0] = request{}
		r.queue = r.queue[1:]
		if len(r.queue) == 0 {
			r.queue = nil
		}
		if q.ctx.Err() != nil {
			q.complete(q.ctx.Err())
			continue
		}
		r.busy = true
		r.activeComplete = q.complete
		r.activeID = q.id
		timeout := r.operationTimeout(q.op, false)
		requestCtx, stop := context.WithTimeout(q.ctx, timeout)
		if !r.StartJob(requestCtx, "connector-work", func(job context.Context) (string, any, error) {
			r.workMu.Lock()
			defer r.workMu.Unlock()
			if job.Err() != nil {
				return "", nil, job.Err()
			}
			timeout := r.operationTimeout(q.op, false)
			job, cancel := context.WithTimeout(context.WithValue(job, hostClientKey{}, c), timeout)
			defer cancel()
			v := result{request: q}
			var err error
			switch q.op {
			case "discover":
				v.choices, err = r.driver.Discover(job, q.settings)
			case "test":
				v.value, err = readDriver(job, c, r.driver, q.actionUUID, q.settings)
				v.outcome.Message = "Connection succeeded"
				if check, ok := v.value.(interface{ TestResult() string }); ok {
					v.outcome.Message = check.TestResult()
				}
			case "authorize", "disconnect":
				v.outcome, err = r.driver.Command(job, q.op, q.settings, 0)
			default:
				v.outcome, err = r.driver.Command(job, q.action, q.settings, q.ticks)
			}
			if err != nil {
				err = SafeError(err, q.settings)
			}
			return "", v, err
		}, func(ctx context.Context, c *sdk.Client, j engine.JobResult, _ engine.BackgroundJob) error {
			defer stop()
			ctx = context.WithoutCancel(ctx)
			r.busy = false
			r.activeComplete = nil
			r.activeID = ""
			if len(r.Bindings) == 0 {
				defer r.driver.Close()
			}
			v, ok := j.Value.(result)
			if !ok {
				q.complete(j.Err)
				return nil
			}
			q.complete(j.Err)
			message := v.outcome.Message
			if j.Err != nil {
				message = "Unavailable\n" + j.Err.Error()
			}
			if message == "" {
				message = "Request completed"
			}
			r.messages[q.id] = message
			if q.op != "" {
				payload := map[string]any{"message": message, "devices": v.choices}
				if q.requestID != "" {
					failure := ""
					if j.Err != nil {
						failure = "failed"
					}
					reply, err := discovery.Envelope(discovery.Request{Context: q.id, Channel: "service", ID: q.requestID, Operation: q.op, Scope: q.scope}, payload, failure)
					if err != nil {
						return err
					}
					payload = reply.Payload
				}
				reply := sdk.Message("sendToPropertyInspector", q.id, payload)
				reply.Action = q.actionUUID
				if err := c.Send(ctx, reply); err != nil {
					return err
				}
			}
			// Only reads may be scheduled again. Commands are removed before dispatch
			for key, state := range r.resources.States {
				state.Next = time.Time{}
				r.resources.States[key] = state
			}
			return r.render(ctx, c)
		}) {
			stop()
			r.busy = false
			q.complete(fmt.Errorf("worker unavailable"))
		}
		return nil
	}
	keys := r.resources.Due(r.Time())
	if len(keys) == 0 {
		return nil
	}
	key := keys[0]
	var settings map[string]any
	var actionUUID string
	shortest := time.Duration(1<<63 - 1)
	ids := make([]string, 0, len(r.Bindings))
	for id := range r.Bindings {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		s := r.bindingSettings(r.Bindings[id])
		if r.driver.Key(s) == key {
			interval := polling.Interval(s, r.options.Interval)
			if interval == 0 {
				interval = time.Duration(1<<63 - 1)
			}
			if settings == nil || interval < shortest {
				settings, shortest = s, interval
				actionUUID = r.Bindings[id].Action
			}
		}
	}
	if settings == nil {
		return nil
	}
	generation, err := r.resources.Begin(key)
	if err != nil {
		return nil
	}
	r.busy = true
	readCtx, stop := context.WithTimeout(ctx, r.operationTimeout("", true))
	if !r.StartJob(readCtx, "connector-work", func(job context.Context) (string, any, error) {
		r.workMu.Lock()
		defer r.workMu.Unlock()
		if job.Err() != nil {
			return "", nil, job.Err()
		}
		job, cancel := context.WithTimeout(context.WithValue(job, hostClientKey{}, c), r.operationTimeout("", true))
		defer cancel()
		value, err := readDriver(job, c, r.driver, actionUUID, settings)
		discard := discardManagedSample(err)
		if err != nil {
			err = SafeError(err, settings)
		}
		return "", result{key: key, generation: generation, value: value, discard: discard}, err
	}, func(ctx context.Context, c *sdk.Client, j engine.JobResult, _ engine.BackgroundJob) error {
		defer stop()
		ctx = context.WithoutCancel(ctx)
		r.busy = false
		v, ok := j.Value.(result)
		if !ok {
			v = result{key: key, generation: generation}
		}
		previous := r.resources.States[v.key]
		accepted := false
		if v.discard {
			accepted = r.resources.CompleteInvalidated(v.key, v.generation, r.Time(), 0.5, RetryAt(j.Err))
		} else {
			accepted = r.resources.Complete(v.key, v.generation, v.value, j.Err != nil, r.Time(), 0.5, RetryAt(j.Err))
		}
		if accepted {
			if c != nil && c.Supports("plugin-logs-v1") {
				if j.Err != nil {
					failures := previous.Failures + 1
					if failures == 1 || failures&(failures-1) == 0 {
						_ = c.LogStream(context.WithoutCancel(ctx), sdk.Stderr, fmt.Sprintf("Connection read failed (%d consecutive failures): %s", failures, j.Err))
					}
				} else if !previous.Available || previous.Failures > 0 {
					_ = c.LogStream(context.WithoutCancel(ctx), sdk.Stdout, "Connection available")
				}
			}
			for id, b := range r.Bindings {
				if r.driver.Key(r.bindingSettings(b)) == v.key {
					delete(r.messages, id)
					state, _ := r.resources.Snapshot(id)
					if j.Err != nil && !state.Available {
						r.messages[id] = "Unavailable\n" + j.Err.Error()
					}
				}
			}
		}
		return r.render(ctx, c)
	}) {
		r.busy = false
		stop()
		r.resources.Complete(key, generation, nil, true, r.Time(), 0, time.Time{})
	}
	return nil
}

// Refresh connector displays through the authoring engine
func (r *Runtime) render(ctx context.Context, c *sdk.Client) error {
	for id := range r.Bindings {
		if err := r.Render(ctx, c, id); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runtime) needsTick() bool {
	if r.options.Tick <= 0 || len(r.Bindings) == 0 {
		return false
	}
	driver, conditional := r.driver.(interface{ NeedsTick(any, string) bool })
	if !conditional {
		return true
	}
	for id, b := range r.Bindings {
		if state, ok := r.resources.Snapshot(id); ok && state.Available && driver.NeedsTick(state.LastValue, engine.Action(b.Action)) {
			return true
		}
	}
	return false
}

// Apply pending connector work and due polling results
func (r *Runtime) poll(ctx context.Context, c *sdk.Client) (bool, error) {
	if source, ok := r.driver.(interface{ Changed() bool }); ok && !r.busy && source.Changed() {
		for key, state := range r.resources.States {
			state.Next = time.Time{}
			r.resources.States[key] = state
		}
	}
	if !r.Time().Before(r.nextTick) && r.needsTick() {
		r.nextTick = r.Time().Add(r.options.Tick)
		if err := r.render(ctx, c); err != nil {
			return true, err
		}
	}
	return true, r.start(ctx, c)
}

// Find the next queued-operation or polling deadline
func (r *Runtime) deadline(now time.Time) time.Time {
	if r.busy {
		return time.Time{}
	}
	if len(r.queue) > 0 {
		return now
	}
	var at time.Time
	for key := range r.resources.States {
		due := r.resources.Deadline(key)
		if due.Equal(polling.Never) {
			continue
		}
		if due.IsZero() {
			return now
		}
		if at.IsZero() || due.Before(at) {
			at = due
		}
	}
	if r.needsTick() {
		if r.nextTick.IsZero() {
			return now
		}
		if at.IsZero() || r.nextTick.Before(at) {
			at = r.nextTick
		}
	}
	return at
}

func (r *Runtime) beforeSettings(_ context.Context, _ *sdk.Client, ev sdk.Event) (bool, error) {
	switch ev.Event {
	case "streamDockManagerPermissionsChanged":
		if r.options.UsesVault {
			r.settings("")
			r.watch()
		}
	case "didReceiveSettings":
		r.settings(ev.Context)
	case "didReceiveGlobalSettings":
		next := ev.Data().Settings
		changed := len(r.options.SharedKeys) == 0
		for _, key := range r.options.SharedKeys {
			previous, was := r.Global[key]
			value, has := next[key]
			if was != has || !engine.FeedbackEqual(previous, value) {
				changed = true
				break
			}
		}
		if !changed {
			r.UpdateGlobalSettings(next, "connector-work")
			return true, nil
		}
		r.settings("")
	}
	return false, nil
}

// Invalidate connector state after settings change
func (r *Runtime) settings(id string) {
	if id != "" && len(r.options.SharedKeys) > 0 {
		r.cancelBinding(id)
		return
	}
	r.CancelJob("connector-work")
	r.busy = false
	r.activeID = ""
	if r.activeComplete != nil {
		r.activeComplete(context.Canceled)
		r.activeComplete = nil
	}
	for _, q := range r.queue {
		q.complete(context.Canceled)
	}
	r.queue = nil
	r.resources = polling.NewResources()
	clear(r.messages)
	r.driver.Close()
}

// Release work and watchers belonging to a removed binding
func (r *Runtime) unbound(id string) {
	r.resources.Unwatch(id)
	r.cancelBinding(id)
	if len(r.Bindings) == 0 {
		r.settings("")
	} else {
		for key, state := range r.resources.States {
			state.Next = time.Time{}
			r.resources.States[key] = state
		}
	}
}

// Release connector state during engine shutdown
func (r *Runtime) close() { r.settings("") }

// Apply lifecycle follow-up work after event handling
func (r *Runtime) after(_ context.Context, _ *sdk.Client, ev sdk.Event) error {
	if ev.Event != "propertyInspectorDidDisappear" {
		return nil
	}
	if _, bound := r.Bindings[ev.Context]; bound {
		return nil
	}
	r.cancelBinding(ev.Context)
	if len(r.Bindings) == 0 {
		r.driver.Close()
	}
	return nil
}

func (r *Runtime) cancelBinding(id string) {
	delete(r.messages, id)
	retained := r.queue[:0]
	for _, q := range r.queue {
		if q.id == id {
			q.complete(context.Canceled)
		} else {
			retained = append(retained, q)
		}
	}
	clear(r.queue[len(retained):])
	r.queue = retained
	if len(r.queue) == 0 {
		r.queue = nil
	}
	if r.activeID == id {
		r.CancelJob("connector-work")
		r.busy = false
		r.activeID = ""
		if r.activeComplete != nil {
			r.activeComplete(context.Canceled)
			r.activeComplete = nil
		}
	}
}

// Merge connector and binding-specific settings
func (r *Runtime) bindingSettings(b engine.Binding) map[string]any {
	s := r.MergedSettings(b.Settings)
	r.sharedSettings(s)
	s["_action"] = engine.Action(b.Action)
	return s
}

// Update shared connector settings
func (r *Runtime) sharedSettings(s map[string]any) {
	for _, key := range r.options.SharedKeys {
		if value, ok := r.Global[key]; ok {
			s[key] = settings.Clone(value)
		} else {
			delete(s, key)
		}
	}
}

// SingleResource supplies the connection-wide identity for status samples
func SingleResource(map[string]any) string { return "connection" }

// Recognize actions whose dial input may be coalesced
func (r *Runtime) dialAction(action string) bool {
	if !r.options.Dial {
		return false
	}
	actions := r.options.DialActions
	if len(actions) == 0 {
		actions = []string{"volume", "seek"}
	}
	return slices.Contains(actions, action)
}

// Select the bounded timeout for a read or command
func (r *Runtime) operationTimeout(op string, read bool) time.Duration {
	if read {
		if r.options.ReadTimeout > 0 {
			return r.options.ReadTimeout
		}
		return 12 * time.Second
	}
	if op == "authorize" {
		if r.options.AuthorizeTimeout > 0 {
			return r.options.AuthorizeTimeout
		}
		return 2 * time.Minute
	}
	if r.options.CommandTimeout > 0 {
		return r.options.CommandTimeout
	}
	return 15 * time.Second
}
