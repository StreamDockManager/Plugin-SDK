package statefile

import (
	"os"
	"path/filepath"
)

// Write atomically persists already validated JSON bytes owned by the caller
// Internal callers serialize once, compare and publish those exact bytes
func Write(path string, data []byte) error {
	if len(data) > MaximumBytes {
		return ErrTooLarge
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".state-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closed := f.Close()
	if err != nil {
		return err
	}
	if closed != nil {
		return closed
	}
	return os.Rename(f.Name(), path)
}
