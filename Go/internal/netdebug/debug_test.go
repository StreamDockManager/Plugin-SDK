package netdebug

import (
	"encoding/base64"
	"net/url"
	"strings"
	"testing"
)

// Verify disable stops in flight diagnostics and old sessions cannot disable new ones
func TestDisableStopsInFlightDiagnosticsAndOldSessionsCannotDisableNewOnes(t *testing.T) {
	endpoint, _ := url.Parse("https://private:secret@example.test/path?token=private#private")
	var entries []Event
	report := func(event Event) { entries = append(entries, event) }
	Configure("old", true, report)
	defer Clear("new")
	old := Start("GET", endpoint)
	Configure("new", true, report)
	Clear("old")
	old.Finish(200, nil)
	if len(entries) != 0 {
		t.Fatal("retired request emitted completion", entries)
	}
	trace := Start("GET", endpoint)
	if trace == nil {
		t.Fatal("old session disabled new session")
	}
	trace.Finish(200, nil)
	trace.Finish(200, nil)
	late := Start("GET", endpoint)
	Clear("new")
	late.Finish(200, nil)
	if len(entries) != 1 || strings.Contains(entries[0].Message, "private") {
		t.Fatal(entries)
	}
	if Start("GET", nil) != nil {
		t.Fatal("disabled request was traced")
	}
	IO(nil, "sent", 0)
}

// Verify oversized bodies are omitted without retaining partial credentials
func TestOversizedBodiesAreOmittedWithoutRetainingPartialCredentials(t *testing.T) {
	capture := NewCapture("application/json", -1, false)
	capture.Write([]byte(strings.Repeat("x", BodyLimit) + "secret"))
	capture.Done(true)
	body := capture.Snapshot()
	if !body.Truncated || body.Text != "" || body.Bytes != BodyLimit+6 || !body.Complete {
		t.Fatal(body)
	}
}

// Verify body sanitization masks nested JSON forms and binary credentials
func TestBodySanitizationMasksNestedJSONFormsAndBinaryCredentials(t *testing.T) {
	redact := func(s string) string {
		return strings.NewReplacer("known-secret", "[credential]", "quoted-\"secret", "[credential]").Replace(s)
	}
	for _, raw := range []string{`{"nested":{"access_token":"private","result":"visible"},"note":"known-secret"}`, `{"password":["private"]}`, `{"note":"known-secret","array":["known-secret"]}`, `{"note":"quoted-\"secret"}`} {
		c := NewCapture("application/json", int64(len(raw)), false)
		c.Write([]byte(raw))
		c.Done(true)
		b := c.Snapshot()
		b.Sanitize(redact)
		if strings.Contains(b.Text, "private") || strings.Contains(b.Text, "known-secret") || strings.Contains(b.Text, "quoted-") || !b.Complete {
			t.Fatal(b)
		}
	}
	c := NewCapture("application/x-www-form-urlencoded", -1, false)
	c.Write([]byte("password=private&name=visible"))
	b := c.Snapshot()
	b.Sanitize(redact)
	if strings.Contains(b.Text, "private") || !strings.Contains(b.Text, "visible") {
		t.Fatal(b)
	}
	c = NewCapture("application/octet-stream", -1, false)
	c.Write(append([]byte{255, 0}, []byte("known-secret")...))
	b = c.Snapshot()
	b.Sanitize(redact)
	data, err := base64.StdEncoding.DecodeString(b.Text)
	if b.Encoding != "base64" || err != nil || string(data) != string(append([]byte{255, 0}, []byte("[credential]")...)) {
		t.Fatal(b, err)
	}
}

// Verify nested JSON does not expand beyond the body budget
func TestNestedJSONDoesNotExpandBeyondTheBodyBudget(t *testing.T) {
	raw := strings.Repeat("[", 512) + "0" + strings.Repeat("]", 512)
	c := NewCapture("application/json", int64(len(raw)), false)
	c.Write([]byte(raw))
	c.Done(true)
	body := c.Snapshot()
	body.Sanitize(func(value string) string { return value })
	if body.Truncated || body.Text != raw {
		t.Fatal("formatting expanded a small nested body beyond the capture budget")
	}
}
