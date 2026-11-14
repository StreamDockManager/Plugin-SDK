package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type countedValue struct{ calls *int }

// Count serialization attempts while encoding the test value
func (value countedValue) MarshalJSON() ([]byte, error) {
	*value.calls++
	return []byte(`{"opaque":{"keep":["unknown",3]},"count":2}`), nil
}

// Verify changed state serializes once and retries exact bytes
func TestChangedStateSerializesOnceAndRetriesExactBytes(t *testing.T) {
	for repetition := range 5 {
		path := filepath.Join(t.TempDir(), "state.json")
		calls := 0
		value := countedValue{&calls}
		store := New(path, 1)
		store.Change(value, time.Time{})
		if err := store.Close(); err != nil {
			t.Fatal(err)
		}
		if calls != 1 {
			t.Fatalf("serialized %d times", calls)
		}
		want := []byte(`{"data":{"opaque":{"keep":["unknown",3]},"count":2},"version":1}`)
		actual, _ := os.ReadFile(path)
		if string(actual) != string(want) {
			t.Fatal(string(actual))
		}
		before := actual
		if err := os.Rename(path, path+".backup"); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
		store.Change(map[string]any{"changed": true}, time.Time{})
		if err := store.Close(); err == nil || !store.Dirty {
			t.Fatal("failed write lost dirty state", err)
		}
		actual, _ = os.ReadFile(path + ".backup")
		if string(actual) != string(before) {
			t.Fatal("durable state changed")
		}
		os.Remove(path)
		os.Rename(path+".backup", path)
		if err := store.Close(); err != nil || store.Dirty {
			t.Fatal(err)
		}
		t.Logf("STATE_SERIALIZATION repetition=%d passes=%d", repetition, calls)
	}
}

// Verify shared state write fixtures
func TestSharedStateWriteFixtures(t *testing.T) {
	raw, err := os.ReadFile("../testdata/authoring-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Writes struct {
			Cases []struct {
				Name    string
				Version int
				Value   any
			}
		} `json:"state_writes"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Writes.Cases) == 0 {
		t.Fatal("missing state writes fixture")
	}
	for _, item := range fixture.Writes.Cases {
		t.Run(item.Name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			store := New(path, item.Version)
			store.Change(item.Value, time.Time{})
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			var expected any = item.Value
			if item.Version > 0 {
				expected = map[string]any{"version": item.Version, "data": item.Value}
			}
			encoded, _ := json.Marshal(expected)
			actual, _ := os.ReadFile(path)
			if string(encoded) != string(actual) {
				t.Fatal("format changed")
			}
			var loaded any
			if err := New(path, item.Version).Load(&loaded); err != nil {
				t.Fatal(err)
			}
			encoded, _ = json.Marshal(item.Value)
			actual, _ = json.Marshal(loaded)
			if string(encoded) != string(actual) {
				t.Fatal("unknown values changed")
			}
		})
	}
}
