package sdk

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"maps"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const RPCRequestEvent = "streamDockManagerRpcRequest"
const RPCResponseEvent = "streamDockManagerRpcResponse"
const StorageProtocol = "plugin-storage-v1"
const SettingsProtocol = "plugin-settings-rpc-v1"
const MaximumStorageMessage = 512 << 10
const MaximumStorageValue = 256 << 10

// RPCError never includes the request's settings or private values
type RPCError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Format the host RPC error code and message
func (e *RPCError) Error() string { return e.Code + ": " + e.Message }

type requestResult struct {
	value json.RawMessage
	err   error
}
type pendingRequest struct {
	protocol string
	result   chan requestResult
}

// OperationID is independent of the authentication token and may be retained for
// explicit reconciliation. Reuse it only for the identical logical mutation
func OperationID() string {
	var bytes [16]byte
	if _, err := rand.Read(bytes[:]); err != nil {
		panic(err)
	}
	bytes[6] = bytes[6]&15 | 64
	bytes[8] = bytes[8]&63 | 128
	return fmt.Sprintf("%x-%x-%x-%x-%x", bytes[:4], bytes[4:6], bytes[6:8], bytes[8:10], bytes[10:])
}

// Request performs a bounded, capability-negotiated Manager RPC. Cancellation or
// timeout after enqueue is indeterminate; it never retries through another route
func (c *Client) Request(ctx context.Context, protocol, method string, payload any) (json.RawMessage, error) {
	if ctx.Err() != nil {
		return nil, &RPCError{"cancelled_before_commit", "request cancelled before enqueue"}
	}
	if !c.Supports(protocol) {
		return nil, &UnsupportedFeatureError{protocol}
	}
	id := OperationID()
	e := Message(RPCRequestEvent, "", payload)
	e.ProtocolID, e.RequestID, e.Method = protocol, id, method
	encoded, err := json.Marshal(e)
	if err != nil {
		return nil, err
	}
	if len(encoded) > MaximumStorageMessage {
		return nil, &RPCError{"quota", "storage request exceeds 512 KiB"}
	}
	pending := &pendingRequest{protocol: protocol, result: make(chan requestResult, 1)}
	c.requestsMu.Lock()
	if c.requestsClosed {
		c.requestsMu.Unlock()
		return nil, &RPCError{"cancelled_before_commit", "host session is closed"}
	}
	if len(c.requests)+len(c.settingsReads) >= 8 {
		c.requestsMu.Unlock()
		return nil, &RPCError{"busy", "eight storage requests are already pending"}
	}
	if c.requests == nil {
		c.requests = map[string]*pendingRequest{}
	}
	c.requests[id] = pending
	c.requestsMu.Unlock()
	defer func() { c.requestsMu.Lock(); delete(c.requests, id); c.requestsMu.Unlock() }()
	if err = c.Send(ctx, e); err != nil {
		return nil, err
	}
	return waitRequest(ctx, pending)
}

// Wait for a pending RPC result or context cancellation
func waitRequest(ctx context.Context, pending *pendingRequest) (json.RawMessage, error) {
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	select {
	case result := <-pending.result:
		return result.value, result.err
	case <-ctx.Done():
		return nil, &RPCError{"indeterminate", "request cancelled after enqueue; reconcile before retrying"}
	case <-timer.C:
		return nil, &RPCError{"indeterminate", "request timed out; reconcile before retrying"}
	}
}

// ObserveControl is called by the transport before ordinary event admission
// True means an RPC response was consumed; base settings notifications always
// remain available to the normal event loop. It never invokes plugin callbacks
func (c *Client) ObserveControl(e Event) bool {
	c.requestsMu.Lock()
	defer c.requestsMu.Unlock()
	if e.Event == RPCResponseEvent {
		pending := c.requests[e.RequestID]
		if pending == nil {
			return true
		}
		result := requestResult{value: append(json.RawMessage(nil), e.Result...)}
		encoded, err := json.Marshal(e)
		raw := e.Raw
		if len(raw) == 0 {
			raw = encoded
		}
		var fields map[string]json.RawMessage
		decodeErr := json.Unmarshal(raw, &fields)
		_, hasResult := fields["result"]
		_, hasError := fields["error"]
		if err != nil || decodeErr != nil || len(raw) > MaximumStorageMessage || len(encoded) > MaximumStorageMessage || e.ProtocolID != pending.protocol || hasResult == hasError {
			result = requestResult{err: &RPCError{"invalid_response", "invalid Manager RPC response"}}
		} else if hasError {
			var failure struct {
				Code    *string `json:"code"`
				Message *string `json:"message"`
			}
			if json.Unmarshal(fields["error"], &failure) != nil || failure.Code == nil || failure.Message == nil {
				result = requestResult{err: &RPCError{"invalid_response", "invalid Manager RPC error"}}
			} else {
				result = requestResult{err: &RPCError{*failure.Code, *failure.Message}}
			}
		}
		select {
		case pending.result <- result:
		default:
		}
		delete(c.requests, e.RequestID)
		return true
	}
	key := "action:" + e.Context
	if e.Event == "didReceiveGlobalSettings" {
		key = "global"
	} else if e.Event != "didReceiveSettings" {
		return false
	}
	if pending := c.settingsReads[key]; pending != nil {
		var payload map[string]json.RawMessage
		var settings map[string]any
		result := requestResult{}
		if json.Unmarshal(e.Payload, &payload) != nil || json.Unmarshal(payload["settings"], &settings) != nil || settings == nil {
			result.err = &RPCError{"invalid_response", "settings response must be an object"}
		} else {
			result.value = append(json.RawMessage(nil), payload["settings"]...)
		}
		select {
		case pending.result <- result:
		default:
		}
	}
	return false
}

