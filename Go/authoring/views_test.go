package authoring

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	"github.com/StreamDockManager/Plugin-SDK/Go"
)

// Create an engine that records permitted view updates
func viewFixture(t *testing.T) (*Engine, *[]string) {
	t.Helper()
	e := fixture(t)
	t.Cleanup(func() { _ = e.Close() })
	e.ViewTargets = map[string]ViewTarget{
		"start":  {Action: "start", UpdatedBy: []string{"start", "stop"}, Live: true, AfterActivation: true},
		"stop":   {Action: "stop", UpdatedBy: []string{"stop"}},
		"status": {Action: "status", UpdatedBy: []string{"start", "stop"}, Live: true},
	}
	var rendered []string
	e.Feature.Render = func(_ context.Context, _ *sdk.Client, id string) error {
		rendered = append(rendered, id)
		return nil
	}
	e.Feature.Action = func(ctx context.Context, c *sdk.Client, _ sdk.Event, _ Binding) error {
		// Deliberately try the old broadcast path: the engine must contain it
		for id := range e.Bindings {
			if err := e.Render(ctx, c, id); err != nil {
				return err
			}
		}
		return nil
	}
	return e, &rendered
}

// Verify view routing across keys dials touch and copies
func TestViewRoutingAcrossKeysDialsTouchAndCopies(t *testing.T) {
	for _, kind := range []string{"keyDown", "dialDown", "dialRotate", "touchTap"} {
		t.Run(kind, func(t *testing.T) {
			e, rendered := viewFixture(t)
			for id, action := range map[string]string{"key": "start", "duplicate": "start", "dial": "stop", "touch": "status"} {
				if err := e.Handle(context.Background(), nil, sdk.Event{Event: "willAppear", Context: id, Action: action}); err != nil {
					t.Fatal(err)
				}
			}
			if !reflect.DeepEqual(*rendered, []string{"touch"}) {
				t.Fatal("unused command controls must keep primary views", *rendered)
			}
			*rendered = nil
			if err := e.Handle(context.Background(), nil, sdk.Event{Event: kind, Context: "key", Action: "start"}); err != nil {
				t.Fatal(err)
			}
			if !slices.Contains(*rendered, "key") || !slices.Contains(*rendered, "touch") || slices.Contains(*rendered, "duplicate") || slices.Contains(*rendered, "dial") {
				t.Fatal("action escaped declared targets", *rendered)
			}
			*rendered = nil
			if err := e.Poll(context.Background(), nil); err != nil {
				t.Fatal(err)
			}
			slices.Sort(*rendered)
			if !reflect.DeepEqual(*rendered, []string{"key", "touch"}) {
				t.Fatal("polling changed unused controls", *rendered)
			}
		})
	}
}

// Verify shared view targets require explicit matching identity
func TestSharedViewTargetsRequireExplicitMatchingIdentity(t *testing.T) {
	e, _ := viewFixture(t)
	e.ViewTargets["start"] = ViewTarget{Action: "start", UpdatedBy: []string{"start", "stop"}, Shared: true}
	e.Bindings["source"] = Binding{Action: "start", Settings: map[string]any{"shared_id": "timer-a"}}
	e.Bindings["linked"] = Binding{Action: "start", Settings: map[string]any{"shared_id": "timer-a"}}
	e.Bindings["other"] = Binding{Action: "start", Settings: map[string]any{"shared_id": "timer-b"}}
	e.Bindings["implicit"] = Binding{Action: "start"}
	ctx := e.eventViewContext(context.Background(), sdk.Event{Event: "keyDown", Context: "source", Action: "start"})
	for _, id := range []string{"source", "linked", "other", "implicit", "gone"} {
		if got, want := e.CanUpdateView(ctx, id), id == "source" || id == "linked"; got != want {
			t.Fatalf("%s: allowed=%v, want %v", id, got, want)
		}
	}
	delete(e.Bindings, "source")
	if e.CanUpdateView(ctx, "linked") {
		t.Fatal("removed source kept permission to update a linked control")
	}
}

