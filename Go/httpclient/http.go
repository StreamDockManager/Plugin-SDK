package httpclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	sdk "github.com/StreamDockManager/Plugin-SDK/Go"
	"github.com/StreamDockManager/Plugin-SDK/Go/httptrack"
	"io"
	"math"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	configvalue "github.com/StreamDockManager/Plugin-SDK/Go/settings"
)

type pathPart struct {
	key   string
	index int
	array bool
}

// ParsePath supports dotted names, escaped delimiters, numeric indices and
// quoted literal keys. Empty paths select the root. Offsets are byte offsets
func ParsePath(path string) ([]pathPart, error) {
	if len(path) > 8192 {
		return nil, fmt.Errorf("path exceeds 8192 bytes")
	}
	if path == "" {
		return nil, nil
	}
	parts := []pathPart{}
	i := 0
	fail := func(msg string) ([]pathPart, error) { return nil, fmt.Errorf("path at byte %d: %s", i, msg) }
	quoted := func() (string, error) {
		if path[i] == '"' {
			start := i
			i++
			for i < len(path) {
				ch := path[i]
				i++
				if ch == '\\' {
					if i >= len(path) {
						return "", fmt.Errorf("unfinished escape")
					}
					i++
					continue
				}
				if ch == '"' {
					var value string
					if err := json.Unmarshal([]byte(path[start:i]), &value); err != nil {
						return "", fmt.Errorf("invalid quoted literal: %w", err)
					}
					return value, nil
				}
			}
			return "", fmt.Errorf("unterminated quoted key")
		}
		q := path[i]
		i++
		var b strings.Builder
		for i < len(path) {
			ch := path[i]
			i++
			if ch == q {
				return b.String(), nil
			}
			if ch == '\\' {
				if i >= len(path) {
					return "", fmt.Errorf("unfinished escape")
				}
				ch = path[i]
				i++
				switch ch {
				case 'n':
					ch = '\n'
				case 'r':
					ch = '\r'
				case 't':
					ch = '\t'
				case 'u':
					if i+4 > len(path) {
						return "", fmt.Errorf("short unicode escape")
					}
					n, err := strconv.ParseUint(path[i:i+4], 16, 16)
					if err != nil {
						return "", fmt.Errorf("invalid unicode escape")
					}
					b.WriteRune(rune(n))
					i += 4
					continue
				}
			}
			b.WriteByte(ch)
		}
		return "", fmt.Errorf("unterminated quoted key")
	}
	for i < len(path) {
		bracket := path[i] == '['
		if bracket {
			i++
			if i >= len(path) {
				return fail("unclosed bracket")
			}
		}
		literal := path[i] == '"' || path[i] == '\''
		key := ""
		if literal {
			var err error
			key, err = quoted()
			if err != nil {
				return fail(err.Error())
			}
		} else {
			var b strings.Builder
			for i < len(path) && path[i] != '.' && path[i] != '[' && path[i] != ']' {
				ch := path[i]
				i++
				if ch == '\\' {
					if i == len(path) {
						return fail("unfinished escape")
					}
					ch = path[i]
					i++
				}
				b.WriteByte(ch)
			}
			key = b.String()
			if key == "" {
				return fail("empty component")
			}
		}
		part := pathPart{key: key}
		if !literal {
			if n, err := strconv.Atoi(key); err == nil && n >= 0 {
				part.index = n
				part.array = true
			} else if bracket {
				return fail("brackets require a nonnegative index or quoted key")
			}
		}
		if len(parts) >= 128 {
			return fail("path exceeds 128 components")
		}
		parts = append(parts, part)
		if bracket {
			if i >= len(path) || path[i] != ']' {
				return fail("expected closing bracket")
			}
			i++
		}
		if i == len(path) {
			break
		}
		if path[i] == '[' {
			continue
		}
		if path[i] != '.' {
			return fail("expected dot or bracket")
		}
		i++
		if i == len(path) || path[i] == '.' || path[i] == '[' {
			return fail("expected component after dot")
		}
	}
	return parts, nil
}

// Extract a value using a validated selection path
func ExtractPath(root any, path string) (any, bool, error) {
	parts, err := ParsePath(path)
	if err != nil {
		return nil, false, err
	}
	return extractParts(root, parts)
}

// Walk parsed selectors without reparsing the path
func extractParts(root any, parts []pathPart) (any, bool, error) {
	value := root
	for _, p := range parts {
		switch node := value.(type) {
		case map[string]any:
			var ok bool
			value, ok = node[p.key]
			if !ok {
				return nil, false, nil
			}
		case []any:
			if !p.array || p.index >= len(node) {
				return nil, false, nil
			}
			value = node[p.index]
		default:
			return nil, false, nil
		}
	}
	return value, true, nil
}

// Suggest bounded selection paths from a decoded JSON value
func PathSuggestions(root any) []string {
	result := []string{}
	var walk func(any, string, int)
	walk = func(value any, path string, depth int) {
		if len(result) >= 256 || depth > 12 {
			return
		}
		result = append(result, path)
		switch node := value.(type) {
		case map[string]any:
			keys := []string{}
			for k := range node {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			for _, k := range keys {
				quoted := strconv.Quote(k)
				child := path + "[" + quoted + "]"
				if k != "" && !strings.ContainsAny(k, ".[]\\\"'") {
					if path == "" {
						child = k
					} else {
						child = path + "." + k
					}
				}
				walk(node[k], child, depth+1)
			}
		case []any:
			for i, item := range node {
				if i >= 8 {
					break
				}
				walk(item, fmt.Sprintf("%s[%d]", path, i), depth+1)
			}
		}
	}
	walk(root, "", 0)
	return result
}

// Resolve explicit environment references in a setting string
func envReferences(value string) (string, error) {
	missing := ""
	result := os.Expand(value, func(key string) string {
		v, ok := os.LookupEnv(key)
		if !ok {
			missing = key
		}
		return v
	})
	if missing != "" {
		return "", fmt.Errorf("credential environment variable %q is unavailable", missing)
	}
	return result, nil
}

type HTTPResult struct {
	PollingFeedback
	Status      int            `json:"status"`
	DurationMS  int64          `json:"duration_ms"`
	Fields      map[string]any `json:"fields"`
	Missing     []string       `json:"missing"`
	Preview     any            `json:"preview"`
	Paths       []string       `json:"paths"`
	Revalidated bool           `json:"revalidated,omitempty"`
	RetryAfter  time.Time      `json:"-"`
}

// Perform an HTTP request with the default options
func RequestHTTP(ctx context.Context, s map[string]any) (HTTPResult, error) {
	return Request(ctx, s, Options{})
}

type Options struct {
	// VaultURL is an explicitly authorized secret endpoint. Its path, response,
	// and validators must never enter diagnostics or a shared cache.
	VaultURL      *sdk.VaultSecret
	Cache         *ValidatorCache
	Automatic     bool
	StatusOnly    bool
	NoRedirect    bool
	AllowUserInfo bool
}

// DisplayRequest extracts configured fields without generating an inspector preview or path list.
func (o Options) DisplayRequest(ctx context.Context, settings map[string]any) (HTTPResult, error) {
	return request(ctx, settings, o, false)
}

// Perform a bounded HTTP request with preview and error handling
func Request(ctx context.Context, s map[string]any, options Options) (HTTPResult, error) {
	return request(ctx, s, options, true)
}

func request(ctx context.Context, s map[string]any, options Options, preview bool) (result HTTPResult, requestErr error) {
	result = HTTPResult{Fields: map[string]any{}, Missing: []string{}}
	paths := map[string][]pathPart{}
	redactor := newPreviewRedactor(s)
	if fields, ok := s["fields"].(map[string]any); ok {
		if len(fields) > 32 {
			return result, fmt.Errorf("configure at most 32 named fields")
		}
		for name, path := range fields {
			parts, err := ParsePath(fmt.Sprint(path))
			if err != nil {
				return result, fmt.Errorf("field %q: %w", name, err)
			}
			paths[name] = parts
		}
	}
	method := strings.ToUpper(configvalue.String(s, "method", "GET"))
	switch method {
	case "GET", "HEAD", "POST", "PUT", "PATCH", "DELETE", "OPTIONS":
	default:
		return result, fmt.Errorf("unsupported HTTP method")
	}
	if options.Automatic && method != "GET" && method != "HEAD" {
		return result, fmt.Errorf("automatic HTTP requests must be GET or HEAD")
	}
	var raw string
	var err error
	if options.VaultURL != nil {
		raw = options.VaultURL.Reveal()
		options.Cache, options.NoRedirect, options.StatusOnly = nil, true, true
	} else {
		raw, err = envReferences(configvalue.String(s, "url", ""))
	}
	if err != nil {
		return result, err
	}
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || (u.User != nil && !options.AllowUserInfo) {
		return result, fmt.Errorf("request URL must be HTTP(S) without embedded credentials")
	}
	if options.VaultURL != nil {
		addr, _ := netip.ParseAddr(u.Hostname())
		if u.User != nil || u.Fragment != "" || (u.Scheme != "https" && !addr.IsLoopback()) {
			return result, fmt.Errorf("Vault webhooks require HTTPS or a loopback HTTP address")
		}
	}
	body, err := envReferences(configvalue.String(s, "body", ""))
	if err != nil {
		return result, err
	}
	seconds := configvalue.Number(s, "timeout", 10)
	if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= 0 || seconds > 120 {
		return result, fmt.Errorf("timeout must be greater than zero and at most 120 seconds")
	}
	ctx, cancel := context.WithTimeout(ctx, time.Duration(seconds*float64(time.Second)))
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, u.String(), strings.NewReader(body))
	if err != nil {
		return result, fmt.Errorf("invalid request")
	}
	if headers, ok := s["headers"].(map[string]any); ok {
		for k, v := range headers {
			value, err := envReferences(fmt.Sprint(v))
			if err != nil {
				return result, err
			}
			req.Header.Set(k, value)
		}
	}
	redactor = redactor.forRequest(req, body)
	ticket := options.Cache.begin(req, body, s, redactor, options, preview)
	client := http.Client{CheckRedirect: func(next *http.Request, via []*http.Request) error {
		if options.NoRedirect {
			return http.ErrUseLastResponse
		}
		if len(via) >= 5 {
			return fmt.Errorf("too many redirects")
		}
		if next.URL.Host != via[0].URL.Host || next.URL.Scheme != via[0].URL.Scheme {
			return fmt.Errorf("cross-origin redirects require an explicit URL")
		}
		return nil
	}}
	start := time.Now()
	sender := &client
	if options.VaultURL == nil {
		sender = httptrack.TrackHTTPClient(sender)
	}
	resp, err := sender.Do(req)
	result.DurationMS = time.Since(start).Milliseconds()
	if err != nil {
		if ctx.Err() != nil {
			return result, fmt.Errorf("request cancelled or timed out")
		}
		return result, fmt.Errorf("request failed (check URL, network, TLS and redirect policy)")
	}
	defer resp.Body.Close()
	result.Status = resp.StatusCode
	defer func() { result.RetryAfter = ParseRetryAfter(resp.Header.Get("Retry-After"), time.Now()) }()
	if options.Automatic && method == "GET" && resp.StatusCode == http.StatusNotModified {
		return options.Cache.revalidate(ticket, resp, result)
	}
	defer func() {
		if cacheForbidden(resp) || (requestErr == nil && ctx.Err() == nil) {
			options.Cache.finish(ticket, resp, result)
		}
	}()
	if options.StatusOnly {
		return result, nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	result.DurationMS = time.Since(start).Milliseconds()
	if err != nil {
		return result, fmt.Errorf("response read failed")
	}
	if len(data) > 1<<20 {
		return result, fmt.Errorf("response exceeds 1 MiB")
	}
	if method == "HEAD" || len(data) == 0 {
		return result, nil
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	var root any
	if err = decoder.Decode(&root); err != nil {
		if preview {
			result.Preview = redactor.redact(string(data[:min(len(data), 4096)]), 0)
		}
		return result, nil
	}
	if err = decoder.Decode(new(any)); err != io.EOF {
		return result, fmt.Errorf("response contains trailing JSON data")
	}
	if preview {
		result.Preview = redactor.redact(root, 0)
		result.Paths = PathSuggestions(root)
	}
	if len(paths) > 0 {
		for name, parts := range paths {
			value, found, err := extractParts(root, parts)
			if err != nil {
				return result, fmt.Errorf("field %q: %w", name, err)
			}
			if found {
				result.Fields[name] = redactor.redact(value, 0)
			} else {
				result.Missing = append(result.Missing, name)
			}
		}
	}
	sort.Strings(result.Missing)
	return result, nil
}

type previewRedactor struct{ secrets []string }

// Add request-specific secrets to the preview redactor
func (r previewRedactor) forRequest(req *http.Request, body string) previewRedactor {
	seen := map[string]struct{}{}
	for _, value := range r.secrets {
		seen[value] = struct{}{}
	}
	add := func(value string) {
		if value != "" {
			seen[value] = struct{}{}
		}
	}
	if req.URL.User != nil {
		if password, ok := req.URL.User.Password(); ok {
			add(password)
		}
	}

	for key, values := range req.Header {
		if sensitive(key) {
			for _, value := range values {
				add(value)
				if strings.EqualFold(key, "Authorization") {
					if _, token, found := strings.Cut(value, " "); found {
						add(token)
					}
				}
				if strings.EqualFold(key, "Cookie") {
					for cookie := range strings.SplitSeq(value, ";") {
						if _, token, found := strings.Cut(cookie, "="); found {
							add(strings.TrimSpace(token))
						}
					}
				}
			}
		}
	}
	for key, values := range req.URL.Query() {
		if sensitive(key) {
			for _, value := range values {
				add(value)
			}
		}
	}
	var root any
	if len(body) <= 1<<20 && json.Unmarshal([]byte(body), &root) == nil {
		var scan func(any, int)
		scan = func(value any, depth int) {
			if depth > 16 {
				return
			}
			switch node := value.(type) {
			case map[string]any:
				for key, item := range node {
					if sensitive(key) {
						if text, ok := item.(string); ok {
							add(text)
						}
					}
					scan(item, depth+1)
				}
			case []any:
				for _, item := range node {
					scan(item, depth+1)
				}
			}
		}
		scan(root, 0)
	}
	r.secrets = make([]string, 0, len(seen))
	for value := range seen {
		r.secrets = append(r.secrets, value)
	}
	sort.Slice(r.secrets, func(i, j int) bool {
		if len(r.secrets[i]) == len(r.secrets[j]) {
			return r.secrets[i] < r.secrets[j]
		}
		return len(r.secrets[i]) > len(r.secrets[j])
	})
	return r
}

// Create a preview redactor from sensitive settings
func newPreviewRedactor(settings map[string]any) previewRedactor {
	seen := map[string]struct{}{}
	var scan func(any)
	scan = func(value any) {
		switch value := value.(type) {
		case map[string]any:
			for _, item := range value {
				scan(item)
			}
		case []any:
			for _, item := range value {
				scan(item)
			}
		case string:
			_ = os.Expand(value, func(key string) string {
				if secret := os.Getenv(key); secret != "" {
					seen[secret] = struct{}{}
				}
				return ""
			})
		}
	}
	scan(settings)
	redactor := previewRedactor{secrets: make([]string, 0, len(seen))}
	for secret := range seen {
		redactor.secrets = append(redactor.secrets, secret)
	}
	sort.Slice(redactor.secrets, func(i, j int) bool { return len(redactor.secrets[i]) > len(redactor.secrets[j]) })
	return redactor
}

// Redact nested preview values within the recursion bound
func (r previewRedactor) redact(value any, depth int) any {
	if depth > 10 {
		return "[preview depth limit]"
	}
	switch node := value.(type) {
	case map[string]any:
		result := make(map[string]any, min(len(node), 128))
		for key, value := range node {
			if len(result) >= 128 {
				break
			}
			if sensitive(key) {
				result[key] = "[redacted]"
			} else {
				result[key] = r.redact(value, depth+1)
			}
		}
		return result
	case []any:
		result := make([]any, min(len(node), 32))
		for i := range result {
			result[i] = r.redact(node[i], depth+1)
		}
		return result
	case string:
		result := node
		for _, secret := range r.secrets {
			result = strings.ReplaceAll(result, secret, "[redacted]")
		}
		if len(result) > 4096 {
			result = result[:4096] + "…"
		}
		return result
	}
	return value
}

// sensitive is the shared v1 response-preview/request-secret field classification
func sensitive(key string) bool {
	key = strings.ToLower(key)
	return strings.Contains(key, "token") || strings.Contains(key, "password") || strings.Contains(key, "secret") || strings.Contains(key, "credential") || strings.Contains(key, "authorization") || strings.Contains(key, "cookie") || strings.Contains(key, "api-key") || strings.Contains(key, "api_key") || key == "key" || key == "signature"
}
