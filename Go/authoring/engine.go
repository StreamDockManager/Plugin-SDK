package authoring

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/StreamDockManager/Plugin-SDK/Go/credentials"
	"os"
	"reflect"
	"time"

	"github.com/StreamDockManager/Plugin-SDK/Go"
	"github.com/StreamDockManager/Plugin-SDK/Go/internal/statefile"
)

// Construct the action engine and initialize its state maps
func New(def Definition, path string, factory Factory) *Engine {
	e := &Engine{Slug: def.Slug, Path: path, Global: map[string]any{}, Bindings: map[string]Binding{}, Statuses: map[string]SampledStatus{}, Samples: map[string]SampledStatus{}, Probes: sdk.NewProbePool(), WakeChannel: make(chan struct{}, 1), ProbeKeys: map[string]string{}, Effective: map[string]map[string]any{}, DisplayTicks: map[string]int{}, Jobs: map[string]BackgroundJob{}, ResultsText: map[string]string{}, Results: map[string]any{}, Checked: map[string]time.Time{}, Last: map[string]Display{}, LastStatus: map[string]DisplayStatus{}}
	e.ViewTargets = map[string]ViewTarget{}
	e.ViewActive = map[string]bool{}
	for _, rule := range def.ViewTargets {
		e.ViewTargets[rule.Action] = rule
	}
	if factory != nil {
		e.Feature = factory(e)
	}
	return e
}

