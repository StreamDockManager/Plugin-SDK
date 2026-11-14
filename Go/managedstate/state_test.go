package managedstate

import (
	"context"
	"encoding/json"
	"fmt"
	sdk "github.com/StreamDockManager/Plugin-SDK/Go"
	"maps"
	"os"
	"reflect"
	"testing"
)

type recorded struct {
	Version   sdk.StorageVersion
	ID        string
	Mutations []sdk.StorageMutation
}
type memory struct {
	recovery              string
	revision              int
	fail, conflict, cycle bool
	records               map[string]stateRecord
	receipts              map[string]sdk.StorageReceipt
	commits               []recorded
}

// Return the fake storage generation and revision
func (m *memory) version() sdk.StorageVersion {
	return sdk.StorageVersion{Generation: "1", Revision: fmt.Sprint(m.revision)}
}

// List keys from the in-memory storage test backend
func (m *memory) ListKeys(context.Context, string) (sdk.StorageKeys, error) {
	keys := []string{}
	for key := range m.records {
		keys = append(keys, key)
	}
	cursor := ""
	if m.cycle {
		cursor = "same"
	}
	return sdk.StorageKeys{StorageVersion: m.version(), Keys: keys, Cursor: cursor}, nil
}

// Read a value from the in-memory storage test backend
func (m *memory) Get(_ context.Context, key string) (sdk.StorageValue, error) {
	record, ok := m.records[key]
	raw, _ := json.Marshal(record)
	return sdk.StorageValue{StorageVersion: m.version(), Found: ok, Value: raw}, nil
}

// Look up a recorded fake commit receipt
func (m *memory) Receipt(_ context.Context, _ string, id string) (sdk.StorageReceipt, error) {
	if receipt, ok := m.receipts[id]; ok {
		return receipt, nil
	}
	return sdk.StorageReceipt{StorageVersion: m.version(), OperationID: id}, nil
}

// Apply version-checked mutations in the fake storage backend
func (m *memory) Commit(_ context.Context, version sdk.StorageVersion, id string, mutations []sdk.StorageMutation) (sdk.StorageReceipt, error) {
	m.commits = append(m.commits, recorded{version, id, append([]sdk.StorageMutation{}, mutations...)})
	if m.conflict || version != m.version() {
		return sdk.StorageReceipt{}, &sdk.RPCError{Code: "conflict", Message: "changed"}
	}
	if m.fail && m.recovery == "not-applied" {
		m.fail = false
		return sdk.StorageReceipt{}, &sdk.RPCError{Code: "indeterminate", Message: "lost reply"}
	}
	for _, mutation := range mutations {
		if mutation.Op == "delete" {
			delete(m.records, mutation.Key)
		} else {
			m.records[mutation.Key] = mutation.Value.(stateRecord)
		}
	}
	m.revision++
	receipt := sdk.StorageReceipt{StorageVersion: m.version(), OperationID: id, Found: true, Committed: true}
	m.receipts[id] = receipt
	if m.fail {
		m.fail = false
		return sdk.StorageReceipt{}, &sdk.RPCError{Code: "indeterminate", Message: "lost reply"}
	}
	return receipt, nil
}

// Load shared managed-state snapshots and receipt-recovery cases
func fixture(t *testing.T) struct {
	Initial, Next, Newer map[string]json.RawMessage
	Recoveries           []string
} {
	t.Helper()
	raw, err := os.ReadFile("../testdata/extensions-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var all struct {
		State struct {
			Initial, Next, Newer map[string]json.RawMessage
			Recoveries           []string
		} `json:"managed_state"`
	}
	if err = json.Unmarshal(raw, &all); err != nil {
		t.Fatal(err)
	}
	all.State.Initial, _ = clone(all.State.Initial)
	all.State.Next, _ = clone(all.State.Next)
	all.State.Newer, _ = clone(all.State.Newer)
	return all.State
}

