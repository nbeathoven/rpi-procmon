package events

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type Store interface {
	Load() (History, error)
	Save(History) error
}

type FileStore struct {
	Path       string
	MaxEntries int
}

func (s FileStore) Load() (History, error) {
	var history History
	data, err := os.ReadFile(s.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return History{Version: SchemaVersion}, nil
		}
		return history, err
	}
	if err := json.Unmarshal(data, &history); err != nil {
		return history, err
	}
	if history.Version == 0 {
		history.Version = SchemaVersion
	}
	history.Events = trimToLimit(history.Events, s.MaxEntries)
	return history, nil
}

func (s FileStore) Save(history History) error {
	history.Version = SchemaVersion
	history.Events = trimToLimit(history.Events, s.MaxEntries)
	if err := os.MkdirAll(filepath.Dir(s.Path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}

func trimToLimit(in []Event, limit int) []Event {
	if limit <= 0 || len(in) <= limit {
		return in
	}
	start := len(in) - limit
	out := make([]Event, limit)
	copy(out, in[start:])
	return out
}
