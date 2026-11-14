package authoring

import (
	"context"
	"testing"
	"time"
)

// Verify binding state uses live public maps
func TestBindingStateUsesLivePublicMaps(t *testing.T) {
	e := New(Definition{}, "", nil)
	state := e.bindingState("one")
	e.Effective["one"] = map[string]any{"value": "direct"}
	if got := state.effectiveSettings(nil)["value"]; got != "direct" {
		t.Fatalf("direct map write lost: %v", got)
	}
	// A retained view must also follow replacement maps, not capture old aliases
	e.Last = map[string]Display{"one": {Title: "replaced"}}
	if got, ok := state.lastDisplay(); !ok || got.Title != "replaced" {
		t.Fatalf("replacement map lost: %#v", got)
	}
	state.setLastDisplay(Display{Title: "view"})
	if e.Last["one"].Title != "view" {
		t.Fatal("view did not update public map")
	}
}

// Verify binding state clear preserves peer and lifecycle phases
func TestBindingStateClearPreservesPeerAndLifecyclePhases(t *testing.T) {
	e := New(Definition{}, "", nil)
	for _, id := range []string{"one", "peer"} {
		e.Bindings[id] = Binding{Action: "test"}
		e.Effective[id] = map[string]any{"value": id}
		e.Last[id] = Display{Title: id}
		e.LastStatus[id] = DisplayStatus{Text: id}
		e.Results[id] = id
		e.ResultsText[id] = id
		e.Checked[id] = time.Now()
		e.DisplayTicks[id] = 1
		e.Statuses[id] = SampledStatus{}
		e.ViewActive[id] = true
	}
	cancelled := false
	e.Jobs["one"] = BackgroundJob{Cancel: func() { cancelled = true }}
	e.Feature.Clear = func(id string) {
		if cancelled || e.Results[id] == nil {
			t.Fatal("feature cleanup reordered")
		}
	}
	e.ClearContext("one")
	if !cancelled {
		t.Fatal("binding job was not cancelled")
	}
	if e.Results["one"] != nil || e.Effective["one"] != nil || e.DisplayTicks["one"] != 0 {
		t.Fatal("binding transient state retained")
	}
	if _, ok := e.LastStatus["one"]; ok {
		t.Fatal("status retained")
	}
	if e.Results["peer"] != "peer" || e.Effective["peer"]["value"] != "peer" {
		t.Fatal("peer state cleared")
	}
	if e.Last["one"].Title != "one" || !e.ViewActive["one"] || e.Bindings["one"].Action != "test" {
		t.Fatal("later lifecycle phase cleared early")
	}
	// Close must clear aliases, not replace them, after cancelling remaining work
	last := e.Last
	e.Feature.Clear = nil
	ctx, cancel := context.WithCancel(context.Background())
	e.Jobs["peer"] = BackgroundJob{Cancel: cancel}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if ctx.Err() == nil || len(last) != 0 || len(e.Bindings) != 0 {
		t.Fatal("close retained state or work")
	}
}
