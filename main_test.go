package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

type fakeRunner struct {
	responses map[string][]CommandOutcome
	errors    map[string][]error
	counts    map[string]int
}

func (f *fakeRunner) Run(_ context.Context, command string) (CommandOutcome, error) {
	if f.counts == nil {
		f.counts = make(map[string]int)
	}
	index := f.counts[command]
	f.counts[command] = index + 1

	outcomes := f.responses[command]
	var outcome CommandOutcome
	if index < len(outcomes) {
		outcome = outcomes[index]
	} else if len(outcomes) > 0 {
		outcome = outcomes[len(outcomes)-1]
	}

	errs := f.errors[command]
	var err error
	if index < len(errs) {
		err = errs[index]
	} else if len(errs) > 0 {
		err = errs[len(errs)-1]
	}
	return outcome, err
}

func TestLoadConfigFromFile(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{
  "api": {"listen_address":"127.0.0.1:9900"},
  "monitors": [{
    "id":"scrypted-arlo",
    "type":"docker-log",
    "interval":"5m",
    "failure_threshold":2,
    "cooldown":"15m",
    "checks":[{"type":"docker_log_pattern","container":"scrypted","patterns":["Arlo"],"match_count_threshold":4}],
    "recovery":[{"type":"command","command":"docker restart scrypted"}]
  }]
}`), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PROC_CONFIG_FILE", cfgPath)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	if cfg.API.ListenAddress != "127.0.0.1:9900" {
		t.Fatalf("unexpected listen address %q", cfg.API.ListenAddress)
	}
	if len(cfg.Monitors) != 1 || cfg.Monitors[0].ID != "scrypted-arlo" {
		t.Fatalf("unexpected monitors %#v", cfg.Monitors)
	}
}

func TestLoadConfigDeduplicatesMonitorIDs(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{
  "api": {"listen_address":"127.0.0.1:9900"},
  "monitors": [
    {
      "id":"scrypted-arlo",
      "type":"docker-log",
      "interval":"5m",
      "checks":[{"type":"docker_container","container":"old-scrypted"}],
      "recovery":[{"type":"command","command":"docker restart old-scrypted"}]
    },
    {
      "id":"scrypted-arlo",
      "type":"docker-log",
      "interval":"10m",
      "checks":[{"type":"docker_container","container":"scrypted"}],
      "recovery":[{"type":"command","command":"docker restart scrypted"}]
    }
  ]
}`), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PROC_CONFIG_FILE", cfgPath)
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	if len(cfg.Monitors) != 1 {
		t.Fatalf("expected one deduplicated monitor, got %d", len(cfg.Monitors))
	}
	if cfg.Monitors[0].Interval != "10m" {
		t.Fatalf("expected last duplicate definition to win, got interval %q", cfg.Monitors[0].Interval)
	}
	if len(cfg.Monitors[0].Checks) != 1 || cfg.Monitors[0].Checks[0].Container != "scrypted" {
		t.Fatalf("unexpected checks after dedupe: %#v", cfg.Monitors[0].Checks)
	}

	rewritten, err := os.ReadFile(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	var persisted Config
	if err := json.Unmarshal(rewritten, &persisted); err != nil {
		t.Fatalf("rewritten config is invalid JSON: %v", err)
	}
	if len(persisted.Monitors) != 1 {
		t.Fatalf("expected rewritten config to contain one monitor, got %d", len(persisted.Monitors))
	}
}

func TestLegacyConfigFallback(t *testing.T) {
	t.Setenv("PROC_CONFIG_FILE", filepath.Join(t.TempDir(), "missing.json"))
	t.Setenv("PROC_HEALTH_URL", "http://127.0.0.1:5000/health")
	t.Setenv("PROC_REBOOT_ON_HEALTH_FAIL", "true")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig failed: %v", err)
	}
	if len(cfg.Monitors) != 1 {
		t.Fatalf("expected one legacy monitor, got %d", len(cfg.Monitors))
	}
	if cfg.Monitors[0].ID != "ma352" {
		t.Fatalf("unexpected legacy monitor id %q", cfg.Monitors[0].ID)
	}
}

