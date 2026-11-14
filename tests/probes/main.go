// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/StreamDockManager/Plugin-SDK/Go"
	"github.com/StreamDockManager/Plugin-SDK/Go/httptrack"
	"io"
	"net/http"
	"os"
	"reflect"
	"strings"
	"time"
)

type probe struct{ binding string }

// Include key releases alongside standard runtime events in probe logs
func (h *probe) ShouldLogAction(e sdk.Event) bool {
	return e.Event == "keyUp" || sdk.ShouldLogAction(e)
}

// Exercise the host capabilities selected by each fixture event
func (h *probe) Handle(ctx context.Context, c *sdk.Client, e sdk.Event) error {
	if phase, ok := e.Data().Settings["storage_phase"].(string); ok && phase != "" {
		if e.Event == "willAppear" {
			h.binding = e.Context
			return c.Title(ctx, e.Context, "storage ready")
		}
		if e.Event == "keyUp" {
			if err := storageProbe(ctx, c, e.Context, phase); err != nil {
				return err
			}
			return c.Title(ctx, e.Context, "storage "+phase+" passed")
		}
		return nil
	}
	switch e.Event {
	case "keyDown":
		if e.Data().Settings["fixture_message_limit"] == true {
			prefix := "é😀"
			base, _ := json.Marshal(sdk.Message("setFeedback", e.Context, map[string]any{"boundary": prefix}))
			for _, delta := range []int{-1, 0, 1} {
				payload := map[string]any{"boundary": prefix + strings.Repeat("x", sdk.MaxMessageBytes-len(base)+delta)}
				err := c.Send(ctx, sdk.Message("setFeedback", e.Context, payload))
				if delta > 0 {
					if err == nil || !strings.Contains(err.Error(), "1048576") {
						return fmt.Errorf("missing local message-limit rejection: %v", err)
					}
				} else if err != nil {
					return err
				}
			}
			if err := c.Feedback(ctx, e.Context, map[string]any{"boundary": nil}); err != nil {
				return err
			}
			return c.Title(ctx, e.Context, "message-limit:ok")
		}
		endpoint, _ := e.Data().Settings["fixture_endpoint"].(string)
		if endpoint == "" {
			return nil
		}
		request, err := http.NewRequestWithContext(ctx, "GET", endpoint, nil)
		if err != nil {
			return err
		}
		request.Header.Set("Authorization", "Bearer fixture-private")
		response, err := httptrack.TrackHTTPClient(&http.Client{Timeout: 2 * time.Second}).Do(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if _, err = io.ReadAll(io.LimitReader(response.Body, 1024)); err != nil {
			return err
		}
		_ = c.LogStream(ctx, sdk.Stdout, "SDK network request finished")
		return c.Title(ctx, e.Context, "network:done")
	case "willAppear":
		c.SetLogRedactor(func(text string) string { return strings.ReplaceAll(text, "fixture-private", "[redacted]") })
		_ = c.LogStream(ctx, sdk.Stdout, "SDK stdout fixture-private")
		_ = c.LogStream(ctx, sdk.Stderr, "SDK stderr fixture-private")
		h.binding = e.Context
		if err := c.Settings(ctx, e.Context, e.Data().Settings); err != nil {
			return err
		}
		if err := c.GlobalSettings(ctx, map[string]any{"fixture": "saved"}); err != nil {
			return err
		}
		return c.SubscribeTelemetry(ctx, []string{"cpu"})
	case "didReceiveGlobalSettings":
		if e.Data().Settings["fixture"] == "saved" {
			return c.Title(ctx, h.binding, "global:saved")
		}
	case sdk.ThemeEvent:
		var value struct{ Resolved string }
		if err := json.Unmarshal(e.Payload, &value); err != nil {
			return err
		}
		if h.binding != "" {
			return c.Title(ctx, h.binding, "theme:"+value.Resolved)
		}
	case "didReceiveTelemetry":
		var value sdk.TelemetrySnapshot
		if err := json.Unmarshal(e.Payload, &value); err != nil {
			return err
		}
		if value.CPU.Fresh(time.Now()) {
			return c.Title(ctx, h.binding, fmt.Sprintf("telemetry:%g", *value.CPU.Value))
		}
	case "dialRotate":
		return c.Title(ctx, e.Context, fmt.Sprintf("dial:%d", e.Data().Ticks))
	case "touchTap":
		return c.Title(ctx, e.Context, "touch")
	case "keyUp":
		if e.Surface == nil {
			return fmt.Errorf("missing tracked surface")
		}
		return c.DesktopInput(ctx, e.Context, map[string]any{"op": "copy", "request_id": e.Surface.RequestID + "-input"})
	case "willDisappear":
		h.binding = ""
	}
	return nil
}

const storageOperation = "12345678-1234-4234-8234-123456789abc"
const storageJSON = `{"unicode":"é😀","unknown":{"keep":true},"false":false,"zero":0,"null":null,"empty":[],"object":{}}`

// Verify private storage replay, conflicts and portable settings across restart phases
func storageProbe(ctx context.Context, c *sdk.Client, binding, phase string) error {
	store, err := sdk.NewHostStore(c, "sdk-storage-v1")
	if err != nil {
		return err
	}
	current, err := store.Get(ctx, "value")
	if err != nil {
		return err
	}
	if phase == "write" {
		if current.Found {
			return fmt.Errorf("new private value already exists")
		}
		mutations := []sdk.StorageMutation{{Op: "put", Key: "value", Value: json.RawMessage(storageJSON)}, {Op: "put", Key: "null", Value: nil}}
		committed, err := store.Commit(ctx, current.StorageVersion, storageOperation, mutations)
		if err != nil {
			return err
		}
		replayed, err := store.Commit(ctx, current.StorageVersion, storageOperation, mutations)
		if err != nil || replayed.Revision != committed.Revision {
			return fmt.Errorf("idempotent commit mismatch: %v", err)
		}
		_, err = store.Commit(ctx, current.StorageVersion, "12345678-1234-4234-8234-123456789abd", mutations)
		var failure *sdk.RPCError
		if !errors.As(err, &failure) || failure.Code != "conflict" {
			return fmt.Errorf("stale revision accepted: %v", err)
		}
		for _, scope := range []string{"action", "global"} {
			settings, err := sdk.NewPortableSettings(c, scope, binding)
			if err != nil {
				return err
			}
			before, err := settings.Read(ctx)
			if err != nil {
				return err
			}
			if _, err = settings.Update(ctx, map[string]any{"sdk_storage_probe": "saved"}); err != nil {
				return err
			}
			after, err := settings.Read(ctx)
			if err != nil {
				return err
			}
			for key, value := range before.Settings {
				if !reflect.DeepEqual(after.Settings[key], value) {
					return fmt.Errorf("portable settings lost %s", key)
				}
			}
			if after.Settings["sdk_storage_probe"] != "saved" {
				return fmt.Errorf("portable update not observed")
			}
		}
	} else if phase == "delete" {
		if !current.Found {
			return fmt.Errorf("private value missing before delete")
		}
		if _, err = store.Commit(ctx, current.StorageVersion, "12345678-1234-4234-8234-123456789abe", []sdk.StorageMutation{{Op: "delete", Key: "value"}, {Op: "delete", Key: "null"}}); err != nil {
			return err
		}
		missing, err := store.Get(ctx, "value")
		if err != nil || missing.Found {
			return fmt.Errorf("delete failed: %v", err)
		}
		return nil
	} else if phase != "read" {
		return fmt.Errorf("unknown storage phase")
	}
	value, err := store.Get(ctx, "value")
	if err != nil {
		return err
	}
	var got, want any
	json.Unmarshal(value.Value, &got)
	json.Unmarshal([]byte(storageJSON), &want)
	if !value.Found || !reflect.DeepEqual(got, want) {
		return fmt.Errorf("private value did not round trip")
	}
	null, err := store.Get(ctx, "null")
	if err != nil || !null.Found || string(null.Value) != "null" {
		return fmt.Errorf("null was treated as missing: %v", err)
	}
	missing, err := store.Get(ctx, "missing")
	if err != nil || missing.Found {
		return fmt.Errorf("missing value appeared: %v", err)
	}
	keys, err := store.ListKeys(ctx, "")
	if err != nil || !reflect.DeepEqual(keys.Keys, []string{"null", "value"}) {
		return fmt.Errorf("list keys mismatch: %v", err)
	}
	receipt, err := store.Receipt(ctx, value.Generation, storageOperation)
	if err != nil || !receipt.Found || !receipt.Committed {
		return fmt.Errorf("durable receipt missing: %v", err)
	}
	return nil
}

// Keep this event-driven fixture free of periodic work
func (*probe) Poll(context.Context, *sdk.Client) error { return nil }

// Finish the fixture without retaining resources outside the SDK client
func (*probe) Close() error { return nil }

// Run the protocol probe with the host-provided launch arguments
func main() {
	if err := sdk.Run(context.Background(), os.Args[1:], &probe{}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
