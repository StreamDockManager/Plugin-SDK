package sdk

import (
	"encoding/json"
	"testing"
)

// Verify open action notifications
func TestOpenActionNotifications(t *testing.T) {
	if !OpenActionSettingsRequested(Event{Event: "showSettingsInterface"}) {
		t.Fatal("settings event")
	}
	if OpenActionSettingsRequested(Event{Event: "willAppear"}) {
		t.Fatal("unrelated event")
	}
	value := "openaction://plugins/message/com.example.plugin.sdPlugin/a%2Fb?openaction=hidden"
	raw, _ := json.Marshal(map[string]string{"url": value})
	got, matched, err := OpenActionDeepLink(Event{Event: OpenActionDidReceiveDeepLink, Payload: raw})
	if err != nil || !matched || got != value {
		t.Fatal(got, matched, err)
	}
	for _, invalid := range []string{`{}`, `{"url":2}`, `{"url":""}`, `[]`} {
		if _, _, err := OpenActionDeepLink(Event{Event: OpenActionDidReceiveDeepLink, Payload: json.RawMessage(invalid)}); err == nil {
			t.Fatal(invalid)
		}
	}
	if _, matched, err := OpenActionDeepLink(Event{Event: "keyDown"}); matched || err != nil {
		t.Fatal("unrelated event")
	}
}
