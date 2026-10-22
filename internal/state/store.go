package state

import (
	"encoding/json"

	"github.com/nbeathoven/rpi-procmon/internal/persistence"
)

type Store interface {
	Load() (ProcmonState, error)
	Save(ProcmonState) error
}

type FileStore struct {
	Path string
}

func (s FileStore) Load() (ProcmonState, error) {
	snapshot, err := persistence.Load[ProcmonState](s.Path)
	if err != nil {
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
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return err
	}
	return persistence.Save(s.Path, data)
}
