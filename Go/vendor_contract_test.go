package sdk

import (
	"encoding/json"
	"testing"
)

// Current Elgato core events/ui.ts defines inspector payloads as JsonValue
func TestVendorInspectorJSONPayloadAndCorrelationID(t *testing.T) {
	for _, raw := range []string{`null`, `[1,{"unknown":true}]`, `"text"`, `false`, `0`} {
		var event Event
		if err := json.Unmarshal([]byte(`{"event":"sendToPlugin","context":"action","payload":`+raw+`}`), &event); err != nil {
			t.Fatal(err)
		}
		if string(event.Payload) != raw {
			t.Fatal("payload changed", string(event.Payload))
		}
	}
	raw, err := json.Marshal(Event{Event: "getSettings", Context: "action", ID: "request"})
	if err != nil {
		t.Fatal(err)
	}
	var value map[string]any
	_ = json.Unmarshal(raw, &value)
	if value["id"] != "request" {
		t.Fatal("missing vendor correlation id")
	}
}
