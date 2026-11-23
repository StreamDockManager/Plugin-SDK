package sdk

import (
	"context"
	"encoding/json"
	"errors"
	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"
)

// Load the shared host-storage contract cases
func storageFixture(t *testing.T) map[string]any {
	t.Helper()
	raw, err := os.ReadFile("testdata/storage-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture map[string]any
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	return fixture
}

// Keep explicit VSD Craft requests separate from portable binding reads
func TestVSDCraftSettingsRequest(t *testing.T) {
	client, out := NewLocalClient(HostInfo{}, "registration-id")
	ctx := context.Background()
	for _, tc := range []struct {
		send func(context.Context) error
		want string
	}{
		{client.RequestVSDCraftSettings, `{"event":"getSettings","context":"registration-id"}`},
		{func(ctx context.Context) error { return client.GetSettings(ctx, "binding-id") },
			`{"event":"getSettings","context":"binding-id"}`},
	} {
		if err := tc.send(ctx); err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(<-out)
		if err != nil || string(raw) != tc.want {
			t.Fatalf("settings request: %s, want %s (%v)", raw, tc.want, err)
		}
	}
	for _, id := range []string{"", " \t"} {
		unregistered, queue := NewLocalClient(HostInfo{}, id)
		if err := unregistered.RequestVSDCraftSettings(ctx); err == nil {
			t.Fatal("VSD Craft settings accepted a blank registration UUID")
		}
		select {
		case <-queue:
			t.Fatal("sent a settings request without a registration UUID")
		default:
		}
	}
}

// Verify shared host storage and portable settings
func TestSharedHostStorageAndPortableSettings(t *testing.T) {
	fixture := storageFixture(t)
	for _, metadata := range fixture["malformed_manager"].([]any) {
		data, _ := json.Marshal(map[string]any{"streamDockManager": metadata})
		info, err := ParseHostInfo(data)
		if err != nil || info.Supports(StorageProtocol) {
			t.Fatal("optional metadata disabled base compatibility", string(data), err)
		}
	}
	legacy, _ := ParseHostInfo([]byte(`{}`))
	base, out := NewLocalClient(legacy, "opaque-session-token")
	ctx := context.Background()
	value := fixture["value"].(map[string]any)
	base.GetSettings(ctx, "owned-context")
	base.Settings(ctx, "owned-context", value)
	base.GetGlobalSettings(ctx)
	base.GlobalSettings(ctx, value)
	for _, expected := range fixture["base_requests"].([]any) {
		raw, _ := json.Marshal(<-out)
		var actual map[string]any
		json.Unmarshal(raw, &actual)
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("base envelope: %#v != %#v", actual, expected)
		}
	}
	if _, err := NewHostStore(base, "authoring"); err == nil {
		t.Fatal("legacy HostStore accepted")
	}
	portable, _ := NewPortableSettings(base, "action", "owned-context")
	go func() {
		request := <-out
		if request.Event != "getSettings" {
			panic("wrong base request")
		}
		base.ObserveControl(Message("didReceiveSettings", "owned-context", map[string]any{"settings": value}))
	}()
	observed, err := portable.Read(ctx)
	if err != nil || observed.Assurance != "observed" || !reflect.DeepEqual(observed.Settings, value) {
		t.Fatal(observed, err)
	}
	sent, err := portable.Replace(ctx, value)
	if err != nil || sent.Assurance != "sent" {
		t.Fatal(sent, err)
	}
	<-out
	manager, _ := ParseHostInfo([]byte(`{"streamDockManager":{"contract_version":1,"capabilities":["plugin-storage-v1"]}}`))
	client, requests := NewLocalClient(manager, "opaque-session-token")
	store, err := NewHostStore(client, fixture["namespace"].(string))
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		request := <-requests
		var payload map[string]any
		json.Unmarshal(request.Payload, &payload)
		if request.ProtocolID != StorageProtocol || request.Method != "get" || payload["key"] != fixture["key"] {
			panic("wrong RPC request")
		}
		result, _ := json.Marshal(map[string]any{"found": true, "value": value, "generation": fixture["generation"], "revision": "0"})
		client.ObserveControl(Event{Event: RPCResponseEvent, ProtocolID: StorageProtocol, RequestID: request.RequestID, Result: result})
	}()
	got, err := store.Get(ctx, fixture["key"].(string))
	if err != nil || !got.Found || got.Generation != fixture["generation"] {
		t.Fatal(got, err)
	}
	go func() {
		request := <-requests
		var payload map[string]any
		json.Unmarshal(request.Payload, &payload)
		if payload["operation_id"] != fixture["operation_id"] || payload["expected_revision"] != "0" {
			panic("wrong commit version")
		}
		result, _ := json.Marshal(map[string]any{"committed": true, "operation_id": fixture["operation_id"], "generation": fixture["generation"], "revision": "1"})
		client.ObserveControl(Event{Event: RPCResponseEvent, ProtocolID: StorageProtocol, RequestID: request.RequestID, Result: result})
	}()
	receipt, err := store.Commit(ctx, got.StorageVersion, fixture["operation_id"].(string), []StorageMutation{{Op: "put", Key: "null", Value: nil}})
	if err != nil || !receipt.Committed || receipt.Revision != "1" {
		t.Fatal(receipt, err)
	}
}

