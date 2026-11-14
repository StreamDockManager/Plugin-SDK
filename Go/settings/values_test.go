package settings

import (
	"encoding/json"
	"os"
	"testing"
)

// Verify shared typed values
func TestSharedTypedValues(t *testing.T) {
	data, err := os.ReadFile("../testdata/services-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Typed []struct {
			Input   map[string]any
			Number  float64
			String  string
			Boolean bool
		} `json:"typed_values"`
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, test := range fixture.Typed {
		if Number(test.Input, "n", 99) != test.Number || String(test.Input, "s", "fallback") != test.String || Bool(test.Input, "b") != test.Boolean {
			t.Fatal(test)
		}
	}
}
