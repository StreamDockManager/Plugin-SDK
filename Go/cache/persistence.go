package cache

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/StreamDockManager/Plugin-SDK/Go/managedstate"
	"sort"
)

// Persistence explicitly uses a caller-selected private host namespace. No fallback
// Flush uses bounded atomic batches and managed-state receipt recovery
type Persistence[T any] struct {
	state   *managedstate.State
	ready   bool
	pending map[string]json.RawMessage
}
type stored[T any] struct {
	Value   T     `json:"value"`
	Cost    int   `json:"cost"`
	Expires int64 `json:"expires"`
}

// Bind cache persistence to an explicitly selected private backend
func NewPersistence[T any](backend managedstate.PrivateBackend) (*Persistence[T], error) {
	state, err := managedstate.New("private", "", nil, backend)
	if err != nil {
		return nil, err
	}
	return &Persistence[T]{state: state}, nil
}

// Restore validated cache entries without partially replacing the cache
func (p *Persistence[T]) Load(ctx context.Context, c *Cache[T], now int64) error {
	if p.ready {
		return fmt.Errorf("cache persistence already loaded")
	}
	if c.capacity > 4096 || c.limit > 8<<20 || !validTime(now) {
		return fmt.Errorf("invalid persistent cache limits")
	}
	records, err := p.state.Load(ctx)
	if err != nil {
		return err
	}
	temporary, _ := New[T](c.capacity, c.limit)
	keys := make([]string, 0, len(records))
	for key := range records {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		var fields map[string]json.RawMessage
		if json.Unmarshal(records[key], &fields) != nil || len(fields["value"]) == 0 || len(fields["cost"]) == 0 || len(fields["expires"]) == 0 || bytes.Equal(fields["cost"], []byte("null")) || bytes.Equal(fields["expires"], []byte("null")) {
			return fmt.Errorf("invalid persistent cache record")
		}
		var entry stored[T]
		if err = json.Unmarshal(records[key], &entry); err != nil {
			return err
		}
		encoded, err := json.Marshal(entry.Value)
		if err != nil || entry.Cost < len(encoded) {
			return fmt.Errorf("invalid persistent cache entry")
		}
		if _, err = temporary.Put(key, entry.Value, entry.Cost, entry.Expires, now); err != nil {
			return err
		}
	}
	*c = *temporary
	p.ready = true
	return nil
}

// Flush cache changes in bounded private-storage batches
func (p *Persistence[T]) Flush(ctx context.Context, c *Cache[T], now int64) error {
	if !p.ready {
		return fmt.Errorf("cache persistence is not loaded")
	}
	if !validTime(now) {
		return fmt.Errorf("invalid cache time")
	}
	desired := map[string]json.RawMessage{}
	entries, _ := c.Entries(now)
	for _, e := range entries {
		value, err := json.Marshal(e.Value)
		if err != nil || e.Cost < len(value) {
			return fmt.Errorf("invalid persistent cache cost")
		}
		raw, err := json.Marshal(stored[T]{e.Value, e.Cost, e.Expires})
		if err != nil {
			return err
		}
		record, err := json.Marshal(map[string]any{"identity": e.Key, "value": json.RawMessage(raw)})
		if err != nil || len(record) > 256<<10 {
			return fmt.Errorf("persistent cache record exceeds 256 KiB")
		}
		desired[e.Key] = raw
	}
	// Resolve an earlier uncertain batch before extending it with newer values
	if p.pending != nil {
		if err := p.state.Save(ctx, p.pending); err != nil {
			return err
		}
		p.pending = nil
	}
	for range 8193 {
		if err := ctx.Err(); err != nil {
			return err
		}
		written, err := p.state.Load(ctx)
		if err != nil {
			return err
		}
		keys := []string{}
		for key, value := range desired {
			if !bytes.Equal(value, written[key]) {
				keys = append(keys, key)
			}
		}
		for key := range written {
			if _, ok := desired[key]; !ok {
				keys = append(keys, key)
			}
		}
		sort.Slice(keys, func(i, j int) bool {
			_, a := desired[keys[i]]
			_, b := desired[keys[j]]
			if a != b {
				return !a
			}
			return keys[i] < keys[j]
		})
		if len(keys) == 0 && !p.state.Dirty {
			return nil
		}
		batchBytes := 0
		for _, key := range keys[:min(32, len(keys))] {
			size := 128
			if value, ok := desired[key]; ok {
				encoded, _ := json.Marshal(map[string]any{"identity": key, "value": value})
				if len(encoded) > 256<<10 {
					return fmt.Errorf("persistent cache record exceeds 256 KiB")
				}
				size = len(encoded) + 256
			}
			if batchBytes+size > 400<<10 {
				break
			}
			batchBytes += size
			if value, ok := desired[key]; ok {
				written[key] = value
			} else {
				delete(written, key)
			}
		}
		p.pending = written
		if err = p.state.Save(ctx, written); err != nil {
			return err
		}
		p.pending = nil
	}
	return fmt.Errorf("cache flush exceeded batch budget")
}
