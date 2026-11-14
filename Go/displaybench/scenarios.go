// Scenario fixtures extend correctness coverage without changing the v1 displaybench contract
package displaybench

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"reflect"
	"runtime"
	"sort"
	"sync/atomic"
	"time"

	"github.com/StreamDockManager/Plugin-SDK/Go"
	engine "github.com/StreamDockManager/Plugin-SDK/Go/authoring"
	"github.com/StreamDockManager/Plugin-SDK/Go/internal/benchmarkclock"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

type ScenarioOptions struct {
	Cycles   int
	Surfaces int
}

// Validate lifecycle benchmark options
func (o ScenarioOptions) Validate() error {
	return (Options{Frames: o.Cycles, Surfaces: o.Surfaces}).Validate()
}

var scenarioNames = [...]string{"multi_field_burst", "unchanged", "single_field", "alert_then_normal", "removed_context", "rebound_context", "sustained_update"}

type ScenarioCell struct {
	Name      string  `json:"name"`
	Requests  int     `json:"requests"`
	Events    uint64  `json:"events"`
	LatencyMS Latency `json:"latency_ms"`
}
type ScenarioTrace struct {
	Step      string      `json:"step"`
	Lifecycle []sdk.Event `json:"lifecycle,omitempty"`
	Events    []sdk.Event `json:"events"`
}
type ScenarioReport struct {
	SchemaVersion       int             `json:"schema_version"`
	FixtureVersion      string          `json:"fixture_version"`
	MeasurementScope    string          `json:"measurement_scope"`
	SourceRevision      string          `json:"source_revision"`
	SourceDirty         string          `json:"source_dirty"`
	OS                  string          `json:"os"`
	Arch                string          `json:"arch"`
	GoVersion           string          `json:"go_version"`
	Cycles              int             `json:"cycles"`
	Surfaces            int             `json:"surfaces"`
	EngineInstances     int             `json:"engine_instances"`
	Received            uint64          `json:"received"`
	Expected            uint64          `json:"expected"`
	PayloadBytes        uint64          `json:"payload_bytes"`
	StartupMS           float64         `json:"startup_ms"`
	ElapsedMS           float64         `json:"elapsed_ms"`
	ThroughputPerSecond float64         `json:"throughput_per_second"`
	Scenarios           []ScenarioCell  `json:"scenarios"`
	FirstCycle          []ScenarioTrace `json:"first_cycle"`
	TranscriptSHA256    string          `json:"transcript_sha256"`
	Limitations         []string        `json:"limitations"`
}

// Generate a deterministic binding identifier for a fixture surface
func scenarioID(i int) string { return fmt.Sprintf("fixture-%d-surface-%02d", i%2, i) }

// Build the expected display for a lifecycle step
func scenarioView(cycle, step, i int) engine.Display {
	// The field values are deterministic; fixture strings are never read from disk
	v := engine.Display{Layout: fmt.Sprintf("display/fixture-%d.json", cycle), Image: fmt.Sprintf("icons/fixture-%d.svg", cycle), State: cycle % 2, Title: fmt.Sprintf("Cycle %04d", cycle), Feedback: map[string]any{"cycle": cycle, "surface": i}}
	if step == 2 {
		v.Title += " title"
	}
	if step >= 3 {
		v.Title += " normal"
	}
	if step == 6 {
		v.Title += " sustained"
		v.Feedback["update"] = "sustained"
	}
	return v
}

// Construct a lifecycle input for the selected surface
func lifecycle(step, i int) sdk.Event {
	event := "willDisappear"
	if step == 5 {
		event = "willAppear"
	}
	surface := &sdk.Surface{ControlID: fmt.Sprintf("key-%d", i), Width: 112, Height: 112}
	if i >= 10 {
		surface.ControlID = fmt.Sprintf("touch-%d", i-10)
		surface.Width = 200
		surface.Height = 100
	}
	return sdk.Event{Event: event, Context: scenarioID(i), Action: fmt.Sprintf("fixture.action-%d", i%2), Surface: surface}
}

