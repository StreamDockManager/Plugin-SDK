package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Verify typed log writers redact before framing and keep legacy fallback
func TestTypedLogWritersRedactBeforeFramingAndKeepLegacyFallback(t *testing.T) {
	for _, typed := range []bool{false, true} {
		host := HostInfo{}
		host.Manager.ContractVersion = 1
		if typed {
			host.Manager.Capabilities = []string{"plugin-logs-v1"}
		}
		client, output := NewLocalClient(host, "fixture")
		client.SetLogRedactor(func(text string) string { return strings.ReplaceAll(text, "secret", "[redacted]") })
		text := strings.Repeat("界", 6000) + "secret"
		n, err := client.Stderr(context.Background()).Write([]byte(text))
		if err != nil || n != len(text) {
			t.Fatal(n, err)
		}
		var joined string
		for len(output) > 0 {
			event := <-output
			var payload struct{ Message, Stream string }
			if event.Event != "logMessage" {
				t.Fatal(event)
			}
			if err := json.Unmarshal(event.Payload, &payload); err != nil {
				t.Fatal(err)
			}
			if typed && payload.Stream != "stderr" || !typed && payload.Stream != "" {
				t.Fatal(payload)
			}
			joined += payload.Message
		}
		if joined != strings.ReplaceAll(text, "secret", "[redacted]") {
			t.Fatal("content lost during framing")
		}
	}
}

// Verify action logs wait for deferred completion and ignore duplicate callbacks
func TestActionLogsWaitForDeferredCompletionAndIgnoreDuplicateCallbacks(t *testing.T) {
	host := HostInfo{}
	host.Manager.ContractVersion = 1
	host.Manager.Capabilities = []string{"plugin-logs-v1", "action-results-v1"}
	client, output := NewLocalClient(host, "fixture")
	var complete func(error)
	handler := completionHandler(func(ctx context.Context, _ *Client, _ Event) error { complete = DeferAction(ctx); return nil })
	event := tracked()
	event.Action = "fixture.increment"
	client.Dispatch(context.Background(), handler, event)
	if len(output) > 0 {
		t.Fatal("action was logged before deferred work completed")
	}
	complete(errors.New("fixture failure"))
	complete(nil)
	logs, receipts := 0, 0
	for len(output) > 0 {
		e := <-output
		if e.Event == "actionResult" {
			receipts++
		}
		if e.Event == "logMessage" {
			logs++
			if !strings.Contains(string(e.Payload), "stderr") || !strings.Contains(string(e.Payload), "failed or cancelled") {
				t.Fatal(e)
			}
		}
	}
	if logs != 1 || receipts != 1 {
		t.Fatal(logs, receipts)
	}
}

// Verify log flood cannot consume action or display capacity
func TestLogFloodCannotConsumeActionOrDisplayCapacity(t *testing.T) {
	client := &Client{host: currentTestHost(t), out: make(chan Event, 128), logOut: make(chan Event, 128)}
	for range 200 {
		_ = client.LogStream(context.Background(), Stdout, "flood")
	}
	if client.droppedLogs.Load() != 72 {
		t.Fatal(client.droppedLogs.Load())
	}
	if err := client.Title(context.Background(), "key", "42"); err != nil {
		t.Fatal(err)
	}
	if err := client.Send(context.Background(), Message("actionResult", "key", map[string]any{"success": true})); err != nil {
		t.Fatal(err)
	}
	event, ok := client.nextOutbound(context.Background())
	if !ok || event.Event != "setTitle" {
		t.Fatal(event)
	}
	event, ok = client.nextOutbound(context.Background())
	if !ok || event.Event != "actionResult" {
		t.Fatal(event)
	}
}

// Verify invalid UTF8 log strings cannot panic the plugin
func TestInvalidUTF8LogStringsCannotPanicThePlugin(t *testing.T) {
	client, output := NewLocalClient(HostInfo{}, "fixture")
	if err := client.LogStream(context.Background(), Stderr, strings.Repeat("\x80", 20000)); err != nil {
		t.Fatal(err)
	}
	event := <-output
	var payload struct{ Message string }
	if err := json.Unmarshal(event.Payload, &payload); err != nil {
		t.Fatal(err)
	}
	if payload.Message != "�" {
		t.Fatal("invalid UTF-8 was not normalized")
	}
}
