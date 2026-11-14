package connectors

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/StreamDockManager/Plugin-SDK/Go/credentials"
	"github.com/StreamDockManager/Plugin-SDK/Go/httpclient"
	"github.com/StreamDockManager/Plugin-SDK/Go/settings"
)

var String = settings.String
var Number = settings.Number
var Boolean = settings.Bool
var Secret = credentials.Secret
var SafeError = credentials.SafeError

type HTTPError = httpclient.HTTPError
type HTTP = httpclient.Service

var NewHTTP = httpclient.NewService
var RetryAt = httpclient.RetryAt

type Runner func(context.Context, string, ...string) (string, error)

// Read and validate an object-valued JSON settings field
func JSONInputs(s map[string]any, key string) (map[string]any, error) {
	raw := String(s, key, "{}")
	var out map[string]any
	if len(raw) > 256<<10 || json.Unmarshal([]byte(raw), &out) != nil || out == nil {
		return nil, fmt.Errorf("%s must be a JSON object under 256 KiB", key)
	}
	return out, nil
}

// Clamp a value to inclusive numeric limits
func Clamp(v, lo, hi float64) float64 { return min(hi, max(lo, v)) }

// Return no discovered choices for connectors without discovery
func NoDiscovery(context.Context, map[string]any) ([]Choice, error) { return nil, nil }
