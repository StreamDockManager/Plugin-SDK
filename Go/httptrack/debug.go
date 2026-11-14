package httptrack

import (
	"context"
	"io"
	"net/http"
	"net/http/httptrace"
	"sync"

	"github.com/StreamDockManager/Plugin-SDK/Go/internal/netdebug"
)

type debugBody struct {
	io.ReadCloser
	capture *netdebug.Capture
	finish  func(error)
	once    sync.Once
}

// Finalize captured response diagnostics once
func (b *debugBody) done(err error, eof bool) {
	b.once.Do(func() {
		b.capture.Done(eof)
		if b.finish != nil {
			b.finish(err)
		}
	})
}

// Read and capture bounded response-body diagnostics
func (b *debugBody) Read(buffer []byte) (int, error) {
	n, err := b.ReadCloser.Read(buffer)
	b.capture.Write(buffer[:n])
	if err == io.EOF {
		b.done(nil, true)
	} else if err != nil {
		b.done(err, false)
	}
	return n, err
}

// Close the response body and finalize diagnostics
func (b *debugBody) Close() error { err := b.ReadCloser.Close(); b.done(err, false); return err }

type debugTraceKey struct{}

// Trace one HTTP request through the next transport
func debugResponse(request *http.Request, next http.RoundTripper) (*http.Response, error) {
	trace := netdebug.Start(request.Method, request.URL)
	if trace == nil {
		return next.RoundTrip(request)
	}
	trace.RequestHeaders = netdebug.NewHeaders(request.Header)
	ctx := httptrace.WithClientTrace(request.Context(), &httptrace.ClientTrace{
		WroteHeaderField: trace.RequestHeaders.Written,
		WroteRequest:     func(httptrace.WroteRequestInfo) { trace.RequestHeaders.WroteRequest() },
	})
	copy := request.Clone(context.WithValue(ctx, debugTraceKey{}, trace))
	emptyRequest := request.Body == nil || request.Body == http.NoBody
	expected := request.ContentLength
	if expected == 0 && !emptyRequest {
		expected = -1
	}
	trace.Request = netdebug.NewCapture(request.Header.Get("Content-Type"), expected, emptyRequest)
	trace.Response = netdebug.NewCapture("", -1, false)
	if request.Body != nil && request.Body != http.NoBody {
		copy.Body = &debugBody{ReadCloser: request.Body, capture: trace.Request}
	}
	response, err := next.RoundTrip(copy)
	if err != nil {
		trace.Finish(0, err)
	} else if response == nil {
		trace.Finish(0, nil)
	} else {
		if trace.ResponseHeaders == nil {
			trace.ResponseHeaders = netdebug.NewHeaders(response.Header)
		}
		empty := response.Body == nil || response.Body == http.NoBody
		trace.Response = netdebug.NewCapture(response.Header.Get("Content-Type"), response.ContentLength, empty)
		if empty {
			trace.Finish(response.StatusCode, nil)
		} else {
			status := response.StatusCode
			response.Body = &debugBody{ReadCloser: response.Body, capture: trace.Response, finish: func(err error) { trace.Finish(status, err) }}
		}
	}
	return response, err
}

type roundTripFunc func(*http.Request) (*http.Response, error)

// Adapt a function to the HTTP transport interface
func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

type debugTransport struct{ next http.RoundTripper }

// Send a request through the debug tracing transport
func (t debugTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	return debugResponse(r, t.next)
}

// Forward idle-connection cleanup to the wrapped transport
func (t debugTransport) CloseIdleConnections() {
	if closer, ok := t.next.(interface{ CloseIdleConnections() }); ok {
		closer.CloseIdleConnections()
	}
}

// DebugHTTPClient preserves a library-owned client's transport and accounting
func DebugHTTPClient(client *http.Client) *http.Client {
	copy := *client
	transport := copy.Transport
	if transport == nil {
		transport = http.DefaultTransport
	}
	switch transport.(type) {
	case debugTransport, countingTransport:
		return &copy
	}
	copy.Transport = debugTransport{transport}
	return &copy
}
