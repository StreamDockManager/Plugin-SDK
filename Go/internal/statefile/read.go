// Package statefile bounds state reads before JSON decoding, including files
// that grow after they are opened. All file-backed stores use it
package statefile

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

const MaximumBytes = 8 << 20

var ErrTooLarge = errors.New("state exceeds 8 MiB")

// Describe the explicit recovery required for invalid saved state
func RecoveryRequired() error {
	return errors.New("saved state is protected: repair or move it and reload before writing")
}

// Read a bounded state file without silently resetting it
func Read(path string) ([]byte, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, missingError(path, err)
	}
	defer file.Close()
	return read(file)
}

// Windows PATH_NOT_FOUND also describes a regular file used as a parent
// Only genuinely absent ancestors may count as an empty, creatable store
func missingError(path string, err error) error {
	if !os.IsNotExist(err) {
		return err
	}
	for parent := path; ; parent = filepath.Dir(parent) {
		_, access := os.Lstat(parent)
		if access != nil {
			if os.IsNotExist(access) && filepath.Dir(parent) != parent {
				continue
			}
			return &os.PathError{Op: "open", Path: path, Err: fmt.Errorf("state parent unavailable: %v", access)}
		}
		if parent == path {
			return &os.PathError{Op: "open", Path: path, Err: errors.New("existing state path is unavailable")}
		}
		info, access := os.Stat(parent)
		if access != nil {
			return &os.PathError{Op: "open", Path: path, Err: fmt.Errorf("state parent unavailable: %v", access)}
		}
		if !info.IsDir() {
			return &os.PathError{Op: "open", Path: path, Err: errors.New("state parent is not a directory")}
		}
		return err
	}
}

// Read state bytes within the configured file limit
func read(reader io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(reader, MaximumBytes+1))
	if err != nil {
		return nil, err
	}
	if len(data) > MaximumBytes {
		return nil, ErrTooLarge
	}
	return data, nil
}
