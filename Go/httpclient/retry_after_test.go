package httpclient

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"
)

// Verify retry after shared
func TestRetryAfterShared(t *testing.T) {
	data, err := os.ReadFile("../testdata/retry-after-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Now   int64 `json:"now_ms"`
		Cases []struct {
			Name, Value string
			Delay       *int64 `json:"delay_ms"`
		}
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	now := time.UnixMilli(fixture.Now)
	for _, c := range fixture.Cases {
		t.Run(c.Name, func(t *testing.T) {
			got := ParseRetryAfter(c.Value, now)
			if c.Delay == nil {
				if !got.IsZero() {
					t.Fatal(got)
				}
			} else if !got.Equal(now.Add(time.Duration(*c.Delay) * time.Millisecond)) {
				t.Fatalf("got %v delay %d", got, *c.Delay)
			}
		})
	}
	// The service path previously multiplied overflowing seconds independently
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "9223372037")
		w.WriteHeader(429)
	}))
	defer server.Close()
	service := NewService()
	defer service.Close()
	start := time.Now()
	_, err = service.Do(context.Background(), "GET", server.URL, nil, nil)
	var failure *HTTPError
	if !errors.As(err, &failure) || failure.Retry.Before(start.Add(maximumRetryAfter)) {
		t.Fatalf("service retry: %v", err)
	}
}

// Verify retry after subsecond bound
func TestRetryAfterSubsecondBound(t *testing.T) {
	now := time.Unix(1735689600, 987654321)
	for _, value := range []string{"9223372037", "Fri, 01 Jan 9999 00:00:00 GMT"} {
		got := ParseRetryAfter(value, now)
		if !got.Equal(now.Add(maximumRetryAfter)) || got.Sub(now) != maximumRetryAfter {
			t.Fatalf("%q exceeded subsecond-safe bound: %v", value, got)
		}
	}
}
