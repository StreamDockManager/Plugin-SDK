package authoring

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"reflect"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/StreamDockManager/Plugin-SDK/Go"
	"github.com/StreamDockManager/Plugin-SDK/Go/internal/statefile"
	"github.com/StreamDockManager/Plugin-SDK/Go/settings"
)

// Write changed feature state only after a successful read
func (e *Engine) Persist() error {
	if err := e.stateReadError(); err != nil {
		return err
	}
	b, err := json.Marshal(e.Feature.Snapshot())
	if err != nil {
		return err
	}
	if bytes.Equal(b, e.Written) {
		e.PendingWrite = time.Time{}
		e.DirtyState = false
		return nil
	}
	envelope, err := json.Marshal(map[string]any{"version": 1, "data": json.RawMessage(b)})
	if err != nil {
		return err
	}
	if err := statefile.Write(e.Path, envelope); err != nil {
		return err
	}
	e.Written = b
	e.PendingWrite = time.Time{}
	e.DirtyState = false
	return nil
}

// Clone global settings and overlay binding-specific values
func (e *Engine) MergedSettings(settings map[string]any) map[string]any {
	result := CloneSettings(e.Global)
	for k, v := range settings {
		result[k] = CloneSetting(v)
	}
	return result
}

// Cancel a binding job and settle its deferred action
func (e *Engine) CancelJob(id string) {
	if job, ok := e.Jobs[id]; ok {
		if job.StopCancel != nil {
			job.StopCancel()
		}
		job.Cancel()
		if job.Complete != nil {
			job.Complete(context.Canceled)
		}
		delete(e.Jobs, id)
	}
}

// Schedule bounded background work with binding ownership
func (e *Engine) StartJob(ctx context.Context, id string, work func(context.Context) (string, any, error), apply func(context.Context, *sdk.Client, JobResult, BackgroundJob) error) bool {
	// Queued work (for example coalesced dial rotation) still belongs to its
	// activated control when the scheduler starts it after the input event
	if scope, ok := ctx.Value(viewScopeKey{}).(viewScope); ok && scope.Automatic && e.ViewActive[id] {
		if b, bound := e.Bindings[id]; bound {
			ctx = context.WithValue(ctx, viewScopeKey{}, viewScope{Origin: id, Action: Action(b.Action)})
		}
	}
	complete := sdk.DeferAction(ctx)
	if e.Closed {
		complete(context.Canceled)
		return false
	}
	if e.Completed == nil {
		e.Completed = make(chan JobResult, 128)
	}
	if len(e.Jobs) >= 32 {
		complete(fmt.Errorf("plugin job queue full"))
		e.ResultsText[id] = "Busy · retry when another job finishes"
		return false
	}
	if _, ok := e.Jobs[id]; ok {
		complete(fmt.Errorf("plugin Action is already running"))
		return false
	}
	e.Generation++
	generation := e.Generation
	jobCtx, cancel := context.WithCancel(ctx)
	// Queue cancellation back to the owner loop using the original job generation
	stopCancel := context.AfterFunc(ctx, func() {
		select {
		case e.Completed <- JobResult{ID: id, Generation: generation, Err: ctx.Err()}:
			select {
			case e.WakeChannel <- struct{}{}:
			default:
			}
		default:
		}
	})
	secrets := e.redaction.current
	if done := apply; done != nil {
		apply = func(ctx context.Context, c *sdk.Client, result JobResult, job BackgroundJob) error {
			release := e.redaction.pin(secrets)
			defer release()
			return done(ctx, c, result, job)
		}
	}
	e.Jobs[id] = BackgroundJob{Cancel: cancel, StopCancel: stopCancel, Generation: generation, Complete: complete, ActionCtx: ctx, Apply: apply}
	e.ResultsText[id] = "Working…"
	e.Checked[id] = e.Time()
	// Workers return immutable results and leave binding mutation to the owner loop
	if !e.Probes.Submit(jobCtx, func() {
		defer func() { stopCancel(); cancel() }()
		release := e.redaction.pin(secrets)
		defer release()
		text, value, err := work(jobCtx)
		if jobCtx.Err() != nil {
			complete(jobCtx.Err())
			return
		}
		result := JobResult{ID: id, Generation: generation, Text: text, Value: value, Err: err}
		select {
		case e.Completed <- result:
			select {
			case e.WakeChannel <- struct{}{}:
			default:
			}
		case <-jobCtx.Done():
			complete(jobCtx.Err())
		}
	}) {
		stopCancel()
		cancel()
		complete(fmt.Errorf("plugin job queue full"))
		delete(e.Jobs, id)
		e.ResultsText[id] = "Busy · retry when another job finishes"
		return false
	}
	return true
}

