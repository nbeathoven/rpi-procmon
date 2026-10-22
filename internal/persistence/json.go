package persistence

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"
)

// Load preserves corrupt runtime data before returning an empty snapshot.
// Permission and other I/O errors remain fatal; they must not reset state.
func Load[T any](path string) (T, error) {
	var value T
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return value, nil
	}
	if err != nil {
		return value, err
	}
	err = json.Unmarshal(data, &value)
	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		err = errors.New("null runtime snapshot")
	}
	if err == nil {
		return value, nil
	}
	var empty T
	backup := path + ".corrupt-" + time.Now().UTC().Format("20060102T150405.000000000Z")
	if moveErr := os.Rename(path, backup); moveErr != nil {
		return empty, fmt.Errorf("preserve corrupt %s: %w", path, moveErr)
	}
	if syncErr := syncDir(filepath.Dir(path)); syncErr != nil {
		return empty, syncErr
	}
	log.Printf("runtime snapshot %s invalid (%v); preserved as %s; rebuilding state", path, err, backup)
	return empty, nil
}

// Save flushes both file contents and the rename before reporting success.
func Save(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".procmon-*.tmp")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	if err := f.Chmod(0644); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncDir(dir)
}

func syncDir(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
