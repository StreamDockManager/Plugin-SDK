package authoring

import (
	"encoding/json"
	"testing"
)

// Verify feedback numeric representations
func TestFeedbackNumericRepresentations(t *testing.T) {
	for _, pair := range [][2]any{{1, 1.0}, {json.Number("1000"), json.Number("1e3")}, {[]any{1}, []any{1.0}}} {
		if !FeedbackEqual(pair[0], pair[1]) {
			t.Fatalf("equivalent JSON numbers differ: %#v", pair)
		}
	}
	for _, pair := range [][2]any{{true, 1}, {uint64(9007199254740993), float64(9007199254740992)}, {json.Number("1e9223372036854775807"), json.Number("10e9223372036854775807")}, {1.0, 1.0000000000000002}} {
		if FeedbackEqual(pair[0], pair[1]) {
			t.Fatalf("distinct JSON numbers collapsed: %#v", pair)
		}
	}
}
