package httpclient

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Verify service JSON bounds retry and no redirect
func TestServiceJSONBoundsRetryAndNoRedirect(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/retry":
			w.Header().Set("Retry-After", "3")
			w.WriteHeader(429)
		case "/redirect":
			http.Redirect(w, r, "/json", http.StatusFound)
		case "/json":
			if r.Method != "POST" || r.Header.Get("Content-Type") != "application/json" {
				t.Error("JSON headers/method")
			}
			w.Write([]byte(`{"value":2}`))
		default:
			w.Write([]byte("large"))
		}
	}))
	defer server.Close()
	service := NewService()
	defer service.Close()
	var response struct{ Value int }
	if err := service.JSON(context.Background(), "POST", server.URL+"/json", nil, map[string]int{"request": 1}, &response); err != nil || response.Value != 2 {
		t.Fatal(response, err)
	}
	_, err := service.Do(context.Background(), "GET", server.URL+"/retry", nil, nil)
	var status *HTTPError
	if !errors.As(err, &status) || status.Status != 429 || time.Until(status.Retry) < 2*time.Second {
		t.Fatal(err)
	}
	if _, err = service.Do(context.Background(), "GET", server.URL+"/redirect", nil, nil); err == nil {
		t.Fatal("redirect was followed")
	}
	service.MaximumResponse = 2
	if _, err = service.Do(context.Background(), "GET", server.URL, nil, nil); err == nil {
		t.Fatal("response limit")
	}
}
