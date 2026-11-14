// Package operations tracks external outcomes without executing or retrying work
// Serialize calls on the owner loop or with a caller-held mutex. Values are immutable
// snapshots; cost is a caller-supplied logical retained-byte bound
package operations

import (
	"fmt"
	"sort"
	"unicode/utf8"
)

type State string

const (
	Pending   State = "pending"
	Confirmed State = "confirmed"
	Failed    State = "failed"
	Uncertain State = "uncertain"
)

type Record[T any] struct {
	ID, Group string
	Sequence  uint64
	State     State
	Pinned    bool
	Value     T
	Cost      int
}
type Ledger[T any] struct {
	records                map[string]Record[T]
	capacity, limit, bytes int
	sequence               uint64
}

// Create a count- and byte-bounded operation ledger
func New[T any](capacity, bytes int) (*Ledger[T], error) {
	if capacity < 1 || capacity > 65536 || bytes < 1 || bytes > 64<<20 {
		return nil, fmt.Errorf("invalid operation history limits")
	}
	return &Ledger[T]{records: map[string]Record[T]{}, capacity: capacity, limit: bytes}, nil
}

// Recognize supported operation outcome states
func valid(s State) bool { return s == Pending || s == Confirmed || s == Failed || s == Uncertain }

// Identify outcomes safe to retire automatically
func terminal(s State) bool { return s == Confirmed || s == Failed }

// Charge operation values together with identifiers and overhead
func charge[T any](r Record[T]) int { return len(r.ID) + len(r.Group) + r.Cost + 64 }

// Return the number of retained operation records
func (l *Ledger[T]) Len() int {
	if l == nil {
		return 0
	}
	return len(l.records)
}

// Return the logical bytes retained by the ledger
func (l *Ledger[T]) Bytes() int {
	if l == nil {
		return 0
	}
	return l.bytes
}

// Read a record by its operation ID
func (l *Ledger[T]) Get(id string) (Record[T], bool) {
	if l == nil {
		var zero Record[T]
		return zero, false
	}
	r, ok := l.records[id]
	return r, ok
}

// List retained records for an operation group
func (l *Ledger[T]) Records(group string) []Record[T] {
	out := []Record[T]{}
	if l == nil {
		return out
	}
	for _, r := range l.records {
		if group == "" || r.Group == group {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Sequence < out[j].Sequence })
	return out
}

// Reserve succeeds before the caller submits a new external operation. It never
// evicts history automatically, including when all retained records are terminal
func (l *Ledger[T]) Reserve(group string, value T, cost int) (Record[T], error) {
	if group == "" || len(group) > 4096 || !utf8.ValidString(group) || cost < 0 || cost > l.limit || l.Len() >= l.capacity || l.sequence >= 9007199254740991 {
		return Record[T]{}, fmt.Errorf("invalid operation or history full")
	}
	sequence := l.sequence + 1
	id := group
	if _, exists := l.records[id]; exists {
		for {
			id = fmt.Sprintf("%s#%d", group, sequence)
			if _, exists = l.records[id]; !exists {
				break
			}
			sequence++
			if sequence > 9007199254740991 {
				return Record[T]{}, fmt.Errorf("operation sequence exhausted")
			}
		}
	}
	record := Record[T]{ID: id, Group: group, Sequence: sequence, State: Pending, Value: value, Cost: cost}
	if charge(record) > l.limit-l.bytes {
		return Record[T]{}, fmt.Errorf("operation history byte budget exceeded")
	}
	l.sequence = sequence
	l.records[id] = record
	l.bytes += charge(record)
	return record, nil
}

// Update an operation outcome without exceeding its byte budget
func (l *Ledger[T]) Update(id string, state State, value T, cost int, pinned bool) error {
	prior, ok := l.records[id]
	if !ok || !valid(state) || cost < 0 || cost > l.limit || cost-prior.Cost > l.limit-l.bytes {
		return fmt.Errorf("invalid operation update or history full")
	}
	next := prior
	next.Value = value
	next.Cost = cost
	next.State = state
	next.Pinned = pinned
	l.records[id] = next
	l.bytes += cost - prior.Cost
	return nil
}

// Retire forgets only terminal, unpinned records from inactive groups. Pending and
// uncertain outcomes remain until the caller explicitly reconciles their state
func (l *Ledger[T]) Retire(active map[string]bool) []string {
	out := []string{}
	for _, r := range l.Records("") {
		if terminal(r.State) && !r.Pinned && !active[r.Group] {
			delete(l.records, r.ID)
			l.bytes -= charge(r)
			out = append(out, r.ID)
		}
	}
	return out
}
