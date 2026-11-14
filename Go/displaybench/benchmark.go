// Package displaybench measures the real engine/SDK display path on loopback
// It deliberately imports no official plugin or operating-system action adapter
package displaybench

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
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

type Options struct{ Frames, Surfaces int }

// Validate benchmark dimensions and work limits
func (o Options) Validate() error {
	if o.Frames < 1 || o.Frames > 1200 {
		return errors.New("frames must be between 1 and 1200")
	}
	if o.Surfaces < 1 || o.Surfaces > 14 {
		return errors.New("surfaces must be between 1 and 14")
	}
	return nil
}

type Latency struct {
	P50 float64 `json:"p50"`
	P95 float64 `json:"p95"`
	P99 float64 `json:"p99"`
}
type Report struct {
	SchemaVersion                int      `json:"schema_version"`
	FixtureVersion               string   `json:"fixture_version"`
	SourceRevision               string   `json:"source_revision"`
	SourceDirty                  string   `json:"source_dirty"`
	MeasurementScope             string   `json:"measurement_scope"`
	Surfaces                     int      `json:"surfaces"`
	Frames                       int      `json:"frames"`
	Emitted                      uint64   `json:"emitted"`
	Received                     uint64   `json:"received"`
	PayloadBytes                 uint64   `json:"payload_bytes"`
	DisplayRevisions             uint64   `json:"display_revisions"`
	DuplicateRevisionsSuppressed uint64   `json:"duplicate_revisions_suppressed"`
	StartupMS                    float64  `json:"startup_ms"`
	ElapsedMS                    float64  `json:"elapsed_ms"`
	ThroughputPerSecond          float64  `json:"throughput_per_second"`
	LatencyMS                    Latency  `json:"latency_ms"`
	OS                           string   `json:"os"`
	Arch                         string   `json:"arch"`
	GoVersion                    string   `json:"go_version"`
	Limitations                  []string `json:"limitations"`
}

type frameHandler struct {
	engine   *engine.Engine
	surfaces int
	emitted  uint64
	feedback map[string]map[string]any
}

// Construct the deterministic frame benchmark handler
func newHandler(surfaces int) *frameHandler {
	e := engine.New(engine.Definition{Slug: "display-benchmark"}, "", func(*engine.Engine) engine.Feature { return engine.Feature{} })
	for i := range surfaces {
		id := fmt.Sprintf("surface-%02d", i)
		width, height := 112, 112
		if i >= 10 {
			width, height = 200, 100
		}
		e.Bindings[id] = engine.Binding{Action: "frame", Surface: &sdk.Surface{ControlID: id, Width: width, Height: height}}
	}
	return &frameHandler{engine: e, surfaces: surfaces, feedback: map[string]map[string]any{}}
}

// Render benchmark frames for host events
func (h *frameHandler) Handle(ctx context.Context, c *sdk.Client, event sdk.Event) error {
	if event.Event != "benchmarkFrame" {
		return errors.New("unexpected benchmark event")
	}
	var p struct {
		Frame int `json:"frame"`
	}
	if err := json.Unmarshal(event.Payload, &p); err != nil {
		return err
	}
	if p.Frame < 0 || p.Frame >= 1200 {
		return errors.New("invalid frame")
	}
	for i := 0; i < h.surfaces; i++ {
		id := fmt.Sprintf("surface-%02d", i)
		layout := "display/key.json"
		if i >= 10 {
			layout = "display/wide.json"
		}
		feedback := h.feedback[id]
		if feedback == nil {
			feedback = map[string]any{"frame": 0, "surface": i, "nested": map[string]any{"revision": 0}}
			h.feedback[id] = feedback
		}
		// Reuse the producer map intentionally: engine caching must snapshot it
		feedback["frame"] = p.Frame
		feedback["nested"].(map[string]any)["revision"] = p.Frame
		view := engine.Display{Layout: layout, Image: "icons/fixture.svg", Title: fmt.Sprintf("Frame %04d", p.Frame), Feedback: feedback}
		// Initial revision emits layout/state/image/title/feedback; later revisions
		// emit only title/feedback. Duplicate desired state must emit nothing
		if err := h.engine.SendDisplayView(ctx, c, id, view, true); err != nil {
			return err
		}
		if err := h.engine.SendDisplayView(ctx, c, id, view, true); err != nil {
			return err
		}
		if p.Frame == 0 {
			h.emitted += 5
		} else {
			h.emitted += 2
		}
	}
	return c.Send(ctx, sdk.Message("benchmarkFrameComplete", "", map[string]any{"frame": p.Frame, "emitted": h.emitted}))
}

