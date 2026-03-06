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
			}},
		},
		startedAt: time.Now().UTC(),
		state: ProcmonState{
			StartedAt: time.Now().UTC().Format(time.RFC3339),
			Monitors: map[string]*MonitorRuntimeState{
				"ma352": {
					ID:      "ma352",
					Name:    "MA352",
					Type:    "ma352",
					Enabled: true,
					Status:  "healthy",
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
}
