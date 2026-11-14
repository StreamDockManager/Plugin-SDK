package authoring

import (
	"github.com/StreamDockManager/Plugin-SDK/Go/settings"
	"reflect"
	"testing"
)

// Verify clone compatibility
func TestCloneCompatibility(t *testing.T) {
	type typedMap map[string]any
	type typedSlice []any
	cases := []any{nil, map[string]any(nil), []any(nil), []string(nil), []string{}, typedMap{"v": 1}, typedSlice{1}, map[string]any{"nested": []any{map[string]any{"strings": []string{"a"}}}}}
	for _, value := range cases {
		if !reflect.DeepEqual(CloneSetting(value), settings.Clone(value)) {
			t.Fatalf("clone mismatch for %T", value)
		}
	}
	if CloneSettings(nil) == nil || CloneSetting([]any(nil)).([]any) == nil {
		t.Fatal("maps and any slices must remain nonnil")
	}
	if CloneSetting([]string{}).([]string) != nil {
		t.Fatal("empty string slice must remain nil")
	}
	value := map[string]any{"nested": []any{map[string]any{"strings": []string{"a"}}}}
	copy := CloneSettings(value)
	copy["nested"].([]any)[0].(map[string]any)["strings"].([]string)[0] = "b"
	if value["nested"].([]any)[0].(map[string]any)["strings"].([]string)[0] != "a" {
		t.Fatal("clone aliased")
	}
	original := typedMap{"v": 1}
	CloneSetting(original).(typedMap)["v"] = 2
	if original["v"] != 2 {
		t.Fatal("custom map pass-through changed")
	}
}
