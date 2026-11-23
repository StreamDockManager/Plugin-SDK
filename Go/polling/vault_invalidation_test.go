package polling

import (
	"testing"
	"time"
)

func TestInvalidationClearsAllSubscribersOnlyForCurrentGeneration(t *testing.T) {
	r := NewResources()
	now := time.Now()
	for id, interval := range map[string]time.Duration{"due": time.Second, "not-due": time.Hour} {
		if err := r.Watch(id, "same", interval); err != nil {
			t.Fatal(err)
		}
	}
	one, _ := r.Begin("same")
	if !r.Complete("same", one, "protected sample", false, now, 0, time.Time{}) {
		t.Fatal("sample not applied")
	}
	two, _ := r.Begin("same")
	if r.CompleteInvalidated("same", one, now.Add(time.Second), 0, time.Time{}) {
		t.Fatal("stale generation invalidated a new request")
	}
	if !r.CompleteInvalidated("same", two, now.Add(time.Second), 0, time.Time{}) {
		t.Fatal("current generation did not invalidate")
	}
	for _, id := range []string{"due", "not-due"} {
		state, ok := r.Snapshot(id)
		if !ok || state.Available || state.LastValue != nil || state.Stale || !state.ObservedAt.IsZero() {
			t.Fatal("protected sample remained on subscriber", id)
		}
	}
}
