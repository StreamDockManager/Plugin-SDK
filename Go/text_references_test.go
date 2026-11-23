package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestFormatText(t *testing.T) {
	ctx := context.Background()
	legacy, out := NewLocalClient(HostInfo{}, "local")
	if value, err := legacy.FormatText(ctx, "", "literal"); err != nil || value != "literal" || len(out) != 0 {
		t.Fatal("literal text must remain local", err)
	}
	if value, err := legacy.FormatText(ctx, "", "} then ${unfinished"); err != nil || value != "} then ${unfinished" || len(out) != 0 {
		t.Fatal("incomplete reference must remain local", err)
	}
	var unsupported *UnsupportedFeatureError
	if _, err := legacy.FormatText(ctx, "example.action", "${missing}"); !errors.As(err, &unsupported) {
		t.Fatal("expected unsupported capability", err)
	}
	host, _ := ParseHostInfo([]byte(`{"streamDockManager":{"contract_version":1,"capabilities":["plugin-text-references-v1"]}}`))
	client, outgoing := NewLocalClient(host, "local")
	result := make(chan Event, 1)
	go func() {
		req := <-outgoing
		result <- req
		client.ObserveControl(Event{Event: RPCResponseEvent, ProtocolID: req.ProtocolID, RequestID: req.RequestID, Result: json.RawMessage(`{"text":"Hello "}`)})
	}()
	text, err := client.FormatText(ctx, "example.action", "Hello ${missing}")
	request := <-result
	if err != nil || text != "Hello " || request.ProtocolID != TextReferencesProtocol || request.Method != "format-text" ||
		string(request.Payload) != `{"action":"example.action","text":"Hello ${missing}"}` {
		t.Fatal("wrong format-text result or wire request", err)
	}
	for _, raw := range []string{`{"text":null}`, `{"text":"\ud800"}`, `{"text":"x","text":"y"}`, `{"text":"x","extra":1}`, `{"text":"` + strings.Repeat("x", maxReferenceOutput+1) + `"}`} {
		if _, err := decodeFormattedText([]byte(raw)); err == nil {
			t.Fatal("invalid result accepted")
		}
	}
	if _, err := legacy.FormatText(ctx, "", strings.Repeat("é", maxReferenceInput/2+1)); err == nil {
		t.Fatal("oversized UTF-8 input accepted")
	}
}
