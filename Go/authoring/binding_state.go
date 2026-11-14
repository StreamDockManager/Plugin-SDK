package authoring

// bindingState groups binding-local access and cleanup without duplicating state
// Keep the exported Engine maps as the backing storage: existing consumers can
// mutate or replace them directly. Physical per-binding records require a future
// explicit API migration; copying maps into records would silently lose writes
// Jobs, probe ownership and shared samples are deliberately not binding-local
// Like Engine's maps, this view is owned by the serialized event loop, not workers
type bindingState struct {
	engine *Engine
	id     string
}

// Access binding state through the live Engine maps
func (e *Engine) bindingState(id string) bindingState { return bindingState{e, id} }

// Cache the binding settings merged over global defaults
func (s bindingState) effectiveSettings(settings map[string]any) map[string]any {
	if value, ok := s.engine.Effective[s.id]; ok {
		return value
	}
	value := s.engine.MergedSettings(settings)
	s.engine.Effective[s.id] = value
	return value
}

// Read the last display sent for this binding
func (s bindingState) lastDisplay() (Display, bool) {
	value, ok := s.engine.Last[s.id]
	return value, ok
}

// Record the display used for subsequent diffing
func (s bindingState) setLastDisplay(value Display) { s.engine.Last[s.id] = value }

// Forget display history so the next render sends a full update
func (s bindingState) forgetDisplay() { delete(s.engine.Last, s.id) }

// Read the last collected status for this binding
func (s bindingState) lastStatus() (DisplayStatus, bool) {
	value, ok := s.engine.LastStatus[s.id]
	return value, ok
}

// Cache the latest collected binding status
func (s bindingState) setLastStatus(value DisplayStatus) { s.engine.LastStatus[s.id] = value }

// Invalidate settings and status without losing binding identity
func (s bindingState) invalidateSettings() {
	delete(s.engine.Effective, s.id)
	delete(s.engine.LastStatus, s.id)
}

// clearTransient runs only after feature cleanup and job/probe cancellation
// Retain binding identity, active-view membership and last display until their
// existing lifecycle phases remove them (including settings-change rendering)
func (s bindingState) clearTransient() {
	delete(s.engine.Results, s.id)
	delete(s.engine.ResultsText, s.id)
	delete(s.engine.Checked, s.id)
	delete(s.engine.DisplayTicks, s.id)
	delete(s.engine.Statuses, s.id)
	s.invalidateSettings()
}

// clearBindingStates is the close-time counterpart; callers close shared work
// first. Keep the public maps themselves alive for external references
func (e *Engine) clearBindingStates() {
	clear(e.Bindings)
	clear(e.Statuses)
	clear(e.Effective)
	clear(e.Results)
	clear(e.ResultsText)
	clear(e.Checked)
	clear(e.Last)
	clear(e.LastStatus)
	clear(e.DisplayTicks)
	clear(e.ViewActive)
}
