package authoring

import (
	"context"
	"github.com/StreamDockManager/Plugin-SDK/Go"
)

// DisplayMessages computes one desired revision. Commit the snapshot only after
// every queued write succeeds; reset it after host/context/theme changes
func DisplayMessages(id string, previous *Display, view Display, rich, states bool) []sdk.Event {
	out := []sdk.Event{}
	known := previous != nil
	if !known {
		previous = &Display{}
	}
	layoutChanged := !known || previous.Layout != view.Layout
	if rich && layoutChanged && view.Layout != "" {
		out = append(out, sdk.Message("setFeedbackLayout", id, map[string]any{"layout": view.Layout}))
	}
	stateChanged := states && (!known || previous.State != view.State)
	if stateChanged {
		out = append(out, sdk.Message("setState", id, map[string]any{"state": view.State}))
	}
	if (!known && view.Image != "") || (known && previous.Image != view.Image) || (stateChanged && view.Image != "") {
		out = append(out, sdk.Message("setImage", id, map[string]any{"image": view.Image, "target": 0}))
	}
	if !known || previous.Title != view.Title || stateChanged {
		out = append(out, sdk.Message("setTitle", id, map[string]any{"title": view.Title, "target": 0}))
	}
	feedback := map[string]any{}
	if rich {
		for key, value := range view.Feedback {
			old, present := previous.Feedback[key]
			if layoutChanged || !present || !FeedbackEqual(old, value) {
				feedback[key] = CloneSetting(value)
			}
		}
		for key := range previous.Feedback {
			if _, ok := view.Feedback[key]; !layoutChanged && !ok {
				feedback[key] = nil
			}
		}
		if len(feedback) > 0 {
			out = append(out, sdk.Message("setFeedback", id, feedback))
		}
	}
	return out
}

type Renderer struct{ last map[string]Display }

// Forget the renderer history for one binding
func (r *Renderer) Reset(id string) {
	if id == "" {
		r.last = nil
	} else {
		delete(r.last, id)
	}
}

// Send changed display messages and retain successful rendering state
func (r *Renderer) Render(ctx context.Context, c *sdk.Client, id string, view Display) error {
	var previous *Display
	if old, ok := r.last[id]; ok {
		previous = &old
	}
	for _, message := range DisplayMessages(id, previous, view, true, true) {
		if err := c.Send(ctx, message); err != nil {
			return err
		}
	}
	if r.last == nil {
		r.last = map[string]Display{}
	}
	view.Feedback = CloneSettings(view.Feedback)
	r.last[id] = view
	return nil
}
