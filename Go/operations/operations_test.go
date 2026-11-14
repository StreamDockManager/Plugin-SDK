package operations

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// Verify shared operation history preserves unresolved
func TestSharedOperationHistoryPreservesUnresolved(t *testing.T) {
	var f struct {
		Operations struct {
			Capacity, Bytes, Cost int
			Group                 string
			IDs                   []string
		}
	}
	raw, _ := os.ReadFile("../testdata/helpers-v1.json")
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	s := f.Operations
	l, _ := New[string](s.Capacity, s.Bytes)
	a, _ := l.Reserve(s.Group, "first", s.Cost)
	b, _ := l.Reserve(s.Group, "second", s.Cost)
	c, _ := l.Reserve("other", "third", s.Cost)
	if !reflect.DeepEqual([]string{a.ID, b.ID, c.ID}, s.IDs) {
		t.Fatal(a, b, c)
	}
	l.Update(b.ID, Uncertain, "unknown", s.Cost, false)
	if _, err := l.Reserve("fourth", "no", s.Cost); err == nil {
		t.Fatal("capacity admission")
	}
	if len(l.Retire(nil)) != 0 {
		t.Fatal("unresolved history retired")
	}
	l.Update(a.ID, Confirmed, "done", s.Cost, true)
	if len(l.Retire(nil)) != 0 {
		t.Fatal("pinned history retired")
	}
	l.Update(a.ID, Confirmed, "done", s.Cost, false)
	if len(l.Retire(map[string]bool{s.Group: true})) != 0 {
		t.Fatal("active group retired")
	}
	if !reflect.DeepEqual(l.Retire(nil), []string{a.ID}) {
		t.Fatal("terminal record did not retire")
	}
	before := l.Bytes()
	if l.Update(b.ID, Confirmed, "too large", s.Bytes, false) == nil || l.Bytes() != before {
		t.Fatal("update violated budget")
	}
	got, _ := l.Get(b.ID)
	if got.State != Uncertain {
		t.Fatal("unresolved state lost")
	}
	if _, err := l.Reserve("fourth", "ok", s.Cost); err != nil {
		t.Fatal(err)
	}
}
