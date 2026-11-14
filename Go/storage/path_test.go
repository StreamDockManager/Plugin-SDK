package storage

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Verify shared missing state paths
func TestSharedMissingStatePaths(t *testing.T) {
	data, err := os.ReadFile("../testdata/authoring-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Reads struct {
			Paths []struct {
				Name, Path string
				Error      bool
			} `json:"missing_paths"`
		} `json:"state_reads"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Reads.Paths) == 0 {
		t.Fatal("missing path fixture")
	}
	for _, item := range fixture.Reads.Paths {
		t.Run(item.Name, func(t *testing.T) {
			root := t.TempDir()
			blocker := filepath.Join(root, "blocked")
			if err := os.WriteFile(blocker, []byte("keep"), 0600); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(root, filepath.FromSlash(item.Path))
			store := New(path, 1)
			var value map[string]any
			err := store.Load(&value)
			if (err != nil) != item.Error {
				t.Fatalf("load=%v expected error=%v", err, item.Error)
			}
			if !item.Error {
				store.Change(map[string]any{"saved": true}, time.Now())
				if err := store.Close(); err != nil {
					t.Fatal(err)
				}
			}
			if content, err := os.ReadFile(blocker); err != nil || string(content) != "keep" {
				t.Fatalf("blocking file changed: %v", err)
			}
		})
	}
}

// Verify blocked path does not clear prior load protection
func TestBlockedPathDoesNotClearPriorLoadProtection(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	store := New(filepath.Join(blocker, "state.json"), 1)
	store.loadBlocked = true
	if err := store.Load(new(any)); err == nil {
		t.Fatal("blocked path accepted")
	}
	store.Change(map[string]any{"pending": true}, time.Now())
	if err := store.Close(); err == nil || !store.Dirty || !store.loadBlocked {
		t.Fatal("read failure cleared protection", err)
	}
}
