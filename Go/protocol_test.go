package sdk

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// Verify shared host negotiation and legacy fallback
func TestSharedHostNegotiationAndLegacyFallback(t *testing.T) {
	raw, err := os.ReadFile("testdata/protocol-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Manager, Legacy json.RawMessage
		Gates           map[string]string
		Events          []json.RawMessage
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	manager, err := ParseHostInfo(fixture.Manager)
	if err != nil {
		t.Fatal(err)
	}
	legacy, err := ParseHostInfo(fixture.Legacy)
	if err != nil {
		t.Fatal(err)
	}
	for event, feature := range fixture.Gates {
		for _, host := range []HostInfo{manager, legacy} {
			c := &Client{host: &host, out: make(chan Event, 128)}
			err = c.Send(context.Background(), Message(event, "binding", map[string]any{}))
			if host.Supports(feature) != (err == nil) {
				t.Fatalf("%s: support=%v, error=%v", event, host.Supports(feature), err)
			}
		}
	}
	c := &Client{host: &legacy, out: make(chan Event, 128)}
	if err = c.Display(context.Background(), "binding", "Fallback", "", 1, "layout.json", map[string]any{"value": 1}); err != nil {
		t.Fatal(err)
	}
	if len(c.out) != 4 {
		t.Fatal("base host lost standard rich feedback")
	}
	for _, name := range []string{"setState", "setTitle", "setFeedbackLayout", "setFeedback"} {
		if event := <-c.out; event.Event != name {
			t.Fatal(event)
		}
	}
	var device Event
	if err = json.Unmarshal(fixture.Events[0], &device); err != nil || len(device.DeviceInfo) == 0 {
		t.Fatal("device metadata lost", err)
	}
	for _, data := range []string{"[]", "null", "3", "{"} {
		if _, err = ParseHostInfo([]byte(data)); err == nil {
			t.Fatal("invalid host accepted", data)
		}
	}
}

// Verify inspector replies carry action identity
func TestInspectorRepliesCarryActionIdentity(t *testing.T) {
	host, _ := ParseHostInfo([]byte(`{}`))
	c := &Client{host: &host, out: make(chan Event, 128)}
	if err := c.ToInspector(context.Background(), "binding", "org.fixture.action", map[string]any{"value": 1}); err != nil {
		t.Fatal(err)
	}
	if message := <-c.out; message.Action != "org.fixture.action" {
		t.Fatal("action identity lost")
	}
	if err := c.ToInspector(context.Background(), "binding", "", nil); err == nil {
		t.Fatal("missing action accepted")
	}
}

// Verify encoded message limit
func TestEncodedMessageLimit(t *testing.T) {
	raw, _ := os.ReadFile("testdata/protocol-v1.json")
	var fixture map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fixture)
	for _, mode := range []string{"manager", "legacy"} {
		host, _ := ParseHostInfo(fixture[mode])
		c, out := NewLocalClient(host, "fixture")
		for _, prefix := range []string{"", "é😀", "data:image/png;base64,"} {
			base := Message("setImage", "a", map[string]any{"image": prefix})
			encoded, _ := json.Marshal(base)
			for _, delta := range []int{-1, 0, 1} {
				e := Message("setImage", "a", map[string]any{"image": prefix + strings.Repeat("x", MaxMessageBytes-len(encoded)+delta)})
				err := c.Send(context.Background(), e)
				if delta > 0 {
					if err == nil || !strings.Contains(err.Error(), "1048576") {
						t.Fatal(mode, delta, err)
					}
				} else {
					if err != nil {
						t.Fatal(mode, delta, err)
					}
					got, _ := json.Marshal(<-out)
					if len(got) != MaxMessageBytes+delta {
						t.Fatal(len(got))
					}
				}
			}
		}
	}
}

