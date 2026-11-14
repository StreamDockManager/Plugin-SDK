package sdk

import "testing"

// Verify compiled capability framing
func TestCompiledCapabilityFraming(t *testing.T) {
	prior := CompiledDescriptor
	defer func() { CompiledDescriptor = prior }()
	value := Capabilities{Version: 1, ID: "fixture", PluginVersion: "0.1.0", ThemeProtocol: 1, SettingsProtocol: 1}
	frame, err := FrameCapabilities(value)
	if err != nil {
		t.Fatal(err)
	}
	CompiledDescriptor = frame
	result, err := RuntimeCapabilities()
	if err != nil || result.ID != "fixture" {
		t.Fatal(result, err)
	}
	for _, invalid := range []string{DescriptorPrefix + "ffffffff:x" + DescriptorSuffix, frame[:len(frame)-1], "bad"} {
		CompiledDescriptor = invalid
		if _, err := RuntimeCapabilities(); err == nil {
			t.Fatal("accepted invalid framing")
		}
	}
}
