package sdk

import (
	"context"
	"errors"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type idleContractHandler struct{ polls atomic.Int32 }

// Deliver the test event through the configured fixture behavior
func (*idleContractHandler) Handle(context.Context, *Client, Event) error { return nil }

// Provide the fixture polling behavior used by the host loop
func (h *idleContractHandler) Poll(context.Context, *Client) error { h.polls.Add(1); return nil }

// Perform the fixture shutdown behavior expected by the test
func (*idleContractHandler) Close() error { return nil }

type scheduledContractHandler struct{ *idleContractHandler }

// Expose the fixture wake-channel behavior
func (*scheduledContractHandler) Wake() <-chan struct{} { return nil }

// Return the explicit deadline used by the scheduler fixture
func (h *scheduledContractHandler) NextPoll(now time.Time) time.Time {
	if h.polls.Load() == 0 {
		return now.Add(5 * time.Millisecond)
	}
	return time.Time{}
}

// Verify polling requires explicit scheduler
func TestPollingRequiresExplicitScheduler(t *testing.T) {
	for _, scheduled := range []bool{false, true} {
		t.Run(map[bool]string{false: "event-only", true: "scheduled"}[scheduled], func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				conn, err := websocket.Accept(w, r, nil)
				if err != nil {
					return
				}
				defer conn.CloseNow()
				var registration Event
				if err := wsjson.Read(r.Context(), conn, &registration); err != nil {
					return
				}
				_, _, _ = conn.Read(r.Context())
			}))
			defer server.Close()
			base := &idleContractHandler{}
			var handler Handler = base
			if scheduled {
				handler = &scheduledContractHandler{base}
			}
			ctx, cancel := context.WithTimeout(context.Background(), 1150*time.Millisecond)
			defer cancel()
			err := ConnectWithInfo(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), "fixture-token", handler, HostInfo{})
			if err != nil && !errors.Is(err, context.DeadlineExceeded) {
				t.Fatal(err)
			}
			expected := int32(0)
			if scheduled {
				expected = 1
			}
			if base.polls.Load() != expected {
				t.Fatalf("polls=%d, want %d", base.polls.Load(), expected)
			}
		})
	}
}
