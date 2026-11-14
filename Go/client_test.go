package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type handler struct {
	events chan Event
	closed chan bool
}

// Record each fixture event and reply with a fixture title
func (h *handler) Handle(ctx context.Context, c *Client, e Event) error {
	h.events <- e
	return c.Title(ctx, e.Context, "fixture")
}

// Keep the fixture event-driven without periodic work
func (h *handler) Poll(context.Context, *Client) error { return nil }

// Signal that the fixture handler was closed
func (h *handler) Close() error { h.closed <- true; return nil }

// Verify authenticated lifecycle and shutdown
func TestAuthenticatedLifecycleAndShutdown(t *testing.T) {
	received := make(chan Event, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, e := websocket.Accept(w, r, nil)
		if e != nil {
			t.Error(e)
			return
		}
		defer c.CloseNow()
		c.SetReadLimit(1 << 20)
		var reg Event
		if e = wsjson.Read(context.Background(), c, &reg); e != nil {
			t.Error(e)
			return
		}
		received <- reg
		_ = wsjson.Write(context.Background(), c, Event{Event: "willAppear", Context: "binding", Action: "counter.increment", Payload: json.RawMessage(`{"settings":{"step":2}}`)})
		var response Event
		if e = wsjson.Read(context.Background(), c, &response); e != nil {
			t.Error(e)
			return
		}
		received <- response
		_ = wsjson.Write(context.Background(), c, Event{Event: "shutdown"})
	}))
	defer server.Close()
	h := &handler{make(chan Event, 4), make(chan bool, 1)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if e := ConnectWithInfo(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), "secret-fixture-token", h, HostInfo{}); e != nil {
		t.Fatal(e)
	}
	reg := <-received
	if reg.Event != "registerPlugin" || reg.UUID != "secret-fixture-token" {
		t.Fatal(reg)
	}
	if response := <-received; response.Event != "setTitle" || response.Context != "binding" {
		t.Fatal(response)
	}
	if e := <-h.events; e.Data().Settings["step"] != float64(2) {
		t.Fatal(e)
	}
	select {
	case <-h.closed:
	default:
		t.Fatal("shutdown did not close handler")
	}
}

// Verify bounded queue and cancellation
func TestBoundedQueueAndCancellation(t *testing.T) {
	c := &Client{out: make(chan Event, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	if e := c.Send(ctx, Event{}); e != nil {
		t.Fatal(e)
	}
	if e := c.Send(ctx, Event{}); e == nil {
		t.Fatal("unbounded queue")
	}
	cancel()
	if e := c.Send(ctx, Event{}); !errors.Is(e, context.Canceled) {
		t.Fatal(e)
	}
}

// Verify network reports use independent run identifier
func TestNetworkReportsUseIndependentRunIdentifier(t *testing.T) {
	const token = "private-auth-token"
	const run = "public-run-identifier"
	received := make(chan Event, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		conn.SetReadLimit(1 << 20)
		wsCtx, wsCancel := context.WithDeadline(context.Background(), time.Now().Add(4*time.Second))
		defer wsCancel()
		for range 2 {
			var event Event
			if err := wsjson.Read(wsCtx, conn, &event); err != nil {
				t.Error(err)
				return
			}
			received <- event
		}
		_ = wsjson.Write(wsCtx, conn, Event{Event: "shutdown"})
	}))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	h := &handler{make(chan Event, 4), make(chan bool, 1)}
	host, _ := ParseHostInfo([]byte(`{"streamDockManager":{"contract_version":1,"capabilities":["network-payload-v1"],"run_id":"` + run + `"}}`))
	if err := ConnectWithInfo(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), token, h, host); err != nil {
		t.Fatal(err)
	}
	if event := <-received; event.UUID != token {
		t.Fatal("registration lost authentication")
	}
	event := <-received
	var payload map[string]any
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if event.Event != "networkPayload" || payload["run_id"] != run || strings.Contains(string(event.Payload), token) {
		t.Fatal("network report exposed authentication", event.Event)
	}
}
