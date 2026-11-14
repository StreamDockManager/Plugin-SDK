package sdk

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/StreamDockManager/Plugin-SDK/Go/httptrack"
	"github.com/StreamDockManager/Plugin-SDK/Go/internal/netdebug"
)

// Verify network debug toggle traces every request and redacts request secrets
func TestNetworkDebugToggleTracesEveryRequestAndRedactsRequestSecrets(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			w.WriteHeader(403)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"result":"response-visible","access_token":"response-secret"}`)
	}))
	defer server.Close()
	info, _ := ParseHostInfo([]byte(`{"streamDockManager":{"contract_version":1,"capabilities":["plugin-logs-v1"]}}`))
	c, events := NewLocalClient(info, "fixture")
	c.SetLogRedactor(func(message string) string { return strings.ReplaceAll(message, "path-secret", "[credential]") })
	defer c.SetNetworkDebug(false)
	client := httptrack.TrackHTTPClient(server.Client())
	do := func(method string) {
		req, _ := http.NewRequest(method, strings.Replace(server.URL, "http://", "http://private-user:private-password@", 1)+"/path-secret?token=query-secret#fragment-secret", strings.NewReader(`{"action":"request-visible","password":"request-secret","note":"path-secret"}`))
		req.Header.Set("Authorization", "private-authorization")
		req.Header.Set("Content-Type", "application/json")
		response, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.ReadAll(response.Body)
		_ = response.Body.Close()
	}
	do("GET")
	select {
	case event := <-events:
		t.Fatal("debug was enabled by default", event)
	default:
	}
	c.Dispatch(context.Background(), completionHandler(func(context.Context, *Client, Event) error { return nil }), Message("didReceiveLogSettings", "", map[string]any{"debug_network": true}))
	do("GET")
	do("POST")
	var transcript strings.Builder
	for range 2 {
		event := <-events
		if event.Event != "logMessage" {
			t.Fatal(event)
		}
		transcript.Write(event.Payload)
	}
	text := transcript.String()
	for _, private := range []string{"private-user", "private-password", "path-secret", "query-secret", "fragment-secret", "private-authorization", "request-secret", "response-secret"} {
		if strings.Contains(text, private) {
			t.Fatal("request diagnostics leaked a secret", private, text)
		}
	}
	if !strings.Contains(text, "HTTP 200") || !strings.Contains(text, "HTTP 403") || strings.Count(text, `"source":"network"`) != 2 || !strings.Contains(text, "request-visible") || !strings.Contains(text, "response-visible") {
		t.Fatal(text)
	}
	c.SetNetworkDebug(false)
	do("GET")
	select {
	case event := <-events:
		t.Fatal("disabled debug still emitted", event)
	default:
	}
}

// Verify network debug connections report metadata without payloads
func TestNetworkDebugConnectionsReportMetadataWithoutPayloads(t *testing.T) {
	info, _ := ParseHostInfo([]byte(`{"streamDockManager":{"contract_version":1,"capabilities":["plugin-logs-v1"]}}`))
	c, events := NewLocalClient(info, "fixture")
	c.SetNetworkDebug(true)
	defer c.SetNetworkDebug(false)
	left, right := net.Pipe()
	defer right.Close()
	tracked := TrackConn(left)
	go func() { _, _ = io.WriteString(right, "private-payload") }()
	data := make([]byte, len("private-payload"))
	if _, err := io.ReadFull(tracked, data); err != nil {
		t.Fatal(err)
	}
	_ = tracked.Close()
	_ = tracked.Close()
	if len(events) != 2 {
		t.Fatal("expected one read and one close", len(events))
	}
	for len(events) > 0 {
		event := <-events
		if strings.Contains(string(event.Payload), "private-payload") || !strings.Contains(string(event.Payload), `"source":"network"`) {
			t.Fatal(event)
		}
	}
}

// Verify network bodies preserve automatic gzip and partial reads
func TestNetworkBodiesPreserveAutomaticGzipAndPartialReads(t *testing.T) {
	info, _ := ParseHostInfo([]byte(`{"streamDockManager":{"contract_version":1,"capabilities":["plugin-logs-v1"]}}`))
	c, events := NewLocalClient(info, "fixture")
	c.SetNetworkDebug(true)
	defer c.SetNetworkDebug(false)
	content := strings.Repeat("response", 1024)
	var compressed bytes.Buffer
	encoder := gzip.NewWriter(&compressed)
	_, _ = io.WriteString(encoder, content)
	_ = encoder.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/gzip" {
			w.Header().Set("Content-Encoding", "gzip")
			_, _ = w.Write(compressed.Bytes())
		} else {
			_, _ = io.WriteString(w, "abcdef")
		}
	}))
	defer server.Close()
	client := httptrack.TrackHTTPClient(server.Client())
	defer client.CloseIdleConnections()
	response, err := client.Get(server.URL + "/gzip")
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 0 {
		t.Fatal("body was read eagerly")
	}
	data, err := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if err != nil || string(data) != content {
		t.Fatal(err, "response changed")
	}
	var event struct {
		Network struct {
			ResponseHeaders *netdebug.Headers `json:"response_headers"`
			Response        struct {
				Text     string
				Complete bool
			}
		}
	}
	if err = json.Unmarshal((<-events).Payload, &event); err != nil || event.Network.Response.Text != content || !event.Network.Response.Complete {
		t.Fatal(event, err)
	}
	if strings.Join(headerValues(event.Network.ResponseHeaders, "Content-Encoding"), "") != "gzip" {
		t.Fatal("raw response headers were changed by decompression", event.Network.ResponseHeaders)
	}
	response, err = client.Get(server.URL + "/partial")
	if err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 2)
	_, _ = io.ReadFull(response.Body, buffer)
	_ = response.Body.Close()
	if err = json.Unmarshal((<-events).Payload, &event); err != nil || event.Network.Response.Text != "ab" || event.Network.Response.Complete {
		t.Fatal(event, err)
	}
}

// Verify network body limit does not limit plugin reads or overflow host frames
func TestNetworkBodyLimitDoesNotLimitPluginReadsOrOverflowHostFrames(t *testing.T) {
	info, _ := ParseHostInfo([]byte(`{"streamDockManager":{"contract_version":1,"capabilities":["plugin-logs-v1"]}}`))
	c, events := NewLocalClient(info, "fixture")
	c.SetNetworkDebug(true)
	defer c.SetNetworkDebug(false)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		_, _ = w.Write(data)
	}))
	defer server.Close()
	client := httptrack.TrackHTTPClient(server.Client())
	defer client.CloseIdleConnections()
	for _, size := range []int{64 << 10, (64 << 10) + 1} {
		body := strings.Repeat("\"", size)
		response, err := client.Post(server.URL+"/"+strings.Repeat("x", 20000), "text/plain", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		actual, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil || string(actual) != body {
			t.Fatalf("response mismatch: status=%d bytes=%d expected=%d error=%v", response.StatusCode, len(actual), size, err)
		}
		event := <-events
		wire, err := json.Marshal(event)
		if err != nil || len(wire) >= 1<<20 {
			t.Fatal("network details exceed host frame budget", len(wire), err)
		}
		var payload struct {
			Network struct {
				Request, Response struct {
					Text                string
					Bytes               uint64
					Complete, Truncated bool
				}
			}
		}
		if err = json.Unmarshal(event.Payload, &payload); err != nil {
			t.Fatal(err)
		}
		for _, captured := range []struct {
			Text                string
			Bytes               uint64
			Complete, Truncated bool
		}{payload.Network.Request, payload.Network.Response} {
			if captured.Bytes != uint64(size) || !captured.Complete || captured.Truncated != (size > 64<<10) {
				t.Fatal(captured)
			}
			if captured.Truncated && captured.Text != "" {
				t.Fatal("oversized body retained partial data")
			}
		}
	}
}