// Verify outbound byte budget releases and isolates logs
func TestOutboundByteBudgetReleasesAndIsolatesLogs(t *testing.T) {
	raw, _ := os.ReadFile("testdata/protocol-v1.json")
	var fixture map[string]json.RawMessage
	_ = json.Unmarshal(raw, &fixture)
	host, _ := ParseHostInfo(fixture["manager"])
	c, output := NewLocalClient(host, "fixture")
	c.logOut = make(chan Event, 128)
	message := Message("setTitle", "a", map[string]any{"title": "é"})
	base, _ := json.Marshal(message)
	message = Message("setTitle", "a", map[string]any{"title": "é" + strings.Repeat("x", MaxMessageBytes-len(base))})
	for i := range 8 {
		if err := c.Send(context.Background(), message); err != nil {
			t.Fatal(i, err)
		}
	}
	if c.Send(context.Background(), Message("setTitle", "a", map[string]any{})) == nil {
		t.Fatal("aggregate budget exceeded")
	}
	if err := c.Send(context.Background(), Message("logMessage", "", map[string]any{"message": "independent"})); err != nil {
		t.Fatal(err)
	}
	<-output
	if err := c.Send(context.Background(), message); err != nil {
		t.Fatal("budget not released", err)
	}
	for len(output) > 0 {
		<-output
	}
	for range 128 {
		_ = c.Send(context.Background(), Message("logMessage", "", map[string]any{"message": strings.Repeat("x", 16384)}))
	}
	if err := c.Send(context.Background(), Message("actionResult", "a", map[string]any{"success": true})); err != nil {
		t.Fatal("logs consumed receipt budget", err)
	}
}

// Create host metadata from the current protocol fixture
func currentTestHost(t *testing.T) *HostInfo {
	t.Helper()
	raw, err := os.ReadFile("testdata/protocol-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	host, err := ParseHostInfo(fixture["manager"])
	if err != nil {
		t.Fatal(err)
	}
	return &host
}

// Verify shared explicit negotiation
func TestSharedExplicitNegotiation(t *testing.T) {
	raw, _ := os.ReadFile("testdata/protocol-v1.json")
	var fixture struct {
		Negotiation []struct {
			Name      string
			Host      json.RawMessage
			Supported bool
		}
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, test := range fixture.Negotiation {
		t.Run(test.Name, func(t *testing.T) {
			host, err := ParseHostInfo(test.Host)
			if err != nil {
				t.Fatal(err)
			}
			if host.Supports("plugin-protocol-v1") != test.Supported {
				t.Fatal("wrong negotiation outcome")
			}
			client, output := NewLocalClient(host, "fixture")
			if err := client.Title(context.Background(), "context", "base"); err != nil {
				t.Fatal(err)
			}
			<-output
			err = client.Feedback(context.Background(), "context", map[string]any{"value": 1})
			if err != nil {
				t.Fatal("extension gate", err)
			}
		})
	}
	rawClient := &Client{out: make(chan Event, 1)}
	if err := rawClient.DesktopInput(context.Background(), "context", nil); err == nil {
		t.Fatal("missing negotiation gained privileges")
	}
}

// Verify required telemetry observer
func TestRequiredTelemetryObserver(t *testing.T) {
	raw, err := os.ReadFile("testdata/protocol-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Telemetry []struct {
			Name    string
			Payload json.RawMessage
			Valid   bool
		}
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	for _, c := range f.Telemetry {
		t.Run(c.Name, func(t *testing.T) {
			snapshot := TelemetrySnapshot{AudioObserver: true}
			err := json.Unmarshal(c.Payload, &snapshot)
			if (err == nil) != c.Valid {
				t.Fatalf("snapshot: %v", err)
			}
			if !c.Valid && !snapshot.AudioObserver {
				t.Fatal("invalid decode mutated snapshot")
			}
			raw, _ := json.Marshal(Message("didReceiveTelemetry", "", json.RawMessage(c.Payload)))
			var event Event
			if err := json.Unmarshal(raw, &event); (err == nil) != c.Valid {
				t.Fatalf("event: %v", err)
			}
			if c.Valid {
				encoded, _ := json.Marshal(snapshot)
				var fields map[string]any
				_ = json.Unmarshal(encoded, &fields)
				if _, ok := fields["audio_observer"].(bool); !ok {
					t.Fatal("required flag omitted")
				}
			}
		})
	}
}
