package displaybench

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"testing"
	"time"
)

// Verify bounds
func TestBounds(t *testing.T) {
	for _, o := range []Options{{0, 1}, {1201, 1}, {1, 0}, {1, 15}} {
		if _, err := Run(context.Background(), o, "fixture", "true"); err == nil {
			t.Fatal(o)
		}
	}
}

// Verify actual engine SDK loopback
func TestActualEngineSDKLoopback(t *testing.T) {
	for _, surfaces := range []int{1, 10, 14} {
		t.Run(fmt.Sprintf("surfaces_%d", surfaces), func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			r, err := Run(ctx, Options{Frames: 3, Surfaces: surfaces}, "revision-fixture", "true")
			if err != nil {
				t.Fatal(err)
			}
			want := uint64(surfaces * (5 + 2*2))
			if r.Emitted != want || r.Received != want || r.DisplayRevisions != uint64(3*surfaces) || r.DuplicateRevisionsSuppressed != r.DisplayRevisions {
				t.Fatalf("incorrect counts: %+v", r)
			}
			// Preserve measured samples, including a possible sub-counter-tick
			// sample, while requiring a positive total and finite JSON metrics
			for _, sample := range []float64{r.ElapsedMS, r.ThroughputPerSecond, r.LatencyMS.P50, r.LatencyMS.P95, r.LatencyMS.P99} {
				if math.IsNaN(sample) || math.IsInf(sample, 0) || sample < 0 {
					t.Fatalf("invalid timing value %v: %+v", sample, r)
				}
			}
			if r.SchemaVersion != 1 || r.MeasurementScope != "plugin_engine_sdk_loopback" || r.SourceRevision != "revision-fixture" || r.SourceDirty != "true" || r.StartupMS < 0 || math.IsNaN(r.StartupMS) || math.IsInf(r.StartupMS, 0) || r.StartupMS > 60000 || r.ElapsedMS <= 0 || r.PayloadBytes == 0 || r.LatencyMS.P50 < 0 || r.LatencyMS.P95 < r.LatencyMS.P50 || r.LatencyMS.P99 < r.LatencyMS.P95 {
				t.Fatalf("bad metrics: %+v", r)
			}
			if _, err := json.Marshal(r); err != nil {
				t.Fatalf("report cannot be encoded: %v", err)
			}
		})
	}
}

// Verify already cancelled
func TestAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Run(ctx, Options{Frames: 1200, Surfaces: 14}, "fixture", "true")
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
}
