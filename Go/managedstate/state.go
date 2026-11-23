// Package managedstate persists JSON records through an explicitly selected SDK
// backend. Instances are event-loop owned. No disk or private-to-portable fallback
package managedstate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/StreamDockManager/Plugin-SDK/Go"
	"maps"
	"sort"
)

type PortableBackend interface {
	Read(context.Context) (sdk.SettingsResult, error)
	Update(context.Context, map[string]any) (sdk.SettingsResult, error)
}
type PrivateBackend interface {
	ListKeys(context.Context, string) (sdk.StorageKeys, error)
	Get(context.Context, string) (sdk.StorageValue, error)
	Receipt(context.Context, string, string) (sdk.StorageReceipt, error)
	Commit(context.Context, sdk.StorageVersion, string, []sdk.StorageMutation) (sdk.StorageReceipt, error)
}
type State struct {
	mode, key string
	portable  PortableBackend
	private   PrivateBackend
	ready     bool
	Dirty     bool
	Assurance string
	version   sdk.StorageVersion
	written   map[string]json.RawMessage
	pending   *pendingState
	blocked   error
}
type pendingState struct {
	id        string
	version   sdk.StorageVersion
	mutations []sdk.StorageMutation
	snapshot  map[string]json.RawMessage
}
type stateRecord struct {
	Identity string          `json:"identity"`
	Value    json.RawMessage `json:"value"`
}

// Create state with an explicit session, portable or private mode
func New(mode, key string, portable PortableBackend, private PrivateBackend) (*State, error) {
	if mode != "session" && mode != "portable" && mode != "private" {
		return nil, fmt.Errorf("invalid managed state mode")
	}
	if mode == "portable" && (portable == nil || key == "") || mode == "private" && private == nil {
		return nil, fmt.Errorf("managed state requires an explicit backend")
	}
	return &State{mode: mode, key: key, portable: portable, private: private, written: map[string]json.RawMessage{}}, nil
}

// Clone state into independently owned JSON records
func clone(value any) (map[string]json.RawMessage, error) {
	records, _, err := validatedRecords(value)
	return records, err
}

func validatedRecords(value any) (map[string]json.RawMessage, int, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, 0, err
	}
	if len(data) > 8<<20 {
		return nil, 0, &sdk.RPCError{Code: "quota", Message: "managed state exceeds 8 MiB"}
	}
	var result map[string]json.RawMessage
	if err = json.Unmarshal(data, &result); err != nil {
		return nil, 0, err
	}
	if result == nil {
		return nil, 0, fmt.Errorf("managed state must be a JSON object")
	}
	if len(result) > 4096 {
		return nil, 0, &sdk.RPCError{Code: "quota", Message: "managed state exceeds 4096 records"}
	}
	for key := range result {
		if key == "" {
			return nil, 0, fmt.Errorf("managed state record identity must not be empty")
		}
	}
	return result, len(data), nil
}

// Load state from the explicitly selected persistence backend
func (s *State) Load(ctx context.Context) (map[string]json.RawMessage, error) {
	if s.ready {
		return clone(s.written)
	}
	snapshot := map[string]json.RawMessage{}
	var err error
	switch s.mode {
	case "portable":
		result, e := s.portable.Read(ctx)
		if e != nil {
			return nil, e
		}
		s.Assurance = result.Assurance
		if value, ok := result.Settings[s.key]; ok {
			snapshot, err = clone(value)
		}
	case "private":
		snapshot, err = s.loadPrivate(ctx)
		s.Assurance = "observed"
	case "session":
		s.Assurance = "session"
	}
	if err != nil {
		return nil, err
	}
	s.written, err = clone(snapshot)
	if err != nil {
		return nil, err
	}
	s.ready = true
	return clone(s.written)
}

// Mark state as changed without committing it
func (s *State) MarkDirty() { s.Dirty = true }

// Check whether an uncertain commit still needs reconciliation
func (s *State) CheckPending() error {
	if s.Dirty && s.mode != "session" {
		return fmt.Errorf("managed state has an unconfirmed write")
	}
	return nil
}

