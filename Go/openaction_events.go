package sdk

import (
	"encoding/json"
	"fmt"
)

// OpenAction events are opt-in; no UI or URL is opened by these helpers
const OpenActionShowSettingsInterface = "showSettingsInterface"
const OpenActionDidReceiveDeepLink = "didReceiveDeepLink"

// OpenActionSettingsRequested recognizes OpenDeck's payload-free request
// Set HasSettingsInterface in the manifest only if your handler supplies a UI
func OpenActionSettingsRequested(event Event) bool {
	return event.Event == OpenActionShowSettingsInterface
}

// OpenActionDeepLink returns the untrusted, opaque payload URL, not a decoded
// command. The caller must validate routing/content before taking any action
func OpenActionDeepLink(event Event) (url string, matched bool, err error) {
	if event.Event != OpenActionDidReceiveDeepLink {
		return "", false, nil
	}
	var payload struct {
		URL *string `json:"url"`
	}
	if err := json.Unmarshal(event.Payload, &payload); err != nil || payload.URL == nil || *payload.URL == "" {
		return "", true, fmt.Errorf("OpenAction deep link requires a nonempty payload.url string")
	}
	return *payload.URL, true, nil
}
