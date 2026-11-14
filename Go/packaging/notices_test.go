package packaging

import (
	"os"
	"path/filepath"
	"testing"
)

// Verify resource closure and notices
func TestResourceClosureAndNotices(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "LICENSE"), []byte("runtime license"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := ReadNotices([]Module{{Path: "runtime", Version: "1", Dir: root}})
	if err != nil || len(data) == 0 {
		t.Fatal(err)
	}
	if _, err := AuditResources(root, []string{"LICENSE"}); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"../outside", "/absolute", "missing", "https://invalid"} {
		if _, err := AuditResources(root, []string{name}); err == nil {
			t.Fatal(name)
		}
	}
	if err := ValidateLayoutReferences(map[string]any{"icon": "missing.svg"}, map[string][]byte{}); err == nil {
		t.Fatal("missing layout asset accepted")
	}
}
