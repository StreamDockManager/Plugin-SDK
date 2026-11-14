package httpclient

import (
	"bytes"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"
)

const httpCacheEntries = 64
const httpCacheBytes = 8 << 20
const httpCacheValueBytes = 256 << 10
const httpCacheLifetime = 30 * time.Minute

var errUnmatchedHTTP304 = errors.New("HTTP 304 has no matching retained response; extracted fields were not reused")

// Private, process-local validator storage. Keys are salted digests of resolved
// requests/extraction, never plaintext URLs, headers or credentials. Values are
// already-redacted immutable JSON. Workers only share this bounded cache
type ValidatorCache struct {
	mu       sync.Mutex
	salt     [32]byte
	epoch    uint64
	sequence uint64
	bytes    int
	entries  map[[32]byte]httpCacheEntry
}
type httpCacheEntry struct {
	data           []byte
	etag, modified string
	target         [32]byte
	at             time.Time
	used           uint64
}
type httpCacheTicket struct {
	key      [32]byte
	epoch    uint64
	enabled  bool
	previous *httpCacheEntry
}

// Create a bounded HTTP validator cache
func NewValidatorCache() *ValidatorCache {
	c := &ValidatorCache{entries: map[[32]byte]httpCacheEntry{}}
	if _, err := rand.Read(c.salt[:]); err != nil {
		return nil
	}
	return c
}

// Clear cached validators and response previews
func (c *ValidatorCache) Reset() {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.epoch++
	clear(c.entries)
	c.bytes = 0
}

// Detect a response directive forbidding storage
func noStore(h http.Header) bool {
	for _, value := range h.Values("Cache-Control") {
		for directive := range strings.SplitSeq(value, ",") {
			if strings.EqualFold(strings.TrimSpace(strings.SplitN(directive, "=", 2)[0]), "no-store") {
				return true
			}
		}
	}
	return false
}

// Remove a cached response and release its byte charge
func (c *ValidatorCache) remove(key [32]byte) {
	if old, ok := c.entries[key]; ok {
		c.bytes -= entryBytes(old)
		delete(c.entries, key)
	}
}

// Measure the logical bytes retained by a cached response
func entryBytes(e httpCacheEntry) int {
	return len(e.data) + len(e.etag) + len(e.modified) + 64
}

// Prepare conditional headers and a request-scoped cache ticket
func (c *ValidatorCache) begin(req *http.Request, body string, settings map[string]any, redactor previewRedactor, options Options) httpCacheTicket {
	if c == nil || !options.Automatic || req.Method != http.MethodGet {
		return httpCacheTicket{}
	}
	for _, key := range []string{"If-Match", "If-None-Match", "If-Modified-Since", "If-Unmodified-Since", "If-Range", "Range"} {
		if _, exists := req.Header[http.CanonicalHeaderKey(key)]; exists {
			return httpCacheTicket{}
		}
	}
	identity, err := json.Marshal([]any{req.Method, req.URL.String(), req.Header, body, settings["fields"], settings["field"], redactor.secrets, options.StatusOnly, options.NoRedirect, options.AllowUserInfo})
	if err != nil {
		return httpCacheTicket{}
	}
	digest := hmac.New(sha256.New, c.salt[:])
	_, _ = digest.Write(identity)
	var key [32]byte
	copy(key[:], digest.Sum(nil))
	c.mu.Lock()
	defer c.mu.Unlock()
	if noStore(req.Header) {
		c.remove(key)
		return httpCacheTicket{}
	}
	now := time.Now()
	for k, entry := range c.entries {
		if now.Sub(entry.at) > httpCacheLifetime {
			c.remove(k)
		}
	}
	ticket := httpCacheTicket{key: key, epoch: c.epoch, enabled: true}
	if entry, found := c.entries[key]; found {
		c.sequence++
		entry.used = c.sequence
		c.entries[key] = entry
		ticket.previous = &entry
		if entry.etag != "" {
			req.Header.Set("If-None-Match", entry.etag)
		} else if entry.modified != "" {
			req.Header.Set("If-Modified-Since", entry.modified)
		}
	}
	return ticket
}

// Accept a safe entity-tag validator
func etag(value string) string {
	if len(value) > 4096 {
		return ""
	}
	tag := strings.TrimPrefix(value, "W/")
	if len(tag) < 2 || tag[0] != '"' || tag[len(tag)-1] != '"' {
		return ""
	}
	for _, b := range []byte(tag[1 : len(tag)-1]) {
		if b < 0x21 || b == '"' || b == 0x7f {
			return ""
		}
	}
	return value
}

