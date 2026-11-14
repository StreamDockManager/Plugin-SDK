package cache

import (
	"context"
	"encoding/json"
	"fmt"
	sdk "github.com/StreamDockManager/Plugin-SDK/Go"
	"strings"
	"testing"
)

type memory struct {
	records       map[string]json.RawMessage
	receipts      map[string]sdk.StorageReceipt
	commits       []string
	sizes         []int
	revision      int
	fail, applied bool
}

// Return the fake storage generation and revision
func (m *memory) version() sdk.StorageVersion {
	return sdk.StorageVersion{Generation: "1", Revision: fmt.Sprint(m.revision)}
}

// List keys from the in-memory storage test backend
func (m *memory) ListKeys(context.Context, string) (sdk.StorageKeys, error) {
	keys := []string{}
	for k := range m.records {
		keys = append(keys, k)
	}
	return sdk.StorageKeys{StorageVersion: m.version(), Keys: keys}, nil
}

// Read a value from the in-memory storage test backend
func (m *memory) Get(_ context.Context, key string) (sdk.StorageValue, error) {
	raw, ok := m.records[key]
	return sdk.StorageValue{StorageVersion: m.version(), Found: ok, Value: raw}, nil
}

// Look up a recorded fake commit receipt
func (m *memory) Receipt(_ context.Context, _ string, id string) (sdk.StorageReceipt, error) {
	return m.receipts[id], nil
}

// Apply version-checked mutations in the fake storage backend
func (m *memory) Commit(_ context.Context, v sdk.StorageVersion, id string, mutations []sdk.StorageMutation) (sdk.StorageReceipt, error) {
	raw, _ := json.Marshal(mutations)
	if len(raw) > 512<<10 {
		return sdk.StorageReceipt{}, fmt.Errorf("oversized RPC")
	}
	m.commits = append(m.commits, id)
	m.sizes = append(m.sizes, len(mutations))
	if v != m.version() {
		return sdk.StorageReceipt{}, &sdk.RPCError{Code: "conflict", Message: "changed"}
	}
	if m.fail && !m.applied {
		m.fail = false
		return sdk.StorageReceipt{}, &sdk.RPCError{Code: "indeterminate", Message: "lost reply"}
	}
	for _, x := range mutations {
		if x.Op == "delete" {
			delete(m.records, x.Key)
		} else {
			m.records[x.Key], _ = json.Marshal(x.Value)
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

// Verify shared cache persistence batches and receipt recovery
func TestSharedCachePersistenceBatchesAndReceiptRecovery(t *testing.T) {
	for _, applied := range []bool{false, true} {
		backend := &memory{records: map[string]json.RawMessage{}, receipts: map[string]sdk.StorageReceipt{}, fail: true, applied: applied}
		c, _ := New[int](100, 100000)
		store, _ := NewPersistence[int](backend)
		ctx := context.Background()
		if err := store.Load(ctx, c, 0); err != nil {
			t.Fatal(err)
		}
		for i := range 70 {
			c.Put(fmt.Sprint(i), i, 16, 0, 0)
		}
		if store.Flush(ctx, c, 0) == nil {
			t.Fatal("lost reply ignored")
		}
		c.Put("late", 99, 16, 0, 0)
		if err := store.Flush(ctx, c, 0); err != nil {
			t.Fatal(err)
		}
		if len(backend.records) != 71 {
			t.Fatal(len(backend.records))
		}
		for _, size := range backend.sizes {
			if size > 32 {
				t.Fatal("oversized batch", size)
			}
		}
		if !applied && backend.commits[0] != backend.commits[1] {
			t.Fatal("recovery changed operation ID")
		}
		restored, _ := New[int](100, 100000)
		other, _ := NewPersistence[int](backend)
		if err := other.Load(ctx, restored, 0); err != nil {
			t.Fatal(err)
		}
		if value, _ := restored.Get("late", 0); value != 99 {
			t.Fatal(value)
		}
		c.Put("late", 100, 16, 0, 0)
		backend.revision++
		if store.Flush(ctx, c, 0) == nil {
			t.Fatal("conflict ignored")
		}
		attempts := len(backend.commits)
		if store.Flush(ctx, c, 0) == nil || len(backend.commits) != attempts {
			t.Fatal("conflict retried")
		}
	}
}

// Verify persistent cache batches by wire bytes
func TestPersistentCacheBatchesByWireBytes(t *testing.T) {
	backend := &memory{records: map[string]json.RawMessage{}, receipts: map[string]sdk.StorageReceipt{}}
	c, _ := New[string](10, 1<<20)
	store, _ := NewPersistence[string](backend)
	ctx := context.Background()
	if err := store.Load(ctx, c, 0); err != nil {
		t.Fatal(err)
	}
	for i := range 3 {
		c.Put(fmt.Sprint(i), strings.Repeat("x", 210000), 220000, 0, 0)
	}
	if err := store.Flush(ctx, c, 0); err != nil {
		t.Fatal(err)
	}
	if len(backend.commits) != 3 {
		t.Fatal("large entries were not split", backend.sizes)
	}
}
