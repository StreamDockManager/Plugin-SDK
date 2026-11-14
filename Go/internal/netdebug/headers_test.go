package netdebug

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"
)

// Verify headers retain repeated values and mask credential echoes and UR ls
func TestHeadersRetainRepeatedValuesAndMaskCredentialEchoesAndURLs(t *testing.T) {
	request := NewHeaders(map[string][]string{
		"aUtHoRiZaTiOn": {"Bearer bearer-private"}, "X-API-Key": {"api-private"}, "Cookie": {"session=cookie-private; theme=dark"},
		"Proxy-Authorization": {"Basic " + base64.StdEncoding.EncodeToString([]byte("private-user:private-pass"))},
		"X-Trace":             {"first", "second"},
		":authority":          {"visible.example.test"},
		":path":               {"/status?key=h2-path-private"},
	}).Snapshot()
	response := NewHeaders(map[string][]string{
		"Set-Cookie":                       {"session=response-cookie; HttpOnly", "other=second-cookie; Secure"},
		"X-Echo":                           {"bearer-private api-private cookie-private response-cookie private-user private-pass"},
		"Location":                         {"https://url-user:url-pass@example.test/next?token=query-private#fragment-private"},
		"Link":                             {"</next?key=relative-private>; rel=next"},
		"Access-Control-Allow-Credentials": {"true"},
	}).Snapshot()
	redact := HeaderRedactor(func(value string) string { return value }, request, response)
	request.Sanitize(redact)
	response.Sanitize(redact)
	data, _ := json.Marshal([]*Headers{request, response})
	text := string(data)
	for _, secret := range []string{"bearer-private", "api-private", "cookie-private", "response-cookie", "second-cookie", "private-user", "private-pass", "url-user", "url-pass", "query-private", "fragment-private", "relative-private"} {
		if strings.Contains(text, secret) {
			t.Fatal("credential escaped header sanitizer", secret, text)
		}
	}
	for _, visible := range []string{"first", "second", "example.test/next", "rel=next", "true"} {
		if !strings.Contains(text, visible) {
			t.Fatal("ordinary header metadata lost", visible, text)
		}
	}
	if redact("bearer-private in a body") != "[redacted] in a body" {
		t.Fatal("header credential echo was not redacted")
	}
}

// Verify header snapshots are bounded and never keep a partial field value
func TestHeaderSnapshotsAreBoundedAndNeverKeepAPartialFieldValue(t *testing.T) {
	capture := NewHeaders(map[string][]string{"X-Oversized": {strings.Repeat("private", HeaderLimit)}, "Authorization": {"Bearer safe-private"}})
	headers := capture.Snapshot()
	headers.Sanitize(HeaderRedactor(func(value string) string { return value }, headers))
	data, _ := json.Marshal(headers)
	if !headers.Truncated || len(data) > HeaderLimit*6 || strings.Contains(string(data), "private") {
		t.Fatal(headers, len(data))
	}
	// Replacing fallback headers must not remove the secrets needed to scrub echoes
	capture.Written("Host", []string{"example.test"})
	capture.Written("X-Echo", []string{"safe-private"})
	capture.WroteRequest()
	capture.Written("Host", []string{"retry.test"})
	capture.Written("X-Echo", []string{"safe-private"})
	headers = capture.Snapshot()
	headers.Sanitize(HeaderRedactor(func(value string) string { return value }, headers))
	data, _ = json.Marshal(headers)
	if strings.Contains(string(data), "safe-private") || strings.Contains(string(data), "example.test") || !strings.Contains(string(data), "retry.test") {
		t.Fatal(string(data))
	}
}

// Verify header authentication parameters and overflow remain private
func TestHeaderAuthenticationParametersAndOverflowRemainPrivate(t *testing.T) {
	capture := NewHeaders(map[string][]string{"Authorization": {`Digest username="digest-user", response="digest-private", nonce="nonce-private"`}, "X-Session-ID": {"session-private"}})
	headers := capture.Snapshot()
	redact := HeaderRedactor(func(value string) string { return value }, headers)
	for _, private := range []string{"digest-user", "digest-private", "nonce-private", "session-private"} {
		if strings.Contains(redact(private), private) {
			t.Fatal("authentication parameter escaped redaction", private)
		}
	}
	capture = NewHeaders(map[string][]string{"Authorization": {strings.Repeat("oversized-private", BodyLimit)}})
	headers = capture.Snapshot()
	redact = HeaderRedactor(func(value string) string { return value }, headers)
	headers.Sanitize(redact)
	if !headers.Truncated || !strings.Contains(redact("anything"), "credential capture limit exceeded") {
		t.Fatal("unbounded credentials must not produce partial diagnostic data")
	}
	if SensitiveHeader(":authority") || SensitiveHeader("Access-Control-Allow-Credentials") {
		t.Fatal("public routing or CORS metadata treated as credentials")
	}
}
