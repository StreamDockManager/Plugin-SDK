package sdk

import (
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/StreamDockManager/Plugin-SDK/Go/internal/netdebug"
)

type LogStream string

const (
	Stdout LogStream = "stdout"
	Stderr LogStream = "stderr"
)

// ActionLogging optionally selects the input events that execute a plugin action
// Receipts remain independent of logging; release-based actions can opt in here
type ActionLogging interface {
	ShouldLogAction(Event) bool
}

// Select action events worth recording in plugin logs
func ShouldLogAction(e Event) bool {
	if e.Event == "keyDown" || e.Event == "dialDown" {
		return true
	}
	if e.Event == "dialRotate" {
		return e.Data().Ticks != 0
	}
	if e.Event != "touchTap" {
		return false
	}
	if e.Surface != nil && e.Surface.Touch != nil {
		phase := e.Surface.Touch.Phase
		return phase == "tap" || phase == "end" || phase == ""
	}
	return !e.Data().Hold
}

// SetNetworkDebug is normally driven by Manager's per-plugin log preferences
// Diagnostic traffic uses the independent bounded log queue, never stdout echo
func (c *Client) SetNetworkDebug(enabled bool) {
	if c.networkDebug.Swap(enabled) == enabled {
		return
	}
	netdebug.Configure(c, enabled, func(event netdebug.Event) {
		stream := Stdout
		if event.Failed {
			stream = Stderr
		}
		if event.Details == nil {
			_ = c.logStream(context.Background(), stream, event.Message, "network")
		} else {
			redact := netdebug.HeaderRedactor(c.safeLog, event.Details.RequestHeaders, event.Details.ResponseHeaders)
			event.Details.RequestHeaders.Sanitize(redact)
			event.Details.ResponseHeaders.Sanitize(redact)
			event.Details.Request.Sanitize(redact)
			event.Details.Response.Sanitize(redact)
			message := strings.ToValidUTF8(redact(event.Message), "�")
			end := min(len(message), 16<<10)
			for end < len(message) && message[end]&0xc0 == 0x80 {
				end--
			}
			_ = c.Send(context.Background(), Message("logMessage", "", map[string]any{"stream": stream, "source": "network", "message": message[:end], "network": event.Details}))
		}
	})
}

// SetLogRedactor installs a snapshot-based sanitizer. It is applied before
// framing; writers never echo RPC messages to the process console
func (c *Client) SetLogRedactor(redact func(string) string) {
	c.inputMu.Lock()
	c.logRedactor = redact
	c.inputMu.Unlock()
}

// Redact a message before it reaches host logging
func (c *Client) safeLog(message string) string {
	c.inputMu.Lock()
	redact := c.logRedactor
	c.inputMu.Unlock()
	if redact != nil {
		return redact(message)
	}
	return message
}

// Send a message on the requested output stream
func (c *Client) LogStream(ctx context.Context, stream LogStream, message string) error {
	return c.logStream(ctx, stream, message, "")
}

// Send redacted output with its stream and source metadata
func (c *Client) logStream(ctx context.Context, stream LogStream, message, source string) error {
	if stream != Stdout && stream != Stderr {
		return fmt.Errorf("invalid log stream")
	}
	message = strings.ToValidUTF8(c.safeLog(message), "�")
	for len(message) > 0 {
		end := min(len(message), 16<<10)
		for end < len(message) && message[end]&0xc0 == 0x80 {
			end--
		}
		payload := map[string]any{"message": message[:end]}
		if source != "" {
			payload["source"] = source
		}
		if c.Supports("plugin-logs-v1") {
			payload["stream"] = stream
		}
		if err := c.Send(ctx, Message("logMessage", "", payload)); err != nil {
			return err
		}
		message = message[end:]
	}
	return nil
}

type logWriter struct {
	client *Client
	ctx    context.Context
	stream LogStream
}

// Forward writer bytes through host stream logging
func (w logWriter) Write(data []byte) (int, error) {
	if err := w.client.LogStream(w.ctx, w.stream, strings.ToValidUTF8(string(data), "�")); err != nil {
		return 0, err
	}
	return len(data), nil
}

// Expose stdout as a host-backed writer
func (c *Client) Stdout(ctx context.Context) io.Writer { return logWriter{c, ctx, Stdout} }

// Expose stderr as a host-backed writer
func (c *Client) Stderr(ctx context.Context) io.Writer { return logWriter{c, ctx, Stderr} }
