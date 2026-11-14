package packaging

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type BinaryBuildConfiguration struct {
	Go         json.RawMessage
	ModulePath string
	VCS        any
	SDK        SDKBuildInput
}

// Read effective values from the same Go command and environment used for the
// build. os.Getenv alone misses settings saved with go env -w, and the running
// packager's runtime.Version may differ from the selected compiler's version
func BinaryConfiguration(ctx context.Context, root string, environment []string) (BinaryBuildConfiguration, error) {
	c := exec.CommandContext(ctx, "go", "env", "-json", "GOVERSION", "GOROOT", "GOTOOLDIR", "GOTOOLCHAIN", "GOFLAGS", "GOEXPERIMENT", "GOOS", "GOARCH", "GOAMD64", "GOARM64", "CGO_ENABLED", "GOWORK")
	c.Dir = root
	c.Env = environment
	configuration, err := c.Output()
	if err != nil {
		return BinaryBuildConfiguration{}, fmt.Errorf("read Go build configuration: %w", err)
	}
	modulePath, err := CurrentModulePath(ctx, root, environment)
	if err != nil {
		return BinaryBuildConfiguration{}, err
	}
	vcs, err := VCSConfiguration(ctx, root, environment)
	if err != nil {
		return BinaryBuildConfiguration{}, err
	}
	dependency, err := SDKConfiguration(ctx, root, environment)
	if err != nil {
		return BinaryBuildConfiguration{}, err
	}
	return BinaryBuildConfiguration{configuration, modulePath, vcs, dependency}, nil
}

// Ask the selected Go toolchain which main module owns this go.mod. A workspace
// can contain several main modules; an arbitrary first result may be a sibling
func CurrentModulePath(ctx context.Context, root string, environment []string) (string, error) {
	c := exec.CommandContext(ctx, "go", "list", "-m", "-json")
	c.Dir, c.Env = root, environment
	output, err := c.Output()
	if err != nil {
		return "", fmt.Errorf("read Go module identity: %w", err)
	}
	local, err := os.Stat(filepath.Join(root, "go.mod"))
	if err != nil {
		return "", err
	}
	decoder := json.NewDecoder(bytes.NewReader(output))
	for {
		var module struct {
			Path, GoMod string
			Main        bool
		}
		if err := decoder.Decode(&module); err == io.EOF {
			return "", fmt.Errorf("module owning %s was not reported by Go", root)
		} else if err != nil {
			return "", fmt.Errorf("read Go module identity: %w", err)
		}
		if module.Main && module.Path != "" {
			file, err := os.Stat(module.GoMod)
			if err == nil && os.SameFile(local, file) {
				return module.Path, nil
			}
		}
	}
}

// Go stamps repository-wide revision/dirty state and derives the main module's
// version from local tags and history. These inputs can change without a plugin
// source changing. Keep them in every binary key, including worktree checkouts
func VCSConfiguration(ctx context.Context, root string, environment []string) (any, error) {
	for directory := root; ; directory = filepath.Dir(directory) {
		if _, err := os.Stat(filepath.Join(directory, ".git")); err == nil {
			break
		} else if !os.IsNotExist(err) {
			return nil, err
		}
		if filepath.Dir(directory) == directory {
			return "no-git-repository", nil
		}
	}
	if _, err := exec.LookPath("git"); err != nil {
		// -buildvcs=auto also omits metadata when the VCS tool is unavailable
		return "git-unavailable", nil
	}
	git := func(directory string, arguments ...string) ([]byte, error) {
		c := exec.CommandContext(ctx, "git", arguments...)
		c.Dir, c.Env = directory, environment
		output, err := c.Output()
		if err != nil {
			return nil, fmt.Errorf("read Go VCS metadata (%s): %w", arguments[0], err)
		}
		return output, nil
	}
	directory, err := git(root, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, err
	}
	repository := strings.TrimSpace(string(directory))
	status, err := git(repository, "status", "--porcelain")
	if err != nil {
		return nil, err
	}
	revision, err := git(repository, "-c", "log.showsignature=false", "log", "-1", "--format=%H:%ct")
	if err != nil && len(status) == 0 {
		return nil, err
	}
	// As in Go's VCS loader, an uncommitted repository may have no HEAD yet
	tags, err := git(repository, "for-each-ref", "--sort=refname", "--format=%(refname)%00%(objectname)%00%(*objectname)", "refs/tags", "refs/replace")
	if err != nil {
		return nil, err
	}
	shallowPath, err := git(repository, "rev-parse", "--git-path", "shallow")
	if err != nil {
		return nil, err
	}
	path := strings.TrimSpace(string(shallowPath))
	if !filepath.IsAbs(path) {
		path = filepath.Join(repository, path)
	}
	shallow, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	return struct {
		Revision string
		Modified bool
		Tags     string
		Shallow  string
	}{string(revision), len(status) != 0, string(tags), string(shallow)}, nil
}

const sdkModulePath = "github.com/StreamDockManager/Plugin-SDK/Go"

type SDKBuildInput struct{ ImportPath, Version, Sum, Content string }

// Hash the effective package tree, including replace/go.work sources. A version
// alone is insufficient when SDK development changes files without a new tag
func SDKConfiguration(ctx context.Context, root string, environment []string) (SDKBuildInput, error) {
	input := SDKBuildInput{ImportPath: sdkModulePath}
	mod, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil {
		return input, err
	}
	if !bytes.Contains(mod, []byte(sdkModulePath)) {
		return input, nil
	}
	c := exec.CommandContext(ctx, "go", "list", "-json", sdkModulePath)
	c.Dir, c.Env = root, environment
	output, err := c.Output()
	if err != nil {
		return input, fmt.Errorf("resolve SDK dependency: %w", err)
	}
	var pkg struct {
		ImportPath, Dir string
		Module          struct{ Version, Sum string }
	}
	if err = json.Unmarshal(output, &pkg); err != nil {
		return input, err
	}
	input.ImportPath, input.Version, input.Sum = pkg.ImportPath, pkg.Module.Version, pkg.Module.Sum
	h := sha256.New()
	err = filepath.WalkDir(pkg.Dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if path != pkg.Dir && (d.Name() == ".git" || d.Name() == ".cache" || d.Name() == "artifacts") {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(path, "_test.go") {
			return nil
		}
		if !strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, ".js") && !strings.HasSuffix(path, ".json") && !strings.HasSuffix(path, ".css") && !strings.HasSuffix(path, ".html") && d.Name() != "go.mod" && d.Name() != "go.sum" && d.Name() != "LICENSE" {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(pkg.Dir, path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(relative), len(data))
		h.Write(data)
		return nil
	})
	input.Content = hex.EncodeToString(h.Sum(nil))
	return input, err
}
