// Package packaging audits runtime dependencies and local resource closure
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
	"sort"
	"strings"
)

type Module struct {
	Path, Version, Dir string
	Main               bool
	Replace            *Module
}

// Resolve the Go modules actually used by the target package
func GoModules(ctx context.Context, root, packagePath string, environment []string) ([]Module, error) {
	command := exec.CommandContext(ctx, "go", "list", "-deps", "-json", packagePath)
	command.Dir, command.Env = root, environment
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("list runtime dependencies: %w", err)
	}
	modules := map[string]Module{}
	decoder := json.NewDecoder(bytes.NewReader(output))
	for {
		var pkg struct{ Module *Module }
		if err = decoder.Decode(&pkg); err == io.EOF {
			break
		} else if err != nil {
			return nil, err
		}
		if pkg.Module == nil {
			continue
		}
		if pkg.Module.Main && filepath.Clean(pkg.Module.Dir) == filepath.Clean(root) {
			continue
		}
		module := *pkg.Module
		if module.Replace != nil {
			module.Dir = module.Replace.Dir
			module.Version = module.Replace.Version
		}
		if module.Dir == "" {
			return nil, fmt.Errorf("dependency directory unavailable: %s", module.Path)
		}
		modules[module.Path] = module
	}
	out := make([]Module, 0, len(modules))
	for _, module := range modules {
		out = append(out, module)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// ReadNotices requires a license for every runtime dependency and retains NOTICE
func ReadNotices(modules []Module) ([]byte, error) {
	var out bytes.Buffer
	for _, module := range modules {
		found := false
		for _, name := range []string{"LICENSE", "LICENSE.md", "LICENSE.txt", "COPYING"} {
			data, err := os.ReadFile(filepath.Join(module.Dir, name))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(&out, "\n===== %s %s (%s) =====\n%s\n", module.Path, module.Version, name, data)
			found = true
			break
		}
		if !found {
			return nil, fmt.Errorf("missing dependency license: %s", module.Path)
		}
		for _, name := range []string{"NOTICE", "NOTICE.txt", "NOTICE.md"} {
			data, err := os.ReadFile(filepath.Join(module.Dir, name))
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			fmt.Fprintf(&out, "\n===== %s (%s) =====\n%s\n", module.Path, name, data)
		}
	}
	return out.Bytes(), nil
}

type Resource struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
	Bytes  int64  `json:"bytes"`
}

// Audit bounded packaged resources and their references
func AuditResources(root string, references []string) ([]Resource, error) {
	out := []Resource{}
	seen := map[string]bool{}
	for _, reference := range references {
		if reference == "" || strings.HasPrefix(reference, "data:") {
			continue
		}
		if seen[reference] {
			continue
		}
		seen[reference] = true
		if strings.ContainsAny(reference, "\\\x00") || strings.Contains(reference, ":") || filepath.IsAbs(reference) {
			return nil, fmt.Errorf("invalid resource path %q", reference)
		}
		clean := filepath.Clean(filepath.FromSlash(reference))
		if clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("resource escapes package")
		}
		path := filepath.Join(root, clean)
		relative := clean
		for directory := filepath.Dir(path); directory != filepath.Clean(root) && directory != "."; directory = filepath.Dir(directory) {
			info, err := os.Lstat(directory)
			if err != nil {
				return nil, err
			}
			if info.Mode()&os.ModeSymlink != 0 {
				return nil, fmt.Errorf("resource parent is a symlink")
			}
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, fmt.Errorf("missing packaged resource %s: %w", reference, err)
		}
		if !info.Mode().IsRegular() || info.Size() > 64<<20 {
			return nil, fmt.Errorf("resource is not a bounded regular file: %s", reference)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(data)
		out = append(out, Resource{filepath.ToSlash(relative), hex.EncodeToString(sum[:]), info.Size()})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out, nil
}

// ValidateLayoutReferences walks JSON layouts and checks local image/layout assets
func ValidateLayoutReferences(value any, files map[string][]byte) error {
	switch item := value.(type) {
	case map[string]any:
		for _, value := range item {
			if err := ValidateLayoutReferences(value, files); err != nil {
				return err
			}
		}
	case []any:
		for _, value := range item {
			if err := ValidateLayoutReferences(value, files); err != nil {
				return err
			}
		}
	case string:
		if strings.HasPrefix(item, "data:") || strings.Contains(item, "://") {
			return nil
		}
		if strings.HasSuffix(item, ".svg") || strings.HasSuffix(item, ".png") || strings.HasSuffix(item, ".json") {
			if _, ok := files[item]; !ok {
				return fmt.Errorf("missing referenced asset %s", item)
			}
		}
	}
	return nil
}
