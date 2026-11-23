package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestVaultSlotRPC(t *testing.T) {
	var fixture struct {
		Selector VaultValueRequest
		Methods  []struct {
			Protocol string   `json:"protocol_id"`
			Method   string   `json:"method"`
			Kinds    []string `json:"entry_kinds"`
		}
	}
	data, err := os.ReadFile("testdata/vault-slots-v1.json")
	if err != nil || json.Unmarshal(data, &fixture) != nil || len(fixture.Methods) != 2 {
		t.Fatal("invalid shared Vault fixture")
	}
	selection := fixture.Selector
	legacy, _ := NewLocalClient(HostInfo{}, "session")
	if _, err := legacy.ReadVaultText(context.Background(), selection); err == nil {
		t.Fatal("legacy host unexpectedly has Vault")
	}
	manager, _ := ParseHostInfo([]byte(`{"streamDockManager":{"contract_version":1,"capabilities":["plugin-vault-values-v1","plugin-vault-secret-sharing-v1"]}}`))
	for _, tc := range fixture.Methods {
		kind := tc.Kinds[0]
		client, outgoing := NewLocalClient(manager, "session")
		done := make(chan Event, 1)
		go func() {
			req := <-outgoing
			done <- req
			result := json.RawMessage(fmt.Sprintf(`{"entry_kind":%q,"value":"private-fixture","revision":"3"}`, kind))
			client.ObserveControl(Event{Event: RPCResponseEvent, ProtocolID: req.ProtocolID, RequestID: req.RequestID, Result: result})
		}()
		var actual any
		var value, revision string
		var err error
		if kind == "text" {
			v, failure := client.ReadVaultText(context.Background(), selection)
			actual, value, revision, err = v, v.Value, v.Revision, failure
		} else {
			v, failure := client.ReadVaultSecret(context.Background(), selection)
			actual, value, revision, err = v, v.Reveal(), v.Revision, failure
		}
		event := <-done
		var wire VaultValueRequest
		_ = json.Unmarshal(event.Payload, &wire)
		data, _ := json.Marshal(actual)
		if err != nil || event.ProtocolID != tc.Protocol || event.Method != tc.Method || !reflect.DeepEqual(selection, wire) ||
			revision != "3" || value != "private-fixture" || strings.Contains(fmt.Sprintf("%#v", actual)+string(data), "private-fixture") {
			t.Fatal("Vault request/result or redaction failed", err)
		}
	}
	client, outgoing := NewLocalClient(manager, "session")
	go func() {
		req := <-outgoing
		data, _ := json.Marshal(map[string]any{"event": RPCResponseEvent, "protocol_id": req.ProtocolID,
			"request_id": req.RequestID, "error": map[string]any{"code": "vault_locked", "message": "private-fixture"}})
		var response Event
		_ = json.Unmarshal(data, &response)
		response.Raw = data
		client.ObserveControl(response)
	}()
	_, err = client.ReadVaultText(context.Background(), selection)
	var failure *RPCError
	if !errors.As(err, &failure) || failure.Code != "vault_locked" || strings.Contains(err.Error(), "private-fixture") {
		t.Fatal("Vault error did not preserve safe code and redact details")
	}
}

func TestVaultMalformedResult(t *testing.T) {
	if actual, _, _, err := decodeVault([]byte(`{"entry_kind":"text","value":"\ud83d\ude00","revision":"1"}`), false); err != nil || actual != "😀" {
		t.Fatal("rejected valid supplementary Unicode", err)
	}
	for _, raw := range []string{
		`{"entry_kind":"text","value":null,"revision":"1"}`,
		`{"entry_kind":"text","value":"\ud800","revision":"1"}`,
		`{"entry_kind":"text","value":"x","revision":"01"}`,
		`{"entry_kind":"text","value":"x","revision":"18446744073709551616"}`,
		`{"entry_kind":"credential","value":"x","revision":"1"}`,
		`{"entry_kind":"text","value":"x","value":"y","revision":"1"}`,
		`{"entry_kind":"text","value":"x","revision":"1","entry_ref":"fixture"}`,
		`{"entry_kind":"text","value":"x","revision":"1"}{}`,
		fmt.Sprintf(`{"entry_kind":"text","value":%q,"revision":"1"}`, strings.Repeat("x", maxVaultText+1)),
	} {
		if _, _, _, err := decodeVault([]byte(raw), false); err == nil {
			t.Fatal("accepted malformed response")
		}
	}
}

func TestVaultPermissionDescriptor(t *testing.T) {
	var fixture struct{ Permissions *PermissionDeclaration }
	data, err := os.ReadFile("testdata/vault-slots-v1.json")
	if err != nil || json.Unmarshal(data, &fixture) != nil || fixture.Permissions == nil {
		t.Fatal("invalid permission fixture")
	}
	frame, err := FrameCapabilities(Capabilities{Permissions: fixture.Permissions})
	if err != nil {
		t.Fatal(err)
	}
	previous := CompiledDescriptor
	t.Cleanup(func() { CompiledDescriptor = previous })
	CompiledDescriptor = frame
	value, err := RuntimeCapabilities()
	if err != nil || !reflect.DeepEqual(value.Permissions, fixture.Permissions) {
		t.Fatal("permission descriptor did not survive registration framing")
	}
}

func TestSharedLocation(t *testing.T) {
	var f struct {
		Location struct {
			Method   string
			Protocol string `json:"protocol_id"`
			Selector VaultValueRequest
			Result   json.RawMessage
			Invalid  []any `json:"invalid_coordinates"`
		}
	}
	raw, err := os.ReadFile("testdata/vault-slots-v1.json")
	if err != nil || json.Unmarshal(raw, &f) != nil {
		t.Fatal("invalid fixture")
	}
	info, _ := ParseHostInfo([]byte(`{"streamDockManager":{"contract_version":1,"capabilities":["plugin-vault-values-v1"]}}`))
	client, out := NewLocalClient(info, "session")
	go func() {
		req := <-out
		var sel VaultValueRequest
		_ = json.Unmarshal(req.Payload, &sel)
		if req.ProtocolID != f.Location.Protocol || req.Method != f.Location.Method || sel != f.Location.Selector {
			t.Error("wrong location selector")
		}
		client.ObserveControl(Event{Event: RPCResponseEvent, RequestID: req.RequestID, ProtocolID: req.ProtocolID, Result: f.Location.Result})
	}()
	loc, err := client.ReadVaultLocation(context.Background(), f.Location.Selector)
	encoded, _ := json.Marshal(loc)
	if err != nil || loc.Latitude != 12.34567 || loc.Longitude != 0 || loc.Revision != "3" || strings.Contains(fmt.Sprintf("%v", loc)+string(encoded), "12.34567") {
		t.Fatal("location read or redaction failed")
	}
	for _, bad := range f.Location.Invalid {
		raw, _ := json.Marshal(map[string]any{"value": bad, "entry_kind": "location", "revision": "3"})
		if _, err := decodeVaultLocation(raw); err == nil {
			t.Error("accepted invalid coordinates")
		}
	}
}
