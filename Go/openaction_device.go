package sdk

import (
	"encoding/json"
	"fmt"
	"strings"
)

// OpenActionDeviceProvider builds OpenDeck-specific wire envelopes. It neither
// grants a namespace nor sends messages or accesses hardware. Declare the same
// two-character DeviceNamespace in the manifest and use only on an opted-in host
// Wire reference: nekename/OpenDeck commit 3ddecc1ce8952a423916cf066724356267b97410
type OpenActionDeviceProvider struct{ namespace string }

// Create a device provider with a validated namespace
func NewOpenActionDeviceProvider(namespace string) (*OpenActionDeviceProvider, error) {
	if len(namespace) != 2 || namespace[0] < 33 || namespace[0] > 126 || namespace[1] < 33 || namespace[1] > 126 {
		return nil, fmt.Errorf("DeviceNamespace must be two printable ASCII characters")
	}
	return &OpenActionDeviceProvider{namespace: namespace}, nil
}

// Ensure a device identifier belongs to this provider
func (p *OpenActionDeviceProvider) check(device string) error {
	if p == nil || len(p.namespace) != 2 || len(device) > 256 || !strings.HasPrefix(device, p.namespace) {
		return fmt.Errorf("device must belong to the explicitly selected namespace")
	}
	return nil
}

type OpenActionDeviceInfo struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Rows        uint8  `json:"rows"`
	Columns     uint8  `json:"columns"`
	Encoders    uint8  `json:"encoders"`
	Touchpoints uint8  `json:"touchpoints"`
	Infobars    uint8  `json:"infobars"`
	Type        uint8  `json:"type"`
}

// Build a provider registration envelope
func (p *OpenActionDeviceProvider) Register(info OpenActionDeviceInfo) (map[string]any, error) {
	if err := p.check(info.ID); err != nil {
		return nil, err
	}
	if len(info.Name) == 0 || len(info.Name) > 1024 {
		return nil, fmt.Errorf("device name must contain 1..1024 bytes")
	}
	return map[string]any{"event": "registerDevice", "payload": info}, nil
}

// Build a namespace-checked device lifecycle envelope
func (p *OpenActionDeviceProvider) DeviceEvent(event, device string) (map[string]any, error) {
	if err := p.check(device); err != nil {
		return nil, err
	}
	if event != "deregisterDevice" && event != "rerenderImages" {
		return nil, fmt.Errorf("unsupported device event")
	}
	return map[string]any{"event": event, "payload": device}, nil
}

// Build a key or encoder press envelope
func (p *OpenActionDeviceProvider) Press(event, device string, position uint8) (map[string]any, error) {
	if err := p.check(device); err != nil {
		return nil, err
	}
	switch event {
	case "keyDown", "keyUp", "encoderDown", "encoderUp":
	default:
		return nil, fmt.Errorf("unsupported press event")
	}
	return map[string]any{"event": event, "payload": map[string]any{"device": device, "position": position}}, nil
}

// Build an encoder rotation envelope
func (p *OpenActionDeviceProvider) EncoderChange(device string, position uint8, ticks int16) (map[string]any, error) {
	if err := p.check(device); err != nil {
		return nil, err
	}
	return map[string]any{"event": "encoderChange", "payload": map[string]any{"device": device, "position": position, "ticks": ticks}}, nil
}

// Build a touchscreen press envelope
func (p *OpenActionDeviceProvider) TouchscreenPress(device string, position uint8, x, y uint16, hold bool) (map[string]any, error) {
	if err := p.check(device); err != nil {
		return nil, err
	}
	return map[string]any{"event": "touchscreenPress", "payload": map[string]any{"device": device, "position": position, "x": x, "y": y, "hold": hold}}, nil
}

// OpenActionDeviceOutput is a provider command, not an action-plugin setImage
// A setImage with nil Controller/Position/Image requests clearing the screen
// Image is opaque; callers must validate/decode it before hardware use
type OpenActionDeviceOutput struct {
	Event      string  `json:"event"`
	Device     string  `json:"device"`
	Controller *string `json:"controller"`
	Position   *uint8  `json:"position"`
	Image      *string `json:"image"`
	Brightness *uint8  `json:"brightness"`
}

// Decode bounded host output addressed to this provider
func (p *OpenActionDeviceProvider) ParseOutput(data []byte) (OpenActionDeviceOutput, error) {
	var out OpenActionDeviceOutput
	if len(data) > MaxMessageBytes {
		return out, fmt.Errorf("device output exceeds message bound")
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, err
	}
	if err := p.check(out.Device); err != nil {
		return out, err
	}
	switch out.Event {
	case "setImage":
		if (out.Controller == nil) != (out.Position == nil) || (out.Controller == nil && out.Image != nil) {
			return out, fmt.Errorf("invalid image controller/position")
		}
	case "setBrightness":
		if out.Brightness == nil {
			return out, fmt.Errorf("brightness required")
		}
	default:
		return out, fmt.Errorf("unsupported device output")
	}
	return out, nil
}

// Build a device removal envelope
func (p *OpenActionDeviceProvider) Deregister(device string) (map[string]any, error) {
	return p.DeviceEvent("deregisterDevice", device)
}

// Request the host to resend device images
func (p *OpenActionDeviceProvider) RerenderImages(device string) (map[string]any, error) {
	return p.DeviceEvent("rerenderImages", device)
}

// Build a key-down event for the selected position
func (p *OpenActionDeviceProvider) KeyDown(device string, position uint8) (map[string]any, error) {
	return p.Press("keyDown", device, position)
}

// Build a key-up event for the selected position
func (p *OpenActionDeviceProvider) KeyUp(device string, position uint8) (map[string]any, error) {
	return p.Press("keyUp", device, position)
}

// Build an encoder-down event for the selected position
func (p *OpenActionDeviceProvider) EncoderDown(device string, position uint8) (map[string]any, error) {
	return p.Press("encoderDown", device, position)
}

// Build an encoder-up event for the selected position
func (p *OpenActionDeviceProvider) EncoderUp(device string, position uint8) (map[string]any, error) {
	return p.Press("encoderUp", device, position)
}
