package state

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
)

type Store interface {
	Load() (ProcmonState, error)
	Save(ProcmonState) error
}

type FileStore struct {
	Path string
}

func (s FileStore) Load() (ProcmonState, error) {
	var snapshot ProcmonState
	data, err := os.ReadFile(s.Path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ProcmonState{
				Version:  SchemaVersion,
				Monitors: make(map[string]*MonitorRuntimeState),
			}, nil
		}
		return snapshot, err
	}
	if err := json.Unmarshal(data, &snapshot); err != nil {
		return snapshot, err
	}
	if snapshot.Monitors == nil {
		snapshot.Monitors = make(map[string]*MonitorRuntimeState)
	}
	if snapshot.Version == 0 {
		snapshot.Version = SchemaVersion
	}
	return snapshot, nil
}

func (s FileStore) Save(snapshot ProcmonState) error {
	if err := os.MkdirAll(filepath.Dir(s.Path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.Path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, s.Path)
}