// Invalidate the shared probe associated with a binding
func (e *Engine) InvalidateProbe(id string) {
	key := e.ProbeKeys[id]
	delete(e.ProbeKeys, id)
	delete(e.Statuses, id)
	if key == "" {
		return
	}
	for _, other := range e.ProbeKeys {
		if other == key {

			if job, ok := e.Jobs[key]; ok && job.Owner == id {
				if job.Complete != nil {
					job.Complete(context.Canceled)
				}
				job.Complete = nil
				job.Owner = ""
				e.Jobs[key] = job
			}
			return
		}
	}
	e.CancelJob(key)
	delete(e.Samples, key)
	delete(e.ResultsText, key)
	delete(e.Checked, key)
}

// Request a new sample for the binding probe
func (e *Engine) RefreshProbe(id string) {
	key := e.ProbeKeys[id]
	if key == "" {
		return
	}
	if job, ok := e.Jobs[key]; ok && job.Owner != id {

		job.Refresh = true
		e.Jobs[key] = job
	} else {
		e.CancelJob(key)
	}
	delete(e.Samples, key)
	for peer, source := range e.ProbeKeys {
		if source == key {
			delete(e.Statuses, peer)
		}
	}
}

// Resolve the shared sampling key for a binding
func (e *Engine) ProbeKey(id string, b Binding) string {
	if key := e.ProbeKeys[id]; key != "" {
		return key
	}
	settings, _ := json.Marshal(b.Settings)
	key := "status:" + b.Action + ":" + string(settings)
	e.ProbeKeys[id] = key
	return key
}

// Return the cached effective settings for a binding
func (e *Engine) EffectiveSettings(id string, settings map[string]any) map[string]any {
	return e.bindingState(id).effectiveSettings(settings)
}

// Send a display subject to the current view-update scope
func (e *Engine) SendDisplayView(ctx context.Context, c *sdk.Client, id string, view Display, states bool) error {
	if !e.CanUpdateView(ctx, id) {
		return nil
	}
	var previous *Display
	if old, known := e.bindingState(id).lastDisplay(); known {
		previous = &old
	}
	// A nil client is only used by tests with a custom renderer
	messages := DisplayMessages(id, previous, view, true, states)
	if previous != nil && len(messages) == 0 && previous.State == view.State && previous.Layout == view.Layout {
		return nil
	}
	for _, message := range messages {
		if err := c.Send(ctx, message); err != nil {
			return err
		}
	}
	// Keep an immutable snapshot: feature code may reuse its feedback map for
	// the next desired revision. Aliasing it would suppress that update
	if view.Feedback != nil {
		view.Feedback = CloneSettings(view.Feedback)
	}
	e.bindingState(id).setLastDisplay(view)
	return nil
}

// Resolve an asset using the current plugin theme
func (e *Engine) ThemedAsset(path string) string {
	if e.Theme == "" {
		return path
	}
	return ThemeAsset(path, e.ThemeName())
}

// Resolve a layout using the current plugin theme
func (e *Engine) ThemedLayout(path string) string {
	if e.Theme == "" {
		return path
	}
	return strings.TrimSuffix(path, ".json") + "-" + e.ThemeName() + ".json"
}

// Return the current theme name
func (e *Engine) ThemeName() string {
	if e.Theme == "light" {
		return "light"
	}
	return "dark"
}

// Clone a settings map without sharing nested mutable values
func CloneSettings(value map[string]any) map[string]any {
	return settings.Clone(value).(map[string]any)
}

// Clone one supported settings value
func CloneSetting(value any) any { return settings.Clone(value) }

// Extract the action suffix from its qualified identifier
func Action(id string) string { i := strings.LastIndex(id, "."); return id[i+1:] }

