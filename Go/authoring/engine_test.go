package authoring

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/StreamDockManager/Plugin-SDK/Go"
)

// Construct a minimal engine using a disposable state path
func fixture(t *testing.T) *Engine {
	t.Helper()
	return New(Definition{Slug: "fixture"}, filepath.Join(t.TempDir(), "state.json"), func(e *Engine) Feature {
		return Feature{Snapshot: func() any { return map[string]any{} }, Status: func(context.Context, string, Binding) DisplayStatus { return DisplayStatus{} }, Apply: e.FinishJob}
	})
}

// Verify stale job cannot complete replacement
func TestStaleJobCannotCompleteReplacement(t *testing.T) {
	e := fixture(t)
	defer e.Close()
	var calls int
	e.Jobs["action"] = BackgroundJob{Generation: 2, Cancel: func() {}, Apply: func(context.Context, *sdk.Client, JobResult, BackgroundJob) error { calls++; return nil }}
	e.Completed = make(chan JobResult, 2)
	e.Completed <- JobResult{ID: "action", Generation: 1}
	e.Completed <- JobResult{ID: "action", Generation: 2}
	if err := e.DrainJobs(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || len(e.Jobs) != 0 {
		t.Fatal(calls, e.Jobs)
	}
}

// Verify job receipt includes completion callback failure
func TestJobReceiptIncludesCompletionCallbackFailure(t *testing.T) {
	e := fixture(t)
	defer e.Close()
	want := errors.New("display rejected")
	var receipt error
	e.Jobs["action"] = BackgroundJob{Cancel: func() {}, Complete: func(err error) { receipt = err }, Apply: func(context.Context, *sdk.Client, JobResult, BackgroundJob) error { return want }}
	if err := e.ApplyJobResult(context.Background(), nil, JobResult{ID: "action"}); !errors.Is(err, want) || !errors.Is(receipt, want) {
		t.Fatal(err, receipt)
	}
}

// Verify deadline retains pending persistence and zero suspends
func TestDeadlineRetainsPendingPersistenceAndZeroSuspends(t *testing.T) {
	e := fixture(t)
	defer e.Close()
	now := time.Now()
	if !e.NextPoll(now).IsZero() {
		t.Fatal("idle fixture polls")
	}
	e.PendingWrite = now.Add(time.Second)
	e.Feature.Deadline = func(time.Time) time.Time { return now.Add(time.Minute) }
	if got := e.NextPoll(now); got != e.PendingWrite {
		t.Fatal(got)
	}
	e.PendingWrite = time.Time{}
}

// Verify full live title survives legacy line truncation
func TestFullLiveTitleSurvivesLegacyLineTruncation(t *testing.T) {
	e := fixture(t)
	defer e.Close()
	e.Bindings["key"] = Binding{Action: "increment"}
	e.Feature.Visuals = map[string]Visual{"increment": {Icon: "icons/plus"}}
	text := "Original live title remains available independently of legacy line width"
	view := e.DisplayView("key", DisplayStatus{Text: text})
	if view.Title != text || view.Feedback["line0"] == text {
		t.Fatal(view)
	}
}

// Verify persist uses one frozen snapshot per attempt
func TestPersistUsesOneFrozenSnapshotPerAttempt(t *testing.T) {
	engine := fixture(t)
	calls := 0
	engine.Feature.Snapshot = func() any { calls++; return map[string]any{"revision": calls} }
	if err := engine.Persist(); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || string(engine.Written) != `{"revision":1}` {
		t.Fatal(calls, string(engine.Written))
	}
	raw, err := os.ReadFile(engine.Path)
	if err != nil || string(raw) != `{"data":{"revision":1},"version":1}` {
		t.Fatal(err, string(raw))
	}
}

// Verify job generation lifecycle
func TestJobGenerationLifecycle(t *testing.T) {
	e := fixture(t)
	defer e.Close()
	work := func(ctx context.Context) (string, any, error) { <-ctx.Done(); return "", nil, ctx.Err() }
	calls := 0
	apply := func(context.Context, *sdk.Client, JobResult, BackgroundJob) error { calls++; return nil }
	if !e.StartJob(context.Background(), "job", work, apply) {
		t.Fatal("initial admission failed")
	}
	old := e.Jobs["job"].Generation
	if e.StartJob(context.Background(), "job", work, apply) || e.Jobs["job"].Generation != old {
		t.Fatal("duplicate admission replaced identity")
	}
	e.CancelJob("job")
	if len(e.Jobs) != 0 {
		t.Fatal("cancel retained job")
	}
	if !e.StartJob(context.Background(), "job", work, apply) {
		t.Fatal("replacement admission failed")
	}
	current := e.Jobs["job"]
	if current.Generation <= old {
		t.Fatal("identity reused")
	}
	if err := e.ApplyJobResult(context.Background(), nil, JobResult{ID: "job", Generation: old}); err != nil {
		t.Fatal(err)
	}
	if calls != 0 || e.Jobs["job"].Generation != current.Generation {
		t.Fatal("stale direct completion replaced current job")
	}
	if err := e.ApplyJobResult(context.Background(), nil, JobResult{ID: "missing", Generation: 0}); err != nil || calls != 0 {
		t.Fatal("missing job admitted", err)
	}
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	if len(e.Jobs) != 0 || e.StartJob(context.Background(), "closed", work, apply) {
		t.Fatal("shutdown retained or admitted a job")
	}
}

// Verify failed job admission removes identity
func TestFailedJobAdmissionRemovesIdentity(t *testing.T) {
	e := fixture(t)
	defer e.Close()
	e.Probes.Close()
	if e.StartJob(context.Background(), "job", func(context.Context) (string, any, error) { t.Error("rejected work ran"); return "", nil, nil }, nil) {
		t.Fatal("closed pool admitted work")
	}
	if len(e.Jobs) != 0 || e.Generation != 1 {
		t.Fatal("failed admission leaked job or rewound allocator", e.Jobs, e.Generation)
	}
	e.Probes = sdk.NewProbePool()
	if !e.StartJob(context.Background(), "job", func(ctx context.Context) (string, any, error) { <-ctx.Done(); return "", nil, ctx.Err() }, nil) {
		t.Fatal("retry rejected")
	}
	if e.Jobs["job"].Generation != 2 {
		t.Fatal("failed admission identity reused")
	}
}