// Leave the frame benchmark event-driven
func (*frameHandler) Poll(context.Context, *sdk.Client) error { return nil }

// Close the benchmark authoring engine
func (h *frameHandler) Close() error { return h.engine.Close() }

// Disable implicit polling in the frame benchmark
func (*frameHandler) NextPoll(time.Time) time.Time { return time.Time{} }

// Expose no background wake channel for the frame benchmark
func (*frameHandler) Wake() <-chan struct{} { return nil }

// Run caps the entire operation at 60 seconds and keeps only 1200 latency
// samples. The loopback peer acknowledges each frame before producing another
// this measures bounded round-trip display delivery, not unconstrained saturation
func Run(parent context.Context, options Options, sourceRevision, sourceDirty string) (Report, error) {
	if err := options.Validate(); err != nil {
		return Report{}, err
	}
	if err := parent.Err(); err != nil {
		return Report{}, err
	}
	clock, err := benchmarkclock.New()
	if err != nil {
		return Report{}, err
	}
	initialized, err := clock.Now()
	if err != nil {
		return Report{}, err
	}
	ctx, cancel := context.WithTimeout(parent, 60*time.Second)
	defer cancel()
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		return Report{}, err
	}
	defer listener.Close()
	var tokenBytes [32]byte
	if _, err = rand.Read(tokenBytes[:]); err != nil {
		return Report{}, err
	}
	token := hex.EncodeToString(tokenBytes[:])
	type result struct {
		report Report
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
		report, err := measure(ctx, conn, options, sourceRevision, sourceDirty, clock, initialized)
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
		return Report{}, err
	}
	done := make(chan error, 1)
	go func() {
		done <- sdk.ConnectWithInfo(ctx, "ws://"+listener.Addr().String(), token, newHandler(options.Surfaces), host)
	}()
	select {
	case <-ctx.Done():
		return Report{}, ctx.Err()
	case result := <-results:
		if result.err != nil {
			return Report{}, result.err
		}
		select {
		case err := <-done:
			if err != nil {
				return Report{}, err
			}
		case <-ctx.Done():
			return Report{}, ctx.Err()
		}
		return result.report, nil
	case err := <-done:
		// A connect/handler failure may beat the HTTP reader's error notification
		if err == nil {
			select {
			case result := <-results:
				return result.report, result.err
			case <-ctx.Done():
				return Report{}, ctx.Err()
			}
		}
		return Report{}, err
	}
}

