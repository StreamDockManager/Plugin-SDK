package authoring_test

import (
	"context"
	"encoding/json"
	sdk "github.com/StreamDockManager/Plugin-SDK/Go"
	"github.com/StreamDockManager/Plugin-SDK/Go/authoring"
	"github.com/StreamDockManager/Plugin-SDK/Go/httpclient"
	"github.com/StreamDockManager/Plugin-SDK/Go/polling"
	"github.com/StreamDockManager/Plugin-SDK/Go/settings"
	"github.com/StreamDockManager/Plugin-SDK/Go/storage"
	"github.com/StreamDockManager/Plugin-SDK/Go/testkit"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"testing"
	"time"
)

// Load the shared fixture used by this subsystem test
func fixture(t *testing.T) map[string]json.RawMessage {
	t.Helper()
	data, err := os.ReadFile("../testdata/authoring-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var f map[string]json.RawMessage
	if err = json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	return f
}

// Round-trip a value through JSON for fixture comparison
func normalized(value any) any {
	data, _ := json.Marshal(value)
	var out any
	_ = json.Unmarshal(data, &out)
	return out
}

// Verify shared authoring fixtures
func TestSharedAuthoringFixtures(t *testing.T) {
	f := fixture(t)
	var fields []settings.Field
	_ = json.Unmarshal(f["fields"], &fields)
	var cases []struct {
		Input    map[string]any
		Valid    bool
		Expected map[string]any
	}
	_ = json.Unmarshal(f["settings"], &cases)
	for _, test := range cases {
		got, err := settings.Validate(fields, test.Input)
		if (err == nil) != test.Valid {
			t.Fatalf("settings %v: %v", test.Input, err)
		}
		if test.Valid && !reflect.DeepEqual(normalized(got), normalized(test.Expected)) {
			t.Fatal(got)
		}
	}
	var invalidSchemas [][]settings.Field
	_ = json.Unmarshal(f["invalid_schemas"], &invalidSchemas)
	for _, fields := range invalidSchemas {
		if _, err := settings.Validate(fields, nil); err == nil {
			t.Fatal("invalid schema accepted", fields)
		}
	}

	var displays []struct {
		Name     string
		Previous *authoring.Display
		Desired  authoring.Display
		Rich     bool
		Expected []any
	}
	_ = json.Unmarshal(f["displays"], &displays)
	for _, test := range displays {
		got := authoring.DisplayMessages("a", test.Previous, test.Desired, test.Rich, true)
		if !reflect.DeepEqual(normalized(got), normalized(test.Expected)) {
			t.Fatalf("%s: got %#v expected %#v", test.Name, normalized(got), test.Expected)
		}
	}
	var retries []struct {
		Interval int64 `json:"interval_ms"`
		Failures int
		Jitter   float64
		Expected int64 `json:"expected_ms"`
	}
	_ = json.Unmarshal(f["retries"], &retries)
	for _, test := range retries {
		if got := polling.RetryDelay(time.Duration(test.Interval)*time.Millisecond, test.Failures, test.Jitter).Milliseconds(); got != test.Expected {
			t.Fatal(got, test)
		}
	}
	var paths []struct {
		Path  string
		Root  any
		Found bool
		Value any
	}
	_ = json.Unmarshal(f["paths"], &paths)
	for _, test := range paths {
		got, found, err := httpclient.ExtractPath(test.Root, test.Path)
		if err != nil || found != test.Found || !reflect.DeepEqual(got, test.Value) {
			t.Fatal(test.Path, got, found, err)
		}
	}
	var invalid []string
	_ = json.Unmarshal(f["invalid_paths"], &invalid)
	for _, path := range invalid {
		if _, err := httpclient.ParsePath(path); err == nil {
			t.Fatal("accepted invalid path", path)
		}
	}
}

// Verify state debounce rejection and failure retention
func TestStateDebounceRejectionAndFailureRetention(t *testing.T) {
	clock := testkit.NewClock(time.Unix(1, 0))
	path := filepath.Join(t.TempDir(), "state.json")
	store := storage.New(path, 1)
	store.Change(map[string]any{"count": 1}, clock.Now())
	if err := store.Flush(clock.Now(), false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("early write")
	}
	clock.Advance(250 * time.Millisecond)
	if err := store.Flush(clock.Now(), false); err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	if err := storage.New(path, 1).Load(&value); err != nil || value["count"] != float64(1) {
		t.Fatal(value, err)
	}
	if _, err := storage.Write(path, map[string]any{"legacy": true}); err != nil {
		t.Fatal(err)
	}
	if err := storage.New(path, 1).Load(&value); err == nil {
		t.Fatal("legacy silently loaded")
	}
	rejected := storage.New(path, 1)
	if err := rejected.Load(&value); err == nil {
		t.Fatal("obsolete state accepted")
	}
	rejected.Change(value, clock.Now())
	if err := rejected.Close(); err == nil {
		t.Fatal("rejected state overwritten")
	}
	blocked := storage.New(filepath.Join(path, "child"), 1)
	blocked.Change(value, clock.Now())
	if err := blocked.Close(); err == nil || !blocked.Dirty {
		t.Fatal("failed write cleared dirty state")
	}
}

// Verify resource sharing rejects retired results
func TestResourceSharingRejectsRetiredResults(t *testing.T) {
	resources := polling.NewResources()
	now := time.Time{}
	_ = resources.Watch("a", "resource", time.Second)
	_ = resources.Watch("b", "resource", time.Second*2)
	if got := resources.Due(now); !reflect.DeepEqual(got, []string{"resource"}) {
		t.Fatal(got)
	}
	generation, _ := resources.Begin("resource")
	resources.Unwatch("a")
	resources.Unwatch("b")
	_ = resources.Watch("b", "resource", time.Second)
	replacement, _ := resources.Begin("resource")
	if generation == replacement || resources.Complete("resource", generation, 42, false, now, 0, time.Time{}) {
		t.Fatal("stale result applied")
	}
	if !resources.Complete("resource", replacement, 42, false, now, 0, time.Time{}) {
		t.Fatal("replacement rejected")
	}
	generation, _ = resources.Begin("resource")
	resources.Complete("resource", generation, nil, true, now.Add(time.Second), 0, time.Time{})
	snapshot, _ := resources.Snapshot("b")
	if !snapshot.Stale || snapshot.LastValue != 42 {
		t.Fatal("lost sample from zero-valued fake clock", snapshot)
	}

}

// Verify offline kit uses real handlers and scoped lifecycle
func TestOfflineKitUsesRealHandlersAndScopedLifecycle(t *testing.T) {
	var info sdk.HostInfo
	data, _ := os.ReadFile("../testdata/protocol-v1.json")
	var hosts map[string]json.RawMessage
	_ = json.Unmarshal(data, &hosts)
	info, _ = sdk.ParseHostInfo(hosts["manager"])
	recorder := testkit.New(info)
	engine := authoring.New(authoring.Definition{}, "", nil)
	defer engine.Close()
	calls := 0
	if err := engine.Register("org.example.action", authoring.ActionHooks{Input: func(context.Context, *sdk.Client, sdk.Event, authoring.Binding) error { calls++; return nil }, Render: func(context.Context, *sdk.Client, string, authoring.Binding) (authoring.Display, error) {
		return authoring.Display{Title: "Ready"}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	if err := recorder.Replay(context.Background(), engine, []sdk.Event{{Event: "willAppear", Action: "org.example.action", Context: "a"}, {Event: "keyDown", Action: "org.example.action", Context: "a"}, {Event: "willDisappear", Context: "a"}}); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(engine.Bindings) != 0 {
		t.Fatal(calls, engine.Bindings)
	}
	if len(recorder.Drain()) == 0 {
		t.Fatal("no recorded writes")
	}
}

// Verify appearance lifecycle incarnations
func TestAppearanceLifecycleIncarnations(t *testing.T) {
	var spec struct {
		Action   string
		Settings map[string]any
		Steps    []struct {
			Event         sdk.Event
			Bindings      []string
			RenderContext *string `json:"render_context"`
		}
	}
	if err := json.Unmarshal(fixture(t)["appearance_lifecycle"], &spec); err != nil {
		t.Fatal(err)
	}
	recorder := testkit.New(sdk.HostInfo{})
	engine := authoring.New(authoring.Definition{}, "", nil)
	defer engine.Close()
	if err := engine.Register(spec.Action, authoring.ActionHooks{Render: func(_ context.Context, _ *sdk.Client, _ string, binding authoring.Binding) (authoring.Display, error) {
		if !reflect.DeepEqual(normalized(binding.Settings), normalized(spec.Settings)) {
			t.Fatal("settings changed", binding.Settings)
		}
		return authoring.Display{Title: "Ready"}, nil
	}}); err != nil {
		t.Fatal(err)
	}
	for _, step := range spec.Steps {
		if err := recorder.Replay(context.Background(), engine, []sdk.Event{step.Event}); err != nil {
			t.Fatal(err)
		}
		contexts := []string{}
		for id := range engine.Bindings {
			contexts = append(contexts, id)
		}
		sort.Strings(contexts)
		if !reflect.DeepEqual(contexts, step.Bindings) {
			t.Fatal(contexts, step.Bindings)
		}
		writes := recorder.Drain()
		titles := []string{}
		for _, message := range writes {
			if message.Event == "setTitle" {
				titles = append(titles, message.Context)
			}
		}
		if step.RenderContext == nil {
			if len(titles) != 0 {
				t.Fatal(titles)
			}
		} else if !reflect.DeepEqual(titles, []string{*step.RenderContext}) {
			t.Fatal(titles)
		}
	}
}

// Verify refresh policy and independent deadlines
func TestRefreshPolicyAndIndependentDeadlines(t *testing.T) {
	var fixture struct {
		Policy polling.RefreshPolicy `json:"policy"`
		Cases  []struct {
			Settings map[string]any `json:"settings"`
			Valid    bool           `json:"valid"`
			Expected int64          `json:"expected_ms"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(fixtureData(t), &fixture); err != nil {
		t.Fatal(err)
	}
	for _, c := range fixture.Cases {
		value, err := fixture.Policy.Resolve(c.Settings)
		if (err == nil) != c.Valid || (c.Valid && value.Milliseconds() != c.Expected) {
			t.Fatal(c, value, err)
		}
	}
	resources := polling.NewResources()
	now := time.Unix(100, 0)
	resources.Watch("fast", "shared", time.Second)
	resources.Watch("slow", "shared", 10*time.Second)
	resources.Watch("off", "shared", 0)
	generation, _ := resources.Begin("shared")
	resources.Complete("shared", generation, 1, false, now, 0, time.Time{})
	generation, _ = resources.Begin("shared")
	resources.Complete("shared", generation, 2, false, now.Add(time.Second), 0, time.Time{})
	fast, _ := resources.Snapshot("fast")
	slow, _ := resources.Snapshot("slow")
	off, _ := resources.Snapshot("off")
	if fast.LastValue != 2 || slow.LastValue != 1 || off.LastValue != 1 {
		t.Fatal(fast, slow, off)
	}
	resources.Unwatch("fast")
	resources.Unwatch("slow")
	if len(resources.Due(now.Add(time.Hour))) != 0 {
		t.Fatal("Off scheduled a read")
	}
	state := resources.States["shared"]
	state.Next = time.Time{}
	resources.States["shared"] = state
	generation, _ = resources.Begin("shared")
	resources.Complete("shared", generation, 3, false, now.Add(time.Hour), 0, time.Time{})
	off, _ = resources.Snapshot("off")
	if off.LastValue != 3 {
		t.Fatal("manual/event refresh did not update Off", off)
	}
	state = resources.States["shared"]
	state.Next = time.Time{}
	resources.States["shared"] = state
	resources.Watch("off", "shared", 0)
	if len(resources.Due(now.Add(time.Hour))) != 1 {
		t.Fatal("watch lost manual invalidation")
	}
	generation, _ = resources.Begin("shared")
	cooldown := now.Add(2 * time.Hour)
	resources.Complete("shared", generation, nil, true, now.Add(time.Hour), 0, cooldown)
	state = resources.States["shared"]
	state.Next = time.Time{}
	resources.States["shared"] = state
	resources.Watch("new", "shared", time.Second)
	if len(resources.Due(now.Add(time.Hour))) != 0 || !resources.Deadline("shared").Equal(cooldown) {
		t.Fatal("refresh bypassed cooldown")
	}
	generation, _ = resources.Begin("shared")
	resources.Complete("shared", generation, 4, false, cooldown, 0, cooldown.Add(time.Minute))
	state = resources.States["shared"]
	state.Next = time.Time{}
	resources.States["shared"] = state
	if len(resources.Due(cooldown)) != 0 {
		t.Fatal("successful response lost Retry-After")
	}

}

// Select the refresh-policy cases from the shared fixture
func fixtureData(t *testing.T) json.RawMessage { return fixture(t)["refresh_policy"] }
