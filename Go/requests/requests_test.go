package requests

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"sync/atomic"
	"testing"
	"time"
)

// Load the shared request-group conformance cases
func spec(t *testing.T) struct {
	Key      string
	Value    map[string]any
	Cooldown int `json:"cooldown_ms"`
	Capacity int
} {
	t.Helper()
	raw, e := os.ReadFile("../testdata/extensions-v1.json")
	if e != nil {
		t.Fatal(e)
	}
	var f struct {
		Reads struct {
			Key      string
			Value    map[string]any
			Cooldown int `json:"cooldown_ms"`
			Capacity int
		} `json:"shared_reads"`
	}
	if e = json.Unmarshal(raw, &f); e != nil {
		t.Fatal(e)
	}
	return f.Reads
}

// Wait for the fixture condition within the test deadline
func wait(t *testing.T, ready func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for !ready() {
		if time.Now().After(deadline) {
			t.Fatal("condition did not complete")
		}
		time.Sleep(time.Millisecond)
	}
}

// Verify shared reads cancellation cooldown and retirement
func TestSharedReadsCancellationCooldownAndRetirement(t *testing.T) {
	s := spec(t)
	var group Group[map[string]any]
	defer group.Close()
	release := make(chan struct{})
	seen := make(chan context.Context, 1)
	var calls atomic.Int32
	work := func(ctx context.Context) (map[string]any, error) {
		calls.Add(1)
		seen <- ctx
		<-release
		return s.Value, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	first := make(chan error, 1)
	second := make(chan map[string]any, 1)
	go func() { _, err := group.Get(ctx, s.Key, work, nil); first <- err }()
	go func() {
		value, err := group.Get(context.Background(), s.Key, work, nil)
		if err != nil {
			t.Error(err)
		}
		second <- value
	}()
	wait(t, func() bool {
		group.mu.Lock()
		defer group.mu.Unlock()
		f := group.flights[s.Key]
		return f != nil && f.consumers == 2
	})
	shared := <-seen
	cancel()
	if <-first == nil {
		t.Fatal("consumer cancellation ignored")
	}
	if shared.Err() != nil {
		t.Fatal("cancelled remaining consumer")
	}
	close(release)
	if value := <-second; !reflect.DeepEqual(value, s.Value) || calls.Load() != 1 {
		t.Fatal("read was not shared", value, calls.Load())
	}
	failure := errors.New("service cooldown")
	retry := func(error) time.Duration { return time.Duration(s.Cooldown) * time.Millisecond }
	fail := func(context.Context) (map[string]any, error) { return nil, failure }
	group.Get(context.Background(), "cooldown", fail, retry)
	_, err := group.Get(context.Background(), "cooldown", func(context.Context) (map[string]any, error) { t.Error("ignored cooldown"); return nil, nil }, nil)
	if !errors.Is(err, failure) {
		t.Fatal(err)
	}
	oldRelease := make(chan struct{})
	oldStarted := make(chan struct{})
	oldctx, oldcancel := context.WithCancel(context.Background())
	oldDone := make(chan error, 1)
	go func() {
		_, err := group.Get(oldctx, "retired", func(context.Context) (map[string]any, error) { close(oldStarted); <-oldRelease; return nil, failure }, retry)
		oldDone <- err
	}()
	<-oldStarted
	oldcancel()
	<-oldDone
	read := func(context.Context) (map[string]any, error) { return s.Value, nil }
	if _, err = group.Get(context.Background(), "retired", read, nil); err != nil {
		t.Fatal(err)
	}
	close(oldRelease)
	wait(t, func() bool { group.mu.Lock(); defer group.mu.Unlock(); return group.active == 0 })
	if _, err = group.Get(context.Background(), "retired", read, nil); err != nil {
		t.Fatal("late read poisoned replacement", err)
	}
	group.Close()
	if _, err = group.Get(context.Background(), "closed", read, nil); err == nil {
		t.Fatal("closed group accepted work")
	}
}

// Verify retired work retains capacity until finished
func TestRetiredWorkRetainsCapacityUntilFinished(t *testing.T) {
	s := spec(t)
	var group Group[int]
	defer group.Close()
	release := make(chan struct{})
	cancel := make([]context.CancelFunc, s.Capacity)
	done := make(chan error, s.Capacity)
	for i := range s.Capacity {
		ctx, stop := context.WithCancel(context.Background())
		cancel[i] = stop
		go func(i int) {
			_, err := group.Get(ctx, string(rune('a'+i)), func(context.Context) (int, error) { <-release; return 1, nil }, nil)
			done <- err
		}(i)
	}
	wait(t, func() bool { group.mu.Lock(); defer group.mu.Unlock(); return len(group.flights) == s.Capacity })
	for _, stop := range cancel {
		stop()
	}
	for range s.Capacity {
		if <-done == nil {
			t.Fatal("cancelled read succeeded")
		}
	}
	if _, err := group.Get(context.Background(), "overflow", func(context.Context) (int, error) { return 1, nil }, nil); err == nil {
		t.Fatal("retired workers released capacity too early")
	}
	close(release)
	wait(t, func() bool { group.mu.Lock(); defer group.mu.Unlock(); return group.active == 0 })
	if value, err := group.Get(context.Background(), "reused", func(context.Context) (int, error) { return 1, nil }, nil); err != nil || value != 1 {
		t.Fatal(value, err)
	}
}

// Verify cooldown expires and read resumes
func TestCooldownExpiresAndReadResumes(t *testing.T) {
	var group Group[int]
	defer group.Close()
	_, err := group.Get(context.Background(), "expiry", func(context.Context) (int, error) { return 0, errors.New("cooldown") }, func(error) time.Duration { return 20 * time.Millisecond })
	if err == nil {
		t.Fatal("missing failure")
	}
	time.Sleep(30 * time.Millisecond)
	value, err := group.Get(context.Background(), "expiry", func(context.Context) (int, error) { return 7, nil }, nil)
	if err != nil || value != 7 {
		t.Fatal(value, err)
	}
}