func TestRecoveryFlowUpdatesPerMonitorState(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &fakeRunner{
		responses: map[string][]CommandOutcome{
			"check-service": {
				{Output: "failed", ExitCode: 1},
				{Output: "healthy", ExitCode: 0},
			},
			"restart-service": {
				{Output: "restarted", ExitCode: 0},
			},
		},
		errors: map[string][]error{
			"check-service":   {errors.New("boom"), nil},
			"restart-service": {nil},
		},
	}

	cfg := Config{
		ConfigFile: filepath.Join(tmpDir, "config.json"),
		LogFile:    filepath.Join(tmpDir, "procmon.log"),
		StateFile:  filepath.Join(tmpDir, "state.json"),
		API: APIConfig{
			ListenAddress: "127.0.0.1:9645",
		},
		Monitors: []MonitorConfig{{
			ID:               "svc",
			Name:             "svc",
			Type:             "command",
			Interval:         "1m",
			FailureThreshold: 1,
			Cooldown:         "1m",
			Checks: []CheckConfig{{
				ID:      "check",
				Name:    "check",
				Type:    "command",
				Command: "check-service",
			}},
			Recovery: []ActionConfig{{
				Name:    "restart",
				Type:    "command",
				Command: "restart-service",
			}, {
				Name: "recheck",
				Type: "recheck",
			}},
		}},
	}

	logger, closeLog, err := openLog(cfg.LogFile)
	if err != nil {
		t.Fatal(err)
	}
	defer closeLog()

	manager, err := newManager(cfg, logger, runner)
	if err != nil {
		t.Fatalf("newManager failed: %v", err)
	}

	manager.runMonitorCycle(context.Background(), cfg.Monitors[0], time.Minute)
	monitor, ok := manager.MonitorSnapshot("svc")
	if !ok {
		t.Fatal("monitor state missing")
	}
	if monitor.Status != "healthy" {
		t.Fatalf("expected recovered healthy status, got %q", monitor.Status)
	}
	if monitor.RecoveryCount != 1 {
		t.Fatalf("expected one recovery, got %d", monitor.RecoveryCount)
	}
	if len(monitor.LastRecoveryResults) != 2 {
		t.Fatalf("expected two recovery steps, got %d", len(monitor.LastRecoveryResults))
	}
	if len(monitor.ConfiguredChecks) != 1 {
		t.Fatalf("expected configured checks to be persisted, got %#v", monitor.ConfiguredChecks)
	}
	if len(monitor.ConfiguredRecoveries) != 2 {
		t.Fatalf("expected configured recoveries to be persisted, got %#v", monitor.ConfiguredRecoveries)
	}
}