// Recognize an activating key or dial input
func Active(ev sdk.Event) bool {
	if ev.Event == "keyDown" || ev.Event == "dialDown" {
		return true
	}
	if ev.Event == "touchTap" {
		if ev.Surface != nil && ev.Surface.Touch != nil {
			p := ev.Surface.Touch.Phase
			return p == "tap" || p == "end" || p == ""
		}
		return !ev.Data().Hold
	}
	return false
}

// Wrap text into bounded display lines
func DisplayLines(text string, columns int) []string {
	columns = max(1, columns)
	var lines []string
	paragraphs := strings.Split(text, "\n")
	for _, paragraph := range paragraphs {
		chars := []rune(paragraph)
		if len(paragraphs) > 1 && len(chars) > columns {
			lines = append(lines, string(chars[:max(0, columns-1)])+">")
			continue
		}
		for len(chars) > columns {
			end := columns
			if space := strings.LastIndex(string(chars[:columns]), " "); space > 0 {
				end = utf8.RuneCountInString(string(chars[:columns])[:space])
			}
			lines = append(lines, string(chars[:end]))
			chars = []rune(strings.TrimSpace(string(chars[end:])))
		}
		lines = append(lines, string(chars))
	}
	if len(lines) > 3 {
		lines = lines[:3]
		last := []rune(lines[2])
		if len(last) >= columns {
			last = last[:max(0, columns-1)]
		}
		lines[2] = string(last) + ">"
	}
	return lines
}

// Clone feedback so later mutation cannot change render history
func CloneFeedback(source map[string]any) map[string]any {
	next := make(map[string]any, len(source))
	maps.Copy(next, source)
	return next
}

// Compare feedback with consistent JSON numeric semantics
func FeedbackEqual(a, b any) bool {
	switch value := a.(type) {
	case int:
		if other, ok := b.(int); ok {
			return value == other
		}
	case float64:
		if other, ok := b.(float64); ok {
			return value == other
		}
	}
	// Compare JSON number spellings without converting integers to float64
	// That would incorrectly merge distinct values above 2^53
	if left, ok := feedbackNumber(a); ok {
		right, ok := feedbackNumber(b)
		if !ok {
			return false
		}
		if left == right {
			return true
		}
		ld, le, lok := decimalParts(left)
		rd, re, rok := decimalParts(right)
		return lok && rok && ld == rd && le == re
	}
	switch a := a.(type) {
	case nil:
		return b == nil
	case string:
		v, ok := b.(string)
		return ok && a == v
	case bool:
		v, ok := b.(bool)
		return ok && a == v
	case [4]int:
		v, ok := b.([4]int)
		return ok && a == v
	case []any:
		v, ok := b.([]any)
		if !ok || len(a) != len(v) {
			return false
		}
		for i := range a {
			if !FeedbackEqual(a[i], v[i]) {
				return false
			}
		}
		return true
	case map[string]any:
		v, ok := b.(map[string]any)
		if !ok || len(a) != len(v) {
			return false
		}
		for key, value := range a {
			other, ok := v[key]
			if !ok || !FeedbackEqual(value, other) {
				return false
			}
		}
		return true
	}
	return reflect.DeepEqual(a, b)
}

// Normalize supported numeric values for feedback comparison
func feedbackNumber(value any) (string, bool) {
	switch value.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64, float32, float64, json.Number:
		encoded, err := json.Marshal(value)
		return string(encoded), err == nil
	default:
		return "", false
	}
}

// Normalize decimal digits and scale, without allocating exponent-sized buffers
// JSON Number permits arbitrary exponents; unrepresentable scales fail closed
func decimalParts(value string) (string, int64, bool) {
	negative := strings.HasPrefix(value, "-")
	value = strings.TrimPrefix(value, "-")
	exponent := int64(0)
	if at := strings.IndexAny(value, "eE"); at >= 0 {
		var err error
		exponent, err = strconv.ParseInt(value[at+1:], 10, 32)
		if err != nil {
			return "", 0, false
		}
		value = value[:at]
	}
	if at := strings.IndexByte(value, '.'); at >= 0 {
		exponent -= int64(len(value) - at - 1)
		value = value[:at] + value[at+1:]
	}
	value = strings.TrimLeft(value, "0")
	if value == "" {
		return "0", 0, true
	}
	digits := strings.TrimRight(value, "0")
	exponent += int64(len(value) - len(digits))
	if negative {
		digits = "-" + digits
	}
	return digits, exponent, true
}