// Verify shared managed state recovery
func TestSharedManagedStateRecovery(t *testing.T) {
	spec := fixture(t)
	ctx := context.Background()
	for _, recovery := range spec.Recoveries {
		backend := &memory{recovery: recovery, fail: true, records: map[string]stateRecord{}, receipts: map[string]sdk.StorageReceipt{}}
		for identity, value := range spec.Initial {
			backend.records[stateKey(identity)] = stateRecord{identity, value}
		}
		state, _ := New("private", "", nil, backend)
		got, err := state.Load(ctx)
		if err != nil || !reflect.DeepEqual(got, spec.Initial) {
			t.Fatal(got, err)
		}
		if err = state.Save(ctx, spec.Next); err == nil || !state.Dirty || state.CheckPending() == nil {
			t.Fatal("lost uncertain state", err)
		}
		if err = state.Save(ctx, spec.Newer); err != nil {
			t.Fatal(err)
		}
		if recovery == "not-applied" {
			if !state.Dirty || !reflect.DeepEqual(backend.commits[0], backend.commits[1]) {
				t.Fatal("replayed a changed operation")
			}
			if err = state.Save(ctx, spec.Newer); err != nil {
				t.Fatal(err)
			}
		}
		if state.Dirty || state.CheckPending() != nil || state.Assurance != "committed" {
			t.Fatal("recovery did not finish")
		}
		if !reflect.DeepEqual(backend.records[stateKey("count")].Value, spec.Newer["count"]) {
			t.Fatal("newer value lost")
		}
		if _, exists := backend.records[stateKey("removed")]; exists {
			t.Fatal("deleted record restored")
		}
		count := len(backend.commits)
		if err = state.Save(ctx, spec.Newer); err != nil || len(backend.commits) != count {
			t.Fatal("unchanged state was written", err)
		}
		large := map[string]any{}
		for i := range 33 {
			large[fmt.Sprint(i)] = i
		}
		if err = state.Save(ctx, large); err == nil || len(backend.commits) != count {
			t.Fatal("exceeded commit limit")
		}
	}
}

type portableMemory struct {
	settings map[string]any
	updates  int
}

// Return the portable-settings fixture value with observed assurance
func (p *portableMemory) Read(context.Context) (sdk.SettingsResult, error) {
	return sdk.SettingsResult{Settings: p.settings, Assurance: "observed"}, nil
}

// Merge changes into the portable-settings test backend
func (p *portableMemory) Update(_ context.Context, changes map[string]any) (sdk.SettingsResult, error) {
	p.updates++
	maps.Copy(p.settings, changes)
	return sdk.SettingsResult{Assurance: "sent"}, nil
}

// Verify portable session conflict and restore bounds
func TestPortableSessionConflictAndRestoreBounds(t *testing.T) {
	spec := fixture(t)
	ctx := context.Background()
	backend := &portableMemory{settings: map[string]any{"unknown": true}}
	state, _ := New("portable", "saved", backend, nil)
	state.Load(ctx)
	if err := state.Save(ctx, spec.Next); err != nil {
		t.Fatal(err)
	}
	state.Save(ctx, spec.Next)
	if backend.updates != 1 || backend.settings["unknown"] != true || state.Assurance != "sent" {
		t.Fatal("portable state changed unrelated settings or overstated assurance")
	}
	state, _ = New("session", "", nil, nil)
	state.Load(ctx)
	state.Save(ctx, spec.Next)
	got, _ := state.Load(ctx)
	if !reflect.DeepEqual(got, spec.Next) {
		t.Fatal("session state lost")
	}
	private := &memory{conflict: true, records: map[string]stateRecord{}}
	state, _ = New("private", "", nil, private)
	state.Load(ctx)
	if state.Save(ctx, spec.Next) == nil || state.Save(ctx, spec.Newer) == nil || len(private.commits) != 1 {
		t.Fatal("conflicting write retried")
	}
	private = &memory{cycle: true}
	state, _ = New("private", "", nil, private)
	if _, err := state.Load(ctx); err == nil {
		t.Fatal("cursor cycle accepted")
	}
}
