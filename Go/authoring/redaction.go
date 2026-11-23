package authoring

import (
	"errors"
	"sync"

	"github.com/StreamDockManager/Plugin-SDK/Go/credentials"
)

type secretSnapshot []map[string]any

type jobRedaction struct {
	mu      sync.RWMutex
	current *secretSnapshot
	active  map[*secretSnapshot]int
}

func (r *jobRedaction) set(fields secretSnapshot) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.current = &fields
}

// Capture on the owner loop, then pin only while a worker or its callback runs.
func (r *jobRedaction) pin(fields *secretSnapshot) func() {
	if fields == nil {
		return func() {}
	}
	r.mu.Lock()
	if r.active == nil {
		r.active = map[*secretSnapshot]int{}
	}
	r.active[fields]++
	r.mu.Unlock()
	return func() {
		r.mu.Lock()
		defer r.mu.Unlock()
		r.active[fields]--
		if r.active[fields] == 0 {
			delete(r.active, fields)
		}
	}
}

func (r *jobRedaction) redact(message string) string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	apply := func(fields *secretSnapshot) {
		for _, settings := range *fields {
			message = credentials.SafeError(errors.New(message), settings).Error()
		}
	}
	apply(r.current)
	for fields := range r.active {
		if fields != r.current {
			apply(fields)
		}
	}
	return message
}
