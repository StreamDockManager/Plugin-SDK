// Package netdebug connects optional transport diagnostics to the SDK log queue
// without adding HTTP dependencies to plugins that do not use HTTP
package netdebug

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"time"
	"unicode/utf8"
)

const BodyLimit = 64 << 10

type Body struct {
	Text        string `json:"text"`
	Encoding    string `json:"encoding"`
	ContentType string `json:"content_type"`
	Bytes       uint64 `json:"bytes"`
	Complete    bool   `json:"complete"`
	Truncated   bool   `json:"truncated"`
}
type Details struct {
	RequestID       string   `json:"request_id"`
	Request         Body     `json:"request"`
	Response        Body     `json:"response"`
	RequestHeaders  *Headers `json:"request_headers,omitempty"`
	ResponseHeaders *Headers `json:"response_headers,omitempty"`
}
type Event struct {
	Message string
	Failed  bool
	Details *Details
}
type configuration struct {
	owner  any
	report func(Event)
}

var active atomic.Pointer[configuration]
var sequence atomic.Uint64

// Configure the diagnostic sink for a connection owner
func Configure(owner any, enabled bool, report func(Event)) {
	if !enabled {
		Clear(owner)
		return
	}
	active.Store(&configuration{owner, report})
}

// Remove the diagnostic sink belonging to an owner
func Clear(owner any) {
	for {
		current := active.Load()
		if current == nil || current.owner != owner || active.CompareAndSwap(current, nil) {
			return
		}
	}
}

// Endpoint discards userinfo, fragments and every query value
func Endpoint(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return "[endpoint]"
	}
	endpoint := u.Host + u.EscapedPath()
	query := u.Query()
	for key := range query {
		query[key] = []string{"[redacted]"}
	}
	if len(query) > 0 {
		endpoint += "?" + query.Encode()
	}
	return endpoint
}

// Capture only retains bytes that the caller actually reads. Oversized bodies
// are omitted entirely, so a truncated credential cannot escape sanitization
type Capture struct {
	mu          sync.Mutex
	data        []byte
	size        uint64
	complete    bool
	truncated   bool
	expected    int64
	contentType string
}

// Create a bounded request or response body capture
func NewCapture(contentType string, expected int64, empty bool) *Capture {
	if len(contentType) > 256 {
		contentType = "[content type omitted]"
	}
	return &Capture{contentType: contentType, expected: expected, complete: empty}
}

// Append bytes while respecting the diagnostic capture limit
func (c *Capture) Write(data []byte) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.size += uint64(len(data))
	if c.size > BodyLimit {
		c.truncated = true
		c.data = nil
	} else if !c.truncated {
		c.data = append(c.data, data...)
	}
}

// Record whether the captured body reached its end
func (c *Capture) Done(eof bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.complete = eof || (c.expected >= 0 && uint64(c.expected) == c.size)
}

// Return a snapshot of the captured body
func (c *Capture) Snapshot() Body {
	c.mu.Lock()
	defer c.mu.Unlock()
	b := Body{ContentType: c.contentType, Bytes: c.size, Complete: c.complete, Truncated: c.truncated, Encoding: "utf8"}
	textual := utf8.Valid(c.data)
	for _, b := range c.data {
		if (b < 32 && b != '\n' && b != '\r' && b != '\t') || b == 127 {
			textual = false
			break
		}
	}
	if textual {
		b.Text = string(c.data)
	} else {
		b.Encoding = "base64"
		b.Text = base64.StdEncoding.EncodeToString(c.data)
	}
	return b
}

// Identify diagnostic keys that may contain secrets
func sensitive(key string) bool {
	key = strings.ToLower(key)
	for _, part := range []string{"password", "secret", "token", "api_key", "api-key", "apikey", "authorization", "cookie", "webhook"} {
		if strings.Contains(key, part) {
			return true
		}
	}
	return false
}

