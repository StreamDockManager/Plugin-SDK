package sdk

import (
	"context"
	"encoding/json"
	"github.com/StreamDockManager/Plugin-SDK/Go/httptrack"
	"github.com/StreamDockManager/Plugin-SDK/Go/internal/netdebug"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"testing"
)

// Find the captured values for a selected diagnostic header
func headerValues(headers *netdebug.Headers, name string) []string {
	if headers == nil {
		return nil
	}
	var values []string
	for _, field := range headers.Entries {
		if strings.EqualFold(field.Name, name) {
			values = append(values, field.Values...)
		}
	}
	return values
}

// Verify network headers capture wire defaults preserve traffic and redact credentials
func TestNetworkHeadersCaptureWireDefaultsPreserveTrafficAndRedactCredentials(t *testing.T) {
	var traced atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer request-private" || r.Header.Get("X-Private-Token") != "custom-private" || r.Host != "virtual.example.test" {
			t.Error("diagnostics changed request headers", r.Host)
		}
		data, err := io.ReadAll(r.Body)
		if err != nil || string(data) != `{"payload":"body-visible"}` {
			t.Error("diagnostics changed request body", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.Header().Add("Set-Cookie", "session=response-cookie-private; HttpOnly")
		w.Header().Add("Set-Cookie", "other=second-cookie-private; Secure")
		w.Header().Add("X-Trace", "first")
		w.Header().Add("X-Trace", "second")
		w.Header().Set("X-Echo", "request-private custom-private request-cookie-private response-cookie-private")
		w.Header().Set("Location", "/next?token=location-private")
		_, _ = io.WriteString(w, `{"echo":"request-private","payload":"response-visible"}`)
	}))
	defer server.Close()
	info, _ := ParseHostInfo([]byte(`{"streamDockManager":{"contract_version":1,"capabilities":["plugin-logs-v1"]}}`))
	c, events := NewLocalClient(info, "fixture")
	defer c.SetNetworkDebug(false)
	client := httptrack.TrackHTTPClient(server.Client())
	defer client.CloseIdleConnections()
	do := func() {
		request, _ := http.NewRequest("POST", server.URL, strings.NewReader(`{"payload":"body-visible"}`))
		request.Host = "virtual.example.test"
		request.Header.Set("Authorization", "Bearer request-private")
		request.Header.Set("X-Private-Token", "custom-private")
		request.Header.Set("Cookie", "session=request-cookie-private")
		request.Header.Set("Content-Type", "application/json")
		request = request.WithContext(httptrace.WithClientTrace(context.Background(), &httptrace.ClientTrace{WroteHeaderField: func(string, []string) { traced.Add(1) }}))
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if err != nil || !strings.Contains(string(body), "request-private") || response.Header.Get("Set-Cookie") != "session=response-cookie-private; HttpOnly" {
			t.Fatal("diagnostics changed response", err)
		}
	}
	do()
	if len(events) != 0 {
		t.Fatal("headers logged with debug disabled")
	}
	c.SetNetworkDebug(true)
	do()
	event := <-events
	var payload struct{ Network netdebug.Details }
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"request-private", "custom-private", "request-cookie-private", "response-cookie-private", "second-cookie-private", "location-private"} {
		if strings.Contains(string(event.Payload), secret) {
			t.Fatal("credential leaked", secret, string(event.Payload))
		}
	}
	requestHeaders, responseHeaders := payload.Network.RequestHeaders, payload.Network.ResponseHeaders
	if strings.Join(headerValues(requestHeaders, "Host"), "") != "virtual.example.test" || len(headerValues(requestHeaders, "User-Agent")) != 1 || len(headerValues(requestHeaders, "Content-Length")) != 1 {
		t.Fatal("missing transport-generated request headers", requestHeaders)
	}
	if strings.Join(headerValues(responseHeaders, "X-Trace"), ",") != "first,second" || len(headerValues(responseHeaders, "Set-Cookie")) != 2 {
		t.Fatal("repeated response fields lost", responseHeaders)
	}
	if !strings.Contains(payload.Network.Request.Text, "body-visible") || !strings.Contains(payload.Network.Response.Text, "response-visible") || traced.Load() == 0 {
		t.Fatal("body or pre-existing trace hook lost", payload, traced.Load())
	}
	c.SetNetworkDebug(false)
	do()
	if len(events) != 0 {
		t.Fatal("headers emitted after debug disabled")
	}
}
