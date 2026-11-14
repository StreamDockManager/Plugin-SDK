// A bounded in-process authoring workload, separate from loopback/hardware measurements
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	sdk "github.com/StreamDockManager/Plugin-SDK/Go"
	"github.com/StreamDockManager/Plugin-SDK/Go/authoring"
	"github.com/StreamDockManager/Plugin-SDK/Go/internal/benchmarkclock"
	"github.com/StreamDockManager/Plugin-SDK/Go/testkit"
)

// Run the benchmark plugin using the supplied host arguments
func main() {
	frames := flag.Int("frames", 120, "revisions")
	surfaces := flag.Int("surfaces", 14, "bindings")
	flag.Parse()
	if *frames < 1 || *frames > 1200 || *surfaces < 1 || *surfaces > 14 {
		fmt.Fprintln(os.Stderr, "workload outside bounds")
		os.Exit(2)
	}
	recorder := testkit.New(sdk.HostInfo{})
	renderer := authoring.Renderer{}
	count := 0
	clock, err := benchmarkclock.New()
	if err != nil {
		panic(err)
	}
	started, err := clock.Now()
	if err != nil {
		panic(err)
	}
	for frame := range *frames {
		for surface := range *surfaces {
			id := fmt.Sprintf("fixture-%d", surface)
			view := authoring.Display{Title: fmt.Sprint(frame)}
			for range 2 {
				if err := renderer.Render(context.Background(), recorder.Client, id, view); err != nil {
					panic(err)
				}
			}
			events := recorder.Drain()
			expected := 1
			if frame == 0 {
				expected = 2
			}
			if len(events) != expected {
				panic("unexpected or duplicate display events")
			}
			count += len(events)
		}
	}
	elapsed, err := clock.ElapsedMS(started)
	if err != nil {
		panic(err)
	}
	if err := json.NewEncoder(os.Stdout).Encode(map[string]any{"schema_version": 1, "measurement_scope": "sdk_authoring_inprocess", "implementation": "go", "frames": *frames, "surfaces": *surfaces, "received_events": count, "display_revisions": *frames * *surfaces, "elapsed_ms": elapsed}); err != nil {
		panic(err)
	}
}
