package sdk

import (
	"context"
	"encoding/json"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/StreamDockManager/Plugin-SDK/Go/internal/netdebug"
)

// Verify logging network contract
func TestLoggingNetworkContract(t *testing.T) {
	raw, err := os.ReadFile("testdata/protocol-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Manager json.RawMessage `json:"manager"`
		Logging struct {
			Request struct {
				Method, URL string
				Headers     map[string]string
				Body        map[string]any
			}
			Response struct {
				Status  int
				Headers map[string][]string
				Body    map[string]any
			}
			PartialJSON string `json:"partial_json"`
			Forbidden   []string
		}
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	host, _ := ParseHostInfo(fixture.Manager)
	client, output := NewLocalClient(host, "fixture")
	client.SetNetworkDebug(true)
	defer client.SetNetworkDebug(false)
	request, response := fixture.Logging.Request, fixture.Logging.Response
	endpoint, _ := url.Parse(request.URL)
	trace := netdebug.Start(request.Method, endpoint)
	headers := map[string][]string{}
	for key, value := range request.Headers {
		headers[key] = []string{value}
	}
	trace.RequestHeaders = netdebug.NewHeaders(headers)
	trace.ResponseHeaders = netdebug.NewHeaders(response.Headers)
	body, _ := json.Marshal(request.Body)
	trace.Request = netdebug.NewCapture("application/json", int64(len(body)), false)
	trace.Request.Write(body)
	trace.Request.Done(true)
	body, _ = json.Marshal(response.Body)
	trace.Response = netdebug.NewCapture("application/json", int64(len(body)), false)
	trace.Response.Write(body)
	trace.Response.Done(true)
	trace.Finish(response.Status, nil)
	trace.Finish(response.Status, nil)
	event := <-output
	if len(output) != 0 {
		t.Fatal("duplicate trace")
	}
	for _, secret := range fixture.Logging.Forbidden {
		if strings.Contains(string(event.Payload), secret) {
			t.Fatalf("leaked fixture credential %q", secret)
		}
	}
	var payload struct {
		Source, Stream string
		Network        struct {
			Request, Response struct {
				Text     string
				Complete bool
			}
		}
	}
	if err = json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Source != "network" || payload.Stream != "stdout" || !payload.Network.Response.Complete || !strings.Contains(payload.Network.Request.Text, "retained") {
		t.Fatal(payload)
	}
	stale := netdebug.Start("GET", endpoint)
	client.SetNetworkDebug(false)
	client.SetNetworkDebug(true)
	stale.Finish(200, nil)
	if len(output) != 0 {
		t.Fatal("disabled trace survived a configuration generation")
	}
	malformed := netdebug.Start("GET", endpoint)
	malformed.Response = netdebug.NewCapture("application/json", -1, false)
	malformed.Response.Write([]byte(fixture.Logging.PartialJSON))
	malformed.Finish(200, nil)
	var partial struct {
		Network struct {
			Response struct {
				Text      string
				Truncated bool
			}
		}
	}
	if err := json.Unmarshal((<-output).Payload, &partial); err != nil {
		t.Fatal(err)
	}
	if partial.Network.Response.Text != "" || !partial.Network.Response.Truncated {
		t.Fatal("unsafe partial JSON was retained")
	}

}

type releaseLoggingHandler struct{ completionHandler }

// Limit fixture action logging to key release
func (releaseLoggingHandler) ShouldLogAction(event Event) bool { return event.Event == "keyUp" }

// Verify logging action classification and legacy framing
func TestLoggingActionClassificationAndLegacyFraming(t *testing.T) {
	host := HostInfo{}
	host.Manager.ContractVersion = 1
	host.Manager.Capabilities = []string{"plugin-logs-v1"}
	client, output := NewLocalClient(host, "fixture")
	handler := releaseLoggingHandler{completionHandler(func(context.Context, *Client, Event) error { return nil })}
	client.Dispatch(context.Background(), handler, Event{Event: "keyDown", Action: "counter"})
	if len(output) != 0 {
		t.Fatal("release-based action logged at key down")
	}
	client.Dispatch(context.Background(), handler, Event{Event: "keyUp", Action: "counter"})
	if len(output) != 1 {
		t.Fatal("release was not logged once")
	}
	<-output
	text := strings.Repeat("界", 12000)
	if err := client.Log(context.Background(), text); err != nil {
		t.Fatal(err)
	}
	joined := ""
	for len(output) > 0 {
		var payload struct{ Message string }
		_ = json.Unmarshal((<-output).Payload, &payload)
		if len(payload.Message) > 16384 {
			t.Fatal("oversized UTF-8 frame")
		}
		joined += payload.Message
	}
	if joined != text {
		t.Fatal("legacy log API lost content")
	}
}
