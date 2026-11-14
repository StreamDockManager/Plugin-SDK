package authoring_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	sdk "github.com/StreamDockManager/Plugin-SDK/Go"
	"github.com/StreamDockManager/Plugin-SDK/Go/authoring"
	"github.com/StreamDockManager/Plugin-SDK/Go/storage"
)

// Hash saved state to detect unintended file replacement
func stateDigest(t *testing.T, path string) string {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		t.Fatal(err)
	}
	return string(hash.Sum(nil))
}

// Verify oversized legacy restore cannot overwrite durable state
func TestOversizedLegacyRestoreCannotOverwriteDurableState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	saved := []byte(`{"version":1,"data":{"count":99,"opaque":{"keep":true}}}`)
	if _, err := file.Write(saved); err != nil {
		t.Fatal(err)
	}
	padding := bytes.Repeat([]byte{' '}, 64<<10)
	for left := storage.MaximumBytes + 1 - len(saved); left > 0; {
		n := min(left, len(padding))
		if _, err := file.Write(padding[:n]); err != nil {
			t.Fatal(err)
		}
		left -= n
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	digest := stateDigest(t, path)
	state := map[string]any{"count": 0}
	engine := authoring.New(authoring.Definition{}, path, func(*authoring.Engine) authoring.Feature {
		return authoring.Feature{Snapshot: func() any { return state }}
	})
	if err := engine.Restore(&state); err == nil {
		t.Error("legacy read failure was not inspectable")
	}
	if err := engine.RoutinePersist(); err == nil {
		t.Error("routine write did not surface protected state")
	}
	if err := engine.Persist(); err == nil {
		t.Error("oversized restore allowed persistence of defaults")
	}
	if info, err := os.Stat(path); err != nil || info.Size() != storage.MaximumBytes+1 {
		t.Fatal("normal persistence replaced the protected durable state", err)
	}
	if err := engine.Poll(context.Background(), nil); err == nil {
		t.Error("poll did not surface protected state")
	}
	closing := authoring.New(authoring.Definition{}, path, func(*authoring.Engine) authoring.Feature {
		return authoring.Feature{Snapshot: func() any { return state }}
	})
	closing.Restore(&state)
	_ = closing.RoutinePersist()
	if err := closing.Close(); err == nil {
		t.Error("close did not surface protected state")
	}
	if stateDigest(t, path) != digest {
		t.Fatal("rejected restore changed saved bytes")
	}
	// A protected versioned Store must not prevent the containing engine's
	// resource teardown, even though close returns the persistence error
	versioned := storage.New(path, 1)
	if err := versioned.Load(&state); err == nil {
		t.Fatal("oversized versioned state accepted")
	}
	closed := false
	owner := authoring.New(authoring.Definition{}, path, func(*authoring.Engine) authoring.Feature {
		return authoring.Feature{Close: func() { closed = true }}
	})
	owner.Store = versioned
	if err := owner.Close(); err == nil || !closed {
		t.Fatal("protected close skipped resource teardown", err)
	}
	if !engine.DirtyState {
		t.Fatal("failed persistence discarded the pending change")
	}
	// A subsequent failed read cannot accidentally clear the protection
	if err := os.WriteFile(path, []byte("invalid"), 0600); err != nil {
		t.Fatal(err)
	}
	engine.Restore(&state)
	if err := engine.Persist(); err == nil {
		t.Fatal("invalid reload cleared protection")
	}
	// Repairing the saved file and explicitly restoring it enables normal writes
	if err := os.WriteFile(path, saved, 0600); err != nil {
		t.Fatal(err)
	}
	if err := engine.Restore(&state); err != nil {
		t.Fatal(err)
	}
	state["count"] = 100
	if err := engine.RoutinePersist(); err != nil {
		t.Fatal(err)
	}
	if err := engine.Persist(); err != nil {
		t.Fatal(err)
	}
	if err := engine.Close(); err != nil {
		t.Fatal(err)
	}
	var restored map[string]any
	if err := storage.New(path, 1).Load(&restored); err != nil || restored["count"] != float64(100) || !reflect.DeepEqual(restored["opaque"], map[string]any{"keep": true}) {
		t.Fatalf("recovery lost saved state: %v, %v", restored, err)
	}
}

