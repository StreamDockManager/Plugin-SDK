package sdk

import "testing"

// Verify open action device provider scope
func TestOpenActionDeviceProviderScope(t *testing.T) {
	p, err := NewOpenActionDeviceProvider("xy")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = p.Press("keyDown", "zz-device", 0); err == nil {
		t.Fatal("foreign namespace accepted")
	}
	msg, err := p.EncoderChange("xy-device", 2, -3)
	if err != nil || msg["event"] != "encoderChange" {
		t.Fatal(msg, err)
	}
	if _, err = NewOpenActionDeviceProvider("é"); err == nil {
		t.Fatal("non-ASCII namespace accepted")
	}
	if _, err = p.ParseOutput([]byte(`{"event":"setImage","device":"xy-device","controller":null,"position":null,"image":null}`)); err != nil {
		t.Fatal(err)
	}
	if _, err = p.ParseOutput([]byte(`{"event":"setBrightness","device":"xy-device","brightness":256}`)); err == nil {
		t.Fatal("overflow accepted")
	}
	if _, err = p.ParseOutput([]byte(`{"event":"setBrightness","device":"xy-device"}`)); err == nil {
		t.Fatal("missing brightness accepted")
	}
}
