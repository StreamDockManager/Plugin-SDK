package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"regexp"
	"slices"
	"strconv"
	"unicode/utf8"
)

const VaultValuesProtocol = "plugin-vault-values-v1"
const VaultSecretSharingProtocol = "plugin-vault-secret-sharing-v1"
const maxVaultText = 64 << 10

var vaultIdentifier = regexp.MustCompile("^[A-Za-z0-9_.-]+$")

// VaultValueRequest selects a reviewed logical setup slot; it grants no authority.
type VaultValueRequest struct {
	Slot   string `json:"slot"`
	Field  string `json:"field"`
	Action string `json:"action"`
}

// VaultText contains a nonsecret text slot value (which may still be private).
type VaultText struct {
	Value    string
	Revision string
}

func (VaultText) Format(w fmt.State, _ rune) { _, _ = io.WriteString(w, "[Vault text]") }
func (VaultText) MarshalJSON() ([]byte, error) {
	return json.Marshal("[Vault text]")
}

// VaultSecret keeps a directly shared credential out of default display and serialization.
type VaultSecret struct {
	value     string
	Revision  string
	EntryKind string
}

// Reveal explicitly extracts plaintext. Avoid logging, persistence and replay.
func (s VaultSecret) Reveal() string           { return s.value }
func (VaultSecret) Format(w fmt.State, _ rune) { _, _ = io.WriteString(w, "[Vault secret]") }
func (VaultSecret) MarshalJSON() ([]byte, error) {
	return json.Marshal("[Vault secret]")
}

// ReadVaultText reads one approved nonsecret text slot, without a vendor fallback.
func (c *Client) ReadVaultText(ctx context.Context, selection VaultValueRequest) (VaultText, error) {
	v, r, _, err := c.readVault(ctx, selection, VaultValuesProtocol, "read-value", false)
	return VaultText{Value: v, Revision: r}, err
}

// VaultLocation contains the saved coordinates approved for a single action.
type VaultLocation struct {
	Latitude, Longitude float64
	Revision            string
}

func (VaultLocation) Format(w fmt.State, _ rune)   { _, _ = io.WriteString(w, "[Vault location]") }
func (VaultLocation) MarshalJSON() ([]byte, error) { return json.Marshal("[Vault location]") }

func (c *Client) ReadVaultLocation(ctx context.Context, selection VaultValueRequest) (VaultLocation, error) {
	if !validVaultID(selection.Slot, 128) || !validVaultID(selection.Field, 128) || !validVaultID(selection.Action, 256) {
		return VaultLocation{}, &RPCError{"invalid_request", "Invalid Vault location selector"}
	}
	raw, err := c.Request(ctx, VaultValuesProtocol, "read-location", selection)
	if err != nil {
		return VaultLocation{}, privateServiceError(VaultValuesProtocol, err)
	}
	return decodeVaultLocation(raw)
}

// Decode exact, case-sensitive object keys and reject ambiguous coordinates.
func vaultObject(raw json.RawMessage, keys ...string) (map[string]json.RawMessage, bool) {
	d := json.NewDecoder(bytes.NewReader(raw))
	first, err := d.Token()
	if err != nil || first != json.Delim('{') {
		return nil, false
	}
	fields := make(map[string]json.RawMessage, len(keys))
	for d.More() {
		token, err := d.Token()
		key, ok := token.(string)
		if err != nil || !ok || !slices.Contains(keys, key) || fields[key] != nil {
			return nil, false
		}
		var value json.RawMessage
		if d.Decode(&value) != nil {
			return nil, false
		}
		fields[key] = value
	}
	last, err := d.Token()
	if err != nil || last != json.Delim('}') || len(fields) != len(keys) {
		return nil, false
	}
	_, err = d.Token()
	return fields, err == io.EOF
}

func decodeVaultLocation(raw json.RawMessage) (VaultLocation, error) {
	invalid := &RPCError{"invalid_response", "Invalid Vault location response"}
	fields, ok := vaultObject(raw, "entry_kind", "value", "revision")
	var kind, revision string
	if !ok || json.Unmarshal(fields["entry_kind"], &kind) != nil || kind != "location" || json.Unmarshal(fields["revision"], &revision) != nil {
		return VaultLocation{}, invalid
	}
	n, err := strconv.ParseUint(revision, 10, 64)
	coords, ok := vaultObject(fields["value"], "latitude", "longitude")
	if err != nil || strconv.FormatUint(n, 10) != revision || !ok {
		return VaultLocation{}, invalid
	}
	var lat, lon *float64
	if json.Unmarshal(coords["latitude"], &lat) != nil || lat == nil || math.Abs(*lat) > 90 || json.Unmarshal(coords["longitude"], &lon) != nil || lon == nil || math.Abs(*lon) > 180 {
		return VaultLocation{}, invalid
	}
	return VaultLocation{*lat, *lon, revision}, nil
}

// ReadVaultSecret reads one explicitly approved credential, without a vendor fallback.
func (c *Client) ReadVaultSecret(ctx context.Context, selection VaultValueRequest) (VaultSecret, error) {
	v, r, k, err := c.readVault(ctx, selection, VaultSecretSharingProtocol, "read-secret", true)
	return VaultSecret{value: v, Revision: r, EntryKind: k}, err
}

func validVaultID(value string, max int) bool {
	return len(value) > 0 && len(value) <= max && vaultIdentifier.MatchString(value)
}

