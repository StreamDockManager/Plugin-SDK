package sdk

import (
	"context"
	"encoding/json"
	"os"
	"reflect"
	"strings"
	"testing"
)

func TestPermissionServiceContract(t *testing.T) {
	data, err := os.ReadFile("testdata/permissions-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Webhook                                                         map[string]string
		Permissions, Broker, Action, Revision, Writes, Text, References string
		Use, Browse                                                     map[string]string
		Page                                                            VaultEntryPage
		Write                                                           map[string]string
		Receipt                                                         VaultWriteReceipt
		Token                                                           string `json:"activation_token"`
		Review                                                          string `json:"review_id"`
		Handle                                                          string `json:"result_handle"`
		Scope                                                           map[string]string
	}
	if err = json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	manager, _ := ParseHostInfo([]byte(`{"streamDockManager":{"contract_version":1,"capabilities":["plugin-permissions-v1","plugin-http-broker-v1","plugin-vault-writes-v1","plugin-vault-references-v1"]}}`))
	c, ch := NewLocalClient(manager, "session")
	ctx := context.Background()
	type call struct {
		method, cap     string
		payload, result any
		run             func() error
	}
	for _, tc := range []call{
		{"get-permissions", f.Permissions, map[string]any{}, map[string]any{"revision": f.Revision, "scopes": []any{}}, func() error {
			v, e := c.GetPermissions(ctx)
			if v.Revision != f.Revision {
				t.Error("revision")
			}
			return e
		}},
		{"has-permission", f.Permissions, map[string]any{"action": f.Action, "scope": f.Scope}, map[string]any{"revision": f.Revision, "granted": false, "available": true}, func() error {
			v, e := c.HasPermission(ctx, f.Action, f.Scope)
			if !v.Available || v.Granted {
				t.Error("decision")
			}
			return e
		}},
		{"request-permissions", f.Permissions, map[string]any{"activation_token": f.Token, "scopes": []map[string]string{f.Scope}}, map[string]any{"state": "pending", "review_id": f.Review, "revision": f.Revision}, func() error {
			v, e := c.RequestPermissions(ctx, f.Token, []map[string]string{f.Scope})
			if v.ReviewID != f.Review {
				t.Error("review")
			}
			return e
		}},
		{"clipboard-summary", f.Broker, map[string]any{"activation_token": f.Token}, map[string]any{"state": "pending", "result_handle": f.Handle}, func() error {
			v, e := c.SummarizeClipboard(ctx, f.Token)
			if v.ResultHandle != f.Handle {
				t.Error("handle")
			}
			return e
		}},

		{"manager-webhook", f.Broker, map[string]string{"activation_token": f.Token, "certificate": f.Webhook["certificate"]}, map[string]string{"state": "pending", "result_handle": f.Handle}, func() error {
			r, e := c.SendExampleWebhook(ctx, f.Token, f.Webhook["certificate"])
			if r.ResultHandle != f.Handle {
				t.Error("webhook handle")
			}
			return e
		}},

		{"manager-local-webhook", f.Broker, map[string]string{"activation_token": f.Token, "origin": f.Webhook["origin"]}, map[string]string{"state": "pending", "result_handle": f.Handle}, func() error {
			r, e := c.SendLocalWebhook(ctx, f.Token, f.Webhook["origin"])
			if r.ResultHandle != f.Handle {
				t.Error("webhook handle")
			}
			return e
		}},
		{"read-clipboard-text", f.Permissions, map[string]string{"activation_token": f.Token}, map[string]string{"text": f.Text, "revision": "7"}, func() error {
			v, e := c.ReadClipboardText(ctx, f.Token)
			if v.Text != f.Text {
				t.Error("clipboard")
			}
			return e
		}},
		{"show-text-result", f.Permissions, map[string]string{"activation_token": f.Token, "text": f.Text}, map[string]string{"result_handle": f.Handle, "state": "pending"}, func() error {
			v, e := c.ShowTextResult(ctx, f.Token, f.Text)
			if v.ResultHandle != f.Handle {
				t.Error("result")
			}
			return e
		}},
		{"notify-completion", f.Permissions, map[string]string{"activation_token": f.Token}, map[string]string{"state": "completed"}, func() error {
			v, e := c.NotifyCompletion(ctx, f.Token)
			if v.State != "completed" {
				t.Error("completion")
			}
			return e
		}},
		{"write-secret", f.Writes, f.Write, f.Receipt, func() error {
			w := f.Write
			v, e := c.WriteVaultSecret(ctx, VaultValueRequest{w["slot"], w["field"], f.Action}, w["entry_kind"], w["value"], "3")
			if v != f.Receipt {
				t.Error("receipt")
			}
			return e
		}},

		{"list-vault-entries", f.References, map[string]any{"action": f.Action, "scope": f.Browse, "start": 0}, f.Page, func() error {
			v, e := c.ListVaultEntries(ctx, f.Action, f.Browse, 0)
			if !reflect.DeepEqual(v, f.Page) {
				t.Error("entries")
			}
			return e
		}},
		{"select-vault-entry", f.References, map[string]any{"action": f.Action, "scope": f.Use, "activation_token": f.Token}, map[string]string{"state": "pending", "review_id": f.Review, "revision": "7"}, func() error {
			v, e := c.SelectVaultEntry(ctx, f.Token, f.Action, f.Use)
			if v.ReviewID != f.Review {
				t.Error("selection")
			}
			return e
		}},
		{"clipboard-summary", f.Broker, map[string]any{"entry_ref": f.Receipt.EntryRef, "scope": f.Use, "activation_token": f.Token}, map[string]string{"result_handle": f.Handle, "state": "pending"}, func() error {
			v, e := c.RunVaultRecipe(ctx, f.Token, f.Use, f.Receipt.EntryRef)
			if v.ResultHandle != f.Handle {
				t.Error("credentialed summary")
			}
			return e
		}},
	} {
		done := make(chan error, 1)
		go func() { done <- tc.run() }()
		req := <-ch
		var got, want any
		_ = json.Unmarshal(req.Payload, &got)
		wire, _ := json.Marshal(tc.payload)
		_ = json.Unmarshal(wire, &want)
		if tc.method != req.Method || tc.cap != req.ProtocolID || !reflect.DeepEqual(got, want) {
			t.Fatal("wrong request", req.Method)
		}
		result, _ := json.Marshal(tc.result)
		c.ObserveControl(Event{Event: RPCResponseEvent, ProtocolID: req.ProtocolID, RequestID: req.RequestID, Result: result})
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	var fixtures map[string]json.RawMessage
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"terminal_event", "changed_event", "selection_event"} {
		var ev Event
		if err := json.Unmarshal(fixtures[key], &ev); err != nil {
			t.Fatal(err)
		}
		notice, ok := ev.PermissionNotice()
		if !ok || notice.Event != ev.Event {
			t.Fatal(key)
		}
		encoded, _ := json.Marshal(notice)
		var got, want any
		json.Unmarshal(encoded, &got)
		json.Unmarshal(fixtures[key], &want)
		if !reflect.DeepEqual(got, want) {
			t.Fatal("notice changed", key)
		}
	}
	failed := make(chan error, 1)
	go func() { _, err := c.SummarizeClipboard(ctx, f.Token); failed <- err }()
	req := <-ch
	c.ObserveControl(Event{Event: RPCResponseEvent, ProtocolID: req.ProtocolID, RequestID: req.RequestID, Error: &RPCError{"activation_expired", "private fixture contents"}})
	failure := <-failed
	if code, ok := failure.(*RPCError); !ok || code.Code != "activation_expired" || strings.Contains(failure.Error(), "private fixture contents") {
		t.Fatal("unsafe error mapping")
	}
	legacy, out := NewLocalClient(HostInfo{}, "legacy")
	if _, err := legacy.SummarizeClipboard(ctx, f.Token); err == nil || len(out) > 0 {
		t.Fatal("legacy fallback")
	}
}