// Accept a safe Last-Modified validator
func modified(value string) string {
	if len(value) > 128 {
		return ""
	}
	if _, err := http.ParseTime(value); err != nil {
		return ""
	}
	return value
}

// Derive the final response target used for cache ownership
func (c *ValidatorCache) responseTarget(resp *http.Response) [32]byte {
	if resp.Request == nil || resp.Request.URL == nil {
		return [32]byte{}
	}
	digest := hmac.New(sha256.New, c.salt[:])
	_, _ = digest.Write([]byte(resp.Request.URL.String()))
	var target [32]byte
	copy(target[:], digest.Sum(nil))
	return target
}

// Reject responses whose metadata forbids caching
func cacheForbidden(resp *http.Response) bool {
	if noStore(resp.Header) {
		return true
	}
	for _, value := range resp.Header.Values("Vary") {
		for name := range strings.SplitSeq(value, ",") {
			if strings.TrimSpace(name) == "*" {
				return true
			}
		}
	}
	return false
}

// Store a response only if its request ticket remains current
func (c *ValidatorCache) store(ticket httpCacheTicket, entry httpCacheEntry, forbidden bool) {
	if c == nil || !ticket.enabled {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if ticket.epoch != c.epoch {
		return
	}
	c.remove(ticket.key)
	if forbidden || (entry.etag == "" && entry.modified == "") || len(entry.data) > httpCacheValueBytes {
		return
	}
	for len(c.entries) >= httpCacheEntries || c.bytes+entryBytes(entry) > httpCacheBytes {
		var oldest [32]byte
		var used uint64
		first := true
		for key, value := range c.entries {
			if first || value.used < used {
				oldest, used, first = key, value.used, false
			}
		}
		if first {
			return
		}
		c.remove(oldest)
	}
	c.sequence++
	entry.used = c.sequence
	entry.at = time.Now()
	c.entries[ticket.key] = entry
	c.bytes += entryBytes(entry)
}

// Record validators and preview data for a completed response
func (c *ValidatorCache) finish(ticket httpCacheTicket, resp *http.Response, result HTTPResult) {
	if c == nil || !ticket.enabled {
		return
	}
	if cacheForbidden(resp) {
		c.store(ticket, httpCacheEntry{}, true)
		return
	}
	if resp.StatusCode != http.StatusOK {
		return
	}
	result.Preview = nil
	result.Paths = nil
	data, err := json.Marshal(result)
	if err != nil {
		return
	}
	c.store(ticket, httpCacheEntry{data: data, etag: etag(resp.Header.Get("ETag")), modified: modified(resp.Header.Get("Last-Modified")), target: c.responseTarget(resp)}, cacheForbidden(resp))
}

// Apply a valid 304 response to the matching cached representation
func (c *ValidatorCache) revalidate(ticket httpCacheTicket, resp *http.Response, result HTTPResult) (HTTPResult, error) {
	fail := func() (HTTPResult, error) {
		if c != nil {
			c.store(ticket, httpCacheEntry{}, true)
		}
		return result, errUnmatchedHTTP304
	}
	if c == nil || !ticket.enabled || ticket.previous == nil {
		return fail()
	}
	entry := *ticket.previous
	c.mu.Lock()
	current, exists := c.entries[ticket.key]
	valid := ticket.epoch == c.epoch && exists && bytes.Equal(current.data, entry.data) && current.etag == entry.etag && current.modified == entry.modified
	c.mu.Unlock()
	if !valid || c.responseTarget(resp) != entry.target {
		return fail()
	}
	if value := resp.Header.Get("ETag"); value != "" && (etag(value) == "" || (entry.etag != "" && strings.TrimPrefix(value, "W/") != strings.TrimPrefix(entry.etag, "W/"))) {
		return fail()
	}
	if entry.etag == "" && resp.Header.Get("Last-Modified") != "" && resp.Header.Get("Last-Modified") != entry.modified {
		return fail()
	}
	decoder := json.NewDecoder(bytes.NewReader(entry.data))
	decoder.UseNumber()
	var prior HTTPResult
	if decoder.Decode(&prior) != nil {
		return fail()
	}
	prior.Status = result.Status
	prior.DurationMS = result.DurationMS
	prior.Revalidated = true
	prior.RetryAfter = result.RetryAfter
	if value := etag(resp.Header.Get("ETag")); value != "" {
		entry.etag = value
	}
	if value := modified(resp.Header.Get("Last-Modified")); value != "" {
		entry.modified = value
	}
	c.store(ticket, entry, cacheForbidden(resp))
	return prior, nil
}
