package settings

import (
	"strconv"
)

// Read a boolean setting or return false
func Bool(s map[string]any, key string) bool { v, _ := s[key].(bool); return v }

// Read a finite numeric setting or use the fallback
func Number(s map[string]any, key string, fallback float64) float64 {
	if n, ok := s[key].(float64); ok {
		return n
	}
	if x, ok := s[key].(string); ok {
		if n, e := strconv.ParseFloat(x, 64); e == nil {
			return n
		}
	}
	return fallback
}

// Read a string setting or use the fallback
func String(s map[string]any, key, fallback string) string {
	if v, ok := s[key].(string); ok && v != "" {
		return v
	}
	return fallback
}
