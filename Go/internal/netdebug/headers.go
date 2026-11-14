package netdebug

import (
	"encoding/base64"
	"encoding/json"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strings"
	"sync"
)

const HeaderLimit = 8 << 10
const HeaderCount = 128

type Header struct {
	Name   string   `json:"name"`
	Values []string `json:"values"`
}
type Headers struct {
	Entries        []Header `json:"entries"`
	Truncated      bool     `json:"truncated"`
	secrets        []string
	secretBytes    int
	secretOverflow bool
}

// Normalize an HTTP header name for comparison
func headerName(name string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			r += 'a' - 'A'
		}
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			return r
		}
		return -1
	}, name)
}

// Identify credential-bearing and otherwise sensitive headers
func SensitiveHeader(name string) bool {
	name = headerName(name)
	if name == "accesscontrolallowcredentials" || name == "authority" {
		return false
	}
	for _, part := range []string{"auth", "cookie", "token", "secret", "password", "credential", "signature", "apikey", "csrf", "xsrf", "hmac", "bearer", "sessionid"} {
		if strings.Contains(name, part) {
			return true
		}
	}
	return name == "key" || strings.HasSuffix(name, "key") || strings.HasSuffix(name, "session")
}

// Retain a sensitive value for later message redaction
func (h *Headers) remember(value string) {
	if h.secretOverflow {
		return
	}
	if value == "" || value == "[redacted]" || value == "[credential]" {
		return
	}
	if slices.Contains(h.secrets, value) {
		return
	}
	if h.secretBytes+len(value) > BodyLimit || len(h.secrets) >= 256 {
		h.secretOverflow = true
		return
	}
	h.secrets = append(h.secrets, value)
	h.secretBytes += len(value)
}

// Collect secrets from credential-bearing header values
func (h *Headers) credentials(name, value string) {
	if h.secretOverflow {
		return
	}
	if len(value) > BodyLimit {
		h.secretOverflow = true
		return
	}
	h.remember(value)
	name = headerName(name)
	if strings.Contains(name, "auth") {
		value = strings.TrimSpace(value)
		for parameter := range strings.SplitSeq(value, ",") {
			if key, part, ok := strings.Cut(parameter, "="); ok {
				// The scheme can precede the first authentication parameter
				keys := strings.Fields(key)
				if len(keys) > 0 {
					key = headerName(keys[len(keys)-1])
				}
				if SensitiveHeader(key) || key == "username" || key == "response" || key == "nonce" || key == "cnonce" {
					part = strings.Trim(strings.TrimSpace(part), "\"")
					h.remember(part)
					if key == "credential" {
						if identity, _, ok := strings.Cut(part, "/"); ok {
							h.remember(identity)
						}
					}
				}
			}
		}
		if _, token, ok := strings.Cut(value, " "); ok {
			token = strings.TrimSpace(token)
			h.remember(token)
			if strings.HasPrefix(strings.ToLower(value), "basic ") {
				if decoded, err := base64.StdEncoding.DecodeString(token); err == nil {
					h.remember(string(decoded))
					if user, password, ok := strings.Cut(string(decoded), ":"); ok {
						h.remember(user)
						h.remember(password)
					}
				}
			}
		}
	}
	if strings.Contains(name, "cookie") {
		pairs := strings.Split(value, ";")
		if strings.Contains(name, "setcookie") {
			pairs = pairs[:1]
		}
		for _, pair := range pairs {
			if _, value, ok := strings.Cut(pair, "="); ok {
				h.remember(strings.Trim(strings.TrimSpace(value), "\""))
			}
		}
	}
}

// HeaderCapture keeps a bounded snapshot, never retaining a partial field value
// Wire trace callbacks replace the request-map fallback without mutating traffic
type HeaderCapture struct {
	mu      sync.Mutex
	headers Headers
	used    int
	wire    bool
	next    bool
}

// Create a bounded capture of HTTP headers
func NewHeaders(values map[string][]string) *HeaderCapture {
	c := &HeaderCapture{headers: Headers{Entries: []Header{}}}
	for name, values := range values {
		c.add(name, values)
	}
	return c
}

// Add sanitized values to the header capture
func (c *HeaderCapture) add(name string, values []string) {
	if len(name) > 256 {
		c.headers.Truncated = true
		c.headers.secretOverflow = true
		return
	}
	safe := SensitiveHeader(name)
	for _, value := range values {
		if safe {
			c.headers.credentials(name, value)
		}
	}
	if len(c.headers.Entries) >= HeaderCount {
		c.headers.Truncated = true
		return
	}
	cost := len(name) + 32
	if c.used+cost > HeaderLimit {
		c.headers.Truncated = true
		return
	}
	field := Header{Name: name, Values: []string{}}
	for _, value := range values {
		if safe {
			value = "[redacted]"
		}
		if c.used+cost+len(value)+8 > HeaderLimit {
			c.headers.Truncated = true
			continue
		}
		field.Values = append(field.Values, value)
		cost += len(value) + 8
	}
	c.used += cost
	c.headers.Entries = append(c.headers.Entries, field)
}