// Produce the expected host messages for a scenario step
func expectedScenario(cycle, step, surfaces int) []sdk.Event {
	events := []sdk.Event{}
	for i := range surfaces {
		id := scenarioID(i)
		v := scenarioView(cycle, step, i)
		switch step {
		case 0, 5:
			events = append(events, sdk.Message("setFeedbackLayout", id, map[string]any{"layout": v.Layout}), sdk.Message("setState", id, map[string]any{"state": v.State}), sdk.Message("setImage", id, map[string]any{"image": v.Image, "target": 0}), sdk.Message("setTitle", id, map[string]any{"title": v.Title, "target": 0}), sdk.Message("setFeedback", id, v.Feedback))
		case 2:
			events = append(events, sdk.Message("setTitle", id, map[string]any{"title": v.Title, "target": 0}))
		case 3:
			events = append(events, sdk.Message("showAlert", id, map[string]any{}), sdk.Message("setTitle", id, map[string]any{"title": v.Title, "target": 0}))
		case 6:
			events = append(events, sdk.Message("setTitle", id, map[string]any{"title": v.Title, "target": 0}), sdk.Message("setFeedback", id, map[string]any{"update": "sustained"}))
		}
	}
	return events
}

type scenarioHandler struct {
	engines  [2]*engine.Engine
	surfaces int
	next     int
}

// Create the lifecycle scenario handler
func newScenarioHandler(surfaces int) *scenarioHandler {
	h := &scenarioHandler{surfaces: surfaces}
	for p := range h.engines {
		h.engines[p] = engine.New(engine.Definition{Slug: fmt.Sprintf("fixture-%d", p)}, "", func(*engine.Engine) engine.Feature {
			return engine.Feature{Render: func(context.Context, *sdk.Client, string) error { return nil }}
		})
	}
	for i := range surfaces {
		h.engines[i%2].Bindings[scenarioID(i)] = engine.Binding{Action: fmt.Sprintf("fixture.action-%d", i%2), Surface: lifecycle(5, i).Surface}
	}
	return h
}

// Apply a scenario input and render its resulting display
func (h *scenarioHandler) Handle(ctx context.Context, c *sdk.Client, event sdk.Event) error {
	var request struct {
		Cycle int `json:"cycle"`
		Step  int `json:"step"`
	}
	if event.Event != "benchmarkScenario" || event.Context != "" || event.Action != "" || event.UUID != "" || event.Device != "" || event.Surface != nil {
		return errors.New("unexpected scenario request")
	}
	fields := map[string]int{}
	if err := json.Unmarshal(event.Payload, &fields); err != nil {
		return err
	}
	cycle, hasCycle := fields["cycle"]
	step, hasStep := fields["step"]
	if len(fields) != 2 || !hasCycle || !hasStep {
		return errors.New("malformed scenario request")
	}
	request.Cycle, request.Step = cycle, step
	if request.Cycle < 0 || request.Cycle >= 1200 || request.Step < 0 || request.Step >= len(scenarioNames) || request.Cycle*len(scenarioNames)+request.Step != h.next {
		return errors.New("out-of-order scenario request")
	}
	h.next++
	for i := 0; i < h.surfaces; i++ {
		e := h.engines[i%2]
		id := scenarioID(i)
		if request.Step == 4 || request.Step == 5 {
			if err := e.Handle(ctx, c, lifecycle(request.Step, i)); err != nil {
				return err
			}
		}
		if request.Step == 3 {
			if err := c.Send(ctx, sdk.Message("showAlert", id, map[string]any{})); err != nil {
				return err
			}
		}
		view := scenarioView(request.Cycle, request.Step, i)
		// Two identical producer calls per revision stress immutable cached state
		for range 2 {
			if err := e.SendDisplayView(ctx, c, id, view, true); err != nil {
				return err
			}
		}
	}
	return c.Send(ctx, sdk.Message("benchmarkScenarioComplete", "", request))
}

// Keep lifecycle scenarios event-driven
func (*scenarioHandler) Poll(context.Context, *sdk.Client) error { return nil }

// Close the lifecycle scenario engine
func (h *scenarioHandler) Close() error {
	for _, e := range h.engines {
		if err := e.Close(); err != nil {
			return err
		}
	}
	return nil
}

// Disable implicit scenario polling
func (*scenarioHandler) NextPoll(time.Time) time.Time { return time.Time{} }