// Cancel and release outstanding RPC requests
func (c *Client) cancelRequests() {
	c.requestsMu.Lock()
	defer c.requestsMu.Unlock()
	c.requestsClosed = true
	for _, group := range []map[string]*pendingRequest{c.requests, c.settingsReads} {
		for key, pending := range group {
			select {
			case pending.result <- requestResult{err: &RPCError{"indeterminate", "host disconnected; reconcile before retrying"}}:
			default:
			}
			delete(group, key)
		}
	}
}

// Read the selected portable settings scope
func (c *Client) readSettings(ctx context.Context, scope, address string) (json.RawMessage, error) {
	if ctx.Err() != nil {
		return nil, &RPCError{"cancelled_before_commit", "request cancelled before enqueue"}
	}
	key := "action:" + address
	if scope == "global" {
		key = "global"
	}
	pending := &pendingRequest{result: make(chan requestResult, 1)}
	c.requestsMu.Lock()
	if c.requestsClosed {
		c.requestsMu.Unlock()
		return nil, &RPCError{"cancelled_before_commit", "host session is closed"}
	}
	if c.settingsReads[key] != nil || len(c.requests)+len(c.settingsReads) >= 8 {
		c.requestsMu.Unlock()
		return nil, &RPCError{"busy", "settings read already pending or request capacity exhausted"}
	}
	if c.settingsReads == nil {
		c.settingsReads = map[string]*pendingRequest{}
	}
	c.settingsReads[key] = pending
	c.requestsMu.Unlock()
	defer func() { c.requestsMu.Lock(); delete(c.settingsReads, key); c.requestsMu.Unlock() }()
	var err error
	if scope == "global" {
		err = c.GetGlobalSettings(ctx)
	} else {
		err = c.GetSettings(ctx, address)
	}
	if err != nil {
		return nil, err
	}
	return waitRequest(ctx, pending)
}

// PortableSettings uses the same action/global object on every host. Its base
// writes report only "sent"; reads report observed state, never a commit receipt
type PortableSettings struct {
	client         *Client
	scope, address string
	mu             sync.Mutex
}
type SettingsResult struct {
	Settings  map[string]any `json:"settings,omitempty"`
	Backend   string         `json:"backend"`
	Assurance string         `json:"assurance"`
}

// Create a portable settings accessor for an explicit scope
func NewPortableSettings(client *Client, scope, address string) (*PortableSettings, error) {
	if scope != "global" && (scope != "action" || address == "") {
		return nil, fmt.Errorf("settings scope must be global or an owned action context")
	}
	return &PortableSettings{client: client, scope: scope, address: address}, nil
}

// Read portable settings through the selected host contract
func (s *PortableSettings) Read(ctx context.Context) (SettingsResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.read(ctx)
}

// Choose the negotiated settings read protocol
func (s *PortableSettings) read(ctx context.Context) (SettingsResult, error) {
	if s.client.Supports(SettingsProtocol) {
		data, err := s.client.Request(ctx, SettingsProtocol, "get", map[string]any{"scope": s.scope, "context": s.address})
		if err == nil {
			var result SettingsResult
			err = json.Unmarshal(data, &result)
			if err != nil || result.Settings == nil || result.Assurance != "observed" {
				return SettingsResult{}, &RPCError{"invalid_response", "invalid enhanced settings result"}
			}
			result.Backend = SettingsProtocol
			return result, err
		}
		if failure, ok := err.(*RPCError); !ok || failure.Code != "unsupported" {
			return SettingsResult{}, err
		}
	}
	data, err := s.client.readSettings(ctx, s.scope, s.address)
	if err != nil {
		return SettingsResult{}, err
	}
	result := SettingsResult{Backend: "base-vsd", Assurance: "observed"}
	err = json.Unmarshal(data, &result.Settings)
	if err == nil && result.Settings == nil {
		err = fmt.Errorf("settings response must be an object")
	}
	return result, err
}

