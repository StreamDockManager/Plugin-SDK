package authoring

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

// Verify shared semantic compact layouts
func TestSharedSemanticCompactLayouts(t *testing.T) {
	raw, err := os.ReadFile("../testdata/extensions-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Layouts []struct {
			Name          string
			Width, Height int
			Content       Presentation
			Rects         map[string][4]int
			Progress      *float64
		}
	}
	if err = json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, c := range fixture.Layouts {
		base := Display{Title: "Full title", Image: "icon.svg", Layout: "custom.json", Feedback: map[string]any{"custom": map[string]any{"value": "kept"}}}
		result := CompactDisplay(Present(base, &c.Content, c.Width, c.Height), c.Width, c.Height)
		if len(base.Feedback) != 1 || result.Title != base.Title || result.Layout != base.Layout {
			t.Fatal("changed snapshot", c.Name)
		}
		for key, rect := range c.Rects {
			raw, _ := json.Marshal(result.Feedback[key].(map[string]any)["rect"])
			var got [4]int
			json.Unmarshal(raw, &got)
			if !reflect.DeepEqual(got, rect) {
				t.Fatal(c.Name, key, got, rect)
			}
		}
		if c.Progress != nil && result.Feedback["indicator"].(map[string]any)["value"] != *c.Progress {
			t.Fatal("progress not clamped")
		}
	}
}
