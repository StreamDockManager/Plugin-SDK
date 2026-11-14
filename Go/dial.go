package sdk

import (
	"encoding/json"
	"time"
)

// dialBatch is owned by the event loop. Its deadline never extends with activity
type dialBatch struct {
	pending  *Event
	deadline time.Time
}

// Deliver the pending dial batch and reset its deadline
func (b *dialBatch) flush(deliver func(Event)) {
	if b.pending != nil {
		deliver(*b.pending)
		b.pending = nil
		b.deadline = time.Time{}
	}
}

// Coalesce compatible same-direction ticks within a fixed deadline
func (b *dialBatch) accept(event Event, eligible bool, now time.Time, deliver func(Event)) {
	if !eligible || event.Event != "dialRotate" {
		b.flush(deliver)
		deliver(event)
		return
	}
	ticks := event.Data().Ticks
	if b.pending != nil {
		old := b.pending.Data().Ticks
		sum := old + ticks
		// Never merge across controls, direction reversals, overflow or the original deadline
		if now.Before(b.deadline) && event.Context == b.pending.Context && event.Action == b.pending.Action && event.Device == b.pending.Device && (old > 0) == (ticks > 0) && !(ticks > 0 && sum < old || ticks < 0 && sum > old) {
			var payload map[string]json.RawMessage
			if json.Unmarshal(b.pending.Payload, &payload) == nil {
				payload["ticks"], _ = json.Marshal(sum)
				b.pending.Payload, _ = json.Marshal(payload)
				event.retained.release()
				return
			}
		}
		b.flush(deliver)
	}
	copy := event
	b.pending = &copy
	b.deadline = now.Add(16 * time.Millisecond)
}
