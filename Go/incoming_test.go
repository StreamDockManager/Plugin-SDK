package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

// Verify incoming admission retains selected work and cancels waiters
func TestIncomingAdmissionRetainsSelectedWorkAndCancelsWaiters(t *testing.T) {
	data, err := os.ReadFile("testdata/protocol-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Incoming struct {
			Bytes, Items int
			Staging      int `json:"receive_staging_bytes"`
		} `json:"incoming_admission"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if fixture.Incoming.Bytes != MaxInputBytes || fixture.Incoming.Items != MaxInputItems || fixture.Incoming.Staging != MaxMessageBytes {
		t.Fatal("incoming limits differ from shared contract")
	}
	var budget incomingBudget
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var leases []*incomingLease
	for range MaxInputBytes / MaxMessageBytes {
		lease, err := budget.acquire(ctx, MaxMessageBytes)
		if err != nil {
			t.Fatal(err)
		}
		leases = append(leases, lease)
	}
	selected := leases[0] // Dequeue does not release a selected handler or batch
	waiting := make(chan error, 1)
	go func() {
		lease, err := budget.acquire(ctx, 1)
		if lease != nil {
			lease.release()
		}
		waiting <- err
	}()
	select {
	case err := <-waiting:
		t.Fatal("selected work escaped the byte budget", err)
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	if err := <-waiting; !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if budget.bytes != MaxInputBytes || budget.items != len(leases) {
		t.Fatal("waiter changed ownership")
	}
	selected.release()
	selected.release() // A merged event cannot return capacity twice
	for _, lease := range leases[1:] {
		lease.release()
	}
	if budget.bytes != 0 || budget.items != 0 {
		t.Fatal("retained input did not retire")
	}

	ctx = context.Background()
	leases = nil
	for range MaxInputItems {
		lease, err := budget.acquire(ctx, 1)
		if err != nil {
			t.Fatal(err)
		}
		leases = append(leases, lease)
	}
	limited, stop := context.WithTimeout(ctx, 20*time.Millisecond)
	defer stop()
	if _, err := budget.acquire(limited, 0); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	for _, lease := range leases {
		lease.release()
	}
	if _, err := budget.acquire(ctx, MaxMessageBytes+1); err == nil {
		t.Fatal("oversized envelope admitted")
	}
}

type incomingFloodHandler struct {
	events  []Event
	closed  int
	release <-chan struct{}
}

// Block the first fixture input then retain event identities in arrival order
func (h *incomingFloodHandler) Handle(ctx context.Context, _ *Client, event Event) error {
	if len(h.events) == 0 {
		select {
		case <-h.release:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// Retain only event identities; fixture payloads are deliberately large
	h.events = append(h.events, Event{Event: event.Event, Context: event.Context})
	return nil
}

// Keep the fixture event-driven without periodic work
func (*incomingFloodHandler) Poll(context.Context, *Client) error { return nil }

// Count fixture shutdown calls
func (h *incomingFloodHandler) Close() error { h.closed++; return nil }

// Verify incoming flood preserves FIFO release and shutdown
func TestIncomingFloodPreservesFIFOReleaseAndShutdown(t *testing.T) {
	// Fixture serialization is not part of the transport deadline. In particular,
	// race instrumentation should not spend that deadline re-encoding 14 MiB
	payload, err := json.Marshal(map[string]any{"text": strings.Repeat("é", 300000)})
	if err != nil {
		t.Fatal(err)
	}
	var frames [][]byte
	for i := range 24 {
		frame, err := json.Marshal(Event{Event: "keyDown", Context: strings.Repeat("x", i+1), Payload: payload})
		if err != nil {
			t.Fatal(err)
		}
		frames = append(frames, frame)
	}
	release := make(chan struct{})
	releaseFirst := sync.OnceFunc(func() { close(release) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer releaseFirst()
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		conn.SetReadLimit(1 << 20)
		wsCtx, wsCancel := context.WithDeadline(context.Background(), time.Now().Add(5*time.Second))
		defer wsCancel()
		var registration Event
		if err := wsjson.Read(wsCtx, conn, &registration); err != nil {
			t.Error(err)
			return
		}
		for i, frame := range frames {
			if err := conn.Write(wsCtx, websocket.MessageText, frame); err != nil {
				t.Error(err)
				return
			}
			// Hold the first dispatch until a byte-budget's worth of envelopes
			// plus the one allowed staging envelope has been submitted. Unlike a
			// sleep, this establishes the burst before handlers can drain it
			if i == MaxInputBytes/len(frame) {
				releaseFirst()
			}
		}
		if err := wsjson.Write(wsCtx, conn, Message("keyUp", "release", nil)); err != nil {
			t.Error(err)
			return
		}
		if err := wsjson.Write(wsCtx, conn, Message("shutdown", "", nil)); err != nil {
			t.Error(err)
			return
		}
		_, _, _ = conn.Read(wsCtx)
	}))
	defer server.Close()
	handler := &incomingFloodHandler{release: release}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	if err := ConnectWithInfo(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), "fixture", handler, HostInfo{}); err != nil {
		t.Fatal(err)
	}
	if len(handler.events) != 25 || handler.closed != 1 {
		t.Fatalf("events %d, closes %d", len(handler.events), handler.closed)
	}
	for i, event := range handler.events[:24] {
		if event.Event != "keyDown" || event.Context != strings.Repeat("x", i+1) {
			t.Fatal("input order changed")
		}
	}
	if handler.events[24].Event != "keyUp" {
		t.Fatal("release discarded")
	}
}

// Verify incoming dial batch retires merged envelope after dispatch
func TestIncomingDialBatchRetiresMergedEnvelopeAfterDispatch(t *testing.T) {
	var budget incomingBudget
	var batch dialBatch
	makeEvent := func(ticks int) Event {
		event := Message("dialRotate", "a", map[string]any{"ticks": ticks})
		event.retained, _ = budget.acquire(context.Background(), 100)
		return event
	}
	deliver := func(event Event) {
		if budget.items != 1 || budget.bytes != 100 {
			t.Fatal("dispatch released admission too early")
		}
		if event.Data().Ticks != 5 {
			t.Fatal("dial aggregation changed")
		}
		event.retained.release()
	}
	now := time.Now()
	batch.accept(makeEvent(2), true, now, deliver)
	batch.accept(makeEvent(3), true, now, deliver)
	if budget.items != 1 {
		t.Fatal("merged envelope retained")
	}
	batch.flush(deliver)
	if budget.items != 0 || budget.bytes != 0 {
		t.Fatal("batch retained input")
	}
}