// Verify shared state read limits
func TestSharedStateReadLimits(t *testing.T) {
	var fixtureCase struct {
		MaximumBytes int `json:"maximum_bytes"`
		Payload      map[string]any
		Cases        []struct {
			Name       string
			Version    int
			ExtraBytes int `json:"extra_bytes"`
			Valid      bool
		}
	}
	if err := json.Unmarshal(fixture(t)["state_reads"], &fixtureCase); err != nil {
		t.Fatal(err)
	}
	if storage.MaximumBytes != fixtureCase.MaximumBytes {
		t.Fatal("state limit differs from contract")
	}
	for _, test := range fixtureCase.Cases {
		t.Run(test.Name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			var input any = fixtureCase.Payload
			if test.Version != 0 {
				input = map[string]any{"version": test.Version, "data": input}
			}
			data, err := json.Marshal(input)
			if err != nil {
				t.Fatal(err)
			}
			file, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			if _, err = file.Write(data); err != nil {
				t.Fatal(err)
			}
			padding := bytes.Repeat([]byte{' '}, 64<<10)
			for remaining := fixtureCase.MaximumBytes + test.ExtraBytes - len(data); remaining > 0; {
				n := min(len(padding), remaining)
				if _, err = file.Write(padding[:n]); err != nil {
					t.Fatal(err)
				}
				remaining -= n
			}
			if err = file.Close(); err != nil {
				t.Fatal(err)
			}
			value := map[string]any{"previous": true}
			if test.Valid {
				value = nil
			}
			store := storage.New(path, test.Version)
			store.Value = value
			err = store.Load(&value)
			if test.Valid {
				if err != nil || !reflect.DeepEqual(value, fixtureCase.Payload) {
					t.Fatalf("%v: %v", value, err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "exceeds") || !reflect.DeepEqual(value, map[string]any{"previous": true}) || store.Dirty {
				t.Fatalf("oversized load changed state: %v, %v, dirty %v", value, err, store.Dirty)
			}
			if test.Version == 0 {
				engine := authoring.New(authoring.Definition{}, path, nil)
				engine.Written = []byte("previous")
				value = map[string]any{"previous": true}
				engine.Restore(&value)
				if test.Valid {
					// Restore replaces caller state only after complete decoding
					if !reflect.DeepEqual(value["opaque"], fixtureCase.Payload["opaque"]) {
						t.Fatal(value)
					}
				} else if string(engine.Written) != "previous" || !reflect.DeepEqual(value, map[string]any{"previous": true}) {
					t.Fatal("legacy oversized restore changed state")
				}
			}
			info, err := os.Stat(path)
			if err != nil || info.Size() != int64(fixtureCase.MaximumBytes+test.ExtraBytes) {
				t.Fatal("load modified file", err)
			}
			if !test.Valid {
				digest := stateDigest(t, path)
				store.Change(map[string]any{"replacement": true}, time.Now())
				if err := store.Close(); err == nil || !strings.Contains(err.Error(), "protected") || !store.Dirty {
					t.Fatal("failed load allowed normal persistence", err)
				}
				if stateDigest(t, path) != digest {
					t.Fatal("failed load destroyed saved data")
				}
				if test.Name == "large_file" {
					if err := os.Rename(path, path+".saved"); err != nil {
						t.Fatal(err)
					}
				} else if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
				value = nil
				if err := store.Load(&value); err != nil {
					t.Fatal(err)
				}
				if test.Name != "large_file" && !reflect.DeepEqual(value, fixtureCase.Payload) {
					t.Fatal("repaired reload lost unknown keys", value)
				}
				store.Change(fixtureCase.Payload, time.Now())
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
				value = nil
				if err := storage.New(path, test.Version).Load(&value); err != nil || !reflect.DeepEqual(value, fixtureCase.Payload) {
					t.Fatal("recovered write changed state", err, value)
				}
				if test.Name == "large_file" && stateDigest(t, path+".saved") != digest {
					t.Fatal("recovery changed backup")
				}
			}
		})
	}
}

// Verify restore rejects malformed state without mutation
func TestRestoreRejectsMalformedStateWithoutMutation(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	saved := []byte(`{"Count":7,"Other":"invalid"}`)
	if err := os.WriteFile(path, saved, 0600); err != nil {
		t.Fatal(err)
	}
	state := struct{ Count, Other int }{Count: 99, Other: 2}
	engine := authoring.New(authoring.Definition{}, path, func(*authoring.Engine) authoring.Feature {
		return authoring.Feature{Snapshot: func() any { return state }}
	})
	if err := engine.Restore(&state); err == nil {
		t.Fatal("invalid state accepted")
	}
	if state.Count != 99 || state.Other != 2 {
		t.Fatal("partial mutation", state)
	}
	if err := engine.Handle(context.Background(), nil, sdk.Event{Event: "keyUp"}); err == nil {
		t.Fatal("rejected state allowed actions")
	}
	if err := engine.Persist(); err == nil {
		t.Fatal("rejected state overwritten")
	}
	actual, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(actual, saved) {
		t.Fatal("saved state changed", err)
	}
}