// Expose no asynchronous scenario wake channel
func (*scenarioHandler) Wake() <-chan struct{} { return nil }

// Run the lifecycle and display-diff benchmark
func RunScenarios(parent context.Context, options ScenarioOptions, sourceRevision, sourceDirty string) (ScenarioReport, error) {
	if err := options.Validate(); err != nil {
		return ScenarioReport{}, err
	}
	if err := parent.Err(); err != nil {
		return ScenarioReport{}, err
	}
	clock, err := benchmarkclock.New()
	if err != nil {
		return ScenarioReport{}, err
	}
	initialized, err := clock.Now()
	if err != nil {
		return ScenarioReport{}, err
	}
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return ScenarioReport{}, err
	}
	defer listener.Close()
	var tokenBytes [32]byte
	if _, err = rand.Read(tokenBytes[:]); err != nil {
		return ScenarioReport{}, err
	}
	token := hex.EncodeToString(tokenBytes[:])
	type result struct {
		report ScenarioReport
		err    error
	}
	results := make(chan result, 1)
	mux := http.NewServeMux()
	var claimed atomic.Bool
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		if !claimed.CompareAndSwap(false, true) {
			http.Error(w, "fixture already connected", http.StatusServiceUnavailable)
			return
		}
		if r.Header.Get("Origin") != "" {
			http.Error(w, "origin not allowed", http.StatusForbidden)
			return
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			results <- result{err: err}
			return
		}
		defer conn.CloseNow()
		stop := context.AfterFunc(ctx, func() { _ = conn.CloseNow() })
		defer stop()
		conn.SetReadLimit(64 << 10)
		var reg sdk.Event
		if err = wsjson.Read(ctx, conn, &reg); err != nil {
			results <- result{err: err}
			return
		}
		if reg.Event != "registerPlugin" || reg.UUID != token {
			results <- result{err: errors.New("invalid fixture registration")}
			return
		}
		report, err := measureScenarios(ctx, conn, options, sourceRevision, sourceDirty, clock, initialized)
		if err == nil {
			err = wsjson.Write(ctx, conn, sdk.Event{Event: "shutdown"})
		}
		results <- result{report, err}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second, MaxHeaderBytes: 4096}
	defer server.Close()
	go func() { _ = server.Serve(listener) }()
	host, err := sdk.ParseHostInfo([]byte(`{"streamDockManager":{"contract_version":1,"capabilities":["plugin-protocol-v1"]}}`))
	if err != nil {
		return ScenarioReport{}, err
	}
	done := make(chan error, 1)
	go func() {
		done <- sdk.ConnectWithInfo(ctx, "ws://"+listener.Addr().String(), token, newScenarioHandler(options.Surfaces), host)
	}()
	select {
	case <-ctx.Done():
		return ScenarioReport{}, ctx.Err()
	case result := <-results:
		if result.err != nil {
			return ScenarioReport{}, result.err
		}
		select {
		case err := <-done:
			if err != nil {
				return ScenarioReport{}, err
			}
		case <-ctx.Done():
			return ScenarioReport{}, ctx.Err()
		}
		return result.report, nil
	case err := <-done:
		// A connect/handler failure may beat the HTTP reader's error notification
		if err == nil {
			select {
			case result := <-results:
				return result.report, result.err
			case <-ctx.Done():
				return ScenarioReport{}, ctx.Err()
			}
		}
		return ScenarioReport{}, err
	}
}

// Compare observed and expected scenario events
func scenarioEventEqual(got, want sdk.Event) bool {
	if got.Event != want.Event || got.Context != want.Context || got.Action != "" || got.Device != "" || got.UUID != "" || got.Surface != nil {
		return false
	}
	var a, b any
	return json.Unmarshal(got.Payload, &a) == nil && json.Unmarshal(want.Payload, &b) == nil && reflect.DeepEqual(a, b)
}

