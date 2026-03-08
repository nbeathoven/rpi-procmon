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

	"github.com/nbeathoven/rpi-procmon/internal/api"
	"github.com/nbeathoven/rpi-procmon/internal/command"
	"github.com/nbeathoven/rpi-procmon/internal/config"
	"github.com/nbeathoven/rpi-procmon/internal/engine"
	"github.com/nbeathoven/rpi-procmon/internal/logging"
	"github.com/nbeathoven/rpi-procmon/internal/state"
)

type fakeRunner struct {
	responses map[string][]command.Outcome
	errors    map[string][]error
	counts    map[string]int
}

func (f *fakeRunner) Run(_ context.Context, command string) (command.Outcome, error) {
	if f.counts == nil {
		f.counts = make(map[string]int)
	}
	index := f.counts[command]
	f.counts[command] = index + 1

	outcomes := f.responses[command]
	var outcome command.Outcome
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
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
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
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
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
	var persisted config.Config
	if err := json.Unmarshal(rewritten, &persisted); err != nil {
		t.Fatalf("rewritten config is invalid JSON: %v", err)
	}
	if len(persisted.Monitors) != 1 {
		t.Fatalf("expected rewritten config to contain one monitor, got %d", len(persisted.Monitors))
	}
}

func TestRecoveryFlowUpdatesPerMonitorState(t *testing.T) {
	tmpDir := t.TempDir()
	runner := &fakeRunner{
		responses: map[string][]command.Outcome{
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

	cfg := config.Config{
		ConfigFile: filepath.Join(tmpDir, "config.json"),
		LogFile:    filepath.Join(tmpDir, "procmon.log"),
		StateFile:  filepath.Join(tmpDir, "state.json"),
		API: config.APIConfig{
			ListenAddress: "127.0.0.1:9645",
		},
		Monitors: []config.MonitorConfig{{
			ID:               "svc",
			Name:             "svc",
			Type:             "command",
			Interval:         "1m",
			FailureThreshold: 1,
			Cooldown:         "1m",
			Checks: []config.CheckConfig{{
				ID:      "check",
				Name:    "check",
				Type:    "command",
				Command: "check-service",
			}},
			Recovery: []config.ActionConfig{{
				Name:    "restart",
				Type:    "command",
				Command: "restart-service",
			}, {
				Name: "recheck",
				Type: "recheck",
			}},
		}},
	}

	logger, closeLog, err := logging.Open(cfg.LogFile)
	if err != nil {
		t.Fatal(err)
	}
	defer closeLog()

	manager, err := engine.NewManager(cfg, logger, runner, "test")
	if err != nil {
		t.Fatalf("engine.NewManager failed: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	manager.Start(ctx)

	time.Sleep(50 * time.Millisecond)
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

func TestLoadConfigRejectsUnsupportedCheckTypes(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{
  "monitors": [{
    "id":"svc",
    "interval":"1m",
    "failure_threshold":1,
    "cooldown":"1m",
    "checks":[{"type":"nope"}],
    "recovery":[{"type":"command","command":"true"}]
  }]
}`), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PROC_CONFIG_FILE", cfgPath)
	if _, err := config.Load(); err == nil {
		t.Fatal("expected unsupported check type to fail validation")
	}
}

func TestStatusAPIExposesMonitorState(t *testing.T) {
	enabled := true
	cfg := config.Config{
		ConfigFile: "/tmp/config.json",
		LogFile:    "/tmp/procmon.log",
		StateFile:  "/tmp/state.json",
		API: config.APIConfig{
			ListenAddress: "127.0.0.1:9645",
		},
		Monitors: []config.MonitorConfig{{
			ID:               "ma352",
			Name:             "MA352",
			Type:             "ma352",
			Enabled:          &enabled,
			Interval:         "1m",
			FailureThreshold: 1,
			Cooldown:         "1m",
			Checks: []config.CheckConfig{{
				ID:   "health",
				Type: "http_json",
				URL:  "http://127.0.0.1:5000/health",
			}},
			Recovery: []config.ActionConfig{{
				Name:    "restart",
				Type:    "command",
				Command: "systemctl restart ma352-bridge",
			}},
		}},
	}
	manager := &stubProvider{
		snapshot: state.ProcmonStatus{
			AppVersion:    "test",
			OverallStatus: "healthy",
			Monitors: []*state.MonitorRuntimeState{{
				ID:                   "ma352",
				Name:                 "MA352",
				Type:                 "ma352",
				Enabled:              true,
				Status:               "healthy",
				ConfiguredChecks:     []config.CheckConfig{{ID: "health", Type: "http_json", URL: "http://127.0.0.1:5000/health"}},
				ConfiguredRecoveries: []config.ActionConfig{{Name: "restart", Type: "command", Command: "systemctl restart ma352-bridge"}},
			}},
		},
		monitor: &state.MonitorRuntimeState{
			ID:                   "ma352",
			Name:                 "MA352",
			Type:                 "ma352",
			Enabled:              true,
			Status:               "healthy",
			ConfiguredChecks:     []config.CheckConfig{{ID: "health", Type: "http_json", URL: "http://127.0.0.1:5000/health"}},
			ConfiguredRecoveries: []config.ActionConfig{{Name: "restart", Type: "command", Command: "systemctl restart ma352-bridge"}},
		},
	}

	server := httptest.NewServer(api.NewServer(cfg, manager).Handler)
	defer server.Close()

	resp, err := http.Get(server.URL + "/monitors/ma352")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var monitor state.MonitorRuntimeState
	if err := json.NewDecoder(resp.Body).Decode(&monitor); err != nil {
		t.Fatal(err)
	}
	if monitor.ID != "ma352" || monitor.Status != "healthy" {
		t.Fatalf("unexpected monitor state %#v", monitor)
	}
	if len(monitor.ConfiguredChecks) != 1 || monitor.ConfiguredChecks[0].ID != "health" {
		t.Fatalf("expected configured checks in API response, got %#v", monitor.ConfiguredChecks)
	}
	if len(monitor.ConfiguredRecoveries) != 1 || monitor.ConfiguredRecoveries[0].Name != "restart" {
		t.Fatalf("expected configured recoveries in API response, got %#v", monitor.ConfiguredRecoveries)
	}
}

func TestHealthAPIReportsServiceUnavailableWhenProcmonIsFailed(t *testing.T) {
	cfg := config.Config{
		API: config.APIConfig{
			ListenAddress: "127.0.0.1:9645",
		},
	}
	manager := &stubProvider{
		snapshot: state.ProcmonStatus{
			AppVersion:    "test",
			OverallStatus: "failed",
			MonitorCount:  1,
		},
	}

	server := httptest.NewServer(api.NewServer(cfg, manager).Handler)
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

type stubProvider struct {
	snapshot state.ProcmonStatus
	monitor  *state.MonitorRuntimeState
}

func (s *stubProvider) Snapshot() state.ProcmonStatus {
	return s.snapshot
}

func (s *stubProvider) MonitorSnapshot(id string) (*state.MonitorRuntimeState, bool) {
	if s.monitor == nil || s.monitor.ID != id {
		return nil, false
	}
	return state.CloneMonitorState(s.monitor), true
}
