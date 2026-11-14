package packaging

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Hash a file for artifact integrity checking
func fileHash(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err = io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Fingerprint includes explicit configuration and sorted, length-delimited inputs
func Fingerprint(inputs map[string][]byte, configuration any) (string, error) {
	data, err := json.Marshal(configuration)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write(data)
	names := make([]string, 0, len(inputs))
	for name := range inputs {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		fmt.Fprintf(h, "%s\x00%d\x00", name, len(inputs[name]))
		h.Write(inputs[name])
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Check whether an artifact and its stored cache key still match
func Cached(path, key string) bool {
	data, err := os.ReadFile(path + ".build.json")
	if err != nil {
		return false
	}
	var record struct{ Input, Output string }
	if json.Unmarshal(data, &record) != nil || record.Input != key {
		return false
	}
	sum, err := fileHash(path)
	if err != nil {
		return false
	}
	return record.Output == sum
}

// Record the cache key and digest of a completed artifact
func Remember(path, key string) error {
	sum, err := fileHash(path)
	if err != nil {
		return err
	}
	record, err := json.Marshal(map[string]string{"input": key, "output": sum})
	if err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".build-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(record); err != nil {
		f.Close()
		return err
	}
	if err = f.Close(); err != nil {
		return err
	}
	return os.Rename(f.Name(), path+".build.json")
}

// Parallel bounds build execution while preserving caller-owned cancellation
func Parallel[T any](ctx context.Context, items []T, jobs int, fn func(T) error) error {
	if jobs < 1 || jobs > 128 {
		return fmt.Errorf("jobs must be between 1 and 128")
	}
	queue := make(chan T)
	failures := make(chan error, len(items))
	var workers sync.WaitGroup
	for range min(jobs, len(items)) {
		workers.Go(func() {
			for item := range queue {
				if ctx.Err() != nil {
					failures <- ctx.Err()
				} else if err := fn(item); err != nil {
					failures <- err
				}
			}
		})
	}
	for _, item := range items {
		queue <- item
	}
	close(queue)
	workers.Wait()
	close(failures)
	for err := range failures {
		return err
	}
	return nil
}