// Sanitize applies known credential redaction to raw bytes before binary encoding,
// and masks credential fields in complete JSON and form bodies
func (b *Body) Sanitize(redact func(string) string) {
	b.ContentType = strings.ToValidUTF8(redact(b.ContentType), "�")
	if len(b.ContentType) > 256 {
		b.ContentType = "[content type omitted]"
	}
	raw := []byte(b.Text)
	if b.Encoding == "base64" {
		var err error
		raw, err = base64.StdEncoding.DecodeString(b.Text)
		if err != nil {
			b.Text = ""
			b.Truncated = true
			return
		}
	}
	var value any
	if json.Unmarshal(raw, &value) == nil {
		var walk func(any) any
		walk = func(v any) any {
			switch v := v.(type) {
			case string:
				return redact(v)
			case map[string]any:
				for key, item := range v {
					if sensitive(key) {
						v[key] = "[redacted]"
					} else {
						v[key] = walk(item)
					}
				}
			case []any:
				for index, item := range v {
					v[index] = walk(item)
				}
			}
			return v
		}
		value = walk(value)
		raw, _ = json.Marshal(value)
	} else if len(raw) > 0 && (strings.HasPrefix(strings.TrimSpace(string(raw)), "{") || strings.HasPrefix(strings.TrimSpace(string(raw)), "[") || strings.Contains(strings.ToLower(b.ContentType), "json")) {
		b.Text = ""
		b.Truncated = true
		return
	} else if strings.Contains(strings.ToLower(b.ContentType), "application/x-www-form-urlencoded") {
		if values, err := url.ParseQuery(string(raw)); err == nil {
			for key := range values {
				if sensitive(key) {
					values[key] = []string{"[redacted]"}
				}
			}
			raw = []byte(values.Encode())
		}
	}
	raw = []byte(redact(string(raw)))
	if len(raw) > BodyLimit {
		b.Text = ""
		b.Truncated = true
		return
	}
	if b.Encoding == "base64" {
		b.Text = base64.StdEncoding.EncodeToString(raw)
	} else {
		b.Text = strings.ToValidUTF8(string(raw), "�")
	}
	// Formatting and redaction can expand text; preserve the same storage bound
	if len(b.Text) > BodyLimit*2 {
		b.Text = ""
		b.Truncated = true
	}
}

type Trace struct {
	config                          *configuration
	id                              uint64
	label                           string
	started                         time.Time
	Request, Response               *Capture
	RequestHeaders, ResponseHeaders *HeaderCapture
	once                            sync.Once
}

// Start a bounded HTTP diagnostic trace
func Start(method string, endpoint *url.URL) *Trace {
	config := active.Load()
	if config == nil {
		return nil
	}
	id := sequence.Add(1)
	method = strings.Map(func(r rune) rune {
		if r < 32 || r == 127 {
			return -1
		}
		return r
	}, method)
	return &Trace{config: config, id: id, started: time.Now(), Request: NewCapture("", 0, true), Response: NewCapture("", -1, false), label: fmt.Sprintf("Request %d · %s %s", id, method, Endpoint(endpoint.String()))}
}

// Publish the completed trace with status or failure details
func (t *Trace) Finish(status int, err error) {
	t.once.Do(func() {
		if active.Load() != t.config {
			return
		}
		result := fmt.Sprintf("HTTP %d", status)
		if err != nil {
			result = "request failed or cancelled"
		}
		details := &Details{RequestID: fmt.Sprint(t.id), Request: t.Request.Snapshot(), Response: t.Response.Snapshot(), RequestHeaders: t.RequestHeaders.Snapshot(), ResponseHeaders: t.ResponseHeaders.Snapshot()}
		t.config.report(Event{Message: fmt.Sprintf("%s · %s · %d ms", t.label, result, time.Since(t.started).Milliseconds()), Failed: err != nil || status >= 400, Details: details})
	})
}

// Report socket I/O metadata without including payload bytes
func IO(address net.Addr, operation string, size int) {
	if config := active.Load(); config != nil {
		endpoint := "[endpoint]"
		if address != nil {
			endpoint = address.String()
		}
		config.report(Event{Message: fmt.Sprintf("Connection %s · %s · %d bytes", endpoint, operation, size)})
	}
}
