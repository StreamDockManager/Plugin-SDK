package sdk

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/StreamDockManager/Plugin-SDK/Go/polling"
)

// CompiledDescriptor is injected from the same Definition that generates the
// manifest. It remains referenced by registration, including stripped builds
var CompiledDescriptor string

const DescriptorPrefix = "SDMCAP1:"
const DescriptorSuffix = ":ENDSDMCAP1"
const DescriptorLimit = 64 << 10

type ThemedAsset struct {
	Light string `json:"light"`
	Dark  string `json:"dark"`
}
type ActionCapability struct {
	Refresh         *polling.RefreshPolicy `json:"refresh,omitempty"`
	ParameterFields []string               `json:"parameter_fields,omitempty"`
	FeedbackLayout  string                 `json:"feedback_layout,omitempty"`
	ControlTypes    []string               `json:"control_types,omitempty"`
	AdditiveDial    bool                   `json:"additive_dial,omitempty"`
	Settings        string                 `json:"settings,omitempty"`
	Icon            ThemedAsset            `json:"icon"`
	States          []ThemedAsset          `json:"states"`
}
type Capabilities struct {
	MinimumManager       string                      `json:"minimum_manager,omitempty"`
	RequiredCapabilities []string                    `json:"required_capabilities,omitempty"`
	Version              int                         `json:"version"`
	ID                   string                      `json:"id"`
	PluginVersion        string                      `json:"plugin_version"`
	ThemeProtocol        int                         `json:"theme_protocol"`
	SettingsProtocol     int                         `json:"settings_protocol"`
	InputProtocol        int                         `json:"input_protocol"`
	PluginSettings       string                      `json:"plugin_settings,omitempty"`
	Icon                 ThemedAsset                 `json:"icon"`
	Actions              map[string]ActionCapability `json:"actions"`
}

// Encode the capability descriptor in its framed wire format
func FrameCapabilities(value Capabilities) (string, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	if len(b) > DescriptorLimit {
		return "", fmt.Errorf("descriptor exceeds limit")
	}
	encoded := base64.StdEncoding.EncodeToString(b)
	return fmt.Sprintf("%s%08x:%s%s", DescriptorPrefix, len(encoded), encoded, DescriptorSuffix), nil
}

// Decode the descriptor embedded at build time
func RuntimeCapabilities() (*Capabilities, error) {
	if CompiledDescriptor == "" {
		return nil, nil
	}
	frame := strings.TrimPrefix(CompiledDescriptor, DescriptorPrefix)
	if frame == CompiledDescriptor || len(frame) < 9 {
		return nil, fmt.Errorf("invalid descriptor framing")
	}
	n, err := strconv.ParseUint(frame[:8], 16, 32)
	if err != nil || n > DescriptorLimit*2 || len(frame) != 9+int(n)+len(DescriptorSuffix) || frame[8] != ':' || !strings.HasSuffix(frame, DescriptorSuffix) {
		return nil, fmt.Errorf("invalid descriptor length")
	}
	b, err := base64.StdEncoding.DecodeString(frame[9 : 9+int(n)])
	if err != nil || len(b) > DescriptorLimit {
		return nil, fmt.Errorf("invalid descriptor data")
	}
	var value Capabilities
	err = json.Unmarshal(b, &value)
	return &value, err
}
