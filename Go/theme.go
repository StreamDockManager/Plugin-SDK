package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"regexp"
)

const ThemeProtocol = "plugin-theme-v1"
const ThemeEvent = "didReceivePluginTheme"

type ThemeWallpaper struct {
	SVG   string
	Media *ThemeMedia
}

type ThemeMedia struct {
	Kind     string `json:"kind"`
	AssetID  string `json:"asset_id"`
	Format   string `json:"format"`
	Animated bool   `json:"animated"`
}

// Decode a bounded wallpaper description
func (w *ThemeWallpaper) UnmarshalJSON(data []byte) error {
	var svg string
	if json.Unmarshal(data, &svg) == nil && svg != "" && len(svg) <= 64<<10 {
		*w = ThemeWallpaper{SVG: svg}
		return nil
	}
	var media struct {
		ThemeMedia
		Animated *bool `json:"animated"`
	}
	if err := json.Unmarshal(data, &media); err == nil && media.Kind == "media" &&
		media.AssetID != "" && media.Format != "" && media.Animated != nil {
		media.ThemeMedia.Animated = *media.Animated
		*w = ThemeWallpaper{Media: &media.ThemeMedia}
		return nil
	}
	return errors.New("invalid plugin theme wallpaper")
}

type ThemeColors struct {
	Surface   string         `json:"surface"`
	Ink       string         `json:"ink"`
	Muted     string         `json:"muted"`
	Accent    string         `json:"accent"`
	Meter     string         `json:"meter"`
	Border    string         `json:"border"`
	Wallpaper ThemeWallpaper `json:"wallpaper"`
}

type PluginTheme struct {
	Version  int         `json:"version"`
	Resolved string      `json:"resolved"`
	Revision uint64      `json:"revision"`
	Theme    ThemeColors `json:"theme"`
}

var themeColor = regexp.MustCompile("^#[0-9a-fA-F]{6}$")

// Decode and validate plugin theme data
func decodePluginTheme(raw json.RawMessage) (*PluginTheme, error) {
	var value struct {
		PluginTheme
		Revision *uint64 `json:"revision"`
	}
	if json.Unmarshal(raw, &value) != nil || value.Version != 1 ||
		value.Revision == nil || *value.Revision > 1<<53-1 ||
		(value.Resolved != "light" && value.Resolved != "dark") ||
		(value.Theme.Wallpaper.SVG == "" && value.Theme.Wallpaper.Media == nil) {
		return nil, errors.New("invalid plugin theme")
	}
	for _, color := range []string{value.Theme.Surface, value.Theme.Ink, value.Theme.Muted,
		value.Theme.Accent, value.Theme.Meter, value.Theme.Border} {
		if !themeColor.MatchString(color) {
			return nil, errors.New("invalid plugin theme color")
		}
	}
	value.PluginTheme.Revision = *value.Revision
	return &value.PluginTheme, nil
}

// Check whether the host supports theme RPC
func (c *Client) SupportsThemeRPC() bool {
	var metadata struct {
		ThemeRPC bool `json:"theme_rpc"`
	}
	return c.Supports(ThemeProtocol) &&
		json.Unmarshal(c.Host().Raw["streamDockManager"], &metadata) == nil && metadata.ThemeRPC
}

// Fetch the current theme through negotiated RPC
func (c *Client) GetPluginTheme(ctx context.Context) (*PluginTheme, error) {
	if !c.SupportsThemeRPC() {
		return nil, &UnsupportedFeatureError{ThemeProtocol}
	}
	raw, err := c.Request(ctx, ThemeProtocol, "get", map[string]any{})
	if err != nil {
		return nil, err
	}
	value, err := decodePluginTheme(raw)
	if err != nil {
		return nil, err
	}
	c.rememberPluginTheme(value)
	return c.CurrentPluginTheme(), nil
}

// Read the cached plugin theme
func (c *Client) CurrentPluginTheme() *PluginTheme {
	current := c.pluginTheme.Load()
	if current == nil {
		return nil
	}
	copy := *current
	if media := current.Theme.Wallpaper.Media; media != nil {
		copyMedia := *media
		copy.Theme.Wallpaper.Media = &copyMedia
	}
	return &copy
}

// Replace the cached theme only when its value changes
func (c *Client) rememberPluginTheme(value *PluginTheme) bool {
	for {
		current := c.pluginTheme.Load()
		if current != nil && current.Revision >= value.Revision {
			return false
		}
		if c.pluginTheme.CompareAndSwap(current, value) {
			return true
		}
	}
}

// Observe a host theme update and refresh the cache
func (c *Client) observePluginTheme(event Event) bool {
	if !c.SupportsThemeRPC() {
		return false
	}
	value, err := decodePluginTheme(event.Payload)
	return err == nil && c.rememberPluginTheme(value)
}