// Convert a job result to concise display text
func ResultText(value any) string {
	data, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprint(value)
	}
	return string(data)
}

// Choose a themed variant of an asset path
func ThemeAsset(path, ThemeName string) string {
	ext := ".svg"
	if !strings.HasSuffix(path, ext) {
		return path
	}
	return strings.TrimSuffix(path, ext) + "-" + ThemeName + ext
}

// Convert status and visual preferences into a display
func StatusDisplay(status DisplayStatus, surface *sdk.Surface, visual Visual, theme string) Display {
	if title := []rune(status.Text); len(title) > 500 {
		status.Text = string(title[:500])
	}
	state := status.State
	if state < 0 || state >= len(visual.States) {
		state = 0
	}
	icon := visual.Icon
	if len(visual.States) > 1 {
		icon = visual.States[state]
	}

	if status.Icon != "" {
		icon = status.Icon
	}
	if status.Unavailable || icon == "" {
		icon = "icons/unavailable"
		state = 0
	}
	view := Display{Layout: "display/key.json", Image: themedAsset(icon+".svg", theme), Title: status.Text, State: state}
	width, height := 112, 112
	if surface != nil && surface.Width > 0 && surface.Height > 0 {
		width, height = min(16384, surface.Width), min(16384, surface.Height)
	}
	textWidth := width * 192 / 200
	iconSize := min(width*60/100, height*43/100)
	iconX, iconY := (width-iconSize)/2, 0
	if width > height*13/10 {
		view.Layout = "display/wide.json"
		textWidth = width * 120 / 200
		iconSize = min(width*36/100, height*80/100)
		iconX, iconY = width*2/100, (height-iconSize)/2
	}
	view.Feedback = map[string]any{"icon": map[string]any{"value": view.Image, "rect": [4]int{iconX * 200 / width, iconY * 100 / height, iconSize * 200 / width, iconSize * 100 / height}}, "indicator": map[string]any{"rect": [4]int{0, 0, 0, 0}}}
	scale := min(6, max(1, height/100))
	lines := DisplayLines(status.Text, max(1, textWidth/(8*scale)))
	for i := range 3 {
		line := ""
		if i < len(lines) {
			line = lines[i]
		}
		view.Feedback[fmt.Sprintf("line%d", i)] = line
	}
	view.Layout = themedLayout(view.Layout, theme)
	return Present(view, status.Content, width, height)
}

// Build a binding display using its current surface and theme
func (e *Engine) BuildDisplayView(id string, status DisplayStatus) Display {
	b := e.Bindings[id]
	if status.Content == nil && e.Feature.Present != nil {
		content := e.Feature.Present(id, status)
		status.Content = &content
	}
	return StatusDisplay(status, b.Surface, e.Feature.Visuals[e.ActionKey(b.Action)], e.Theme)
}

// Resolve a themed asset while preserving unsupported path forms
func themedAsset(path, theme string) string {
	if theme == "" {
		return path
	}
	return ThemeAsset(path, theme)
}

// Resolve a theme-specific layout path
func themedLayout(path, theme string) string {
	if theme == "" {
		return path
	}
	return strings.TrimSuffix(path, ".json") + "-" + theme + ".json"
}

// Build and send the binding status display
func (e *Engine) SendDisplay(ctx context.Context, c *sdk.Client, id string, status DisplayStatus) error {
	if !e.CanUpdateView(ctx, id) {
		return nil
	}
	if e.Feature.FilterStatus != nil {
		status = e.Feature.FilterStatus(ctx, c, id, status)
	}
	if previous, known := e.bindingState(id).lastStatus(); known && statusEqual(previous, status) && !e.Feature.DynamicDisplay {
		if _, rendered := e.bindingState(id).lastDisplay(); rendered {
			return nil
		}
	}
	if err := e.SendDisplayView(ctx, c, id, e.DisplayView(id, status), true); err != nil {
		return err
	}
	if status.Content != nil {
		content := *status.Content
		status.Content = &content
	}
	e.bindingState(id).setLastStatus(status)
	return nil
}