func TestStatusAPIExposesMonitorState(t *testing.T) {
	enabled := true
	manager := &manager{
		cfg: Config{
			ConfigFile: "/tmp/config.json",
			LogFile:    "/tmp/procmon.log",
			StateFile:  "/tmp/state.json",
			API: APIConfig{
				ListenAddress: "127.0.0.1:9645",
			},
			Monitors: []MonitorConfig{{
				ID:               "ma352",
				Name:             "MA352",
				Type:             "ma352",
				Enabled:          &enabled,
				Interval:         "1m",
				FailureThreshold: 1,
				Cooldown:         "1m",
				Checks: []CheckConfig{{
					ID:   "health",
					Type: "http_json",
					URL:  "http://127.0.0.1:5000/health",
				}},
				Recovery: []ActionConfig{{
					Name:    "restart",
					Type:    "command",
					Command: "systemctl restart ma352-bridge",
				}},
			}},
		},
		startedAt: time.Now().UTC(),
		state: ProcmonState{
			StartedAt: time.Now().UTC().Format(time.RFC3339),
			Monitors: map[string]*MonitorRuntimeState{
				"ma352": {
					ID:                   "ma352",
					Name:                 "MA352",
					Type:                 "ma352",
					Enabled:              true,
					Status:               "healthy",
					ConfiguredChecks:     StoredCheckConfigs{{ID: "health", Type: "http_json", URL: "http://127.0.0.1:5000/health"}},
					ConfiguredRecoveries: StoredActions{{Name: "restart", Type: "command", Command: "systemctl restart ma352-bridge"}},
				},
			},
		},
	}

	server := httptest.NewServer(newAPIServer(manager.cfg, manager).Handler)
	defer server.Close()

	resp, err := http.Get(server.URL + "/monitors/ma352")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var state MonitorRuntimeState
	if err := json.NewDecoder(resp.Body).Decode(&state); err != nil {
		t.Fatal(err)
	}
	if state.ID != "ma352" || state.Status != "healthy" {
		t.Fatalf("unexpected monitor state %#v", state)
	}
	if len(state.ConfiguredChecks) != 1 || state.ConfiguredChecks[0].ID != "health" {
		t.Fatalf("expected configured checks in API response, got %#v", state.ConfiguredChecks)
	}
	if len(state.ConfiguredRecoveries) != 1 || state.ConfiguredRecoveries[0].Name != "restart" {
		t.Fatalf("expected configured recoveries in API response, got %#v", state.ConfiguredRecoveries)
	}
}

func TestHealthAPIReportsServiceUnavailableWhenProcmonIsFailed(t *testing.T) {
	manager := &manager{
		cfg: Config{
			API: APIConfig{
				ListenAddress: "127.0.0.1:9645",
			},
			Monitors: []MonitorConfig{{
				ID:       "svc",
				Name:     "svc",
				Type:     "command",
				Interval: "1m",
				Cooldown: "1m",
			}},
		},
		startedAt: time.Now().UTC(),
		state: ProcmonState{
			StartedAt: time.Now().UTC().Format(time.RFC3339),
			Monitors: map[string]*MonitorRuntimeState{
				"svc": {
					ID:      "svc",
					Name:    "svc",
					Type:    "command",
					Enabled: true,
					Status:  "failed",
				},
			},
		},
	}

	server := httptest.NewServer(newAPIServer(manager.cfg, manager).Handler)
	defer server.Close()

	resp, err := http.Get(server.URL + "/health")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("expected /health status 503, got %d", resp.StatusCode)
	}

	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if ok, _ := body["ok"].(bool); ok {
		t.Fatalf("expected health response ok=false, got %#v", body)
	}
	if alive, _ := body["alive"].(bool); !alive {
		t.Fatalf("expected health response alive=true, got %#v", body)
	}
}

func TestReadProcmonStateAcceptsLegacyConfiguredCountFields(t *testing.T) {
	tmpDir := t.TempDir()
	statePath := filepath.Join(tmpDir, "state.json")
	if err := os.WriteFile(statePath, []byte(`{
  "version": 2,
  "monitors": {
    "svc": {
      "id": "svc",
      "name": "svc",
      "type": "command",
      "enabled": true,
      "status": "healthy",
      "configured_checks": 1,
      "configured_recoveries": 2
    }
  }
}`), 0644); err != nil {
		t.Fatal(err)
	}

	state, err := readProcmonState(statePath)
	if err != nil {
		t.Fatalf("readProcmonState failed: %v", err)
	}
	monitor := state.Monitors["svc"]
	if monitor == nil {
		t.Fatal("expected monitor state to be loaded")
	}
	if len(monitor.ConfiguredChecks) != 0 {
		t.Fatalf("expected legacy configured_checks count to be accepted without retained entries, got %#v", monitor.ConfiguredChecks)
	}
	if len(monitor.ConfiguredRecoveries) != 0 {
		t.Fatalf("expected legacy configured_recoveries count to be accepted without retained entries, got %#v", monitor.ConfiguredRecoveries)
	}
}
