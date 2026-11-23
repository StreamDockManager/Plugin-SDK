package sdk

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"slices"
	"testing"
)

// Verify tracked TCP counts only transferred payload
func TestTrackedTCPCountsOnlyTransferredPayload(t *testing.T) {
	sent0, received0 := NetworkPayload()
	left, right := net.Pipe()
	defer left.Close()
	defer right.Close()
	tracked := TrackConn(left)
	done := make(chan error, 1)
	go func() {
		_, err := io.WriteString(right, "incoming")
		if err == nil {
			data := make([]byte, 3)
			_, err = io.ReadFull(right, data)
		}
		done <- err
	}()
	data := make([]byte, 8)
	if _, err := io.ReadFull(tracked, data); err != nil {
		t.Fatal(err)
	}
	if _, err := tracked.Write([]byte("out")); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	sent, received := NetworkPayload()
	if sent-sent0 != 3 || received-received0 != 8 {
		t.Fatal("TCP payload accounting mismatch")
	}
}

func TestOptionalNetworkDemand(t *testing.T) {
	var fixture struct {
		Manager json.RawMessage
		Demand  struct {
			Capability string
			Request    Event
			Response   string
		} `json:"network_demand"`
	}
	raw, err := os.ReadFile("testdata/protocol-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, opt := range []bool{false, true} {
		host, err := ParseHostInfo(fixture.Manager)
		if err != nil {
			t.Fatal(err)
		}
		if !opt {
			host.Manager.Capabilities = slices.DeleteFunc(host.Manager.Capabilities, func(value string) bool { return value == fixture.Demand.Capability })
		}
		c, out := NewLocalClient(host, "private-token")
		h := &handler{make(chan Event, 1), make(chan bool, 1)}
		handleEvent(context.Background(), c, h, fixture.Demand.Request)
		if c.networkDemand() != opt {
			t.Fatal("incorrect negotiation")
		}
		event := <-out
		if opt {
			var payload map[string]any
			if err := json.Unmarshal(event.Payload, &payload); err != nil || event.Event != fixture.Demand.Response || payload["run_id"] != host.Manager.RunID || len(h.events) != 0 {
				t.Fatal(event)
			}
		} else if event.Event != "setTitle" || len(h.events) != 1 {
			t.Fatal("legacy dispatch changed")
		}
	}
}
