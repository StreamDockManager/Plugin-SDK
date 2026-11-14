package cache

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// Verify shared cache eviction expiry and admission
func TestSharedCacheEvictionExpiryAndAdmission(t *testing.T) {
	var f struct {
		Cache struct {
			Capacity, Bytes, Cost int
			Keys                  []string
			Expiry                int64
		}
	}
	raw, _ := os.ReadFile("../testdata/helpers-v1.json")
	if err := json.Unmarshal(raw, &f); err != nil {
		t.Fatal(err)
	}
	spec := f.Cache
	c, _ := New[string](spec.Capacity, spec.Bytes)
	a, b, d := spec.Keys[0], spec.Keys[1], spec.Keys[2]
	c.Put(a, "A", spec.Cost, 0, 0)
	c.Put(b, "B", spec.Cost, 0, 0)
	c.Get(a, 1)
	evicted, err := c.Put(d, "C", spec.Cost, spec.Expiry, 2)
	if err != nil || !reflect.DeepEqual(evicted, []string{b}) {
		t.Fatal(evicted, err)
	}
	if _, err = c.Put(a, "bad", 1000, 0, 3); err == nil {
		t.Fatal("oversized entry admitted")
	}
	if value, _ := c.Peek(a, 3); value != "A" {
		t.Fatal("failed admission mutated cache")
	}
	if _, ok := c.Get(d, spec.Expiry); ok {
		t.Fatal("expired entry retained")
	}
	if c.Len() != 1 || c.Bytes() != 75 {
		t.Fatal(c.Len(), c.Bytes())
	}
	evicted, err = c.Trim(spec.Bytes, 31)
	if err != nil || !reflect.DeepEqual(evicted, []string{a}) || c.Bytes() != 0 {
		t.Fatal(evicted, err)
	}
	budget, _ := NewBudget(100)
	if !budget.Reserve(80) || budget.Reserve(21) || budget.Used() != 80 {
		t.Fatal("budget")
	}
	if budget.Release(81) == nil || budget.Used() != 80 {
		t.Fatal("release underflow")
	}
	budget.Release(80)
}