// Restore loads file-backed state atomically and reports failures directly
// Rejected state remains protected until an explicit successful restore
func (e *Engine) Restore(dst any) error {
	b, err := statefile.Read(e.Path)
	if os.IsNotExist(err) {
		e.stateReadBlocked = false
		e.Written = nil
		return nil
	}
	e.stateReadBlocked = true
	if err != nil {
		return err
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(b, &envelope); err != nil {
		return err
	}
	var version int
	if json.Unmarshal(envelope["version"], &version) != nil || version != 1 || envelope["data"] == nil {
		return fmt.Errorf("unsupported state format: version 1 envelope required")
	}
	b = envelope["data"]
	target := reflect.ValueOf(dst)
	if target.Kind() != reflect.Pointer || target.IsNil() {
		return fmt.Errorf("state destination must be a non-nil pointer")
	}
	value := reflect.New(target.Elem().Type())
	if err := json.Unmarshal(b, value.Interface()); err != nil {
		return err
	}
	target.Elem().Set(value.Elem())
	e.Written, _ = json.Marshal(dst)
	e.stateReadBlocked = false
	return nil
}

// Report the saved read failure that blocks persistence
func (e *Engine) stateReadError() error {
	if e.stateReadBlocked {
		return statefile.RecoveryRequired()
	}
	return nil
}

// Expose the wake signal used by the host event loop
func (e *Engine) Wake() <-chan struct{} { return e.WakeChannel }

// Decide whether an input event merits an action log
func (e *Engine) ShouldLogAction(ev sdk.Event) bool {
	if e.Feature.ShouldLogAction != nil {
		return e.Feature.ShouldLogAction(ev)
	}
	return sdk.ShouldLogAction(ev)
}

// Check whether this action permits dial event coalescing
func (e *Engine) BatchDial(id string) bool {
	return e.Feature.AdditiveDial != nil && e.Feature.AdditiveDial(id)
}

// Choose the next explicit job or feature deadline
func (e *Engine) NextPoll(now time.Time) time.Time {
	var at time.Time
	if e.Feature.Deadline != nil {
		at = e.Feature.Deadline(now)
	}
	if !e.PendingWrite.IsZero() && (at.IsZero() || e.PendingWrite.Before(at)) {
		at = e.PendingWrite
	}
	if e.Store != nil && !e.Store.Due.IsZero() && (at.IsZero() || e.Store.Due.Before(at)) {
		at = e.Store.Due
	}
	return at
}

// Persist dirty state once its debounce deadline is due
func (e *Engine) RoutinePersist() error {
	e.DirtyState = true
	if err := e.stateReadError(); err != nil {
		return err
	}
	if e.PendingWrite.IsZero() {
		e.PendingWrite = e.Time().Add(250 * time.Millisecond)
	}
	return nil
}

// Dispatch lifecycle and input events through feature hooks
func (e *Engine) Handle(ctx context.Context, c *sdk.Client, ev sdk.Event) (err error) {
	defer func() {
		if err != nil && e.ShouldLogAction(ev) && e.Feature.ObserveFailure != nil {
			e.Feature.ObserveFailure(ctx, c, ev.Context, err)
		}
	}()
	if err := e.stateReadError(); err != nil {
		return err
	}
	if c != nil && e.Theme == "" {
		if theme := c.CurrentPluginTheme(); theme != nil {
			e.Theme = theme.Resolved
		}
	}
	if old, ok := e.Bindings[ev.Context]; ok && (ev.Event == "willAppear" || ev.Event == "didReceiveSettings") && old.Action != ev.Action {
		if hook := e.Registered[old.Action].Disappeared; hook != nil {
			if err := hook(ctx, c, ev, old); err != nil {
				return err
			}
		}
		e.ClearContext(ev.Context)
		delete(e.ViewActive, ev.Context)
		e.bindingState(ev.Context).forgetDisplay()
		if e.Feature.Unbound != nil {
			e.Feature.Unbound(ev.Context)
		}
	}
	if c != nil && (ev.Event == "willAppear" || ev.Event == "didReceiveSettings" || ev.Event == "didReceiveGlobalSettings") {
		if e.secretFields == nil {
			e.secretFields = map[string]map[string]any{}
			for id, b := range e.Bindings {
				if fields := credentials.SecretSettings(b.Settings); len(fields) > 0 {
					e.secretFields[id] = fields
				}
			}
		}
		incoming := credentials.SecretSettings(ev.Data().Settings)
		snapshots := []map[string]any{credentials.SecretSettings(e.Global), incoming}
		for _, fields := range e.secretFields {
			snapshots = append(snapshots, fields)
		}
		if ev.Event != "didReceiveGlobalSettings" {
			if len(incoming) > 0 {
				e.secretFields[ev.Context] = incoming
			} else {
				delete(e.secretFields, ev.Context)
			}
		}
		e.redaction.set(snapshots)
		c.SetLogRedactor(e.redaction.redact)
	}
	if ev.Event == "willDisappear" {
		delete(e.secretFields, ev.Context)
	}
	ctx = e.eventViewContext(ctx, ev)
	defer func() {
		if e.Feature.After != nil {
			if v := e.Feature.After(ctx, c, ev); err == nil {
				err = v
			}
		}
		if err == nil && (Active(ev) || ev.Event == "dialRotate") {
			err = e.RenderViewTargets(ctx, c)
		}
	}()
	if e.Feature.Before != nil {
		if handled, v := e.Feature.Before(ctx, c, ev); handled || v != nil {
			return v
		}
	}
	if err = e.DrainJobs(ctx, c); err != nil {
		return err
	}
	switch ev.Event {
	case sdk.ThemeEvent:
		var p struct{ Resolved string }
		if err = json.Unmarshal(ev.Payload, &p); err != nil {
			return err
		}
		if p.Resolved != "light" && p.Resolved != "dark" {
			return fmt.Errorf("unsupported theme")
		}
		e.Theme = p.Resolved
		previous := e.Last
		e.Last = map[string]Display{}
		for id := range e.Bindings {
			if view, ok := previous[id]; ok && !e.viewRule(id).Live {
				if err = e.SendDisplayView(context.WithValue(ctx, viewScopeKey{}, viewScope{Origin: id}), c, id, e.rethemeView(view), true); err != nil {
					return err
				}
				continue
			}
			if err = e.Render(ctx, c, id); err != nil {
				return err
			}
		}
		return nil
	case "didReceiveGlobalSettings":
		if e.Feature.Settings != nil {
			e.Feature.Settings("")
		}
		e.UpdateGlobalSettings(ev.Data().Settings, "")
		return nil
	}
	if ev.Context == "" {
		return nil
	}
	if Active(ev) || ev.Event == "dialRotate" {
		e.RefreshProbe(ev.Context)
		e.InvalidateProbe(ev.Context)
	} else if ev.Event == "willAppear" {
		e.InvalidateProbe(ev.Context)
	}
	switch ev.Event {
	case "willDisappear":
		if old, ok := e.Bindings[ev.Context]; ok {
			if hook := e.Registered[old.Action].Disappeared; hook != nil {
				if err := hook(ctx, c, ev, old); err != nil {
					return err
				}
			}
		}
		delete(e.ViewActive, ev.Context)
		e.ClearContext(ev.Context)
		delete(e.Bindings, ev.Context)
		if e.Feature.DemandChanged != nil {
			e.Feature.DemandChanged()
		}
		if e.Feature.Unbound != nil {
			e.Feature.Unbound(ev.Context)
		}
		e.bindingState(ev.Context).forgetDisplay()
		return nil
	case "propertyInspectorDidAppear":
		return nil
	case "propertyInspectorDidDisappear":
		if _, visible := e.Bindings[ev.Context]; !visible {
			e.ClearContext(ev.Context)
			e.bindingState(ev.Context).forgetDisplay()
		}
		return nil
	case "sendToPlugin":
		if e.Feature.Inspector != nil {
			_, err = e.Feature.Inspector(ctx, c, ev)
		}
		return err
	case "willAppear", "didReceiveSettings":
		e.bindingState(ev.Context).invalidateSettings()
		if ev.Event == "didReceiveSettings" {
			e.ClearContext(ev.Context)
			if e.Feature.Settings != nil {
				e.Feature.Settings(ev.Context)
			}
		}
		surface := ev.Surface
		if surface == nil {
			surface = e.Bindings[ev.Context].Surface
		}
		e.Bindings[ev.Context] = Binding{Action: ev.Action, Settings: ev.Data().Settings, Surface: surface}
		if e.Feature.DemandChanged != nil {
			e.Feature.DemandChanged()
		}
		if ev.Event == "willAppear" {
			e.bindingState(ev.Context).forgetDisplay()
		}
		hook := e.Registered[ev.Action].Appeared
		if ev.Event == "didReceiveSettings" {
			hook = e.Registered[ev.Action].Changed
		}
		if hook != nil {
			if err := hook(ctx, c, ev, e.Bindings[ev.Context]); err != nil {
				return err
			}
		}
		return e.Render(ctx, c, ev.Context)
	}
	b, ok := e.Bindings[ev.Context]
	if !ok {
		b = Binding{Action: ev.Action, Settings: ev.Data().Settings, Surface: ev.Surface}
		e.Bindings[ev.Context] = b
		if e.Feature.DemandChanged != nil {
			e.Feature.DemandChanged()
		}
	}
	if hook := e.Registered[ev.Action].Input; hook != nil {
		return hook(ctx, c, ev, b)
	}
	if handler := e.Feature.Actions[ev.Action]; handler != nil {
		return handler(ctx, c, ev, b)
	}
	if e.Feature.Action != nil {
		return e.Feature.Action(ctx, c, ev, b)
	}
	return nil
}

// Process scheduled feature work and completed jobs
func (e *Engine) Poll(ctx context.Context, c *sdk.Client) (err error) {
	defer func() {
		if err != nil && e.Feature.ObserveFailure != nil {
			e.Feature.ObserveFailure(ctx, c, "", err)
		}
	}()
	if err := e.stateReadError(); err != nil {
		return err
	}
	ctx = context.WithValue(ctx, viewScopeKey{}, viewScope{Automatic: true})
	defer func() { e.NextPeriodic = e.Time().Add(time.Second) }()
	if !e.PendingWrite.IsZero() && !e.Time().Before(e.PendingWrite) {
		if err := e.Persist(); err != nil {
			return err
		}
	}
	if e.Store != nil {
		if err := e.Store.Flush(e.Time(), false); err != nil {
			return err
		}
	}
	if err := e.DrainJobs(ctx, c); err != nil {
		return err
	}
	if e.Feature.Poll != nil {
		if handled, err := e.Feature.Poll(ctx, c); handled || err != nil {
			return err
		}
	}
	for id := range e.Bindings {
		if err := e.Render(ctx, c, id); err != nil {
			return err
		}
	}
	return nil
}

// Build and send the current binding display
func (e *Engine) Render(ctx context.Context, c *sdk.Client, id string) error {
	if !e.CanUpdateView(ctx, id) {
		return nil
	}
	b, ok := e.Bindings[id]
	if !ok {
		return nil
	}
	if hook := e.Registered[b.Action].Render; hook != nil {
		view, err := hook(ctx, c, id, b)
		if err != nil {
			return err
		}
		return e.SendDisplayView(ctx, c, id, view, true)
	}
	if e.Feature.Render != nil {
		return e.Feature.Render(ctx, c, id)
	}
	if e.Feature.Collect == nil && e.Feature.Status == nil {
		return nil
	}
	return e.SendDisplay(ctx, c, id, e.CollectStatus(ctx, id, b))
}

// Collect feature status and cached job outcomes
func (e *Engine) CollectStatus(ctx context.Context, id string, b Binding) DisplayStatus {
	if e.Feature.Collect != nil {
		return e.Feature.Collect(ctx, id, b)
	}
	if e.Feature.Status != nil {
		return e.Feature.Status(ctx, id, b)
	}
	return DisplayStatus{}
}

// Cancel binding-owned work and clear transient context
func (e *Engine) ClearContext(id string) {
	if e.Feature.Clear != nil {
		e.Feature.Clear(id)
	}
	e.InvalidateProbe(id)
	e.CancelJob(id)
	e.bindingState(id).clearTransient()
}

// Stop feature work and flush pending state
func (e *Engine) Close() error {
	e.Closed = true
	for id := range e.Jobs {
		e.CancelJob(id)
	}
	e.Probes.Close()
	err := e.stateReadError()
	if e.Store != nil {
		err = errors.Join(err, e.Store.Close())
	}
	if e.Feature.Close != nil {
		e.Feature.Close()
	}
	if e.DirtyState || !e.PendingWrite.IsZero() {
		err = errors.Join(err, e.Persist())
	}
	e.clearBindingStates()
	clear(e.ProbeKeys)
	clear(e.Samples)
	for {
		select {
		case <-e.Completed:
		default:
			return err
		}
	}
}

// Register adds a complete action UUID with live rendering by default
func (e *Engine) Register(action string, hooks ActionHooks) error {
	if action == "" {
		return fmt.Errorf("action UUID is required")
	}
	if e.Registered == nil {
		e.Registered = map[string]ActionHooks{}
	}
	if _, exists := e.Registered[action]; exists {
		return fmt.Errorf("duplicate action UUID")
	}
	e.Registered[action] = hooks
	key := action
	if _, exists := e.ViewTargets[key]; !exists {
		e.ViewTargets[key] = ViewTarget{Action: key, Live: true}
	}
	return nil
}

// Admit a job or explicitly replace the existing binding job
func (e *Engine) StartOrReplaceJob(ctx context.Context, id string, replace bool, work func(context.Context) (string, any, error), apply func(context.Context, *sdk.Client, JobResult, BackgroundJob) error) bool {
	if replace {
		e.CancelJob(id)
	}
	return e.StartJob(ctx, id, work, apply)
}

// Read the injected clock or the system clock
func (e *Engine) Time() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// UpdateGlobalSettings replaces the global snapshot and invalidates dependent work.
// A connector can preserve its shared job when its connection settings did not change.
func (e *Engine) UpdateGlobalSettings(settings map[string]any, preserveJob string) {
	e.Global = CloneSettings(settings)
	clear(e.Effective)
	for id := range e.ProbeKeys {
		e.InvalidateProbe(id)
	}
	for id := range e.Jobs {
		if preserveJob == "" || id != preserveJob {
			e.CancelJob(id)
		}
	}
	if e.Feature.DemandChanged != nil {
		e.Feature.DemandChanged()
	}
}
