package storage

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Verify reject obsolete or malformed state without mutation
func TestRejectObsoleteOrMalformedStateWithoutMutation(t *testing.T) {
	for _, raw := range []string{`{"count":1}`, `{"version":0,"data":{"count":1}}`, `{"version":2,"data":{"count":1}}`, `{"version":1}`, `{`, `{"version":1,"data":{"Count":3,"Other":"invalid"}}`} {
		t.Run(raw, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "state.json")
			if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			state := struct{ Count, Other int }{9, 8}
			store := New(path, 1)
			if err := store.Load(&state); err == nil {
				t.Fatal("invalid state accepted")
			}
			if state.Count != 9 || state.Other != 8 {
				t.Fatal("partial mutation", state)
			}
			store.Change(&state, time.Time{})
			if err := store.Close(); err == nil {
				t.Fatal("rejected load allowed write")
			}
			actual, err := os.ReadFile(path)
			if err != nil || string(actual) != raw {
				t.Fatal("saved state changed", err)
			}
		})
	}
}

// Verify version boundaries preserve state
func TestVersionBoundariesPreserveState(t *testing.T) {
	for _, version := range []int{0, 2, -1, int(uint64(1) << 32)} {
		for _, exists := range []bool{false, true} {
			path := filepath.Join(t.TempDir(), "state.json")
			raw := `{"version":1,"data":{"count":7}}`
			if exists {
				if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
					t.Fatal(err)
				}
			}
			check := func() {
				t.Helper()
				data, err := os.ReadFile(path)
				if exists {
					if err != nil || string(data) != raw {
						t.Fatal("state changed", err)
					}
				} else if !os.IsNotExist(err) {
					t.Fatal("unexpected file", err)
				}
			}
			for _, dirty := range []bool{false, true} {
				for _, force := range []bool{false, true} {
					store := New(path, 1)
					store.Version = version
					if dirty {
						store.Change(map[string]int{"count": 8}, time.Now())
					}
					if err := store.Flush(time.Now(), force); err == nil {
						t.Fatalf("accepted %d dirty=%v force=%v", version, dirty, force)
					}
					if store.Dirty != dirty {
						t.Fatal("dirty state changed")
					}
					check()
					if err := store.Close(); err == nil {
						t.Fatal("close accepted")
					}
					check()
				}
			}
			store := New(path, version)
			value := map[string]int{"count": 99}
			if err := store.Load(&value); err == nil {
				t.Fatal("load accepted")
			}
			if value["count"] != 99 {
				t.Fatal("destination changed")
			}
			store.Version = 1
			store.Change(value, time.Now())
			if err := store.Close(); err == nil {
				t.Fatal("load protection lost")
			}
			check()
			if err := store.Load(&value); err != nil {
				t.Fatal("explicit reload failed", err)
			}
			store.Change(map[string]int{"count": 7}, time.Now())
			if err := store.Close(); err != nil {
				t.Fatal(err)
			}
			if err := New(path, 1).Load(&value); err != nil || value["count"] != 7 {
				t.Fatal(value, err)
			}
		}
	}
}