// Build a display without sending host messages
func (e *Engine) DisplayView(id string, status DisplayStatus) Display {
	count := 0
	for offset := range status.Text {
		if count == 500 {
			status.Text = status.Text[:offset]
			break
		}
		count++
	}
	if e.Feature.View != nil {
		return e.Feature.View(id, status)
	}
	return e.BuildDisplayView(id, status)
}

// Drain completed background jobs on the owner loop
func (e *Engine) DrainJobs(ctx context.Context, c *sdk.Client) error {
	if e.Feature.Progress != nil {
		e.Feature.Progress()
	}
	for {
		select {
		case result := <-e.Completed:
			if err := e.ApplyJobResult(ctx, c, result); err != nil {
				return err
			}
		default:
			return nil
		}
	}
}

// Completion callbacks are attached to individual jobs, never selected by plugin identity
func (e *Engine) ApplyJobResult(ctx context.Context, c *sdk.Client, result JobResult) (err error) {
	job, ok := e.Jobs[result.ID]
	if !ok || job.Generation != result.Generation {
		return nil
	}
	defer func() {
		if failure := errors.Join(result.Err, err); failure != nil && e.Feature.ObserveFailure != nil {
			id := job.Owner
			if id == "" {
				id = result.ID
			}
			e.Feature.ObserveFailure(ctx, c, id, failure)
		}
	}()
	if job.StopCancel != nil {
		job.StopCancel()
	}
	if job.ActionCtx != nil {
		ctx = job.ActionCtx
	}
	if job.Complete != nil {
		defer func() { job.Complete(errors.Join(result.Err, err)) }()
	}
	// Flush immutable progress while the job generation is still live
	if e.Feature.Progress != nil {
		e.Feature.Progress()
	}
	delete(e.Jobs, result.ID)
	apply := job.Apply
	if apply == nil {
		apply = e.Feature.Apply
	}
	if apply != nil {
		if err = apply(ctx, c, result, job); err != nil {
			return err
		}
		return e.RenderViewTargets(ctx, c)
	}
	return nil
}

// Finish a job and settle its deferred action
func (e *Engine) FinishJob(ctx context.Context, c *sdk.Client, result JobResult, job BackgroundJob) error {

	text := result.Text
	if result.Err != nil {
		text = "Unavailable\n" + result.Err.Error()
	}
	e.ResultsText[result.ID] = text
	delete(e.Results, result.ID)

	destination := result.ID

	if result.Value != nil && destination != "" {
		diagnostic := result.Value
		if data, err := json.Marshal(diagnostic); err == nil && len(data) > 256*1024 {
			diagnostic = map[string]any{"message": "Preview exceeds 256 KiB; narrow the selected fields", "truncated": true}
		}
		reply := sdk.Message("sendToPropertyInspector", destination, diagnostic)
		reply.Action = e.Bindings[destination].Action
		if err := c.Send(ctx, reply); err != nil {
			return err
		}
	}
	if _, ok := e.Bindings[result.ID]; ok {
		if err := e.Render(ctx, c, result.ID); err != nil {
			return err
		}
	}

	return nil
}

// Handle shared inspector requests before feature-specific requests
func (e *Engine) InspectBasic(ctx context.Context, c *sdk.Client, ev sdk.Event) (bool, error) {
	var p struct {
		Op string `json:"op"`
	}
	_ = json.Unmarshal(ev.Payload, &p)
	var message string
	switch p.Op {
	case "discover":
		message = "This action has no runtime discovery"
	case "test":
		message = "Activate a configured action to start its session"
	default:
		return false, nil
	}
	reply := sdk.Message("sendToPropertyInspector", ev.Context, map[string]any{"message": message})
	reply.Action = ev.Action
	return true, c.Send(ctx, reply)
}

// Compare statuses without treating equivalent feedback as changed
func statusEqual(a, b DisplayStatus) bool {
	contentA, contentB := a.Content, b.Content
	a.Content, b.Content = nil, nil
	return a == b && (contentA == nil && contentB == nil || contentA != nil && contentB != nil && *contentA == *contentB)
}