// Record the values actually written for a header
func (c *HeaderCapture) Written(name string, values []string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.wire || c.next {
		c.headers.Entries = []Header{}
		c.headers.Truncated = false
		c.used = 0
		c.wire = true
		c.next = false
	}
	c.add(name, values)
}

// Advance the capture to the next request after transmission
func (c *HeaderCapture) WroteRequest() { c.mu.Lock(); c.next = true; c.mu.Unlock() }

// Return a snapshot of the captured headers
func (c *HeaderCapture) Snapshot() *Headers {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	copy := c.headers
	copy.Truncated = copy.Truncated || copy.secretOverflow
	copy.Entries = make([]Header, len(c.headers.Entries))
	copy.secrets = append([]string(nil), c.headers.secrets...)
	for i, field := range c.headers.Entries {
		copy.Entries[i] = Header{Name: field.Name, Values: append([]string{}, field.Values...)}
	}
	sort.SliceStable(copy.Entries, func(i, j int) bool {
		return strings.ToLower(copy.Entries[i].Name) < strings.ToLower(copy.Entries[j].Name)
	})
	return &copy
}

var absoluteHeaderURL = regexp.MustCompile(`(?i)[a-z][a-z0-9+.-]*://[^\s"<>]+`)
var linkHeaderURL = regexp.MustCompile(`<[^>]*>`)

// Sanitize a URL appearing in header metadata
func safeHeaderURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return "[redacted URL]"
	}
	u.User = nil
	u.Fragment = ""
	query := u.Query()
	for key := range query {
		query[key] = []string{"[redacted]"}
	}
	u.RawQuery = query.Encode()
	return u.String()
}

// Sanitize one header value according to its field name
func headerValue(name, value string) string {
	normalized := headerName(name)
	if strings.HasSuffix(normalized, "url") || strings.HasSuffix(normalized, "uri") || strings.HasPrefix(value, "/") || strings.HasPrefix(value, "?") {
		return safeHeaderURL(value)
	}
	switch normalized {
	case "location", "contentlocation", "referer", "destination":
		return safeHeaderURL(value)
	case "link":
		value = linkHeaderURL.ReplaceAllStringFunc(value, func(link string) string { return "<" + safeHeaderURL(link[1:len(link)-1]) + ">" })
	}
	return absoluteHeaderURL.ReplaceAllStringFunc(value, safeHeaderURL)
}

// HeaderRedactor also removes header credentials echoed in another field/body
func HeaderRedactor(redact func(string) string, headers ...*Headers) func(string) string {
	patterns := []string{"[redacted]", "[credential]"}
	for _, h := range headers {
		if h != nil {
			if h.secretOverflow {
				return func(string) string { return "[redacted: credential capture limit exceeded]" }
			}
			for _, value := range h.secrets {
				patterns = append(patterns, value, url.QueryEscape(value), url.PathEscape(value))
				if encoded, err := json.Marshal(value); err == nil {
					patterns = append(patterns, string(encoded[1:len(encoded)-1]))
				}
			}
		}
	}
	sort.Slice(patterns, func(i, j int) bool { return len(patterns[i]) > len(patterns[j]) })
	pairs := make([]string, 0, len(patterns)*2)
	seen := map[string]bool{}
	for _, value := range patterns {
		if value != "" && !seen[value] {
			seen[value] = true
			replacement := "[redacted]"
			if value == "[credential]" {
				replacement = value
			}
			pairs = append(pairs, value, replacement)
		}
	}
	replace := strings.NewReplacer(pairs...)
	return func(value string) string { return redact(replace.Replace(value)) }
}

// Apply redaction to stored header metadata
func (h *Headers) Sanitize(redact func(string) string) {
	if h == nil {
		return
	}
	result := &HeaderCapture{headers: Headers{Entries: []Header{}, Truncated: h.Truncated}}
	for _, field := range h.Entries {
		values := make([]string, 0, len(field.Values))
		for _, value := range field.Values {
			if SensitiveHeader(field.Name) {
				value = "[redacted]"
			} else {
				value = redact(headerValue(field.Name, value))
			}
			values = append(values, value)
		}
		result.add(redact(field.Name), values)
	}
	h.Entries = result.headers.Entries
	h.Truncated = result.headers.Truncated
	h.secrets = nil
}
