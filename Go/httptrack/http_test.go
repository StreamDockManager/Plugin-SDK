package httptrack

import (
	"bytes"
	"compress/gzip"
	"context"
	"github.com/StreamDockManager/Plugin-SDK/Go"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// Verify tracked HTTP bodies preserve streaming redirects and connection reuse
func TestTrackedHTTPBodiesPreserveStreamingRedirectsAndConnectionReuse(t *testing.T) {
	sent0, received0 := sdk.NetworkPayload()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, "/stream", http.StatusTemporaryRedirect)
			return
		}
		data, _ := io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Write(data)
		w.(http.Flusher).Flush()
		io.WriteString(w, "response")
	}))
	defer server.Close()
	original := &http.Client{}
	client := TrackHTTPClient(original)
	if original.Transport != nil {
		t.Fatal("global/client transport mutated")
	}
	var requestBytes, responseBytes uint64
	for _, path := range []string{"/redirect", "/stream"} {
		request, _ := http.NewRequestWithContext(context.Background(), "POST", server.URL+path, strings.NewReader("request"))
		response, err := client.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		bytes, err := io.ReadAll(response.Body)
		response.Body.Close()
		if err != nil || string(bytes) != "requestresponse" {
			t.Fatal("stream changed", string(bytes), err)
		}
		responseBytes += uint64(len(bytes))
		requestBytes += 7
		if path == "/redirect" {
			requestBytes += 7
		}
	}
	sent, received := sdk.NetworkPayload()
	if sent-sent0 != requestBytes || received-received0 != responseBytes {
		t.Fatalf("payload mismatch sent=%d received=%d", sent-sent0, received-received0)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request, _ := http.NewRequestWithContext(ctx, "GET", server.URL, nil)
	if _, err := client.Do(request); err == nil {
		t.Fatal("cancelled request succeeded")
	}
}

// Verify tracked HTTP counts compressed payload while preserving automatic gzip
func TestTrackedHTTPCountsCompressedPayloadWhilePreservingAutomaticGzip(t *testing.T) {
	var compressed bytes.Buffer
	encoder := gzip.NewWriter(&compressed)
	content := strings.Repeat("payload", 1024)
	io.WriteString(encoder, content)
	encoder.Close()
	_, received0 := sdk.NetworkPayload()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Accept-Encoding") != "gzip" {
			t.Error("automatic encoding negotiation changed")
		}
		w.Header().Set("Content-Encoding", "gzip")
		w.Write(compressed.Bytes())
	}))
	defer server.Close()
	client := TrackHTTPClient(server.Client())
	defer client.CloseIdleConnections()
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(response.Body)
	response.Body.Close()
	if err != nil || string(data) != content || !response.Uncompressed || response.Header.Get("Content-Encoding") != "" {
		t.Fatal("automatic gzip behavior changed", err)
	}
	_, received := sdk.NetworkPayload()
	if received-received0 != uint64(compressed.Len()) {
		t.Fatalf("counted decoded rather than transferred bytes: %d != %d", received-received0, compressed.Len())
	}
}