// Verify exact plugin notification negotiation, wire and redacted denials
func TestCreateNotificationContract(t *testing.T) {
	data, err := os.ReadFile("testdata/permissions-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Notification        map[string]string
		NotificationReceipt json.RawMessage `json:"notification_receipt"`
	}
	if err = json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	run := func(c *Client, message string) error {
		return c.CreateNotification(ctx, f.Notification["action"], NotificationWarning, message)
	}
	base, _ := ParseHostInfo([]byte(`{"streamDockManager":{"contract_version":1,"capabilities":["plugin-permissions-v1"]}}`))
	absent, out := NewLocalClient(base, "legacy")
	if err = run(absent, f.Notification["message"]); err == nil || len(out) != 0 {
		t.Fatal("unsupported host sent a notice")
	}
	host, _ := ParseHostInfo([]byte(`{"streamDockManager":{"contract_version":1,"capabilities":["plugin-permissions-v1","plugin-notifications-v1"]}}`))
	client, out := NewLocalClient(host, "fixture")
	if err = run(client, strings.Repeat("é", 257)); err == nil || len(out) != 0 {
		t.Fatal("oversize notice reached host")
	}
	for _, tc := range []struct {
		result  json.RawMessage
		failure *RPCError
		code    string
	}{{f.NotificationReceipt, nil, ""}, {json.RawMessage(`{"state":"unexpected"}`), nil, "invalid_response"}, {nil, &RPCError{"permission_denied", "private fixture content"}, "permission_denied"}} {
		done := make(chan error, 1)
		go func() { done <- run(client, f.Notification["message"]) }()
		req := <-out
		var body map[string]string
		if json.Unmarshal(req.Payload, &body) != nil || !reflect.DeepEqual(body, f.Notification) || req.Method != "create-notification" || req.ProtocolID != PermissionsProtocol {
			t.Fatal("unexpected request")
		}
		client.ObserveControl(Event{Event: RPCResponseEvent, ProtocolID: req.ProtocolID, RequestID: req.RequestID, Result: tc.result, Error: tc.failure})
		err = <-done
		if tc.code == "" {
			if err != nil {
				t.Fatal(err)
			}
		} else if failure, ok := err.(*RPCError); !ok || failure.Code != tc.code || strings.Contains(err.Error(), "private fixture content") {
			t.Fatal("wrong failure", err)
		}
	}
}
