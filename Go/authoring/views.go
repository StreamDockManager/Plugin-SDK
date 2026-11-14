package authoring

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/StreamDockManager/Plugin-SDK/Go"
)

// ViewTarget declares which actions may refresh a view. Command feedback uses
// exact binding contexts; Shared requires an explicit matching shared_id
// Live views observe their own state. AfterActivation reserves live progress for
// a control that has actually been used, rather than every copy of its action
type ViewTarget struct {
	Action          string
	UpdatedBy       []string
	Shared          bool
	Live            bool
	AfterActivation bool
}
type viewScope struct {
	Origin, Action       string
	Automatic, Lifecycle bool
}
type viewScopeKey struct{}

// Attach the origin and lifecycle scope used to authorize view updates
func (e *Engine) eventViewContext(ctx context.Context, ev sdk.Event) context.Context {
	scope := viewScope{Automatic: true}
	switch ev.Event {
	case "willAppear", "didReceiveSettings":
		scope.Lifecycle = true
		scope.Origin = ev.Context
	case "keyDown", "keyUp", "dialDown", "dialUp", "dialRotate", "touchTap", "desktopInputResult":
		scope = viewScope{Origin: ev.Context, Action: e.ActionKey(ev.Action)}
		if scope.Action == "" {
			scope.Action = e.ActionKey(e.Bindings[ev.Context].Action)
		}
	}
	if Active(ev) || ev.Event == "dialRotate" {
		e.ViewActive[ev.Context] = true
	}
	return context.WithValue(ctx, viewScopeKey{}, scope)
}

// Mark a binding as activated for subsequent live updates
func (e *Engine) ActivateView(id string) { e.ViewActive[id] = true }

// Report whether the binding has been activated
func (e *Engine) ViewIsActive(id string) bool { return e.ViewActive[id] }

// Look up the view policy for a binding action
func (e *Engine) viewRule(id string) ViewTarget {
	return e.ViewTargets[e.ActionKey(e.Bindings[id].Action)]
}

// Decide whether live updates are enabled for this binding
func (e *Engine) WantsLiveView(id string) bool {
	rule := e.viewRule(id)
	return rule.Live && (!rule.AfterActivation || e.ViewActive[id])
}

// Authorize rendering using origin, activation and shared identity
func (e *Engine) CanUpdateView(ctx context.Context, id string) bool {
	if _, ok := e.Bindings[id]; !ok {
		return false
	}
	scope, scoped := ctx.Value(viewScopeKey{}).(viewScope)
	if !scoped {
		return true
	} // Explicit low-level rendering, outside lifecycle/poll dispatch
	if !scope.Automatic && scope.Action != "" {
		if source, ok := e.Bindings[scope.Origin]; !ok || e.ActionKey(source.Action) != scope.Action {
			return false
		}
	}
	rule := e.viewRule(id)
	live := e.WantsLiveView(id)
	if scope.Origin == id {
		return !scope.Lifecycle || live || e.ViewActive[id]
	}
	if scope.Automatic {
		return live
	}
	if scope.Origin == "" || !slices.Contains(rule.UpdatedBy, scope.Action) {
		return false
	}
	if rule.AfterActivation && !e.ViewActive[id] {
		return false
	}
	// Cross-binding updates require an explicit matching shared identity, not just an action name
	if rule.Shared {
		source, ok := e.Bindings[scope.Origin]
		if !ok {
			return false
		}
		a, _ := e.EffectiveSettings(scope.Origin, source.Settings)["shared_id"].(string)
		b, _ := e.EffectiveSettings(id, e.Bindings[id].Settings)["shared_id"].(string)
		return a != "" && a == b
	}
	// Copies of an action are independent unless explicitly joined by shared_id
	return scope.Action != e.ActionKey(e.Bindings[id].Action)
}

// Refresh other bindings explicitly targeted by this event
func (e *Engine) RenderViewTargets(ctx context.Context, c *sdk.Client) error {
	scope, ok := ctx.Value(viewScopeKey{}).(viewScope)
	if !ok || scope.Origin == "" {
		return nil
	}
	for id := range e.Bindings {
		if id != scope.Origin && e.CanUpdateView(ctx, id) {
			if err := e.Render(ctx, c, id); err != nil {
				return err
			}
		}
	}
	return nil
}

// Validate complete and unambiguous view-target declarations
func ValidateViewTargets(def Definition) error {
	actions := map[string]bool{}
	for _, a := range def.Actions {
		actions[a] = true
	}
	seen := map[string]bool{}
	for _, rule := range def.ViewTargets {
		if !actions[rule.Action] || seen[rule.Action] {
			return fmt.Errorf("%s: unknown or duplicate view target %q", def.Slug, rule.Action)
		}
		seen[rule.Action] = true
		from := map[string]bool{}
		for _, source := range rule.UpdatedBy {
			if !actions[source] || from[source] {
				return fmt.Errorf("%s: invalid updater %q for %s", def.Slug, source, rule.Action)
			}
			from[source] = true
		}
	}
	if len(seen) != len(actions) {
		return fmt.Errorf("%s: every action needs an explicit view target", def.Slug)
	}
	return nil
}

// Theme changes redraw cached command feedback, without collecting another
// action's shared runtime state and accidentally replacing its last result
func (e *Engine) rethemeView(view Display) Display {
	asset := func(value string) string {
		if strings.Contains(value, "://") || strings.HasPrefix(value, "data:") {
			return value
		}
		for _, ext := range []string{".svg", ".json"} {
			for _, theme := range []string{"light", "dark"} {
				suffix := "-" + theme + ext
				if before, ok := strings.CutSuffix(value, suffix); ok {
					value = before + ext
				}
			}
		}
		if strings.HasSuffix(value, ".svg") {
			return e.ThemedAsset(value)
		}
		if strings.HasSuffix(value, ".json") {
			return e.ThemedLayout(value)
		}
		return value
	}
	view.Image = asset(view.Image)
	view.Layout = asset(view.Layout)
	var convert func(any) any
	convert = func(value any) any {
		switch v := value.(type) {
		case string:
			return asset(v)
		case map[string]any:
			result := map[string]any{}
			for key, value := range v {
				result[key] = convert(value)
			}
			return result
		case []any:
			result := make([]any, len(v))
			for i, value := range v {
				result[i] = convert(value)
			}
			return result
		default:
			return value
		}
	}
	if view.Feedback != nil {
		view.Feedback = convert(view.Feedback).(map[string]any)
	}
	return view
}

// Resolve a registered action key from a qualified identifier
func (e *Engine) ActionKey(action string) string {
	if _, ok := e.ViewTargets[action]; ok {
		return action
	}
	return Action(action)
}
