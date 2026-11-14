package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/StreamDockManager/Plugin-SDK/Go/httptrack"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type HTTPError struct {
	Status int
	Retry  time.Time
}

// Describe the service HTTP failure
func (e *HTTPError) Error() string {
	return fmt.Sprintf("Service returned HTTP %d; check credentials, permissions and selection", e.Status)
}

// Extract the server retry deadline from a service error
func RetryAt(err error) time.Time {
	if e, ok := errors.AsType[*HTTPError](err); ok {
		return e.Retry
	}
	return time.Time{}
}

type Service struct {
	Client          *http.Client
	MaximumResponse int64
}

// Create a service client using normal TLS verification
func NewService() *Service {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	return &Service{Client: httptrack.TrackHTTPClient(&http.Client{Transport: transport, Timeout: 10 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }})}
}

// Close idle service HTTP connections
func (h *Service) Close() { h.Client.CloseIdleConnections() }

// Perform a bounded service request and return its response bytes
func (h *Service) Do(ctx context.Context, method, endpoint string, headers map[string]string, body []byte) ([]byte, error) {
	u, err := url.Parse(endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("invalid service endpoint")
	}
	req, err := http.NewRequestWithContext(ctx, method, u.String(), bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("invalid request")
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	response, err := h.Client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("service connection failed; check address, TLS and credentials")
	}
	defer response.Body.Close()
	limit := h.MaximumResponse
	if limit <= 0 {
		limit = 4 << 20
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, fmt.Errorf("invalid or oversized service response")
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		retry := ParseRetryAfter(response.Header.Get("Retry-After"), time.Now())
		return nil, &HTTPError{Status: response.StatusCode, Retry: retry}
	}
	return data, nil
}

// Encode a JSON request and decode the bounded response
func (h *Service) JSON(ctx context.Context, method, endpoint string, headers map[string]string, body any, out any) error {
	var data []byte
	var err error
	if body != nil {
		copyHeaders := map[string]string{}
		contentType := false
		for key, value := range headers {
			copyHeaders[key] = value
			if strings.EqualFold(key, "Content-Type") {
				contentType = true
			}
		}
		if !contentType {
			copyHeaders["Content-Type"] = "application/json"
		}
		headers = copyHeaders
		data, err = json.Marshal(body)
		if err != nil {
			return fmt.Errorf("invalid JSON request")
		}
	}
	data, err = h.Do(ctx, method, endpoint, headers, data)
	if err != nil {
		return err
	}
	if out != nil && len(data) > 0 {
		if err = json.Unmarshal(data, out); err != nil {
			return fmt.Errorf("malformed service response")
		}
	}
	return nil
}

// Drop response details while retaining status and retry information
func (e *HTTPError) SafeError() error { return &HTTPError{Status: e.Status, Retry: e.Retry} }
