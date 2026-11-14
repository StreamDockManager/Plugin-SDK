package settings

import (
	"encoding/json"
	"testing"
)

// Verify sensitive strings preserve references and unknown settings
func TestSensitiveStringsPreserveReferencesAndUnknownSettings(t *testing.T) {
	var f Field
	if err := json.Unmarshal([]byte(`{"key":"password","label":"Password","type":"text","sensitive":true}`), &f); err != nil || !f.Sensitive {
		t.Fatal(f, err)
	}
	input := map[string]any{"password": "${MAIL_PASSWORD}", "extension": map[string]any{"keep": true}}
	out, err := Validate([]Field{f}, input)
	if err != nil || out["password"] != "${MAIL_PASSWORD}" || out["extension"] == nil {
		t.Fatal(out, err)
	}
	f.Type = "number"
	if _, err = Validate([]Field{f}, nil); err == nil {
		t.Fatal("masked numeric field accepted")
	}
}
