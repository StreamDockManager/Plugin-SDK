// Package storage persists plugin-owned state. Host settings remain host-owned
package storage

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"github.com/StreamDockManager/Plugin-SDK/Go/internal/statefile"
)

const MaximumBytes = statefile.MaximumBytes

// Encode and atomically write a versioned state envelope
func Write(path string, value any) ([]byte, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	if err = statefile.Write(path, data); err != nil {
		return nil, err
	}
	return data, nil
}

type Store struct {
	Path        string
	Version     int
	Value       any
	Due         time.Time
	written     []byte
	Dirty       bool
	loadBlocked bool
}

// Create a file store with an explicit version
func New(path string, version int) *Store { return &Store{Path: path, Version: version} }

// Load validated state without overwriting invalid saved input
func (s *Store) Load(dst any) error {
	s.loadBlocked = true
	if s.Version != 1 {
		return fmt.Errorf("unsupported state version %d; current version is 1", s.Version)
	}
	data, err := statefile.Read(s.Path)
	if os.IsNotExist(err) {
		s.loadBlocked = false
		s.written = nil
		return nil
	}
	s.loadBlocked = true
	if err != nil {
		return err
	}
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(data, &envelope); err != nil {
		return err
	}
	var version int
	if json.Unmarshal(envelope["version"], &version) != nil || version != 1 || envelope["data"] == nil {
		return fmt.Errorf("unsupported state format: version 1 envelope required")
	}
	target := reflect.ValueOf(dst)
	if target.Kind() != reflect.Pointer || target.IsNil() {
		return fmt.Errorf("state destination must be a non-nil pointer")
	}
	value := reflect.New(target.Elem().Type())
	if err := json.Unmarshal(envelope["data"], value.Interface()); err != nil {
		return err
	}
	target.Elem().Set(value.Elem())
	s.Value = dst
	s.loadBlocked = false
	s.written = data
	return nil
}

// Stage changed state for a debounced write
func (s *Store) Change(value any, now time.Time) {
	s.Value = value
	s.Dirty = true
	if s.Due.IsZero() {
		s.Due = now.Add(250 * time.Millisecond)
	}
}

// Flush pending state when forced or its deadline is due
func (s *Store) Flush(now time.Time, force bool) error {
	if s.loadBlocked {
		return statefile.RecoveryRequired()
	}
	if s.Version != 1 {
		return fmt.Errorf("unsupported state version %d; current version is 1", s.Version)
	}
	if !s.Dirty || !force && now.Before(s.Due) {
		return nil
	}
	value := s.Value
	{
		value = map[string]any{"version": s.Version, "data": value}
	}
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	if !bytes.Equal(data, s.written) {
		err = statefile.Write(s.Path, data)
		if err != nil {
			return err
		}
		s.written = data
	}
	s.Dirty = false
	s.Due = time.Time{}
	return nil
}

// Force a final flush of pending state
func (s *Store) Close() error { return s.Flush(time.Now(), true) }

// PathFor isolates plugin state under the selected config root
func PathFor(root, identity string) (string, error) {
	if identity == "" || len(identity) > 180 || strings.Contains(identity, "..") {
		return "", fmt.Errorf("invalid plugin state identity")
	}
	for _, r := range identity {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '.' || r == '-' || r == '_') {
			return "", fmt.Errorf("invalid plugin state identity")
		}
	}
	return filepath.Join(root, "stream-dock-manager-plugins", identity, "state.json"), nil
}

// Resolve the default platform state path for an identity
func DefaultPath(identity string) (string, error) {
	root := os.Getenv("STREAM_DOCK_MANAGER_PLUGIN_STATE_DIR")
	if root == "" {
		var err error
		root, err = os.UserConfigDir()
		if err != nil {
			return "", err
		}
	}
	return PathFor(root, identity)
}