// Verify unauthorized direct display cannot change cached view
func TestUnauthorizedDirectDisplayCannotChangeCachedView(t *testing.T) {
	e, _ := viewFixture(t)
	e.Bindings["source"] = Binding{Action: "start"}
	e.Bindings["target"] = Binding{Action: "stop"}
	e.Last["target"] = Display{Title: "Previous result"}
	ctx := e.eventViewContext(context.Background(), sdk.Event{Event: "keyDown", Context: "source", Action: "start"})
	// A nil client also catches attempts to transmit a forbidden message
	if err := e.SendDisplayView(ctx, nil, "target", Display{Title: "Recording"}, true); err != nil {
		t.Fatal(err)
	}
	if err := e.SendDisplay(ctx, nil, "target", DisplayStatus{Text: "Recording"}); err != nil {
		t.Fatal(err)
	}
	if e.Last["target"].Title != "Previous result" || e.LastStatus["target"].Text != "" {
		t.Fatal("forbidden render poisoned display caches")
	}
}

// Verify retired or replaced origin cannot refresh another view
func TestRetiredOrReplacedOriginCannotRefreshAnotherView(t *testing.T) {
	e, _ := viewFixture(t)
	e.Bindings["source"] = Binding{Action: "stop"}
	e.Bindings["owner"] = Binding{Action: "start"}
	e.ActivateView("owner")
	ctx := e.eventViewContext(context.Background(), sdk.Event{Event: "keyDown", Context: "source", Action: "stop"})
	if !e.CanUpdateView(ctx, "owner") {
		t.Fatal("declared stop-to-start link rejected")
	}
	delete(e.Bindings, "source")
	if e.CanUpdateView(ctx, "owner") {
		t.Fatal("removed origin retained a live target")
	}
	e.Bindings["source"] = Binding{Action: "start"}
	if e.CanUpdateView(ctx, "owner") || e.CanUpdateView(ctx, "source") {
		t.Fatal("old origin scope crossed a replacement binding")
	}
}

