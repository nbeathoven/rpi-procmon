package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/nbeathoven/rpi-procmon/internal/api"
	"github.com/nbeathoven/rpi-procmon/internal/command"
	"github.com/nbeathoven/rpi-procmon/internal/config"
	"github.com/nbeathoven/rpi-procmon/internal/engine"
	"github.com/nbeathoven/rpi-procmon/internal/events"
	"github.com/nbeathoven/rpi-procmon/internal/logging"
	"github.com/nbeathoven/rpi-procmon/internal/state"
)

type fakeRunner struct {
	responses map[string][]command.Outcome
	errors    map[string][]error
	counts    map[string]int
}

func (f *fakeRunner) Run(_ context.Context, cmd string) (command.Outcome, error) {
	if f.counts == nil {
		f.counts = make(map[string]int)
	}
	index := f.counts[cmd]
	f.counts[cmd] = index + 1

	outcomes := f.responses[cmd]
	var outcome command.Outcome
	if index < len(outcomes) {
		outcome = outcomes[index]
	} else if len(outcomes) > 0 {
		outcome = outcomes[len(outcomes)-1]
	}

	errs := f.errors[cmd]
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
	if cfg.EventsFile != "/var/lib/rpi-procmon/events.json" {
		t.Fatalf("unexpected events file %q", cfg.EventsFile)
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
		EventsFile: filepath.Join(tmpDir, "events.json"),
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
	eventLog := manager.EventsSnapshot(events.Filter{MonitorID: "svc", Limit: 10})
	if len(eventLog) != 3 {
		t.Fatalf("expected three emitted events, got %#v", eventLog)
	}
	if eventLog[0].EventType != "recovery_succeeded" {
		t.Fatalf("expected latest event to be recovery_succeeded, got %#v", eventLog[0])
	}
	if eventLog[1].EventType != "recovery_started" {
		t.Fatalf("expected middle event to be recovery_started, got %#v", eventLog[1])
	}
	if eventLog[2].EventType != "check_failed" {
		t.Fatalf("expected oldest event to be check_failed, got %#v", eventLog[2])
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

func TestLoadConfigSupportsSSHTargetAndSystemdTypes(t *testing.T) {
	tmpDir := t.TempDir()
	cfgPath := filepath.Join(tmpDir, "config.json")
	if err := os.WriteFile(cfgPath, []byte(`{
  "monitors": [{
    "id":"ma352",
    "name":"MA352 Bridge",
    "type":"systemd-service",
    "interval":"1m",
    "failure_threshold":1,
    "cooldown":"15m",
    "target":{"transport":"ssh","host":"Epcilon.local","fallback_hosts":["192.168.5.163"],"user":"nima","port":22,"identity_file":"/home/nima/.ssh/id_ed25519"},
    "checks":[
      {"id":"svc","type":"systemd_service","service":"ma352-bridge"},
      {"id":"health","type":"http_json","url":"http://192.168.5.163:5000/health","require_ok":true,"require_serial_connected":true}
    ],
    "recovery":[
      {"name":"restart","type":"restart_systemd_service","service":"ma352-bridge"},
      {"name":"recheck","type":"recheck"}
    ]
  }]
}`), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("PROC_CONFIG_FILE", cfgPath)
	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("config.Load failed: %v", err)
	}

	monitor := cfg.Monitors[0]
	if monitor.Target.Transport != "ssh" || monitor.Target.Host != "Epcilon.local" || len(monitor.Target.FallbackHosts) != 1 || monitor.Target.FallbackHosts[0] != "192.168.5.163" {
		t.Fatalf("unexpected target %#v", monitor.Target)
	}
	if len(monitor.Checks) != 2 || monitor.Checks[0].Type != "systemd_service" {
		t.Fatalf("unexpected checks %#v", monitor.Checks)
	}
	if len(monitor.Recovery) != 2 || monitor.Recovery[0].Type != "restart_systemd_service" {
		t.Fatalf("unexpected recovery %#v", monitor.Recovery)
	}
}

func TestBuildSystemdCommandsRespectTargetTransport(t *testing.T) {
	localCheck := command.BuildSystemdIsActiveCommand(config.TargetConfig{}, "ma352-bridge")
	if localCheck != "systemctl is-active --quiet 'ma352-bridge'" {
		t.Fatalf("unexpected local check command %q", localCheck)
	}

	sshTarget := config.TargetConfig{
		Transport:     "ssh",
		Host:          "Epcilon.local",
		FallbackHosts: []string{"192.168.5.163"},
		User:          "nima",
		Port:          22,
		IdentityFile:  "/home/nima/.ssh/id_ed25519",
	}

	remoteCheck := command.BuildSystemdIsActiveCommand(sshTarget, "ma352-bridge")
	if !strings.Contains(remoteCheck, "ssh") || !strings.Contains(remoteCheck, "nima@Epcilon.local") || !strings.Contains(remoteCheck, "nima@192.168.5.163") || !strings.Contains(remoteCheck, "systemctl is-active --quiet") {
		t.Fatalf("unexpected remote check command %q", remoteCheck)
	}

	remoteRestart := command.BuildSystemdRestartCommand(sshTarget, "ma352-bridge")
	if !strings.Contains(remoteRestart, "sudo -n systemctl restart") || !strings.Contains(remoteRestart, "nima@Epcilon.local") || !strings.Contains(remoteRestart, "nima@192.168.5.163") {
		t.Fatalf("unexpected remote restart command %q", remoteRestart)
	}
}

func TestRecoveryFlowSupportsRestartSystemdService(t *testing.T) {
	tmpDir := t.TempDir()
	sshTarget := config.TargetConfig{
		Transport:    "ssh",
		Host:         "192.168.5.163",
		User:         "nima",
		Port:         22,
		IdentityFile: "/home/nima/.ssh/id_ed25519",
	}
	checkCommand := command.BuildSystemdIsActiveCommand(sshTarget, "ma352-bridge")
	restartCommand := command.BuildSystemdRestartCommand(sshTarget, "ma352-bridge")

	runner := &fakeRunner{
		responses: map[string][]command.Outcome{
			checkCommand: {
				{Output: "inactive", ExitCode: 3},
				{Output: "active", ExitCode: 0},
			},
			restartCommand: {
				{Output: "", ExitCode: 0},
			},
		},
		errors: map[string][]error{
			checkCommand:   {errors.New("inactive"), nil},
			restartCommand: {nil},
		},
	}

	cfg := config.Config{
		ConfigFile: filepath.Join(tmpDir, "config.json"),
		LogFile:    filepath.Join(tmpDir, "procmon.log"),
		StateFile:  filepath.Join(tmpDir, "state.json"),
		EventsFile: filepath.Join(tmpDir, "events.json"),
		API: config.APIConfig{
			ListenAddress: "127.0.0.1:9645",
		},
		Monitors: []config.MonitorConfig{{
			ID:               "ma352",
			Name:             "MA352 Bridge",
			Type:             "systemd-service",
			Interval:         "1m",
			FailureThreshold: 1,
			Cooldown:         "1m",
			Target:           sshTarget,
			Checks: []config.CheckConfig{{
				ID:      "service",
				Name:    "service",
				Type:    "systemd_service",
				Service: "ma352-bridge",
			}},
			Recovery: []config.ActionConfig{{
				Name:    "restart",
				Type:    "restart_systemd_service",
				Service: "ma352-bridge",
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
	monitor, ok := manager.MonitorSnapshot("ma352")
	if !ok {
		t.Fatal("monitor state missing")
	}
	if monitor.Status != "healthy" {
		t.Fatalf("expected recovered healthy status, got %q", monitor.Status)
	}
	if monitor.Target.Transport != "ssh" || monitor.Target.Host != "192.168.5.163" {
		t.Fatalf("expected target to persist, got %#v", monitor.Target)
	}
	if monitor.RecoveryCount != 1 {
		t.Fatalf("expected one recovery, got %d", monitor.RecoveryCount)
	}
}

func TestDockerLogSuccessSupersedesEarlierFailures(t *testing.T) {
	tmpDir := t.TempDir()
	logCommand := "docker logs --timestamps --since '30m' 'scrypted' 2>&1"
	runner := &fakeRunner{
		responses: map[string][]command.Outcome{
			logCommand: {{
				Output: strings.Join([]string{
					"2026-03-29T01:50:13Z [Arlo Provider]: Error discovering devices: Arlo client not connected, cannot discover devices",
					"2026-03-29T01:50:14Z [Arlo Provider]: Error during periodic device discovery: Arlo client not connected, cannot discover devices",
					"2026-03-29T02:02:48Z [Arlo Client]: Arlo Cloud login successful.",
					"2026-03-29T02:02:50Z [Arlo Provider]: Subscribed to Arlo event stream successfully.",
					"2026-03-29T02:02:56Z [Arlo Provider]: Arlo plugin initialized.",
				}, "\n"),
				ExitCode: 0,
			}},
		},
	}

	cfg := config.Config{
		ConfigFile: filepath.Join(tmpDir, "config.json"),
		LogFile:    filepath.Join(tmpDir, "procmon.log"),
		StateFile:  filepath.Join(tmpDir, "state.json"),
		EventsFile: filepath.Join(tmpDir, "events.json"),
		API: config.APIConfig{
			ListenAddress: "127.0.0.1:9645",
		},
		Monitors: []config.MonitorConfig{{
			ID:               "scrypted-arlo",
			Name:             "Scrypted Arlo",
			Type:             "docker-log",
			Interval:         "1m",
			FailureThreshold: 1,
			Cooldown:         "1m",
			Checks: []config.CheckConfig{{
				ID:                  "arlo-log-errors",
				Name:                "arlo-log-errors",
				Type:                "docker_log_pattern",
				Container:           "scrypted",
				Since:               "30m",
				MatchCountThreshold: 2,
				Patterns: []string{
					"Error discovering devices: Arlo client not connected, cannot discover devices",
					"Error during periodic device discovery: Arlo client not connected, cannot discover devices",
				},
				SuccessPatterns: []string{
					"Arlo Cloud login successful\\.",
					"Subscribed to Arlo event stream successfully\\.",
					"Arlo plugin initialized\\.",
				},
			}},
			Recovery: []config.ActionConfig{{
				Name:    "noop",
				Type:    "command",
				Command: "true",
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
	monitor, ok := manager.MonitorSnapshot("scrypted-arlo")
	if !ok {
		t.Fatal("monitor state missing")
	}
	if monitor.Status != "healthy" {
		t.Fatalf("expected healthy status after later success, got %q", monitor.Status)
	}
	if len(monitor.LastCheckResults) != 1 {
		t.Fatalf("expected one check result, got %#v", monitor.LastCheckResults)
	}
	if got := monitor.LastCheckResults[0].Observations["superseded_by_success"]; got != true {
		t.Fatalf("expected superseded_by_success=true, got %#v", monitor.LastCheckResults[0].Observations)
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
			Target: config.TargetConfig{
				Transport: "ssh",
				Host:      "192.168.5.163",
				User:      "nima",
				Port:      22,
			},
			Checks: []config.CheckConfig{{
				ID:      "service",
				Type:    "systemd_service",
				Service: "ma352-bridge",
			}},
			Recovery: []config.ActionConfig{{
				Name:    "restart",
				Type:    "restart_systemd_service",
				Service: "ma352-bridge",
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
				Target:               config.TargetConfig{Transport: "ssh", Host: "192.168.5.163", User: "nima", Port: 22},
				ConfiguredChecks:     []config.CheckConfig{{ID: "service", Type: "systemd_service", Service: "ma352-bridge"}},
				ConfiguredRecoveries: []config.ActionConfig{{Name: "restart", Type: "restart_systemd_service", Service: "ma352-bridge"}},
			}},
		},
		monitor: &state.MonitorRuntimeState{
			ID:                   "ma352",
			Name:                 "MA352",
			Type:                 "ma352",
			Enabled:              true,
			Status:               "healthy",
			Target:               config.TargetConfig{Transport: "ssh", Host: "192.168.5.163", User: "nima", Port: 22},
			ConfiguredChecks:     []config.CheckConfig{{ID: "service", Type: "systemd_service", Service: "ma352-bridge"}},
			ConfiguredRecoveries: []config.ActionConfig{{Name: "restart", Type: "restart_systemd_service", Service: "ma352-bridge"}},
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
	if len(monitor.ConfiguredChecks) != 1 || monitor.ConfiguredChecks[0].ID != "service" {
		t.Fatalf("expected configured checks in API response, got %#v", monitor.ConfiguredChecks)
	}
	if len(monitor.ConfiguredRecoveries) != 1 || monitor.ConfiguredRecoveries[0].Name != "restart" {
		t.Fatalf("expected configured recoveries in API response, got %#v", monitor.ConfiguredRecoveries)
	}
	if monitor.Target.Transport != "ssh" || monitor.Target.Host != "192.168.5.163" {
		t.Fatalf("expected target metadata in API response, got %#v", monitor.Target)
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

func TestEventsAPIExposesRecentHistory(t *testing.T) {
	cfg := config.Config{
		API: config.APIConfig{
			ListenAddress: "127.0.0.1:9645",
		},
	}
	manager := &stubProvider{
		events: []events.Event{
			{
				ID:          "ma352-1",
				Timestamp:   "2026-03-08T00:00:00Z",
				MonitorID:   "ma352",
				MonitorName: "MA352",
				EventType:   "check_failed",
				StatusAfter: "degraded",
			},
			{
				ID:          "ma352-2",
				Timestamp:   "2026-03-08T00:01:00Z",
				MonitorID:   "ma352",
				MonitorName: "MA352",
				EventType:   "recovery_succeeded",
				StatusAfter: "healthy",
			},
			{
				ID:          "homebridge-1",
				Timestamp:   "2026-03-08T00:02:00Z",
				MonitorID:   "homebridge",
				MonitorName: "Homebridge",
				EventType:   "check_failed",
				StatusAfter: "degraded",
			},
		},
	}

	server := httptest.NewServer(api.NewServer(cfg, manager).Handler)
	defer server.Close()

	resp, err := http.Get(server.URL + "/events?monitor_id=ma352&limit=2")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	var body struct {
		Events   []events.Event `json:"events"`
		Returned int            `json:"returned"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body.Returned != 2 {
		t.Fatalf("expected returned=2, got %#v", body)
	}
	if len(body.Events) != 2 {
		t.Fatalf("expected two events, got %#v", body.Events)
	}
	if body.Events[0].ID != "ma352-2" || body.Events[1].ID != "ma352-1" {
		t.Fatalf("expected reverse chronological order, got %#v", body.Events)
	}
}

func TestUIPageIsServedFromAPI(t *testing.T) {
	cfg := config.Config{
		API: config.APIConfig{
			ListenAddress: "127.0.0.1:9645",
		},
	}
	manager := &stubProvider{}

	server := httptest.NewServer(api.NewServer(cfg, manager).Handler)
	defer server.Close()

	resp, err := http.Get(server.URL + "/ui/")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected /ui/ status 200, got %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "Procmon Control Surface") {
		t.Fatalf("expected UI HTML to be served, got %q", string(body))
	}
}

func TestStatusAPIAllowsCrossOriginReads(t *testing.T) {
	cfg := config.Config{
		API: config.APIConfig{
			ListenAddress: "127.0.0.1:9645",
		},
	}
	manager := &stubProvider{
		snapshot: state.ProcmonStatus{
			OverallStatus: "healthy",
		},
	}

	server := httptest.NewServer(api.NewServer(cfg, manager).Handler)
	defer server.Close()

	req, err := http.NewRequest(http.MethodGet, server.URL+"/status", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Origin", "file://")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "*" {
		t.Fatalf("expected Access-Control-Allow-Origin=*, got %q", got)
	}
}

type stubProvider struct {
	snapshot state.ProcmonStatus
	monitor  *state.MonitorRuntimeState
	events   []events.Event
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

func (s *stubProvider) EventsSnapshot(filter events.Filter) []events.Event {
	limit := filter.Limit
	if limit <= 0 {
		limit = 50
	}
	result := make([]events.Event, 0, limit)
	for i := len(s.events) - 1; i >= 0; i-- {
		event := s.events[i]
		if filter.MonitorID != "" && event.MonitorID != filter.MonitorID {
			continue
		}
		if filter.EventType != "" && event.EventType != filter.EventType {
			continue
		}
		if !filter.Since.IsZero() {
			timestamp, err := time.Parse(time.RFC3339, event.Timestamp)
			if err != nil || timestamp.Before(filter.Since) {
				continue
			}
		}
		result = append(result, events.CloneEvent(event))
		if len(result) >= limit {
			break
		}
	}
	return result
}
