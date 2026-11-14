package packaging

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

// Verify shared cache artifacts
func TestSharedCacheArtifacts(t *testing.T) {
	data, err := os.ReadFile("../testdata/services-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Artifacts struct {
			Pattern []byte `json:"byte_pattern"`
			Cases   []struct {
				Bytes  int
				SHA256 string
			}
		} `json:"cache_artifacts"`
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	buffer := make([]byte, 64<<10)
	for i := range buffer {
		buffer[i] = fixture.Artifacts.Pattern[i%len(fixture.Artifacts.Pattern)]
	}
	for _, test := range fixture.Artifacts.Cases {
		t.Run(fmt.Sprint(test.Bytes), func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "artifact")
			file, err := os.Create(path)
			if err != nil {
				t.Fatal(err)
			}
			for remaining := test.Bytes; remaining > 0; {
				n := min(remaining, len(buffer))
				if _, err = file.Write(buffer[:n]); err != nil {
					t.Fatal(err)
				}
				remaining -= n
			}
			if err = file.Close(); err != nil {
				t.Fatal(err)
			}
			if err = Remember(path, "fixture"); err != nil || !Cached(path, "fixture") || Cached(path, "different") {
				t.Fatal(err)
			}
			record, _ := os.ReadFile(path + ".build.json")
			var saved struct{ Output string }
			if json.Unmarshal(record, &saved) != nil || saved.Output != test.SHA256 {
				t.Fatal("artifact digest changed", saved.Output)
			}
			file, err = os.OpenFile(path, os.O_WRONLY, 0600)
			if err != nil {
				t.Fatal(err)
			}
			_, err = file.WriteAt([]byte{99}, int64(max(0, test.Bytes-1)))
			file.Close()
			if err != nil || Cached(path, "fixture") {
				t.Fatal("corrupt tail accepted", err)
			}
			if err = os.Remove(path); err != nil {
				t.Fatal(err)
			}
			if Cached(path, "fixture") || Remember(path, "fixture") == nil {
				t.Fatal("missing artifact accepted")
			}
		})
	}
}

// Verify shared cache fingerprints and corruption
func TestSharedCacheFingerprintsAndCorruption(t *testing.T) {
	data, err := os.ReadFile("../testdata/services-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Fingerprints []struct {
			Inputs        map[string]string
			Configuration any
			Expected      string
		}
	}
	if err = json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, test := range fixture.Fingerprints {
		inputs := map[string][]byte{}
		for k, v := range test.Inputs {
			inputs[k] = []byte(v)
		}
		got, err := Fingerprint(inputs, test.Configuration)
		if err != nil || got != test.Expected {
			t.Fatal(got, err)
		}
	}
	path := filepath.Join(t.TempDir(), "artifact")
	if err = os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = Remember(path, "key"); err != nil || !Cached(path, "key") {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	if Cached(path, "key") {
		t.Fatal("accepted corrupt output")
	}
}
