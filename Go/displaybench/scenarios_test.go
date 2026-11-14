package displaybench

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"
	"time"

	"github.com/StreamDockManager/Plugin-SDK/Go"
)

// Verify scenario bounds and cancellation
func TestScenarioBoundsAndCancellation(t *testing.T) {
	for _, o := range []ScenarioOptions{{0, 1}, {1201, 1}, {1, 0}, {1, 15}} {
		if _, err := RunScenarios(context.Background(), o, "fixture", "true"); err == nil {
			t.Fatalf("accepted %+v", o)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RunScenarios(ctx, ScenarioOptions{1, 1}, "fixture", "true"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}

// Verify scenario engine SDK coverage
func TestScenarioEngineSDKCoverage(t *testing.T) {
	for _, surfaces := range []int{1, 2, 14} {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		r, err := RunScenarios(ctx, ScenarioOptions{Cycles: 3, Surfaces: surfaces}, "fixture-revision", "true")
		cancel()
		if err != nil {
			t.Fatal(err)
		}
		if r.Received != uint64(3*surfaces*15) || r.Received != r.Expected || r.MeasurementScope != "plugin_engine_sdk_scenarios" || len(r.Scenarios) != 7 || len(r.FirstCycle) != 7 || len(r.TranscriptSHA256) != 64 {
			t.Fatalf("bad scenario report: %+v", r)
		}
		if r.ElapsedMS <= 0 || r.ThroughputPerSecond <= 0 || r.StartupMS < 0 {
			t.Fatalf("invalid timing metrics: %+v", r)
		}
		if _, err := json.Marshal(r); err != nil {
			t.Fatalf("report cannot be encoded: %v", err)
		}
		for i, want := range []int{5, 0, 1, 2, 0, 5, 2} {
			if r.Scenarios[i].Events != uint64(3*surfaces*want) || r.Scenarios[i].Requests != 3 || len(r.FirstCycle[i].Events) != surfaces*want {
				t.Fatalf("bad scenario%d: %+v", i, r.Scenarios[i])
			}
			latency := r.Scenarios[i].LatencyMS
			if latency.P50 < 0 || latency.P95 < latency.P50 || latency.P99 < latency.P95 {
				t.Fatalf("invalid scenario%d latency: %+v", i, latency)
			}
		}
		// Alerts remain ordered ahead of normal titles; the fixture never claims visibility
		for i := range surfaces {
			events := r.FirstCycle[3].Events
			if events[2*i].Event != "showAlert" || events[2*i+1].Event != "setTitle" || events[2*i].Context != events[2*i+1].Context {
				t.Fatal("transient event reordered")
			}
		}
		if len(r.FirstCycle[4].Lifecycle) != surfaces || len(r.FirstCycle[5].Lifecycle) != surfaces {
			t.Fatal("missing lifecycle transcript")
		}
	}
}

// Verify scenario transcript is deterministic
func TestScenarioTranscriptIsDeterministic(t *testing.T) {
	var digest string
	for i := range 2 {
		r, err := RunScenarios(context.Background(), ScenarioOptions{2, 2}, "fixture", "true")
		if err != nil {
			t.Fatal(err)
		}
		if i > 0 && r.TranscriptSHA256 != digest {
			t.Fatal("nondeterministic transcript")
		}
		digest = r.TranscriptSHA256
	}
}

// Verify scenario rejects malformed and out of order requests
func TestScenarioRejectsMalformedAndOutOfOrderRequests(t *testing.T) {
	h := newScenarioHandler(2)
	defer h.Close()
	for _, ev := range []sdk.Event{sdk.Message("unexpected", "", nil), sdk.Message("benchmarkScenario", "", map[string]int{}), sdk.Message("benchmarkScenario", "", map[string]int{"cycle": 0, "step": 0, "extra": 1}), sdk.Message("benchmarkScenario", "", map[string]int{"cycle": 1, "step": 0}), sdk.Message("benchmarkScenario", "", map[string]int{"cycle": 0, "step": 7}), {Event: "benchmarkScenario", Payload: json.RawMessage("{")}} {
		if err := h.Handle(context.Background(), nil, ev); err == nil {
			t.Fatal("accepted invalid request")
		}
	}
}

// Verify scenario validator rejects stale and foreign events
func TestScenarioValidatorRejectsStaleAndForeignEvents(t *testing.T) {
	want := sdk.Message("setTitle", "fixture-0-surface-00", map[string]any{"title": "normal", "target": 0})
	for _, got := range []sdk.Event{sdk.Message("showAlert", want.Context, map[string]any{}), sdk.Message("setTitle", "foreign", map[string]any{"title": "normal", "target": 0}), sdk.Message("setTitle", want.Context, map[string]any{"title": "stale", "target": 0}), {Event: want.Event, Context: want.Context, Payload: want.Payload, UUID: "unexpected"}} {
		if scenarioEventEqual(got, want) {
			t.Fatal("accepted stale/foreign event")
		}
	}
}

// Verify scenario committed transcript
func TestScenarioCommittedTranscript(t *testing.T) {
	data, err := os.ReadFile("testdata/scenario-transcript.json")
	if err != nil {
		t.Fatal(err)
	}
	var golden struct {
		Digest string `json:"transcript_sha256"`
		Steps  any    `json:"steps"`
	}
	if err = json.Unmarshal(data, &golden); err != nil {
		t.Fatal(err)
	}
	r, err := RunScenarios(context.Background(), ScenarioOptions{1, 14}, "fixture", "true")
	if err != nil {
		t.Fatal(err)
	}
	data, err = json.Marshal(r.FirstCycle)
	if err != nil {
		t.Fatal(err)
	}
	var actual any
	if err = json.Unmarshal(data, &actual); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(actual, golden.Steps) || r.TranscriptSHA256 != golden.Digest {
		t.Fatal("committed transcript drift")
	}
}