// Replace portable settings with a cloned object
func (s *PortableSettings) Replace(ctx context.Context, value map[string]any) (SettingsResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.replace(ctx, value)
}

// Send a replacement without inventing a durable acknowledgement
func (s *PortableSettings) replace(ctx context.Context, value map[string]any) (SettingsResult, error) {
	if ctx.Err() != nil {
		return SettingsResult{}, &RPCError{"cancelled_before_commit", "request cancelled before enqueue"}
	}
	if value == nil {
		return SettingsResult{}, fmt.Errorf("settings must be an object")
	}
	if s.client.Supports(SettingsProtocol) {
		data, err := s.client.Request(ctx, SettingsProtocol, "replace", map[string]any{"scope": s.scope, "context": s.address, "settings": value})
		if err == nil {
			var result SettingsResult
			err = json.Unmarshal(data, &result)
			if err != nil || result.Assurance != "committed" && result.Assurance != "applied_to_draft" {
				return SettingsResult{}, &RPCError{"invalid_response", "invalid enhanced settings result"}
			}
			result.Backend = SettingsProtocol
			return result, err
		}
		if failure, ok := err.(*RPCError); !ok || failure.Code != "unsupported" {
			return SettingsResult{}, err
		}
	}
	var err error
	if s.scope == "global" {
		err = s.client.GlobalSettings(ctx, value)
	} else {
		err = s.client.Settings(ctx, s.address, value)
	}
	return SettingsResult{Backend: "base-vsd", Assurance: "sent"}, err
}

// Update serializes read/merge/write within this facade only, not across other
// plugin/inspector writers. Explicit null remains a value, not a deletion marker
func (s *PortableSettings) Update(ctx context.Context, changes map[string]any) (SettingsResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, err := s.read(ctx)
	if err != nil {
		return SettingsResult{}, err
	}
	maps.Copy(current.Settings, changes)
	return s.replace(ctx, current.Settings)
}

type StorageVersion struct {
	Generation string `json:"generation"`
	Revision   string `json:"revision"`
}
type StorageValue struct {
	StorageVersion
	Found bool            `json:"found"`
	Value json.RawMessage `json:"value,omitempty"`
}
type StorageKeys struct {
	StorageVersion
	Keys   []string `json:"keys"`
	Cursor string   `json:"cursor,omitempty"`
}
type StorageReceipt struct {
	StorageVersion
	OperationID string `json:"operation_id"`
	Committed   bool   `json:"committed"`
	Found       bool   `json:"found,omitempty"`
}
type StorageMutation struct {
	Op, Key string
	Value   any
}
type HostStore struct {
	client    *Client
	namespace string
}

// Create a namespace-scoped negotiated host store
func NewHostStore(client *Client, namespace string) (*HostStore, error) {
	if !client.Supports(StorageProtocol) {
		return nil, &UnsupportedFeatureError{StorageProtocol}
	}
	if !utf8.ValidString(namespace) || len(namespace) == 0 || len(namespace) > 128 {
		return nil, fmt.Errorf("namespace must be 1–128 UTF-8 bytes")
	}
	return &HostStore{client: client, namespace: namespace}, nil
}

// Call a storage method and decode its typed result
func (s *HostStore) call(ctx context.Context, method string, payload map[string]any, dst any) error {
	payload["namespace"] = s.namespace
	data, err := s.client.Request(ctx, StorageProtocol, method, payload)
	if err != nil {
		return err
	}
	var fields map[string]json.RawMessage
	if json.Unmarshal(data, &fields) != nil || fields == nil {
		return &RPCError{"invalid_response", "storage result must be an object"}
	}
	var generation, revision string
	if json.Unmarshal(fields["generation"], &generation) != nil || !validGeneration(generation) {
		return &RPCError{"invalid_response", "invalid storage generation"}
	}
	var found bool
	if method == "get" || method == "receipt" {
		if string(fields["found"]) != "true" && string(fields["found"]) != "false" || json.Unmarshal(fields["found"], &found) != nil {
			return &RPCError{"invalid_response", "storage found flag is required"}
		}
	}
	if method != "receipt" || found || len(fields["revision"]) > 0 {
		if json.Unmarshal(fields["revision"], &revision) != nil || !validRevision(revision) {
			return &RPCError{"invalid_response", "invalid storage revision"}
		}
	}
	if method == "get" && (len(fields["value"]) > 0) != found {
		return &RPCError{"invalid_response", "invalid storage value presence"}
	}
	if method == "listKeys" {
		var keys []string
		if json.Unmarshal(fields["keys"], &keys) != nil || keys == nil || len(keys) > 128 {
			return &RPCError{"invalid_response", "invalid storage keys"}
		}
		for _, key := range keys {
			if !utf8.ValidString(key) || len(key) == 0 || len(key) > 256 {
				return &RPCError{"invalid_response", "invalid storage key"}
			}
		}
		if raw, ok := fields["cursor"]; ok {
			var cursor string
			if json.Unmarshal(raw, &cursor) != nil || len(cursor) > 512 {
				return &RPCError{"invalid_response", "invalid storage cursor"}
			}
		}
	}
	if method == "commit" || method == "receipt" {
		var operation string
		if json.Unmarshal(fields["operation_id"], &operation) != nil || operation != payload["operation_id"] {
			return &RPCError{"invalid_response", "invalid operation receipt"}
		}
		if method == "commit" || found {
			var committed bool
			if json.Unmarshal(fields["committed"], &committed) != nil || !committed {
				return &RPCError{"invalid_response", "receipt does not confirm commit"}
			}
		}
	}
	return json.Unmarshal(data, dst)
}

