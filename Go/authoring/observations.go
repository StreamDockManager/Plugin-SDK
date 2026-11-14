package authoring

import (
	"context"

	sdk "github.com/StreamDockManager/Plugin-SDK/Go"
)

// An observation inherits cancellation and deadlines, but does not defer or
// complete the action that requested it. Apply retains that action's view scope
type observationContext struct{ context.Context }

// Hide action-completion values while retaining cancellation
func (observationContext) Value(any) any { return nil }

// StartObservationJob schedules a read after a command. Admission, cancellation,
// and read failures never change the command receipt. Apply receives read errors
// in JobResult; use StartJob for work that determines command success
func StartObservationJob(ctx context.Context, e *Engine, id string, work func(context.Context) (string, any, error), apply func(context.Context, *sdk.Client, JobResult, BackgroundJob) error) bool {
	if !e.StartJob(observationContext{ctx}, id, work, apply) {
		return false
	}
	job := e.Jobs[id]
	job.ActionCtx = ctx
	e.Jobs[id] = job
	return true
}