func (c *Client) readVault(ctx context.Context, selection VaultValueRequest, protocol, method string, secret bool) (string, string, string, error) {
	if !validVaultID(selection.Slot, 128) || !validVaultID(selection.Field, 128) || !validVaultID(selection.Action, 256) {
		return "", "", "", &RPCError{"invalid_request", "invalid Vault slot, field or action"}
	}
	raw, err := c.Request(ctx, protocol, method, selection)
	if err != nil {
		return "", "", "", privateServiceError(protocol, err)
	}
	return decodeVault(raw, secret)
}

func decodeVault(raw json.RawMessage, secret bool) (string, string, string, error) {
	invalid := &RPCError{"invalid_response", "invalid Vault response"}
	if !utf8.Valid(raw) {
		return "", "", "", invalid
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	if token, err := d.Token(); err != nil || token != json.Delim('{') {
		return "", "", "", invalid
	}
	fields := map[string]string{}
	for d.More() {
		key, err := d.Token()
		field, ok := key.(string)
		if err != nil || !ok || field != "entry_kind" && field != "value" && field != "revision" {
			return "", "", "", invalid
		}
		if _, exists := fields[field]; exists {
			return "", "", "", invalid
		}
		var encoded json.RawMessage
		var value *string
		if d.Decode(&encoded) != nil || !validVaultSurrogates(encoded) || json.Unmarshal(encoded, &value) != nil || value == nil {
			return "", "", "", invalid
		}
		fields[field] = *value
	}
	if token, err := d.Token(); err != nil || token != json.Delim('}') {
		return "", "", "", invalid
	}
	if _, err := d.Token(); err != io.EOF {
		return "", "", "", invalid
	}
	revision, value := fields["revision"], fields["value"]
	number, err := strconv.ParseUint(revision, 10, 64)
	kind := fields["entry_kind"]
	validKind := !secret && kind == "text" || secret && (kind == "api-key" || kind == "bearer-token" || kind == "credential")
	if len(fields) != 3 || !validKind || len(value) > maxVaultText ||
		err != nil || strconv.FormatUint(number, 10) != revision {
		return "", "", "", invalid
	}
	return value, revision, kind, nil
}

// Go's JSON decoder normally replaces escaped unpaired surrogates. Reject those
// instead so a malformed credential never changes into a different value.
func validVaultSurrogates(raw []byte) bool {
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' || i+1 >= len(raw) {
			continue
		}
		i++
		if raw[i] != 'u' || i+4 >= len(raw) {
			continue
		}
		point, err := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		if err != nil {
			return false
		}
		i += 4
		if point >= 0xdc00 && point <= 0xdfff {
			return false
		}
		if point >= 0xd800 && point <= 0xdbff {
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return false
			}
			tail, err := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if err != nil || tail < 0xdc00 || tail > 0xdfff {
				return false
			}
			i += 6
		}
	}
	return true
}

func privateServiceError(protocol string, err error) error {
	// Do not expose host-supplied error details from a private Vault operation.
	var failure *RPCError
	if errors.As(err, &failure) {
		switch failure.Code {
		case "needs_setup", "vault_locked", "permission_denied", "not_declared", "operation_unavailable", "unsupported", "stale_revision", "cancelled", "cancelled_before_commit", "timeout", "rate_limited", "busy", "indeterminate", "invalid_request", "invalid_response", "quota", "slot_conflict", "activation_required", "activation_expired", "no_text", "unsupported_format", "invalid_encoding", "too_large", "clipboard_changed", "clipboard_access":
			return &RPCError{failure.Code, "Manager request did not complete"}
		}
	}
	var unsupported *UnsupportedFeatureError
	if errors.As(err, &unsupported) {
		return &UnsupportedFeatureError{protocol}
	}
	return &RPCError{"operation_unavailable", "Manager request did not complete"}
}

const VaultWritesProtocol = "plugin-vault-writes-v1"

type VaultWriteReceipt struct {
	EntryRef string `json:"entry_ref"`
	Revision string `json:"revision"`
}

var vaultRef = regexp.MustCompile("^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}[.][0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$")

func positiveRevision(s string) bool {
	n, err := strconv.ParseUint(s, 10, 64)
	return err == nil && n > 0 && strconv.FormatUint(n, 10) == s
}

// WriteVaultSecret creates a private slot, or rotates it when given its current revision.
func (c *Client) WriteVaultSecret(ctx context.Context, selection VaultValueRequest, kind, value, expectedRevision string) (VaultWriteReceipt, error) {
	var receipt VaultWriteReceipt
	if !validVaultID(selection.Slot, 128) || !validVaultID(selection.Field, 128) || !validVaultID(selection.Action, 256) ||
		(kind != "api-key" && kind != "credential" && kind != "bearer-token") || !utf8.ValidString(value) || len(value) == 0 || len(value) > maxVaultText ||
		(expectedRevision != "" && !positiveRevision(expectedRevision)) {
		return receipt, &RPCError{"invalid_request", "Invalid Vault write"}
	}
	payload := map[string]string{"operation": "create", "slot": selection.Slot, "field": selection.Field, "action": selection.Action, "entry_kind": kind, "value": value}
	if expectedRevision != "" {
		payload["operation"] = "rotate"
		payload["expected_revision"] = expectedRevision
	}
	raw, err := c.Request(ctx, VaultWritesProtocol, "write-secret", payload)
	if err != nil {
		return receipt, privateServiceError(VaultWritesProtocol, err)
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&receipt) != nil || !vaultRef.MatchString(receipt.EntryRef) || !positiveRevision(receipt.Revision) {
		return VaultWriteReceipt{}, &RPCError{"invalid_response", "Invalid Vault write receipt"}
	}
	return receipt, nil
}
