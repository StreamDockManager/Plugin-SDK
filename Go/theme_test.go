package sdk

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

// Verify plugin theme RPC
func TestPluginThemeRPC(t *testing.T) {
	raw, err := os.ReadFile("testdata/protocol-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Manager json.RawMessage
		Theme   struct{ Host, Response json.RawMessage } `json:"theme_rpc"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	old, _ := ParseHostInfo(fixture.Manager)
	legacy, _ := NewLocalClient(old, "session")
	if _, err := legacy.GetPluginTheme(context.Background()); err == nil {
		t.Fatal("old host supports only theme events")
	}
	host, _ := ParseHostInfo(fixture.Theme.Host)
	client, outgoing := NewLocalClient(host, "session")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	result := make(chan error, 1)
	go func() {
		_, err := client.GetPluginTheme(ctx)
		result <- err
	}()
	select {
	case request := <-outgoing:
		if request.ProtocolID != ThemeProtocol || request.Method != "get" || string(request.Payload) != "{}" {
			t.Fatalf("unexpected RPC: %+v", request)
		}
		client.ObserveControl(Event{Event: RPCResponseEvent, ProtocolID: request.ProtocolID, RequestID: request.RequestID, Result: fixture.Theme.Response})
	case <-ctx.Done():
		t.Fatal("no RPC request")
	}
	if err := <-result; err != nil {
		t.Fatal(err)
	}
	if current := client.CurrentPluginTheme(); current == nil || current.Theme.Surface != "#172138" {
		t.Fatal("RPC did not seed current theme")
	}
	if client.observePluginTheme(Event{Event: ThemeEvent, Payload: fixture.Theme.Response}) {
		t.Fatal("replayed revision must not redraw")
	}
}
