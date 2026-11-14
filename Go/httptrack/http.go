// Package httptrack optionally tracks transferred HTTP payload for SDK reporting
package httptrack

import (
	"compress/gzip"
	"github.com/StreamDockManager/Plugin-SDK/Go/internal/netdebug"
	"github.com/StreamDockManager/Plugin-SDK/Go/internal/payload"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
)

type countingBody struct {
	io.ReadCloser
	count *atomic.Uint64
}

// Read response bytes and increment payload counters
func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.count.Add(uint64(n))
	return n, err
}

type countedGzipBody struct {
	body   io.ReadCloser
	reader *gzip.Reader
	err    error
}

// Read decompressed content while retaining wire-byte accounting
func (b *countedGzipBody) Read(p []byte) (int, error) {
	if b.reader == nil && b.err == nil {
		b.reader, b.err = gzip.NewReader(b.body)
	}
	if b.err != nil {
		return 0, b.err
	}
	return b.reader.Read(p)
}

// Close the counted compressed response body
func (b *countedGzipBody) Close() error { return b.body.Close() }

type countingTransport struct {
	next          http.RoundTripper
	automaticGzip bool
}

// Wrap a transport with shared payload tracking
func trackingTransport(next http.RoundTripper) countingTransport {
	if original, ok := next.(*http.Transport); ok {
		copy := original.Clone()
		automatic := !copy.DisableCompression
		// Count compressed HTTP body bytes, then reproduce net/http's lazy
		// automatic decompression. No process-global transport is modified
		copy.DisableCompression = true
		return countingTransport{copy, automatic}
	}
	return countingTransport{next, false}
}

var defaultTrackedTransport = sync.OnceValue(func() countingTransport { return trackingTransport(http.DefaultTransport) })

// Close idle connections on the underlying transport
func (t countingTransport) CloseIdleConnections() {
	if closer, ok := t.next.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

// Account for request and response network payload
func (t countingTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	copy := request.Clone(request.Context())
	automaticGzip := t.automaticGzip && copy.Header.Get("Accept-Encoding") == "" && copy.Header.Get("Range") == "" && copy.Method != "HEAD"
	if automaticGzip {
		if copy.Header == nil {
			copy.Header = make(http.Header)
		}
		copy.Header.Set("Accept-Encoding", "gzip")
	}
	if copy.Body != nil && copy.Body != http.NoBody {
		copy.Body = &countingBody{copy.Body, &payload.Sent}
	}
	responseTransport := t
	responseTransport.automaticGzip = automaticGzip
	return debugResponse(copy, roundTripFunc(responseTransport.trackedResponse))
}

// Wrap the response body without double-counting decompression
func (t countingTransport) trackedResponse(request *http.Request) (*http.Response, error) {
	response, err := t.next.RoundTrip(request)
	if trace, ok := request.Context().Value(debugTraceKey{}).(*netdebug.Trace); ok && response != nil {
		trace.ResponseHeaders = netdebug.NewHeaders(response.Header)
	}
	if response != nil && response.Body != nil {
		// An opaque custom RoundTripper may already have decoded a body. Such
		// bytes cannot establish transferred payload size and are untracked
		if !response.Uncompressed {
			response.Body = &countingBody{response.Body, &payload.Received}
		}
		if t.automaticGzip && response.Header.Get("Content-Encoding") == "gzip" {
			response.Body = &countedGzipBody{body: response.Body}
			response.Header.Del("Content-Encoding")
			response.Header.Del("Content-Length")
			response.ContentLength = -1
			response.Uncompressed = true
		}
	}
	return response, err
}

// TrackHTTPClient clones a client, preserving cancellation, redirects, proxy/TLS
// settings and streaming. Reuse the returned client for custom transports; all
// clients with the default transport share the SDK's private connection pool
func TrackHTTPClient(client *http.Client) *http.Client {
	copy := *client
	if copy.Transport == nil {
		copy.Transport = defaultTrackedTransport()
	} else if _, wrapped := copy.Transport.(countingTransport); !wrapped {
		copy.Transport = trackingTransport(copy.Transport)
	}
	return &copy
}
