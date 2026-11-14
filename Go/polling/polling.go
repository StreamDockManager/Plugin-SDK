package polling

import (
	"fmt"
	"github.com/StreamDockManager/Plugin-SDK/Go/settings"
	"sort"
	"time"
)

// PollState stores a resource's last good sample independently of its subscribers
type PollState struct {
	Next       time.Time
	Failures   int
	LastValue  any
	Stale      bool
	ObservedAt time.Time
	Available  bool
}

// Store a polling outcome and compute its next attempt
func (p *PollState) Complete(value any, failed bool, now time.Time, interval time.Duration, jitter float64, retryAt time.Time) {
	if failed {
		p.Failures = min(16, p.Failures+1)
		p.Stale = p.Available || !p.ObservedAt.IsZero()
		p.Next = now.Add(RetryDelay(interval, p.Failures, jitter))
	} else {
		p.Failures = 0
		p.Stale = false
		p.LastValue = settings.Clone(value)
		p.ObservedAt = now
		p.Available = true
		p.Next = Next(now, interval)
	}
	if retryAt.After(p.Next) {
		p.Next = retryAt
	}
}

type subscription struct {
	key      string
	interval time.Duration
	state    PollState
}
type Resources struct {
	bindings  map[string]subscription
	States    map[string]PollState
	pending   map[string]uint64
	cooldowns map[string]time.Time
	sequence  uint64
}

// Create the shared polling-resource registry
func NewResources() *Resources {
	return &Resources{bindings: map[string]subscription{}, States: map[string]PollState{}, pending: map[string]uint64{}, cooldowns: map[string]time.Time{}}
}

// Attach a binding watcher to a shared resource
func (r *Resources) Watch(context, key string, interval time.Duration) error {
	if key == "" || interval < 0 {
		return fmt.Errorf("resource needs key and nonnegative interval")
	}
	if prior, ok := r.bindings[context]; ok && prior.key != key {
		r.Unwatch(context)
	}
	prior, exists := r.bindings[context]
	changed := !exists || prior.interval != interval
	if !exists {
		prior = subscription{key: key, interval: interval, state: r.States[key]}
	}
	if (!exists || prior.interval != interval) && !prior.state.ObservedAt.IsZero() {
		prior.state.Next = Next(prior.state.ObservedAt, interval)
	}
	prior.interval = interval
	r.bindings[context] = prior
	if _, ok := r.States[key]; !ok {
		r.States[key] = PollState{}
	}
	if changed && !r.States[key].Next.IsZero() {
		r.schedule(key)
	}
	return nil
}

// Remove a binding watcher and release unused resources
func (r *Resources) Unwatch(context string) {
	prior, ok := r.bindings[context]
	delete(r.bindings, context)
	if !ok {
		return
	}
	for _, binding := range r.bindings {
		if binding.key == prior.key {
			if !r.States[prior.key].Next.IsZero() {
				r.schedule(prior.key)
			}
			return
		}
	}
	delete(r.States, prior.key)
	delete(r.pending, prior.key)
	delete(r.cooldowns, prior.key)
}

// List resources whose next polls are due
func (r *Resources) Due(now time.Time) []string {
	out := []string{}
	for key, state := range r.States {
		if _, pending := r.pending[key]; !pending && !now.Before(state.Next) && !now.Before(r.cooldowns[key]) {
			out = append(out, key)
		}
	}
	sort.Strings(out)
	return out
}

// Deadline includes a service cooldown even after an explicit invalidation
func (r *Resources) Deadline(key string) time.Time {
	state, ok := r.States[key]
	if !ok {
		return Never
	}
	if r.cooldowns[key].After(state.Next) {
		return r.cooldowns[key]
	}
	return state.Next
}

// Start a resource poll with a new generation
func (r *Resources) Begin(key string) (uint64, error) {
	if _, ok := r.States[key]; !ok {
		return 0, fmt.Errorf("resource absent")
	}
	if _, ok := r.pending[key]; ok {
		return 0, fmt.Errorf("resource already running")
	}
	if r.sequence == ^uint64(0) {
		return 0, fmt.Errorf("resource generation exhausted")
	}
	r.sequence++
	r.pending[key] = r.sequence
	return r.pending[key], nil
}

// Accept a current poll result and ignore obsolete generations
func (r *Resources) Complete(key string, generation uint64, value any, failed bool, now time.Time, jitter float64, retryAt time.Time) bool {
	state, exists := r.States[key]
	if !exists || r.pending[key] != generation {
		return false
	}
	delete(r.pending, key)
	if retryAt.After(now) {
		r.cooldowns[key] = retryAt
	} else {
		delete(r.cooldowns, key)
	}
	forced := state.Next.IsZero()
	for id, binding := range r.bindings {
		if binding.key != key || (!forced && !binding.state.Next.IsZero() && now.Before(binding.state.Next)) {
			continue
		}
		binding.state.Complete(value, failed, now, binding.interval, jitter, retryAt)
		if binding.interval == 0 {
			binding.state.Next = Never
		}
		r.bindings[id] = binding
	}
	state.Complete(value, failed, now, 0, jitter, retryAt)
	r.States[key] = state
	r.schedule(key)
	return true
}

// Read the shared poll state watched by a binding
func (r *Resources) Snapshot(context string) (PollState, bool) {
	subscription, ok := r.bindings[context]
	if !ok {
		return PollState{}, false
	}
	state := subscription.state
	state.LastValue = settings.Clone(state.LastValue)
	return state, true
}

// Recompute a resource schedule from its watchers
func (r *Resources) schedule(key string) {
	state, ok := r.States[key]
	if !ok {
		return
	}
	next := Never
	for _, binding := range r.bindings {
		if binding.key == key && binding.state.Next.Before(next) {
			next = binding.state.Next
		}
	}
	state.Next = next
	if r.cooldowns[key].After(state.Next) {
		state.Next = r.cooldowns[key]
	}
	r.States[key] = state
}

// Calculate bounded retry backoff with jitter
func RetryDelay(interval time.Duration, failures int, jitter float64) time.Duration {
	shift := min(max(failures-1, 0), 10)
	backoff := min(15*time.Minute, 2*time.Second*time.Duration(1<<shift))
	jitter = min(1, max(0, jitter))
	backoff = min(15*time.Minute, backoff+time.Duration(float64(backoff)*0.25*jitter))
	return max(interval, backoff)
}
