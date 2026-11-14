package inspectors

import (
	"bytes"
	"os"
	"os/exec"
	"testing"
)

// Verify discovery packaged controller
func TestDiscoveryPackagedController(t *testing.T) {
	node := os.Getenv("STREAM_DOCK_MANAGER_NODE")
	if node == "" {
		node = "node"
	}
	cmd := exec.Command(node, "../testdata/discovery.mjs", "-")
	cmd.Stdin = bytes.NewReader(Assets()["inspector-workflows.js"])
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("discovery controller: %v\n%s", err, out)
	}
}
