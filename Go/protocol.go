package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
)

// HostInfo retains the host's raw fields so newer metadata remains available
type HostInfo struct {
	Raw     map[string]json.RawMessage
	Manager struct {
		Version         string   `json:"version"`
		ContractVersion int      `json:"contract_version"`
		Capabilities    []string `json:"capabilities"`
		RunID           string   `json:"run_id"`
		DebugNetwork    bool     `json:"debug_network"`
	} `json:"streamDockManager"`
}

// Decode and validate the host registration metadata
func ParseHostInfo(data []byte) (HostInfo, error) {
	var info HostInfo
	if err := json.Unmarshal(data, &info.Raw); err != nil || info.Raw == nil {
		return info, fmt.Errorf("host info must be a JSON object")
	}
	if raw, ok := info.Raw["streamDockManager"]; ok {
		if err := json.Unmarshal(raw, &info.Manager); err != nil {
			// Optional extension metadata cannot disable the base VSD protocol
			info.Manager = HostInfo{}.Manager
			return info, nil
		}

	}
	return info, nil
}

// Check the capabilities advertised by this host
func (h HostInfo) Supports(capability string) bool {
	if h.Manager.ContractVersion != ContractVersion {
		return false
	}
	return slices.Contains(h.Manager.Capabilities, capability)
}

type UnsupportedFeatureError struct{ Feature string }

// Describe the unsupported host feature
func (e *UnsupportedFeatureError) Error() string { return "host does not support " + e.Feature }

// Return the client host metadata
func (c *Client) Host() HostInfo {
	if c.host == nil {
		return HostInfo{}
	}
	return *c.host
}

// Check a capability negotiated for this connection
func (c *Client) Supports(capability string) bool {
	return c.host != nil && c.host.Supports(capability)
}

// Map an outgoing event to its required host capability
func requiredCapability(event string) string {
	switch event {
	case "desktopInput":
		return "desktop-input-v1"
	case "actionResult":
		return "action-results-v1"
	case "subscribeTelemetry":
		return "telemetry-v1"
	case "setDynamicItems":
		return "plugin-settings-v1"
	case "networkPayload":
		return "network-payload-v1"
	}
	return ""
}

// Send an action state update
func (c *Client) State(ctx context.Context, binding string, state int) error {
	return c.Send(ctx, Message("setState", binding, map[string]any{"state": state}))
}

// Select a binding feedback layout
func (c *Client) FeedbackLayout(ctx context.Context, binding, layout string) error {
	return c.Send(ctx, Message("setFeedbackLayout", binding, map[string]any{"layout": layout}))
}

// Replace binding-scoped portable settings
func (c *Client) Settings(ctx context.Context, binding string, value any) error {
	return c.Send(ctx, Message("setSettings", binding, value))
}

// Request the current binding settings
func (c *Client) GetSettings(ctx context.Context, binding string) error {
	return c.Send(ctx, Event{Event: "getSettings", Context: binding})
}

// RequestVSDCraftSettings sends the documented VSD Craft backend request using the registration UUID
// Handle didReceiveSettings as an event; the request does not select an action binding
func (c *Client) RequestVSDCraftSettings(ctx context.Context) error {
	if strings.TrimSpace(c.identity) == "" {
		return fmt.Errorf("VSD Craft settings request requires a registration UUID")
	}
	return c.Send(ctx, Event{Event: "getSettings", Context: c.identity})
}

// Replace plugin-global portable settings
func (c *Client) GlobalSettings(ctx context.Context, value any) error {
	return c.Send(ctx, Message("setGlobalSettings", c.identity, value))
}

// Request the current plugin-global settings
func (c *Client) GetGlobalSettings(ctx context.Context) error {
	return c.Send(ctx, Event{Event: "getGlobalSettings", Context: c.identity})
}

// Send a payload to the matching property inspector
func (c *Client) ToInspector(ctx context.Context, binding, action string, value any) error {
	if action == "" {
		return fmt.Errorf("inspector messaging requires an action UUID")
	}
	message := Message("sendToPropertyInspector", binding, value)
	message.Action = action
	return c.Send(ctx, message)
}

// Show an alert indicator on a binding
func (c *Client) Alert(ctx context.Context, binding string) error {
	return c.Send(ctx, Message("showAlert", binding, map[string]any{}))
}

// Show a success indicator on a binding
func (c *Client) OK(ctx context.Context, binding string) error {
	return c.Send(ctx, Message("showOk", binding, map[string]any{}))
}

// Send a plugin log message
func (c *Client) Log(ctx context.Context, message string) error {
	return c.LogStream(ctx, Stdout, message)
}

// Ask the host to open a URL
func (c *Client) OpenURL(ctx context.Context, address string) error {
	return c.Send(ctx, Message("openUrl", "", map[string]any{"url": address}))
}

// Send dynamic inspector choice data
func (c *Client) DynamicItems(ctx context.Context, value any) error {
	return c.Send(ctx, Message("setDynamicItems", "", value))
}

// Request negotiated desktop input for a binding
func (c *Client) DesktopInput(ctx context.Context, binding string, value any) error {
	return c.Send(ctx, Message("desktopInput", binding, value))
}

// Display sends standard vendor display commands; Manager-only extensions remain negotiated
func (c *Client) Display(ctx context.Context, binding, title, image string, state int, layout string, feedback any) error {
	if err := c.State(ctx, binding, state); err != nil {
		return err
	}
	if err := c.Title(ctx, binding, title); err != nil {
		return err
	}
	if image != "" {
		if err := c.Image(ctx, binding, image); err != nil {
			return err
		}
	}
	{
		if layout != "" {
			if err := c.FeedbackLayout(ctx, binding, layout); err != nil {
				return err
			}
		}
		if feedback != nil {
			return c.Feedback(ctx, binding, feedback)
		}
	}
	return nil
}
