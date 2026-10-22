package events

import (
	"encoding/json"

	"github.com/nbeathoven/rpi-procmon/internal/persistence"
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
	history, err := persistence.Load[History](s.Path)
	if err != nil {
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
	data, err := json.MarshalIndent(history, "", "  ")
	if err != nil {
		return err
	}
	return persistence.Save(s.Path, data)
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
