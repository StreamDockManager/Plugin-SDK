package discovery

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/StreamDockManager/Plugin-SDK/Go"
	"github.com/StreamDockManager/Plugin-SDK/Go/authoring"
	"time"
)

// Run connects the coordinator to the SDK's bounded observation executor. It
// preserves command receipts and isolates jobs by inspector context and channel
func (r *Runner) Run(ctx context.Context, e *authoring.Engine, c *sdk.Client, request Request, timeout time.Duration, work func(context.Context) (map[string]any, error)) error {
	parent, cancel := context.WithTimeout(ctx, timeout)
	ticket, err := r.Start(parent, request, time.Now().UnixMilli(), timeout.Milliseconds())
	if err != nil {
		cancel()
		return err
	}
	originalCancel := ticket.cancel
	ticket.cancel = func() { originalCancel(); cancel() }
	identity, _ := json.Marshal([2]string{request.Context, request.Channel})
	key := fmt.Sprintf("discovery:%x", sha256.Sum256(identity))
	e.CancelJob(key)
	cleanup := func() { cancel(); delete(e.Results, key); delete(e.ResultsText, key); delete(e.Checked, key) }
	send := func(reply *Reply) error {
		if reply == nil {
			return nil
		}
		message := sdk.Message("sendToPropertyInspector", reply.Context, reply.Payload)
		message.Action = reply.Action
		return c.Send(context.WithoutCancel(ctx), message)
	}
	if authoring.StartObservationJob(ticket.Context, e, key, func(job context.Context) (string, any, error) { payload, err := work(job); return "", payload, err }, func(_ context.Context, c *sdk.Client, result authoring.JobResult, _ authoring.BackgroundJob) error {
		defer cleanup()
		payload, _ := result.Value.(map[string]any)
		failure := ""
		now := time.Now().UnixMilli()
		if result.Err != nil {
			failure = "failed"
		}
		if errors.Is(result.Err, context.DeadlineExceeded) {
			now = max(now, ticket.Deadline)
		}
		reply, err := r.Complete(ticket, payload, failure, now)
		if err != nil {
			return err
		}
		return send(reply)
	}) {
		return nil
	}
	cleanup()
	r.Cancel(request.Context, request.Channel, request.ID)
	reply, err := Envelope(request, nil, "busy")
	if err != nil {
		return err
	}
	return send(&reply)
}