// Load private records and their optimistic concurrency version
func (s *State) loadPrivate(ctx context.Context) (map[string]json.RawMessage, error) {
	result := map[string]json.RawMessage{}
	cursor := ""
	seenCursors := map[string]bool{}
	total := 0
	for {
		if seenCursors[cursor] || len(seenCursors) >= 4096 {
			return nil, fmt.Errorf("repeated storage cursor or restore page budget exceeded")
		}
		seenCursors[cursor] = true
		page, err := s.private.ListKeys(ctx, cursor)
		if err != nil {
			return nil, err
		}
		if cursor == "" {
			s.version = page.StorageVersion
		} else if page.StorageVersion != s.version {
			return nil, &sdk.RPCError{Code: "conflict", Message: "private state changed during load"}
		}
		for _, key := range page.Keys {
			value, err := s.private.Get(ctx, key)
			if err != nil {
				return nil, err
			}
			if !value.Found || value.StorageVersion != s.version {
				return nil, &sdk.RPCError{Code: "conflict", Message: "private state changed during load"}
			}
			total += len(value.Value)
			if total > 8<<20 || len(result) >= 4096 {
				return nil, &sdk.RPCError{Code: "quota", Message: "managed state exceeds restore budget"}
			}
			var record stateRecord
			if json.Unmarshal(value.Value, &record) != nil || record.Identity == "" || len(record.Value) == 0 || stateKey(record.Identity) != key {
				return nil, fmt.Errorf("invalid managed state record")
			}
			if _, duplicate := result[record.Identity]; duplicate {
				return nil, fmt.Errorf("duplicate managed state identity")
			}
			result[record.Identity] = record.Value
		}
		if page.Cursor == "" {
			return result, nil
		}
		cursor = page.Cursor
	}
}

// Derive the bounded host-storage key for a state identity
func stateKey(identity string) string {
	return fmt.Sprintf("record:%x", sha256.Sum256([]byte(identity)))
}

func equalRecords(a, b map[string]json.RawMessage) bool {
	return maps.EqualFunc(a, b, func(x, y json.RawMessage) bool { return bytes.Equal(x, y) })
}

// Save changed state using the selected persistence contract
func (s *State) Save(ctx context.Context, value any) error {
	if !s.ready {
		return fmt.Errorf("host state is not initialized")
	}
	if s.blocked != nil {
		return s.blocked
	}
	snapshot, size, err := validatedRecords(value)
	if err != nil {
		return err
	}
	s.Dirty = true
	if s.mode == "session" {
		s.written = snapshot
		s.Dirty = false
		return nil
	}
	if s.mode == "portable" {
		if !equalRecords(snapshot, s.written) {
			if size > 256<<10 {
				// Custom marshalers can supply duplicate keys. The portable quota applies to the normalized object.
				encoded, _ := json.Marshal(snapshot)
				if len(encoded) > 256<<10 {
					return &sdk.RPCError{Code: "quota", Message: "portable runtime state exceeds 256 KiB"}
				}
			}
			result, e := s.portable.Update(ctx, map[string]any{s.key: snapshot})
			if e != nil {
				return e
			}
			s.Assurance = result.Assurance
		}
		s.written = snapshot
		s.Dirty = false
		return nil
	}
	if s.pending != nil {
		receipt, err := s.private.Receipt(ctx, s.pending.version.Generation, s.pending.id)
		if err != nil {
			return err
		}
		if receipt.Found && receipt.Committed {
			s.version = receipt.StorageVersion
			s.Assurance = "committed"
			s.written = s.pending.snapshot
			s.pending = nil
		}
	}
	if s.pending == nil {
		var keys []string
		for key := range snapshot {
			if !bytes.Equal(snapshot[key], s.written[key]) {
				keys = append(keys, key)
			}
		}
		for key := range s.written {
			if _, ok := snapshot[key]; !ok {
				keys = append(keys, key)
			}
		}
		sort.Strings(keys)
		if len(keys) > 32 {
			return &sdk.RPCError{Code: "quota", Message: "managed state change exceeds one atomic 32-record commit"}
		}
		if len(keys) == 0 {
			s.Dirty = false
			return nil
		}
		pending := &pendingState{id: sdk.OperationID(), version: s.version, snapshot: snapshot}
		for _, key := range keys {
			mutation := sdk.StorageMutation{Op: "delete", Key: stateKey(key)}
			if value, ok := snapshot[key]; ok {
				mutation.Op = "put"
				mutation.Value = stateRecord{Identity: key, Value: value}
			}
			pending.mutations = append(pending.mutations, mutation)
		}
		for _, mutation := range pending.mutations {
			if mutation.Op == "put" {
				encoded, e := json.Marshal(mutation.Value)
				if e != nil {
					return e
				}
				if len(encoded) > 256<<10 {
					return &sdk.RPCError{Code: "quota", Message: "managed state record exceeds 256 KiB"}
				}
			}
		}
		s.pending = pending
	}
	pending := s.pending
	receipt, err := s.private.Commit(ctx, pending.version, pending.id, pending.mutations)
	if err != nil {
		var failure *sdk.RPCError
		if errors.As(err, &failure) && failure.Code == "conflict" {
			s.blocked = err
		}
		return err
	}
	s.version = receipt.StorageVersion
	s.Assurance = "committed"
	s.written = pending.snapshot
	s.pending = nil
	s.Dirty = !equalRecords(snapshot, s.written)
	// A newer snapshot is deliberately not folded into an ambiguous prior commit
	return nil
}
