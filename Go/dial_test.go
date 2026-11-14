package sdk

import (
	"testing"
	"time"
)

// Verify dial totals boundaries and fixed window
func TestDialTotalsBoundariesAndFixedWindow(t *testing.T) {
	var batch dialBatch
	var received []Event
	deliver := func(e Event) { received = append(received, e) }
	now := time.Now()
	event := func(context string, ticks int) Event {
		e := Message("dialRotate", context, map[string]any{"ticks": ticks})
		e.Action = "count"
		return e
	}
	batch.accept(event("a", 2), true, now, deliver)
	batch.accept(event("a", 3), true, now.Add(10*time.Millisecond), deliver)
	if batch.deadline != now.Add(16*time.Millisecond) {
		t.Fatal("batch window extended")
	}
	batch.accept(event("a", -1), true, now.Add(11*time.Millisecond), deliver)
	batch.accept(Message("dialDown", "a", nil), false, now.Add(12*time.Millisecond), deliver)
	if len(received) != 3 || received[0].Data().Ticks != 5 || received[1].Data().Ticks != -1 || received[2].Event != "dialDown" {
		t.Fatal(received)
	}
	batch.accept(event("a", 7), true, now, deliver)
	batch.accept(event("b", 9), true, now, deliver)
	batch.flush(deliver)
	if received[3].Context != "a" || received[4].Context != "b" {
		t.Fatal("context ordering changed")
	}
}
