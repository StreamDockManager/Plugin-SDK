// SPDX-License-Identifier: MIT
package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	sdk "github.com/StreamDockManager/Plugin-SDK/Go"
	"github.com/StreamDockManager/Plugin-SDK/Go/authoring"
	"os"
	"os/signal"
	"syscall"
)

type counter struct{ *authoring.Engine }

// Normalize the counter fields while preserving unrelated saved settings
func clean(value map[string]any) map[string]any {
	out := authoring.CloneSettings(value)
	prefix, ok := out["prefix"].(string)
	if !ok {
		prefix = "Count"
	}
	runes := []rune(prefix)
	if len(runes) > 80 {
		prefix = string(runes[:80])
	}
	out["prefix"] = prefix
	var count int64
	switch value := out["count"].(type) {
	case int64:
		count = value
	case float64:
		if value >= 0 && value < 1<<53 {
			count = int64(value)
		}
	}
	if count < 0 || count >= 1<<53 {
		count = 0
	}
	out["count"] = count
	return out
}

// Create the authoring engine and route inspector input to the counter handler
func newCounter() *counter {
	c := &counter{Engine: authoring.New(authoring.Definition{}, "", nil)}
	// Share the same settings update path for inspector and action events
	c.Feature.Inspector = func(ctx context.Context, client *sdk.Client, event sdk.Event) (bool, error) {
		return true, c.input(ctx, client, event, c.Bindings[event.Context])
	}
	return c
}

// Log completed key presses rather than every counter lifecycle event
func (c *counter) ShouldLogAction(event sdk.Event) bool { return event.Event == "keyUp" }

// Register unseen action IDs before delegating lifecycle handling to the engine
func (c *counter) Handle(ctx context.Context, client *sdk.Client, event sdk.Event) error {
	if event.Action != "" {
		if _, ok := c.Registered[event.Action]; !ok {
			// Normalize a binding after appearance or host settings changes
			update := func(_ context.Context, _ *sdk.Client, event sdk.Event, binding authoring.Binding) error {
				binding.Settings = clean(binding.Settings)
				c.Bindings[event.Context] = binding
				return nil
			}
			// Render counter parity through the same title, image and layout feedback hooks
			if err := c.Register(event.Action, authoring.ActionHooks{Input: c.input, Appeared: update, Changed: update, Render: func(_ context.Context, _ *sdk.Client, _ string, binding authoring.Binding) (authoring.Display, error) {
				settings := clean(binding.Settings)
				count := settings["count"].(int64)
				name := "even"
				if count%2 != 0 {
					name = "odd"
				}
				path := "M7 9h18M7 19h18"
				if count%2 != 0 {
					path = "M16 6v20M6 16h20"
				}
				// Fixed trusted literals only; keep this starter buildable with its pinned SDK
				// New SDK consumers can use sdk.ImageDataURI for bounded caller-supplied bytes
				image := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 32 32"><g fill="none" stroke="#ffffff" stroke-width="3" stroke-linecap="round"><path d="%s"/></g></svg>`, path)))
				return authoring.Display{Title: fmt.Sprintf("%s %d", settings["prefix"], count), Image: image, State: int(count % 2), Layout: "display/counter.json", Feedback: map[string]any{"prefix": settings["prefix"], "count": count, "icon": "icons/" + name + ".svg"}}, nil
			}}); err != nil {
				return err
			}
		}
	}
	return c.Engine.Handle(ctx, client, event)
}

// Apply key or inspector input and synchronize saved settings and rendered feedback
func (c *counter) input(ctx context.Context, client *sdk.Client, event sdk.Event, binding authoring.Binding) error {
	if _, exists := c.Bindings[event.Context]; !exists {
		return nil
	}
	settings := clean(binding.Settings)
	var payload map[string]any
	_ = json.Unmarshal(event.Payload, &payload)
	switch event.Event {
	case "keyUp":
		settings["count"] = settings["count"].(int64) + 1
	case "sendToPlugin":
		if payload["reset"] == true {
			settings["count"] = int64(0)
		} else if value, ok := payload["settings"].(map[string]any); ok {
			settings["prefix"] = clean(value)["prefix"]
		} else if payload["request"] != "settings" {
			return nil
		}
	default:
		return nil
	}
	binding.Settings = settings
	c.Bindings[event.Context] = binding
	if event.Event == "keyUp" || payload["request"] != "settings" {
		portable, err := sdk.NewPortableSettings(client, "action", event.Context)
		if err != nil {
			return err
		}
		if _, err := portable.Replace(ctx, settings); err != nil {
			return err
		}
	}
	if err := c.Render(ctx, client, event.Context); err != nil {
		return err
	}
	return client.ToInspector(ctx, event.Context, event.Action, map[string]any{"settings": settings})
}

// Run the counter until the host exits or the process receives a shutdown signal
func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if err := sdk.Run(ctx, os.Args[1:], newCounter()); err != nil && ctx.Err() == nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