// Verify host storage correlation and bounded failure
func TestHostStorageCorrelationAndBoundedFailure(t *testing.T) {
	info, _ := ParseHostInfo([]byte(`{"streamDockManager":{"contract_version":1,"capabilities":["plugin-storage-v1","plugin-settings-rpc-v1"]}}`))
	client, out := NewLocalClient(info, "opaque")
	go func() {
		request := <-out
		client.ObserveControl(Event{Event: RPCResponseEvent, ProtocolID: "wrong-protocol", RequestID: request.RequestID, Result: json.RawMessage(`{}`)})
	}()
	if _, err := client.Request(context.Background(), StorageProtocol, "get", map[string]any{}); err == nil {
		t.Fatal("wrong protocol accepted")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	_, err := client.Request(ctx, StorageProtocol, "commit", map[string]any{})
	var failure *RPCError
	if !errors.As(err, &failure) || failure.Code != "indeterminate" {
		t.Fatal(err)
	}
	<-out
	// A failed enhanced write cannot enqueue a second base write
	portable, _ := NewPortableSettings(client, "global", "")
	go func() {
		request := <-out
		client.ObserveControl(Event{Event: RPCResponseEvent, ProtocolID: SettingsProtocol, RequestID: request.RequestID, Error: &RPCError{"storage_failure", "storage unavailable"}})
	}()
	if _, err := portable.Replace(context.Background(), map[string]any{"x": true}); err == nil {
		t.Fatal("failed write accepted")
	}
	select {
	case message := <-out:
		t.Fatal("unsafe fallback", message)
	default:
	}
}

// Verify shared malformed storage and base responses
func TestSharedMalformedStorageAndBaseResponses(t *testing.T) {
	fixture := storageFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	legacy, _ := ParseHostInfo([]byte(`{}`))
	base, baseOut := NewLocalClient(legacy, "opaque")
	portable, _ := NewPortableSettings(base, "action", "owned")
	for _, payload := range fixture["invalid_base_payloads"].([]any) {
		go func() {
			<-baseOut
			base.ObserveControl(Message("didReceiveSettings", "owned", payload))
		}()
		_, err := portable.Read(ctx)
		var failure *RPCError
		if !errors.As(err, &failure) || failure.Code != "invalid_response" {
			t.Fatalf("invalid base payload %#v: %v", payload, err)
		}
	}
	info, _ := ParseHostInfo([]byte(`{"streamDockManager":{"contract_version":1,"capabilities":["plugin-storage-v1"]}}`))
	client, out := NewLocalClient(info, "opaque")
	store, _ := NewHostStore(client, "fixture")
	answer := func(field string, value any) {
		request := <-out
		raw, _ := json.Marshal(map[string]any{"event": RPCResponseEvent, "protocol_id": request.ProtocolID, "request_id": request.RequestID, field: value})
		var response Event
		if err := json.Unmarshal(raw, &response); err != nil {
			t.Error(err)
			return
		}
		client.ObserveControl(response)
	}
	for _, value := range fixture["invalid_rpc_errors"].([]any) {
		go answer("error", value)
		_, err := store.Get(ctx, "key")
		var failure *RPCError
		if !errors.As(err, &failure) || failure.Code != "invalid_response" {
			t.Fatalf("invalid RPC error %#v: %v", value, err)
		}
	}
	for _, revision := range fixture["invalid_receipt_revisions"].([]any) {
		go answer("result", map[string]any{"found": false, "generation": fixture["generation"], "revision": revision, "operation_id": fixture["operation_id"]})
		_, err := store.Receipt(ctx, fixture["generation"].(string), fixture["operation_id"].(string))
		var failure *RPCError
		if !errors.As(err, &failure) || failure.Code != "invalid_response" {
			t.Fatalf("invalid receipt revision %#v: %v", revision, err)
		}
	}
}

type storageWaitHandler struct {
	events  []string
	failure error
}

// Keep the fixture event-driven without periodic work
func (h *storageWaitHandler) Poll(context.Context, *Client) error { return nil }

// Perform the fixture shutdown behavior expected by the test
func (h *storageWaitHandler) Close() error { return nil }

// Request private storage during input handling to exercise RPC progress
func (h *storageWaitHandler) Handle(ctx context.Context, c *Client, event Event) error {
	h.events = append(h.events, event.Event)
	if event.Event != "keyDown" {
		return nil
	}
	store, err := NewHostStore(c, "fixture")
	if err != nil {
		h.failure = err
		return err
	}
	value, err := store.Get(ctx, "missing")
	if err != nil {
		h.failure = err
		return err
	}
	if value.Found || len(h.events) != 1 {
		h.failure = errors.New("handler reentered or missing value found")
		return h.failure
	}
	return c.Title(ctx, "a", "done")
}

// Verify storage response unblocks waiting handler before ordinary events
func TestStorageResponseUnblocksWaitingHandlerBeforeOrdinaryEvents(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		conn.SetReadLimit(1 << 20)
		wsCtx, wsCancel := context.WithDeadline(context.Background(), time.Now().Add(4*time.Second))
		defer wsCancel()
		var request Event
		if err = wsjson.Read(wsCtx, conn, &request); err != nil {
			t.Error(err)
			return
		}
		wsjson.Write(wsCtx, conn, Event{Event: "keyDown", Context: "a"})
		if err = wsjson.Read(wsCtx, conn, &request); err != nil {
			t.Error(err)
			return
		}
		if request.Event != RPCRequestEvent {
			t.Error("wrong RPC event", request.Event)
			return
		}
		wsjson.Write(wsCtx, conn, Event{Event: "keyUp", Context: "a"})
		wsjson.Write(wsCtx, conn, Event{Event: RPCResponseEvent, ProtocolID: StorageProtocol, RequestID: request.RequestID, Result: json.RawMessage(`{"found":false,"generation":"1","revision":"0"}`)})
		if err = wsjson.Read(wsCtx, conn, &request); err != nil {
			t.Error(err)
			return
		}
		if request.Event != "setTitle" {
			t.Error("handler failed", request.Event)
		}
		wsjson.Write(wsCtx, conn, Event{Event: "shutdown"})
	}))
	defer server.Close()
	info, _ := ParseHostInfo([]byte(`{"streamDockManager":{"contract_version":1,"capabilities":["plugin-storage-v1"]}}`))
	handler := &storageWaitHandler{}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := ConnectWithInfo(ctx, "ws"+strings.TrimPrefix(server.URL, "http"), "opaque", handler, info); err != nil {
		t.Fatal(err)
	}
	if handler.failure != nil || !reflect.DeepEqual(handler.events, []string{"keyDown", "keyUp"}) {
		t.Fatal(handler.events, handler.failure)
	}
}
