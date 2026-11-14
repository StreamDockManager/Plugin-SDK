package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	engine "github.com/StreamDockManager/Plugin-SDK/Go/authoring"
)

// Verify automatic HTTP revalidation preserves fields and scopes resolved identity
func TestAutomaticHTTPRevalidationPreservesFieldsAndScopesResolvedIdentity(t *testing.T) {
	t.Setenv("POLL_CREDENTIAL", "first-secret")
	var mu sync.Mutex
	var headers []http.Header
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		headers = append(headers, r.Header.Clone())
		mu.Unlock()
		w.Header().Set("ETag", `W/"one"`)
		if r.Header.Get("If-None-Match") == `W/"one"` {
			w.WriteHeader(304)
			return
		}
		_, _ = io.WriteString(w, `{"n":90071992547409931234,"nullable":null,"echo":"`+strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")+`"}`)
	}))
	defer server.Close()
	Cache := NewValidatorCache()
	settings := map[string]any{"url": server.URL + "?key=${POLL_CREDENTIAL}", "headers": map[string]any{"Authorization": "Bearer ${POLL_CREDENTIAL}"}, "fields": map[string]any{"number": "n", "empty": "nullable", "echo": "echo", "missing": "absent"}}
	options := Options{Cache: Cache, Automatic: true}
	first, err := Request(context.Background(), settings, options)
	if err != nil || first.Status != 200 {
		t.Fatal(first, err)
	}
	second, err := Request(context.Background(), settings, options)
	if err != nil || !second.Revalidated || second.Status != 304 || resultText(second.Fields["number"]) != "90071992547409931234" || second.Fields["empty"] != nil || len(second.Missing) != 1 || second.Fields["echo"] != "[redacted]" {
		t.Fatal(second, err)
	}
	if _, err = RequestHTTP(context.Background(), settings); err != nil {
		t.Fatal(err)
	}
	settings["fields"] = map[string]any{"changed": "n"}
	if _, err = Request(context.Background(), settings, options); err != nil {
		t.Fatal(err)
	}
	t.Setenv("POLL_CREDENTIAL", "second-secret")
	if _, err = Request(context.Background(), settings, options); err != nil {
		t.Fatal(err)
	}
	settings["headers"].(map[string]any)["If-None-Match"] = `"user-condition"`
	if _, err = Request(context.Background(), settings, options); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	for i, want := range []string{"", `W/"one"`, "", "", "", `"user-condition"`} {
		if headers[i].Get("If-None-Match") != want {
			t.Fatalf("request %d conditional header %q", i, headers[i].Get("If-None-Match"))
		}
	}
	for _, entry := range Cache.entries {
		if strings.Contains(string(entry.data), "first-secret") || strings.Contains(string(entry.data), "second-secret") {
			t.Fatal("credential escaped redaction into cache")
		}
	}
}

// Verify automatic HTTP no store eviction headers and invalid304
func TestAutomaticHTTPNoStoreEvictionHeadersAndInvalid304(t *testing.T) {
	for _, scenario := range []string{"no-store-304", "no-store-error", "vary", "changed-etag", "last-modified", "head", "explicit"} {
		t.Run(scenario, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := calls.Add(1)
				if scenario == "head" && (r.Header.Get("If-None-Match") != "" || r.Header.Get("If-Modified-Since") != "") {
					t.Error("HEAD acquired generated validators")
				}
				if scenario == "explicit" && r.Header.Get("If-Modified-Since") != "Sat, 01 Jan 2000 00:00:00 GMT" {
					t.Error("user condition changed")
				}
				if scenario == "last-modified" {
					w.Header().Set("Last-Modified", "Sat, 01 Jan 2000 00:00:00 GMT")
				} else {
					w.Header().Set("ETag", `"first"`)
				}
				if scenario == "vary" {
					w.Header().Set("Vary", "*")
				}
				if n == 2 && scenario == "no-store-error" {
					w.Header().Set("Cache-Control", "private, No-Store")
					_, _ = io.WriteString(w, `{} trailing`)
					return
				}
				conditional := r.Header.Get("If-None-Match") != "" || (scenario == "last-modified" && r.Header.Get("If-Modified-Since") != "")
				if n == 2 && conditional {
					if scenario == "no-store-304" {
						w.Header().Set("Cache-Control", "no-store")
					}
					if scenario == "changed-etag" {
						w.Header().Set("ETag", `"different"`)
					}
					w.WriteHeader(304)
					return
				}
				if n == 3 && (scenario == "no-store-304" || scenario == "no-store-error") && conditional {
					t.Error("no-store response retained a validator")
				}
				_, _ = io.WriteString(w, `{"value":42}`)
			}))
			defer server.Close()
			settings := map[string]any{"url": server.URL, "fields": map[string]any{"v": "value"}}
			if scenario == "head" {
				settings["method"] = "HEAD"
			}
			if scenario == "explicit" {
				settings["headers"] = map[string]any{"If-Modified-Since": "Sat, 01 Jan 2000 00:00:00 GMT"}
			}
			Cache := NewValidatorCache()
			options := Options{Cache: Cache, Automatic: true}
			if _, err := Request(context.Background(), settings, options); err != nil {
				t.Fatal(err)
			}
			result, err := Request(context.Background(), settings, options)
			invalid := scenario == "changed-etag" || scenario == "no-store-error"
			if (err != nil) != invalid {
				t.Fatal(result, err)
			}
			if scenario == "changed-etag" && (len(result.Fields) != 0 || result.Revalidated) {
				t.Fatal("unmatched 304 reused data")
			}
			if scenario == "last-modified" && (!result.Revalidated || result.Fields["v"] == nil) {
				t.Fatal(result)
			}
			if _, err = Request(context.Background(), settings, options); err != nil {
				t.Fatal(err)
			}
			if (scenario == "vary" || scenario == "head" || scenario == "explicit") && len(Cache.entries) != 0 {
				t.Fatal("ineligible response cached")
			}
		})
	}
}

// Verify automatic HTTP redirect target and reset cannot reuse an old response
func TestAutomaticHTTPRedirectTargetAndResetCannotReuseAnOldResponse(t *testing.T) {
	var target atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/start" {
			http.Redirect(w, r, fmt.Sprint("/target-", target.Load()), http.StatusFound)
			return
		}
		w.Header().Set("ETag", `"same"`)
		if r.Header.Get("If-None-Match") != "" {
			w.WriteHeader(304)
			return
		}
		_, _ = io.WriteString(w, `{"v":1}`)
	}))
	defer server.Close()
	Cache := NewValidatorCache()
	options := Options{Cache: Cache, Automatic: true}
	settings := map[string]any{"url": server.URL + "/start", "fields": map[string]any{"v": "v"}}
	if _, err := Request(context.Background(), settings, options); err != nil {
		t.Fatal(err)
	}
	target.Store(1)
	result, err := Request(context.Background(), settings, options)
	if err == nil || result.Revalidated || len(result.Fields) != 0 {
		t.Fatal("different redirect target reused prior fields", result, err)
	}
	req, _ := http.NewRequest("GET", server.URL, nil)
	ticket := Cache.begin(req, "", settings, previewRedactor{}, options)
	Cache.Reset()
	Cache.finish(ticket, &http.Response{StatusCode: 200, Request: req, Header: http.Header{"Etag": []string{`"one"`}}}, HTTPResult{})
	if len(Cache.entries) != 0 {
		t.Fatal("completion after settings reset repopulated cache")
	}
	response := &http.Response{StatusCode: 200, Request: req, Header: http.Header{"Etag": []string{`"one"`}}}
	ticket = Cache.begin(req, "", settings, previewRedactor{}, options)
	Cache.finish(ticket, response, HTTPResult{Status: 200, Fields: map[string]any{"v": 42}})
	ticket = Cache.begin(req, "", settings, previewRedactor{}, options)
	Cache.mu.Lock()
	Cache.remove(ticket.key)
	Cache.mu.Unlock()
	response.StatusCode = 304
	if result, err := Cache.revalidate(ticket, response, HTTPResult{Status: 304}); err == nil || len(result.Fields) != 0 {
		t.Fatal("evicted representation reused on 304", result, err)
	}
}

// Verify http validator cache bounds expiry and concurrent workers
func TestHTTPValidatorCacheBoundsExpiryAndConcurrentWorkers(t *testing.T) {
	Cache := NewValidatorCache()
	options := Options{Cache: Cache, Automatic: true}
	// Both count and byte bounds are exercised without retaining request text
	for i := range 100 {
		req, _ := http.NewRequest("GET", fmt.Sprintf("https://fixture.invalid/%d?token=private", i), nil)
		ticket := Cache.begin(req, "", nil, previewRedactor{}, options)
		Cache.store(ticket, httpCacheEntry{data: []byte(strings.Repeat("x", 200<<10)), etag: `"one"`}, false)
	}
	if len(Cache.entries) > httpCacheEntries || Cache.bytes > httpCacheBytes || len(Cache.entries) == 0 {
		t.Fatal("cache bounds", len(Cache.entries), Cache.bytes)
	}
	for key, entry := range Cache.entries {
		entry.at = time.Now().Add(-httpCacheLifetime - time.Second)
		Cache.entries[key] = entry
	}
	req, _ := http.NewRequest("GET", "https://fixture.invalid/new", nil)
	Cache.begin(req, "", nil, previewRedactor{}, options)
	if len(Cache.entries) != 0 || Cache.bytes != 0 {
		t.Fatal("expired data retained")
	}
	ticket := Cache.begin(req, "", nil, previewRedactor{}, options)
	Cache.store(ticket, httpCacheEntry{data: make([]byte, httpCacheValueBytes+1), etag: `"one"`}, false)
	if len(Cache.entries) != 0 {
		t.Fatal("oversized value retained")
	}
	var wg sync.WaitGroup
	for i := range 20 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := range 10 {
				req, _ := http.NewRequest("GET", fmt.Sprintf("https://fixture.invalid/%d", i), nil)
				ticket := Cache.begin(req, "", nil, previewRedactor{}, options)
				Cache.store(ticket, httpCacheEntry{data: []byte(`{}`), etag: `"one"`}, false)
				if j == 5 {
					Cache.Reset()
				}
			}
		}(i)
	}
	wg.Wait()
	if Cache.bytes < 0 || Cache.bytes > httpCacheBytes || len(Cache.entries) > httpCacheEntries {
		t.Fatal("concurrent cache accounting")
	}
}

// Verify http literal request credentials are redacted from extracted strings
func TestHTTPLiteralRequestCredentialsAreRedactedFromExtractedStrings(t *testing.T) {
	req, _ := http.NewRequest("GET", "https://fixture.invalid?api_key=query-secret", nil)
	req.Header.Set("Authorization", "Bearer header-secret")
	req.Header.Set("Cookie", "session=cookie-secret; second=another-secret")
	r := previewRedactor{}.forRequest(req, `{"nested":{"password":"body-secret"}}`)
	for _, secret := range []string{"query-secret", "header-secret", "cookie-secret", "another-secret", "body-secret"} {
		if value := r.redact(secret, 0); value != "[redacted]" {
			t.Fatal("request credential retained", value)
		}
	}
}

var resultText = engine.ResultText

// Verify retry scheduling metadata does not enter inspector payload
func TestRetrySchedulingMetadataDoesNotEnterInspectorPayload(t *testing.T) {
	value := HTTPResult{RetryAfter: time.Now(), Stale: true}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte("RetryAfter")) || bytes.Contains(data, []byte("PollingFeedback")) || !bytes.Contains(data, []byte(`"stale":true`)) {
		t.Fatalf("changed inspector payload: %s", data)
	}
}

// Verify shared preview redaction
func TestSharedPreviewRedaction(t *testing.T) {
	data, err := os.ReadFile("../testdata/authoring-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Redaction struct {
			Input    any
			Secrets  []string
			Expected any
		}
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	r := previewRedactor{secrets: fixture.Redaction.Secrets}
	if got := r.redact(fixture.Redaction.Input, 0); !reflect.DeepEqual(got, fixture.Redaction.Expected) {
		t.Fatalf("preview mismatch: %#v", got)
	}
}