// Measure acknowledged frame traffic against the benchmark clock
func measure(ctx context.Context, conn *websocket.Conn, o Options, revision, dirty string, clock *benchmarkclock.Clock, initialized int64) (Report, error) {
	report := Report{SchemaVersion: 1, FixtureVersion: "1", SourceRevision: revision, SourceDirty: dirty, MeasurementScope: "plugin_engine_sdk_loopback", Surfaces: o.Surfaces, Frames: o.Frames, DisplayRevisions: uint64(o.Frames * o.Surfaces), DuplicateRevisionsSuppressed: uint64(o.Frames * o.Surfaces), OS: runtime.GOOS, Arch: runtime.GOARCH, GoVersion: runtime.Version(), Limitations: []string{"Synthetic engine/SDK loopback fixture; no installed plugin actions or external services executed.", "Host-received protocol messages, not Manager rendering, USB transfer, displayed FPS, or physical input latency.", "Sequential frame request/acknowledgment; payload bytes exclude WebSocket/TCP framing; registration excluded.", "Duplicate desired revisions are submitted twice; measured counts must show only one delivery."}}
	samples := make([]float64, 0, o.Frames)
	started, err := clock.Now()
	if err != nil {
		return Report{}, err
	}
	report.StartupMS, err = clock.BetweenMS(initialized, started)
	if err != nil {
		return Report{}, err
	}
	for frame := 0; frame < o.Frames; frame++ {
		t, err := clock.Now()
		if err != nil {
			return Report{}, err
		}
		if err := wsjson.Write(ctx, conn, sdk.Message("benchmarkFrame", "", map[string]any{"frame": frame})); err != nil {
			return Report{}, err
		}
		expectedPerSurface := 2
		if frame == 0 {
			expectedPerSurface = 5
		}
		received := 0
		seen := map[string]map[string]bool{}
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return Report{}, err
			}
			var ev sdk.Event
			if err = json.Unmarshal(data, &ev); err != nil {
				return Report{}, err
			}
			if ev.Event == "benchmarkFrameComplete" {
				var barrier struct {
					Frame   int    `json:"frame"`
					Emitted uint64 `json:"emitted"`
				}
				if err = json.Unmarshal(ev.Payload, &barrier); err != nil {
					return Report{}, err
				}
				if barrier.Frame != frame || received != expectedPerSurface*o.Surfaces || barrier.Emitted != report.Received {
					return Report{}, fmt.Errorf("display delivery mismatch at frame %d", frame)
				}
				for i := 0; i < o.Surfaces; i++ {
					id := fmt.Sprintf("surface-%02d", i)
					if !seen[id]["setTitle"] || !seen[id]["setFeedback"] {
						return Report{}, fmt.Errorf("missing display state for %s", id)
					}
				}
				report.Emitted = barrier.Emitted
				break
			}
			switch ev.Event {
			case "setFeedbackLayout", "setState", "setImage", "setTitle", "setFeedback":
			default:
				return Report{}, fmt.Errorf("unexpected output %q", ev.Event)
			}
			var payload map[string]any
			if err = json.Unmarshal(ev.Payload, &payload); err != nil {
				return Report{}, err
			}
			index := -1
			for i := 0; i < o.Surfaces; i++ {
				if ev.Context == fmt.Sprintf("surface-%02d", i) {
					index = i
					break
				}
			}
			if index < 0 {
				return Report{}, errors.New("display escaped synthetic context")
			}
			if seen[ev.Context] == nil {
				seen[ev.Context] = map[string]bool{}
			}
			if seen[ev.Context][ev.Event] {
				return Report{}, errors.New("duplicate display event")
			}
			seen[ev.Context][ev.Event] = true
			if ev.Event == "setTitle" && payload["title"] != fmt.Sprintf("Frame %04d", frame) {
				return Report{}, errors.New("stale display title")
			}
			if ev.Event == "setFeedback" && payload["frame"] != float64(frame) {
				return Report{}, errors.New("stale display feedback")
			}
			received++
			report.Received++
			report.PayloadBytes += uint64(len(data))
			if received > expectedPerSurface*o.Surfaces {
				return Report{}, errors.New("duplicate or excessive display output")
			}
		}
		sample, err := clock.SinceMS(t)
		if err != nil {
			return Report{}, err
		}
		samples = append(samples, sample)
	}
	report.ElapsedMS, err = clock.ElapsedMS(started)
	if err != nil {
		return Report{}, err
	}
	report.ThroughputPerSecond = float64(report.Received) * 1000 / report.ElapsedMS
	sort.Float64s(samples)
	percentile := func(p int) float64 { return samples[(len(samples)*p+99)/100-1] }
	report.LatencyMS = Latency{percentile(50), percentile(95), percentile(99)}
	return report, nil
}
