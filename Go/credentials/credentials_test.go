package credentials

import (
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
)

// Verify shared credentials
func TestSharedCredentials(t *testing.T) {
	data, err := os.ReadFile("../testdata/services-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Credentials []struct {
			Settings   map[string]any
			Key, Value string
			Lookup     map[string]string
			Invalid    bool
		}
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, test := range fixture.Credentials {
		for key, value := range test.Lookup {
			t.Setenv(key, value)
		}
		value, err := Secret(test.Settings, test.Key)
		if (err != nil) != test.Invalid || (!test.Invalid && value != test.Value) {
			t.Fatal(value, err)
		}
	}
	t.Setenv("SDK_SERVICE_SECRET", "exact+secret")
	failure := SafeError(errors.New("exact+secret exact%2Bsecret https://u:p@host/"), map[string]any{"password": "${SDK_SERVICE_SECRET}"})
	if strings.Contains(failure.Error(), "secret") || strings.Contains(failure.Error(), "u:p") {
		t.Fatal(failure)
	}
}