// Verify job completion keeps original view scope and receipt
func TestJobCompletionKeepsOriginalViewScopeAndReceipt(t *testing.T) {
	e, rendered := viewFixture(t)
	e.Bindings["source"] = Binding{Action: "stop"}
	e.Bindings["owner"] = Binding{Action: "start"}
	e.Bindings["unused"] = Binding{Action: "start"}
	e.Bindings["other-stop"] = Binding{Action: "stop"}
	e.ActivateView("owner")
	ctx := e.eventViewContext(context.Background(), sdk.Event{Event: "keyDown", Context: "source", Action: "stop"})
	var receipts int
	want := errors.New("capture failed")
	e.Jobs["source"] = BackgroundJob{Generation: 2, ActionCtx: ctx, Cancel: func() {}, Complete: func(err error) {
		receipts++
		if !errors.Is(err, want) {
			t.Error(err)
		}
	}, Apply: func(ctx context.Context, c *sdk.Client, _ JobResult, _ BackgroundJob) error {
		for id := range e.Bindings {
			if err := e.Render(ctx, c, id); err != nil {
				return err
			}
		}
		return nil
	}}
	e.Completed = make(chan JobResult, 2)
	e.Completed <- JobResult{ID: "source", Generation: 1}
	e.Completed <- JobResult{ID: "source", Generation: 2, Err: want}
	if err := e.DrainJobs(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if receipts != 1 || !slices.Contains(*rendered, "source") || !slices.Contains(*rendered, "owner") || slices.Contains(*rendered, "unused") || slices.Contains(*rendered, "other-stop") {
		t.Fatal(receipts, *rendered)
	}
}

// Verify view activation survives settings but not rebinding
func TestViewActivationSurvivesSettingsButNotRebinding(t *testing.T) {
	e, rendered := viewFixture(t)
	e.Bindings["control"] = Binding{Action: "stop"}
	e.ActivateView("control")
	if err := e.Handle(context.Background(), nil, sdk.Event{Event: "didReceiveSettings", Context: "control", Action: "stop"}); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(*rendered, "control") {
		t.Fatal("settings did not update this control's feedback")
	}
	*rendered = nil
	var canceled bool
	e.Jobs["control"] = BackgroundJob{Cancel: func() { canceled = true }}
	if err := e.Handle(context.Background(), nil, sdk.Event{Event: "willAppear", Context: "control", Action: "start"}); err != nil {
		t.Fatal(err)
	}
	if !canceled || e.ViewIsActive("control") || len(*rendered) != 0 {
		t.Fatal("replacement inherited activation or pending work", canceled, *rendered)
	}
	e.ActivateView("control")
	if err := e.Handle(context.Background(), nil, sdk.Event{Event: "willDisappear", Context: "control", Action: "start"}); err != nil {
		t.Fatal(err)
	}
	if e.ViewIsActive("control") || e.CanUpdateView(context.Background(), "control") {
		t.Fatal("retired control remained an update target")
	}
}

// Verify queued work retains its activated control
func TestQueuedWorkRetainsItsActivatedControl(t *testing.T) {
	e, _ := viewFixture(t)
	e.Bindings["dial"] = Binding{Action: "stop"}
	e.Bindings["other"] = Binding{Action: "stop"}
	e.ActivateView("dial")
	e.Feature.Poll = func(ctx context.Context, _ *sdk.Client) (bool, error) {
		e.StartJob(ctx, "dial", func(ctx context.Context) (string, any, error) {
			<-ctx.Done()
			return "", nil, ctx.Err()
		}, nil)
		return true, nil
	}
	if err := e.Poll(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	job := e.Jobs["dial"]
	if job.ActionCtx == nil || !e.CanUpdateView(job.ActionCtx, "dial") || e.CanUpdateView(job.ActionCtx, "other") {
		t.Fatal("queued work lost its exact view owner")
	}
	e.CancelJob("dial")
}

// Verify retheme keeps command result and does not mutate cached feedback
func TestRethemeKeepsCommandResultAndDoesNotMutateCachedFeedback(t *testing.T) {
	e, _ := viewFixture(t)
	e.Theme = "light"
	view := Display{Title: "Resumed", Image: "icons/play-dark.svg", Layout: "display/key-dark.json", Feedback: map[string]any{"icon": map[string]any{"value": "icons/play-dark.svg"}, "line0": "Resumed"}}
	got := e.rethemeView(view)
	if got.Title != "Resumed" || got.Image != "icons/play-light.svg" || got.Layout != "display/key-light.json" || got.Feedback["icon"].(map[string]any)["value"] != got.Image {
		t.Fatal(got)
	}
	if view.Feedback["icon"].(map[string]any)["value"] != "icons/play-dark.svg" || !reflect.DeepEqual(e.rethemeView(got), got) {
		t.Fatal("theme updates mutated the cached original or duplicated suffixes")
	}
}

// Verify view target validation
func TestViewTargetValidation(t *testing.T) {
	for name, rules := range map[string][]ViewTarget{
		"missing":           {{Action: "start"}},
		"unknown target":    {{Action: "start"}, {Action: "other"}},
		"duplicate target":  {{Action: "start"}, {Action: "start"}},
		"unknown updater":   {{Action: "start", UpdatedBy: []string{"other"}}, {Action: "stop"}},
		"duplicate updater": {{Action: "start", UpdatedBy: []string{"stop", "stop"}}, {Action: "stop"}},
	} {
		t.Run(name, func(t *testing.T) {
			if err := ValidateViewTargets(Definition{Actions: []string{"start", "stop"}, ViewTargets: rules}); err == nil {
				t.Fatal("invalid view policy accepted")
			}
		})
	}
}