// Measure scenario traffic and validate its expected output
func measureScenarios(ctx context.Context, conn *websocket.Conn, o ScenarioOptions, revision, dirty string, clock *benchmarkclock.Clock, initialized int64) (ScenarioReport, error) {
	report := ScenarioReport{SchemaVersion: 1, FixtureVersion: "1", MeasurementScope: "plugin_engine_sdk_scenarios", SourceRevision: revision, SourceDirty: dirty, OS: runtime.GOOS, Arch: runtime.GOARCH, GoVersion: runtime.Version(), Cycles: o.Cycles, Surfaces: o.Surfaces, EngineInstances: 2, Expected: uint64(o.Cycles * o.Surfaces * 15), Limitations: []string{
		"Real engine/SDK loopback with two isolated engine instances in one synthetic process/SDK connection, not two installed plugin processes.",
		"Transient alert ordering is verified, not Manager badge duration or optical visibility. Removed/rebound context lifecycle uses the engine; stale Manager-generation rejection requires Manager tests.",
		"Sequential bounded request/barrier cycles; at most70 visual events plus one barrier per request and1200 latency samples per scenario. Sustained means repeated finite cycles, not unbounded saturation.",
		"No host actions, external services, images read from disk, hardware transport or optical FPS. First-cycle transcript is bounded deterministic synthetic data.",
	}}
	samples := make([][]float64, len(scenarioNames))
	for _, name := range scenarioNames {
		report.Scenarios = append(report.Scenarios, ScenarioCell{Name: name})
	}
	started, err := clock.Now()
	if err != nil {
		return ScenarioReport{}, err
	}
	report.StartupMS, err = clock.BetweenMS(initialized, started)
	if err != nil {
		return ScenarioReport{}, err
	}
	for cycle := 0; cycle < o.Cycles; cycle++ {
		for step, name := range scenarioNames {
			want := expectedScenario(cycle, step, o.Surfaces)
			at, err := clock.Now()
			if err != nil {
				return ScenarioReport{}, err
			}
			if err := wsjson.Write(ctx, conn, sdk.Message("benchmarkScenario", "", map[string]int{"cycle": cycle, "step": step})); err != nil {
				return ScenarioReport{}, err
			}
			trace := ScenarioTrace{Step: name, Events: []sdk.Event{}}
			if cycle == 0 && (step == 4 || step == 5) {
				for i := 0; i < o.Surfaces; i++ {
					trace.Lifecycle = append(trace.Lifecycle, lifecycle(step, i))
				}
			}
			for index := 0; index <= len(want); index++ {
				_, data, err := conn.Read(ctx)
				if err != nil {
					return ScenarioReport{}, err
				}
				var got sdk.Event
				if err = json.Unmarshal(data, &got); err != nil {
					return ScenarioReport{}, err
				}
				if index == len(want) {
					barrier := sdk.Message("benchmarkScenarioComplete", "", map[string]int{"cycle": cycle, "step": step})
					if !scenarioEventEqual(got, barrier) {
						return ScenarioReport{}, fmt.Errorf("missing/malformed barrier at cycle%d step%s", cycle, name)
					}
				} else {
					if !scenarioEventEqual(got, want[index]) {
						return ScenarioReport{}, fmt.Errorf("unexpected/duplicate/stale event at cycle%d step%s index%d", cycle, name, index)
					}
					report.Received++
					report.PayloadBytes += uint64(len(data))
					report.Scenarios[step].Events++
					if cycle == 0 {
						trace.Events = append(trace.Events, got)
					}
				}
			}
			if cycle == 0 {
				report.FirstCycle = append(report.FirstCycle, trace)
			}
			report.Scenarios[step].Requests++
			sample, err := clock.SinceMS(at)
			if err != nil {
				return ScenarioReport{}, err
			}
			samples[step] = append(samples[step], sample)
		}
	}
	report.ElapsedMS, err = clock.ElapsedMS(started)
	if err != nil {
		return ScenarioReport{}, err
	}
	report.ThroughputPerSecond = float64(report.Received) * 1000 / report.ElapsedMS
	for i, values := range samples {
		sort.Float64s(values)
		p := func(n int) float64 { return values[(len(values)*n+99)/100-1] }
		report.Scenarios[i].LatencyMS = Latency{p(50), p(95), p(99)}
	}
	if report.Received != report.Expected {
		return ScenarioReport{}, errors.New("scenario event total mismatch")
	}
	trace, _ := json.Marshal(report.FirstCycle)
	report.TranscriptSHA256 = fmt.Sprintf("%x", sha256.Sum256(trace))
	return report, nil
}
