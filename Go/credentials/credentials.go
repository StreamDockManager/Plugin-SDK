package credentials

import (
	"errors"
	"fmt"
	"github.com/StreamDockManager/Plugin-SDK/Go/settings"
	"net/url"
	"os"
	"regexp"
	"slices"
	"strings"
)

var String = settings.String
var envReference = regexp.MustCompile(`^\$\{([A-Za-z_][A-Za-z0-9_]*)\}$`)

// Resolve a configured secret or its environment reference
func Secret(s map[string]any, key string) (string, error) {
	value := String(s, key, "")
	if m := envReference.FindStringSubmatch(value); m != nil {
		value = os.Getenv(m[1])
		if value == "" {
			return "", fmt.Errorf("%s environment reference is unset", key)
		}
	} else if strings.Contains(value, "${") {
		return "", fmt.Errorf("%s requires a whole-value environment reference", key)
	}
	return value, nil
}

// Redact known settings secrets from an error
func SafeError(err error, s map[string]any) error { return Redact(err, s, nil) }

var serviceURL = regexp.MustCompile(`https?://[^\s"<>]+`)

func sensitive(key string) bool {
	k := strings.ToLower(key)
	return strings.Contains(k, "password") || strings.Contains(k, "secret") || strings.Contains(k, "token") || strings.Contains(k, "api_key") || strings.Contains(k, "api-key") || strings.Contains(k, "authorization") || strings.Contains(k, "cookie") || k == "webhook"
}

// SecretSettings retains only the string fields used by the diagnostic redactor.
func SecretSettings(settings map[string]any) map[string]any {
	out := map[string]any{}
	for key, value := range settings {
		if raw, ok := value.(string); ok && sensitive(key) && raw != "" {
			out[key] = raw
		}
	}
	return out
}

// Remove default and caller-selected sensitive settings from an error
func Redact(err error, s map[string]any, sensitiveKeys []string) error {
	if err == nil {
		return nil
	}
	text := err.Error()
	for key, v := range s {
		if slices.Contains(sensitiveKeys, key) || sensitive(key) {
			if raw, ok := v.(string); ok {
				for _, value := range []string{raw, os.ExpandEnv(raw)} {
					if value != "" {
						text = strings.ReplaceAll(text, value, "[redacted]")
						text = strings.ReplaceAll(text, url.QueryEscape(value), "[redacted]")
						text = strings.ReplaceAll(text, strings.ReplaceAll(url.QueryEscape(value), "+", "%20"), "[redacted]")
					}
				}
			}
		}
	}
	// net/http errors may contain an authenticated URL or service-controlled body
	text = serviceURL.ReplaceAllString(text, "[service URL]")
	var safe interface{ SafeError() error }
	if errors.As(err, &safe) {
		return safe.SafeError()
	}
	return errors.New(text)
}
