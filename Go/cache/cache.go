// Package cache provides owner-serialized caches with deterministic LRU eviction
// Values are caller-owned immutable snapshots. Costs are logical retained bytes,
// not process RSS; keys and fixed entry overhead are charged automatically
package cache

import (
	"fmt"
	"sort"
	"sync"
	"unicode/utf8"
)

const MaxEntries = 65536
const MaxBytes = 64 << 20
const MaxTime int64 = 9007199254740991

type Entry[T any] struct {
	Key     string
	Value   T
	Cost    int
	Expires int64
	used    uint64
}
type Cache[T any] struct {
	entries                map[string]Entry[T]
	limit, capacity, bytes int
	sequence               uint64
}

// Create an entry-count and byte-bounded cache
func New[T any](capacity, bytes int) (*Cache[T], error) {
	if capacity < 1 || capacity > MaxEntries || bytes < 1 || bytes > MaxBytes {
		return nil, fmt.Errorf("invalid cache limits")
	}
	return &Cache[T]{entries: map[string]Entry[T]{}, capacity: capacity, limit: bytes}, nil
}

// Validate a nonempty bounded UTF-8 cache key
func validKey(key string) bool { return key != "" && len(key) <= 4096 && utf8.ValidString(key) }

// Check the supported nonnegative timestamp range
func validTime(t int64) bool { return t >= 0 && t <= MaxTime }

// Charge retained values together with key and entry overhead
func charge[T any](entry Entry[T]) int { return entry.Cost + len(entry.Key) + 64 }

// Return the number of retained entries
func (c *Cache[T]) Len() int { return len(c.entries) }

// Return the cache logical byte charge
func (c *Cache[T]) Bytes() int { return c.bytes }

// Remove an entry and release its charged bytes
func (c *Cache[T]) Delete(key string) bool {
	e, ok := c.entries[key]
	if ok {
		c.bytes -= charge(e)
		delete(c.entries, key)
	}
	return ok
}

// Discard all cache entries and reset byte accounting
func (c *Cache[T]) Clear() { clear(c.entries); c.bytes = 0 }

// Read an unexpired entry without changing recency
func (c *Cache[T]) Peek(key string, now int64) (T, bool) {
	var zero T
	if !validTime(now) {
		return zero, false
	}
	e, ok := c.entries[key]
	if ok && e.Expires != 0 && e.Expires <= now {
		c.Delete(key)
		ok = false
	}
	if !ok {
		return zero, false
	}
	return e.Value, true
}

// Read an unexpired entry and update its LRU position
func (c *Cache[T]) Get(key string, now int64) (T, bool) {
	v, ok := c.Peek(key, now)
	if ok {
		c.sequence++
		e := c.entries[key]
		e.used = c.sequence
		c.entries[key] = e
	}
	return v, ok
}

// Collect current entries after validating the supplied clock
func (c *Cache[T]) Entries(now int64) ([]Entry[T], error) {
	if !validTime(now) {
		return nil, fmt.Errorf("invalid cache time")
	}
	out := make([]Entry[T], 0, len(c.entries))
	for key, e := range c.entries {
		if e.Expires != 0 && e.Expires <= now {
			c.Delete(key)
		} else {
			out = append(out, e)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].used == out[j].used {
			return out[i].Key < out[j].Key
		}
		return out[i].used < out[j].used
	})
	return out, nil
}

// Trim reserves space for an additional owner-held snapshot (such as an index)
func (c *Cache[T]) Trim(reserved int, now int64) ([]string, error) {
	if reserved < 0 || reserved > c.limit || !validTime(now) {
		return nil, fmt.Errorf("invalid cache reservation")
	}
	out := []string{}
	for key, e := range c.entries {
		if e.Expires != 0 && e.Expires <= now {
			c.Delete(key)
			out = append(out, key)
		}
	}
	sort.Strings(out)
	entries, _ := c.Entries(now)
	for _, e := range entries {
		if c.bytes+reserved <= c.limit && len(c.entries) <= c.capacity {
			break
		}
		c.Delete(e.Key)
		out = append(out, e.Key)
	}
	return out, nil
}

// Insert a bounded entry and evict eligible older entries
func (c *Cache[T]) Put(key string, value T, cost int, expires, now int64) ([]string, error) {
	if !validKey(key) || cost < 0 || cost > c.limit || cost+len(key)+64 > c.limit || !validTime(expires) || !validTime(now) {
		return nil, fmt.Errorf("invalid or oversized cache entry")
	}
	c.Delete(key)
	evicted, _ := c.Trim(0, now)
	if expires != 0 && expires <= now {
		return evicted, nil
	}
	c.sequence++
	e := Entry[T]{Key: key, Value: value, Cost: cost, Expires: expires, used: c.sequence}
	c.entries[key] = e
	c.bytes += charge(e)
	more, _ := c.Trim(0, now)
	return append(evicted, more...), nil
}

// Budget bounds aggregate retained data shared by several snapshots
type Budget struct {
	mu          sync.Mutex
	limit, used int
}

// Create a shared logical-byte reservation budget
func NewBudget(limit int) (*Budget, error) {
	if limit < 1 || limit > MaxBytes {
		return nil, fmt.Errorf("invalid byte budget")
	}
	return &Budget{limit: limit}, nil
}

// Atomically reserve bytes when capacity permits
func (b *Budget) Reserve(size int) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if size < 0 || size > b.limit-b.used {
		return false
	}
	b.used += size
	return true
}

// Release a valid outstanding byte reservation
func (b *Budget) Release(size int) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if size < 0 || size > b.used {
		return fmt.Errorf("invalid budget release")
	}
	b.used -= size
	return nil
}

// Read the currently reserved logical bytes
func (b *Budget) Used() int { b.mu.Lock(); defer b.mu.Unlock(); return b.used }