// Read a value and version from private host storage
func (s *HostStore) Get(ctx context.Context, key string) (StorageValue, error) {
	var result StorageValue
	if !utf8.ValidString(key) || len(key) == 0 || len(key) > 256 {
		return result, fmt.Errorf("key must be 1–256 UTF-8 bytes")
	}
	err := s.call(ctx, "get", map[string]any{"key": key}, &result)
	return result, err
}

// List a bounded page of namespace keys
func (s *HostStore) ListKeys(ctx context.Context, cursor string) (StorageKeys, error) {
	var result StorageKeys
	if len(cursor) > 512 {
		return result, fmt.Errorf("cursor exceeds 512 bytes")
	}
	payload := map[string]any{}
	if cursor != "" {
		payload["cursor"] = cursor
	}
	err := s.call(ctx, "listKeys", payload, &result)
	return result, err
}

// Check canonical unsigned decimal spelling
func canonicalDecimal(value string) bool {
	return value == "0" || len(value) > 0 && value[0] >= '1' && value[0] <= '9' && strings.Trim(value, "0123456789") == ""
}

// Validate a bounded generation identifier
func validGeneration(value string) bool { return len(value) <= 39 && canonicalDecimal(value) }

// Validate the storage revision representation
func validRevision(value string) bool {
	_, err := strconv.ParseUint(value, 10, 64)
	return err == nil && canonicalDecimal(value)
}

// Check canonical UUID spelling
func canonicalUUID(value string) bool {
	if len(value) != 36 {
		return false
	}
	for i, c := range value {
		if i == 8 || i == 13 || i == 18 || i == 23 {
			if c != '-' {
				return false
			}
		} else if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f') {
			return false
		}
	}
	return true
}

// Commit validated mutations against an expected storage version
func (s *HostStore) Commit(ctx context.Context, version StorageVersion, operation string, mutations []StorageMutation) (StorageReceipt, error) {
	var result StorageReceipt
	if !validGeneration(version.Generation) || !validRevision(version.Revision) || !canonicalUUID(operation) {
		return result, fmt.Errorf("canonical generation, revision and operation ID required")
	}
	if len(mutations) == 0 || len(mutations) > 32 {
		return result, fmt.Errorf("commit requires 1–32 mutations")
	}
	items := make([]map[string]any, 0, len(mutations))
	for _, mutation := range mutations {
		if !utf8.ValidString(mutation.Key) || len(mutation.Key) == 0 || len(mutation.Key) > 256 {
			return result, fmt.Errorf("invalid storage key")
		}
		item := map[string]any{"op": mutation.Op, "key": mutation.Key}
		if mutation.Op == "put" {
			encoded, err := json.Marshal(mutation.Value)
			if err != nil {
				return result, err
			}
			if len(encoded) > MaximumStorageValue {
				return result, &RPCError{"quota", "value exceeds 256 KiB"}
			}
			item["value"] = mutation.Value
		} else if mutation.Op != "delete" {
			return result, fmt.Errorf("mutation must be put or delete")
		}
		items = append(items, item)
	}
	err := s.call(ctx, "commit", map[string]any{"generation": version.Generation, "expected_revision": version.Revision, "operation_id": operation, "mutations": items}, &result)
	return result, err
}

// Retrieve the receipt for a possibly completed operation
func (s *HostStore) Receipt(ctx context.Context, generation, operation string) (StorageReceipt, error) {
	var result StorageReceipt
	if !validGeneration(generation) || !canonicalUUID(operation) {
		return result, fmt.Errorf("canonical generation and operation ID required")
	}
	err := s.call(ctx, "receipt", map[string]any{"generation": generation, "operation_id": operation}, &result)
	return result, err
}
