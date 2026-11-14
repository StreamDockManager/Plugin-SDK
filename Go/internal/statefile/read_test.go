package statefile

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// Verify ambiguous missing read rejects existing paths
func TestAmbiguousMissingReadRejectsExistingPaths(t *testing.T) {
	root := t.TempDir()
	blocker := filepath.Join(root, "blocked")
	if err := os.WriteFile(blocker, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{blocker, filepath.Join(blocker, "state.json"), filepath.Join(blocker, "missing", "state.json")} {
		if err := missingError(path, os.ErrNotExist); err == nil || os.IsNotExist(err) {
			t.Fatalf("ambiguous missing error lost storage failure for %s: %v", path, err)
		}
	}
	if !os.IsNotExist(missingError(filepath.Join(root, "new", "nested", "state.json"), os.ErrNotExist)) {
		t.Fatal("absent tree rejected")
	}
}

// Directory junctions exercise Windows reparse aliases without requiring the
// optional symlink privilege. Both endpoints belong to the test's temp directory
func directoryLink(t *testing.T, target, link string) {
	t.Helper()
	if err := os.Symlink(target, link); err == nil {
		t.Log("directory alias fixture: symlink")
		return
	} else if runtime.GOOS != "windows" {
		t.Fatalf("directory symlink fixture unavailable: %v", err)
	}
	// cmd.exe interprets metacharacters even with shell=false. Refuse unusual
	// fixture paths instead of allowing any command expansion
	if strings.ContainsAny(target+link, "%!&|<>^\"()\r\n") {
		t.Fatal("junction fixture cannot safely quote this temporary directory")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, "cmd.exe", "/D", "/V:OFF", "/C", "mklink", "/J", link, target).CombinedOutput()
	if err != nil {
		t.Fatalf("directory symlink unavailable and junction fixture failed: %v: %s", err, output)
	}
	t.Log("directory alias fixture: Windows junction (symlink privilege unavailable)")
}

// Verify directory aliases distinguish dangling and valid parents
func TestDirectoryAliasesDistinguishDanglingAndValidParents(t *testing.T) {
	root := t.TempDir()
	target, link := filepath.Join(root, "target"), filepath.Join(root, "alias")
	if err := os.Mkdir(target, 0700); err != nil {
		t.Fatal(err)
	}
	directoryLink(t, target, link)
	if _, err := Read(filepath.Join(link, "missing", "state.json")); !os.IsNotExist(err) {
		t.Fatalf("valid directory alias did not preserve absent state: %v", err)
	}
	if err := os.WriteFile(filepath.Join(link, "state.json"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	if data, err := Read(filepath.Join(target, "state.json")); err != nil || string(data) != "keep" {
		t.Fatalf("valid directory alias did not reach target: %q %v", data, err)
	}
	if err := os.Remove(filepath.Join(target, "state.json")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{link, filepath.Join(link, "state.json"), filepath.Join(link, "missing", "state.json")} {
		if _, err := Read(path); err == nil || os.IsNotExist(err) {
			t.Fatalf("dangling directory alias treated as absent: %v", err)
		}
		if err := missingError(path, os.ErrNotExist); err == nil || os.IsNotExist(err) {
			t.Fatalf("injected ambiguous missing error accepted dangling alias: %v", err)
		}
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
}

// This source keeps producing bytes, as a file growing during the read can do
// It has no size metadata and must not be drained to EOF
type growingReader struct{ consumed int }

// Produce bounded chunks without EOF to exercise the read-size limit
func (r *growingReader) Read(p []byte) (int, error) {
	n := min(len(p), 997)
	clear(p[:n])
	r.consumed += n
	return n, nil
}

// Verify growing read stops at cap plus one
func TestGrowingReadStopsAtCapPlusOne(t *testing.T) {
	reader := &growingReader{}
	if _, err := read(reader); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected limit error, got %v", err)
	}
	if reader.consumed != MaximumBytes+1 {
		t.Fatalf("read %d bytes, expected %d", reader.consumed, MaximumBytes+1)
	}
}

// Verify bounded read preserves data and errors
func TestBoundedReadPreservesDataAndErrors(t *testing.T) {
	data := []byte("{\"opaque\":{\"retain\":[1,2]}}")
	if actual, err := read(bytes.NewReader(data)); err != nil || !bytes.Equal(actual, data) {
		t.Fatalf("read %q: %v", actual, err)
	}
	if _, err := read(failedReader{}); err != io.ErrUnexpectedEOF {
		t.Fatalf("lost read failure: %v", err)
	}
}

type failedReader struct{}

// Inject an unexpected EOF while reading saved state
func (failedReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }
