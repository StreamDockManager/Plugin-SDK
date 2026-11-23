package sdk

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"
)

const TextReferencesProtocol = "plugin-text-references-v1"
const maxReferenceInput = 64 << 10
const maxReferenceOutput = 256 << 10

// FormatText resolves one transient string in the host's permission-checked action scope
func (c *Client) FormatText(ctx context.Context, action, text string) (string, error) {
	if len(text) > maxReferenceInput || !utf8.ValidString(text) {
		return "", &RPCError{"invalid_request", "Invalid text to format"}
	}
	if _, tail, found := strings.Cut(text, "${"); !found || !strings.Contains(tail, "}") {
		return text, nil
	}
	if !c.Supports(TextReferencesProtocol) {
		return "", &UnsupportedFeatureError{TextReferencesProtocol}
	}
	if !validVaultID(action, 256) {
		return "", &RPCError{"invalid_request", "Invalid text action"}
	}
	raw, err := c.Request(ctx, TextReferencesProtocol, "format-text", map[string]string{"action": action, "text": text})
	if err != nil {
		return "", privateServiceError(TextReferencesProtocol, err)
	}
	return decodeFormattedText(raw)
}

// Decode the exact host response without exposing private message details
func decodeFormattedText(raw json.RawMessage) (string, error) {
	fields, ok := vaultObject(raw, "text")
	var text *string
	if !ok || !utf8.Valid(raw) || !validVaultSurrogates(fields["text"]) ||
		json.Unmarshal(fields["text"], &text) != nil || text == nil || len(*text) > maxReferenceOutput {
		return "", &RPCError{"invalid_response", "Invalid formatted text response"}
	}
	return *text, nil
}
