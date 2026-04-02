package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

const (
	defaultConfigFile     = "/etc/rpi-procmon/config.json"
	defaultLogFile        = "/var/log/rpi-procmon.log"
	defaultStateFile      = "/var/lib/rpi-procmon/state.json"
	defaultEventsFile     = "/var/lib/rpi-procmon/events.json"
	defaultEventsMaxCount = 1000
	defaultListenAddr     = "127.0.0.1:9645"
	defaultReadHeader     = "5s"
)

func Load() (Config, error) {
	cfgFile := envString("PROC_CONFIG_FILE", defaultConfigFile)
	cfg := Config{
		ConfigFile: cfgFile,
	}

	data, err := os.ReadFile(cfgFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return Config{}, fmt.Errorf("read %s: %w", cfgFile, err)
		}
		return Config{}, fmt.Errorf("read %s: %w", cfgFile, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return Config{}, fmt.Errorf("parse %s: empty config", cfgFile)
	}

	if err := json.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", cfgFile, err)
	}
	cfg.ConfigFile = cfgFile

	deduped, duplicateIDs, err := dedupeMonitors(cfg.Monitors)
	if err != nil {
		return Config{}, fmt.Errorf("sanitize %s: %w", cfgFile, err)
	}
	cfg.Monitors = deduped
	if len(duplicateIDs) > 0 {
		if err := rewriteConfig(cfg); err != nil {
			return Config{}, fmt.Errorf("rewrite deduplicated %s: %w", cfgFile, err)
		}
	}

	applyEnvOverrides(&cfg)
	applyDefaults(&cfg)
	if len(cfg.Monitors) == 0 {
		return Config{}, errors.New("no monitors configured")
	}
	if err := Normalize(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func Normalize(cfg *Config) error {
	seenIDs := make(map[string]struct{}, len(cfg.Monitors))
	for i := range cfg.Monitors {
		mon := &cfg.Monitors[i]
		mon.ID = strings.TrimSpace(mon.ID)
		if mon.ID == "" {
			return errors.New("monitor id is required")
		}
		if _, ok := seenIDs[mon.ID]; ok {
			return fmt.Errorf("duplicate monitor id %q", mon.ID)
		}
		seenIDs[mon.ID] = struct{}{}
		if strings.TrimSpace(mon.Name) == "" {
			mon.Name = mon.ID
		}
		if strings.TrimSpace(mon.Type) == "" {
			mon.Type = "generic"
		}
		if strings.TrimSpace(mon.Interval) == "" {
			mon.Interval = "1m"
		}
		if mon.FailureThreshold <= 0 {
			mon.FailureThreshold = 1
		}
		if strings.TrimSpace(mon.Cooldown) == "" {
			mon.Cooldown = "5m"
		}
		if strings.TrimSpace(mon.Target.Transport) == "" {
			mon.Target.Transport = "local"
		}
		if _, err := time.ParseDuration(strings.TrimSpace(mon.Interval)); err != nil {
			return fmt.Errorf("monitor %q interval %q is invalid: %w", mon.ID, mon.Interval, err)
		}
		if _, err := time.ParseDuration(strings.TrimSpace(mon.Cooldown)); err != nil {
			return fmt.Errorf("monitor %q cooldown %q is invalid: %w", mon.ID, mon.Cooldown, err)
		}
		if err := validateTarget(mon.ID, mon.Target); err != nil {
			return err
		}
		if len(mon.Checks) == 0 {
			return fmt.Errorf("monitor %q has no checks", mon.ID)
		}
		checkIDs := make(map[string]struct{}, len(mon.Checks))
		for j := range mon.Checks {
			if strings.TrimSpace(mon.Checks[j].ID) == "" {
				mon.Checks[j].ID = fmt.Sprintf("%s-check-%d", mon.ID, j+1)
			}
			if strings.TrimSpace(mon.Checks[j].Name) == "" {
				mon.Checks[j].Name = mon.Checks[j].ID
			}
			if strings.TrimSpace(mon.Checks[j].Type) == "" {
				return fmt.Errorf("monitor %q check %q missing type", mon.ID, mon.Checks[j].ID)
			}
			if _, ok := checkIDs[mon.Checks[j].ID]; ok {
				return fmt.Errorf("monitor %q has duplicate check id %q", mon.ID, mon.Checks[j].ID)
			}
			checkIDs[mon.Checks[j].ID] = struct{}{}
			if err := validateCheck(mon.ID, mon.Checks[j]); err != nil {
				return err
			}
		}
		if len(mon.Recovery) == 0 {
			return fmt.Errorf("monitor %q has no recovery actions", mon.ID)
		}
		for j := range mon.Recovery {
			if strings.TrimSpace(mon.Recovery[j].Name) == "" {
				mon.Recovery[j].Name = fmt.Sprintf("%s-action-%d", mon.ID, j+1)
			}
			if strings.TrimSpace(mon.Recovery[j].Type) == "" {
				return fmt.Errorf("monitor %q recovery action %q missing type", mon.ID, mon.Recovery[j].Name)
			}
			if err := validateAction(mon.ID, mon.Recovery[j]); err != nil {
				return err
			}
		}
	}
	return nil
}

func applyEnvOverrides(cfg *Config) {
	if value := strings.TrimSpace(os.Getenv("PROC_LOG_FILE")); value != "" {
		cfg.LogFile = value
	}
	if value := strings.TrimSpace(os.Getenv("PROC_STATE_FILE")); value != "" {
		cfg.StateFile = value
	}
	if value := strings.TrimSpace(os.Getenv("PROC_EVENTS_FILE")); value != "" {
		cfg.EventsFile = value
	}
	if value := strings.TrimSpace(os.Getenv("PROC_EVENTS_MAX_ENTRIES")); value != "" {
		if parsed, err := strconv.Atoi(value); err == nil {
			cfg.EventsMaxEntries = parsed
		}
	}
	if value := strings.TrimSpace(os.Getenv("PROC_API_LISTEN_ADDR")); value != "" {
		cfg.API.ListenAddress = value
	}
	if value := strings.TrimSpace(os.Getenv("PROC_API_READ_HEADER_TIMEOUT")); value != "" {
		cfg.API.ReadHeaderTimeout = value
	}
	if value := strings.TrimSpace(os.Getenv("PROC_API_ADMIN_TOKEN")); value != "" {
		cfg.API.AdminToken = value
	}
}

func applyDefaults(cfg *Config) {
	if strings.TrimSpace(cfg.LogFile) == "" {
		cfg.LogFile = defaultLogFile
	}
	if strings.TrimSpace(cfg.StateFile) == "" {
		cfg.StateFile = defaultStateFile
	}
	if strings.TrimSpace(cfg.EventsFile) == "" {
		cfg.EventsFile = defaultEventsFile
	}
	if cfg.EventsMaxEntries <= 0 {
		cfg.EventsMaxEntries = defaultEventsMaxCount
	}
	if strings.TrimSpace(cfg.API.ListenAddress) == "" {
		cfg.API.ListenAddress = defaultListenAddr
	}
	if strings.TrimSpace(cfg.API.ReadHeaderTimeout) == "" {
		cfg.API.ReadHeaderTimeout = defaultReadHeader
	}
}

func dedupeMonitors(monitors []MonitorConfig) ([]MonitorConfig, []string, error) {
	if len(monitors) == 0 {
		return nil, nil, nil
	}

	deduped := make([]MonitorConfig, 0, len(monitors))
	indexByID := make(map[string]int, len(monitors))
	duplicateIDs := make([]string, 0)
	duplicateSeen := make(map[string]struct{})

	for _, monitor := range monitors {
		monitor.ID = strings.TrimSpace(monitor.ID)
		if monitor.ID == "" {
			return nil, nil, errors.New("monitor id is required")
		}
		if index, ok := indexByID[monitor.ID]; ok {
			deduped[index] = monitor
			if _, seen := duplicateSeen[monitor.ID]; !seen {
				duplicateSeen[monitor.ID] = struct{}{}
				duplicateIDs = append(duplicateIDs, monitor.ID)
			}
			continue
		}
		indexByID[monitor.ID] = len(deduped)
		deduped = append(deduped, monitor)
	}

	return deduped, duplicateIDs, nil
}

func rewriteConfig(cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := cfg.ConfigFile + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, cfg.ConfigFile)
}

func envString(key string, def string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return def
	}
	return value
}
