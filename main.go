package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type Config struct {
	ConfigFile string          `json:"-"`
	LogFile    string          `json:"log_file"`
	StateFile  string          `json:"state_file"`
	API        APIConfig       `json:"api"`
	Monitors   []MonitorConfig `json:"monitors"`
}

type APIConfig struct {
	ListenAddress     string `json:"listen_address"`
	ReadHeaderTimeout string `json:"read_header_timeout"`
}

type MonitorConfig struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Type             string            `json:"type"`
	Enabled          *bool             `json:"enabled,omitempty"`
	Interval         string            `json:"interval"`
	FailureThreshold int               `json:"failure_threshold"`
	Cooldown         string            `json:"cooldown"`
	Metadata         map[string]string `json:"metadata,omitempty"`
	Checks           []CheckConfig     `json:"checks"`
	Recovery         []ActionConfig    `json:"recovery"`
}

type CheckConfig struct {
	ID                      string   `json:"id,omitempty"`
	Name                    string   `json:"name,omitempty"`
	Type                    string   `json:"type"`
	URL                     string   `json:"url,omitempty"`
	Timeout                 string   `json:"timeout,omitempty"`
	MaxLatencyMS            int      `json:"max_latency_ms,omitempty"`
	RequireOK               bool     `json:"require_ok,omitempty"`
	RequireSerialConnected  bool     `json:"require_serial_connected,omitempty"`
	MaxLoad1                float64  `json:"max_load1,omitempty"`
	MaxLoadPerCPU           float64  `json:"max_load_per_cpu,omitempty"`
	MaxMemUsedPct           float64  `json:"max_mem_used_pct,omitempty"`
	MaxIOPressureAvg300     float64  `json:"max_io_pressure_avg300,omitempty"`
	Paths                   []string `json:"paths,omitempty"`
	AllowProcesses          []string `json:"allow_processes,omitempty"`
	Container               string   `json:"container,omitempty"`
	Since                   string   `json:"since,omitempty"`
	Patterns                []string `json:"patterns,omitempty"`
	MatchCountThreshold     int      `json:"match_count_threshold,omitempty"`
	Command                 string   `json:"command,omitempty"`
	ExpectedExitCode        int      `json:"expected_exit_code,omitempty"`
	ExpectedOutputPatterns  []string `json:"expected_output_patterns,omitempty"`
	ForbiddenOutputPatterns []string `json:"forbidden_output_patterns,omitempty"`
	MatchAll                bool     `json:"match_all,omitempty"`
}

type ActionConfig struct {
	Name     string `json:"name,omitempty"`
	Type     string `json:"type"`
	Command  string `json:"command,omitempty"`
	Duration string `json:"duration,omitempty"`
}

type ProcmonState struct {
	Version       int                             `json:"version"`
	AppVersion    string                          `json:"app_version"`
	StartedAt     string                          `json:"started_at"`
	LastUpdatedAt string                          `json:"last_updated_at"`
	Monitors      map[string]*MonitorRuntimeState `json:"monitors"`
}

type MonitorRuntimeState struct {
	ID                    string            `json:"id"`
	Name                  string            `json:"name"`
	Type                  string            `json:"type"`
	Enabled               bool              `json:"enabled"`
	Status                string            `json:"status"`
	Interval              string            `json:"interval"`
	FailureThreshold      int               `json:"failure_threshold"`
	Cooldown              string            `json:"cooldown"`
	Metadata              map[string]string `json:"metadata,omitempty"`
	ConfiguredChecks      StoredCheckConfigs `json:"configured_checks"`
	ConfiguredRecoveries  StoredActions      `json:"configured_recoveries"`
	LastCheckStartedAt    string            `json:"last_check_started_at,omitempty"`
	LastCheckFinishedAt   string            `json:"last_check_finished_at,omitempty"`
	LastCheckDurationMS   int64             `json:"last_check_duration_ms,omitempty"`
	NextCheckAt           string            `json:"next_check_at,omitempty"`
	LastSuccessAt         string            `json:"last_success_at,omitempty"`
	LastFailureAt         string            `json:"last_failure_at,omitempty"`
	LastRecoveryAttemptAt string            `json:"last_recovery_attempt_at,omitempty"`
	LastRecoverySuccessAt string            `json:"last_recovery_success_at,omitempty"`
	LastRecoveryFailureAt string            `json:"last_recovery_failure_at,omitempty"`
	CooldownUntil         string            `json:"cooldown_until,omitempty"`
	ConsecutiveFailures   int               `json:"consecutive_failures"`
	CheckRunCount         int               `json:"check_run_count"`
	SuccessCount          int               `json:"success_count"`
	FailureCount          int               `json:"failure_count"`
	RecoveryCount         int               `json:"recovery_count"`
	RecoveryFailureCount  int               `json:"recovery_failure_count"`
	LastError             string            `json:"last_error,omitempty"`
	LastFailureReasons    []string          `json:"last_failure_reasons,omitempty"`
	LastCheckResults      []CheckResult     `json:"last_check_results,omitempty"`
	LastRecoveryResults   []ActionResult    `json:"last_recovery_results,omitempty"`
}

type CheckResult struct {
	ID           string         `json:"id"`
	Name         string         `json:"name"`
	Type         string         `json:"type"`
	Success      bool           `json:"success"`
	Message      string         `json:"message,omitempty"`
	StartedAt    string         `json:"started_at"`
	FinishedAt   string         `json:"finished_at"`
	DurationMS   int64          `json:"duration_ms"`
	Observations map[string]any `json:"observations,omitempty"`
}

type ActionResult struct {
	Name       string `json:"name"`
	Type       string `json:"type"`
	Success    bool   `json:"success"`
	Message    string `json:"message,omitempty"`
	Output     string `json:"output,omitempty"`
	StartedAt  string `json:"started_at"`
	FinishedAt string `json:"finished_at"`
	DurationMS int64  `json:"duration_ms"`
}

// StoredCheckConfigs remains compatible with older state files that stored only a count.
type StoredCheckConfigs []CheckConfig

func (s *StoredCheckConfigs) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*s = nil
		return nil
	}
	if trimmed[0] == '[' {
		var checks []CheckConfig
		if err := json.Unmarshal(trimmed, &checks); err != nil {
			return err
		}
		*s = checks
		return nil
	}
	var ignored int
	if err := json.Unmarshal(trimmed, &ignored); err != nil {
		return err
	}
	*s = nil
	return nil
}

// StoredActions remains compatible with older state files that stored only a count.
type StoredActions []ActionConfig

func (s *StoredActions) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		*s = nil
		return nil
	}
	if trimmed[0] == '[' {
		var actions []ActionConfig
		if err := json.Unmarshal(trimmed, &actions); err != nil {
			return err
		}
		*s = actions
		return nil
	}
	var ignored int
	if err := json.Unmarshal(trimmed, &ignored); err != nil {
		return err
	}
	*s = nil
	return nil
}

type ProcmonStatus struct {
	AppVersion      string                 `json:"app_version"`
	StartedAt       string                 `json:"started_at"`
	UptimeSeconds   int64                  `json:"uptime_seconds"`
	OverallStatus   string                 `json:"overall_status"`
	MonitorCount    int                    `json:"monitor_count"`
	HealthyCount    int                    `json:"healthy_count"`
	DegradedCount   int                    `json:"degraded_count"`
	RecoveringCount int                    `json:"recovering_count"`
	FailedCount     int                    `json:"failed_count"`
	DisabledCount   int                    `json:"disabled_count"`
	ConfigFile      string                 `json:"config_file"`
	StateFile       string                 `json:"state_file"`
	LogFile         string                 `json:"log_file"`
	ListenAddress   string                 `json:"listen_address"`
	LastUpdatedAt   string                 `json:"last_updated_at"`
	Monitors        []*MonitorRuntimeState `json:"monitors"`
}

type healthResponse struct {
	Ok              *bool  `json:"ok"`
	SerialConnected *bool  `json:"serial_connected"`
	Error           string `json:"error"`
}

type procInfo struct {
	pid int
	cmd string
}

type commandRunner interface {
	Run(context.Context, string) (CommandOutcome, error)
}

type CommandOutcome struct {
	Output   string
	ExitCode int
}

type shellRunner struct{}

type manager struct {
	cfg       Config
	logger    io.Writer
	runner    commandRunner
	startedAt time.Time

	mu    sync.RWMutex
	state ProcmonState
}

var appVersion = "dev"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "rpi-procmon: config load failed: %v\n", err)
		os.Exit(1)
	}

	logger, closeLog, err := openLog(cfg.LogFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "rpi-procmon: log open failed: %v\n", err)
		os.Exit(1)
	}
	defer closeLog()

	manager, err := newManager(cfg, logger, shellRunner{})
	if err != nil {
		logf(logger, "startup failed: %v", err)
		os.Exit(1)
	}

	logf(logger, "start: version=%s listen=%s monitors=%d config_file=%s", appVersion, cfg.API.ListenAddress, len(cfg.Monitors), cfg.ConfigFile)

	apiServer := newAPIServer(cfg, manager)
	apiErrCh := make(chan error, 1)
	go func() {
		err := apiServer.ListenAndServe()
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			apiErrCh <- err
			return
		}
		apiErrCh <- nil
	}()

	manager.Start(ctx)

	select {
	case <-ctx.Done():
	case err := <-apiErrCh:
		if err != nil {
			logf(logger, "api server failed: %v", err)
			stop()
		}
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := apiServer.Shutdown(shutdownCtx); err != nil && !errors.Is(err, http.ErrServerClosed) {
		logf(logger, "api shutdown failed: %v", err)
	}
	logf(logger, "stopped")
}

func (m MonitorConfig) isEnabled() bool {
	if m.Enabled == nil {
		return true
	}
	return *m.Enabled
}

func loadConfig() (Config, error) {
	cfgFile := envString("PROC_CONFIG_FILE", "/etc/rpi-procmon/config.json")
	cfg := Config{
		ConfigFile: cfgFile,
		LogFile:    envString("PROC_LOG_FILE", "/var/log/rpi-procmon.log"),
		StateFile:  envString("PROC_STATE_FILE", "/var/lib/rpi-procmon/state.json"),
		API: APIConfig{
			ListenAddress:     envString("PROC_API_LISTEN_ADDR", "127.0.0.1:9645"),
			ReadHeaderTimeout: envString("PROC_API_READ_HEADER_TIMEOUT", "5s"),
		},
	}

	data, err := os.ReadFile(cfgFile)
	if err == nil && strings.TrimSpace(string(data)) != "" {
		var fileCfg Config
		if err := json.Unmarshal(data, &fileCfg); err != nil {
			return Config{}, fmt.Errorf("parse %s: %w", cfgFile, err)
		}
		dedupedMonitors, duplicateIDs, err := dedupeMonitorConfigs(fileCfg.Monitors)
		if err != nil {
			return Config{}, fmt.Errorf("sanitize %s: %w", cfgFile, err)
		}
		if len(duplicateIDs) > 0 {
			fileCfg.Monitors = dedupedMonitors
			fmt.Fprintf(os.Stderr, "rpi-procmon: cleaned duplicate monitor ids in %s: %s\n", cfgFile, strings.Join(duplicateIDs, ", "))
			if err := rewriteConfigFile(cfgFile, fileCfg); err != nil {
				return Config{}, fmt.Errorf("rewrite deduplicated %s: %w", cfgFile, err)
			}
		}
		if strings.TrimSpace(fileCfg.LogFile) != "" {
			cfg.LogFile = fileCfg.LogFile
		}
		if strings.TrimSpace(fileCfg.StateFile) != "" {
			cfg.StateFile = fileCfg.StateFile
		}
		if strings.TrimSpace(fileCfg.API.ListenAddress) != "" {
			cfg.API.ListenAddress = fileCfg.API.ListenAddress
		}
		if strings.TrimSpace(fileCfg.API.ReadHeaderTimeout) != "" {
			cfg.API.ReadHeaderTimeout = fileCfg.API.ReadHeaderTimeout
		}
		cfg.Monitors = dedupedMonitors
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return Config{}, fmt.Errorf("read %s: %w", cfgFile, err)
	}

	if len(cfg.Monitors) == 0 {
		cfg.Monitors = legacyMonitorsFromEnv()
	}
	if len(cfg.Monitors) == 0 {
		return Config{}, errors.New("no monitors configured")
	}
	if err := normalizeConfig(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func normalizeConfig(cfg *Config) error {
	if strings.TrimSpace(cfg.API.ListenAddress) == "" {
		cfg.API.ListenAddress = "127.0.0.1:9645"
	}
	if strings.TrimSpace(cfg.API.ReadHeaderTimeout) == "" {
		cfg.API.ReadHeaderTimeout = "5s"
	}
	for i := range cfg.Monitors {
		mon := &cfg.Monitors[i]
		mon.ID = strings.TrimSpace(mon.ID)
		if mon.ID == "" {
			return errors.New("monitor id is required")
		}
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
		for j := range mon.Checks {
			if strings.TrimSpace(mon.Checks[j].ID) == "" {
				mon.Checks[j].ID = fmt.Sprintf("%s-check-%d", mon.ID, j+1)
			}
			if strings.TrimSpace(mon.Checks[j].Name) == "" {
				mon.Checks[j].Name = mon.Checks[j].ID
			}
		}
		for j := range mon.Recovery {
			if strings.TrimSpace(mon.Recovery[j].Name) == "" {
				mon.Recovery[j].Name = fmt.Sprintf("%s-action-%d", mon.ID, j+1)
			}
		}
	}
	return nil
}

func dedupeMonitorConfigs(monitors []MonitorConfig) ([]MonitorConfig, []string, error) {
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

func rewriteConfigFile(path string, cfg Config) error {
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func legacyMonitorsFromEnv() []MonitorConfig {
	checks := make([]CheckConfig, 0)
	if envBool("PROC_REBOOT_ON_HEALTH_FAIL", true) {
		checks = append(checks, CheckConfig{
			ID:                     "health",
			Name:                   "Health Endpoint",
			Type:                   "http_json",
			URL:                    envString("PROC_HEALTH_URL", "http://127.0.0.1:5000/health"),
			Timeout:                fmt.Sprintf("%ds", envInt("PROC_HEALTH_TIMEOUT_SEC", 3)),
			MaxLatencyMS:           envInt("PROC_MAX_HEALTH_LATENCY_MS", 0),
			RequireOK:              true,
			RequireSerialConnected: envBool("PROC_REQUIRE_SERIAL", false),
		})
	}
	if maxLoad1 := envFloat("PROC_MAX_LOAD1", 0); maxLoad1 > 0 || envFloat("PROC_MAX_LOAD_PER_CPU", 0) > 0 {
		checks = append(checks, CheckConfig{
			ID:            "load",
			Name:          "Load Average",
			Type:          "load",
			MaxLoad1:      maxLoad1,
			MaxLoadPerCPU: envFloat("PROC_MAX_LOAD_PER_CPU", 0),
		})
	}
	if maxMem := envFloat("PROC_MAX_MEM_USED_PCT", 0); maxMem > 0 {
		checks = append(checks, CheckConfig{
			ID:            "memory",
			Name:          "Memory Usage",
			Type:          "memory",
			MaxMemUsedPct: maxMem,
		})
	}
	if maxIO := envFloat("PROC_MAX_IO_PRESSURE_AVG300", 0); maxIO > 0 {
		checks = append(checks, CheckConfig{
			ID:                  "io-pressure",
			Name:                "IO Pressure",
			Type:                "io_pressure",
			MaxIOPressureAvg300: maxIO,
		})
	}
	if paths := envList("PROC_IO_PATHS"); len(paths) > 0 {
		checks = append(checks, CheckConfig{
			ID:             "io-paths",
			Name:           "IO Paths",
			Type:           "io_paths",
			Paths:          paths,
			AllowProcesses: envList("PROC_IO_ALLOW_PROCS"),
		})
	}
	if len(checks) == 0 {
		return nil
	}
	enabled := true
	return []MonitorConfig{{
		ID:               "ma352",
		Name:             "MA352",
		Type:             "ma352",
		Enabled:          &enabled,
		Interval:         envString("PROC_MONITOR_INTERVAL", "1m"),
		FailureThreshold: 1,
		Cooldown:         fmt.Sprintf("%ds", envInt("PROC_MIN_REBOOT_INTERVAL_SEC", 3600)),
		Metadata: map[string]string{
			"source": "legacy-env",
		},
		Checks: checks,
		Recovery: []ActionConfig{{
			Name:    "reboot-host",
			Type:    "command",
			Command: envString("PROC_REBOOT_CMD", "systemctl reboot"),
		}},
	}}
}

func newManager(cfg Config, logger io.Writer, runner commandRunner) (*manager, error) {
	state, err := readProcmonState(cfg.StateFile)
	if err != nil {
		return nil, err
	}
	if state.Monitors == nil {
		state.Monitors = make(map[string]*MonitorRuntimeState)
	}
	if state.StartedAt == "" {
		state.StartedAt = time.Now().UTC().Format(time.RFC3339)
	}
	state.Version = 2
	state.AppVersion = appVersion

	m := &manager{
		cfg:       cfg,
		logger:    logger,
		runner:    runner,
		startedAt: time.Now().UTC(),
		state:     state,
	}

	m.mu.Lock()
	for _, mon := range cfg.Monitors {
		st := m.ensureMonitorStateLocked(mon)
		if !mon.isEnabled() {
			st.Status = "disabled"
		} else if st.Status == "" {
			st.Status = "unknown"
		}
	}
	err = m.persistLocked()
	m.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return m, nil
}

func (m *manager) Start(ctx context.Context) {
	for _, monitor := range m.cfg.Monitors {
		if !monitor.isEnabled() {
			continue
		}
		go m.runMonitorLoop(ctx, monitor)
	}
}

func (m *manager) runMonitorLoop(ctx context.Context, monitor MonitorConfig) {
	interval := parseDuration(monitor.Interval, time.Minute)
	m.runMonitorCycle(ctx, monitor, interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.runMonitorCycle(ctx, monitor, interval)
		}
	}
}

func (m *manager) runMonitorCycle(ctx context.Context, monitor MonitorConfig, interval time.Duration) {
	start := time.Now().UTC()
	m.mu.Lock()
	st := m.ensureMonitorStateLocked(monitor)
	st.Status = "checking"
	st.LastCheckStartedAt = start.Format(time.RFC3339)
	st.NextCheckAt = start.Add(interval).Format(time.RFC3339)
	m.persistOrLogLocked()
	m.mu.Unlock()

	results, reasons := m.evaluateMonitor(ctx, monitor)
	finished := time.Now().UTC()
	durationMS := finished.Sub(start).Milliseconds()

	m.mu.Lock()
	st = m.ensureMonitorStateLocked(monitor)
	st.CheckRunCount++
	st.LastCheckFinishedAt = finished.Format(time.RFC3339)
	st.LastCheckDurationMS = durationMS
	st.LastCheckResults = cloneCheckResults(results)

	if len(reasons) == 0 {
		st.Status = "healthy"
		st.ConsecutiveFailures = 0
		st.SuccessCount++
		st.LastSuccessAt = finished.Format(time.RFC3339)
		st.LastError = ""
		st.LastFailureReasons = nil
		m.persistOrLogLocked()
		m.mu.Unlock()
		logf(m.logger, "monitor check ok: id=%s type=%s duration_ms=%d checks=%d", monitor.ID, monitor.Type, durationMS, len(results))
		return
	}

	st.Status = "degraded"
	st.FailureCount++
	st.ConsecutiveFailures++
	st.LastFailureAt = finished.Format(time.RFC3339)
	st.LastError = joinReasons(reasons)
	st.LastFailureReasons = append([]string(nil), reasons...)
	shouldRecover := st.ConsecutiveFailures >= monitor.FailureThreshold && !cooldownActive(st.CooldownUntil, finished)
	consecutiveFailures := st.ConsecutiveFailures
	m.persistOrLogLocked()
	m.mu.Unlock()
	logf(m.logger, "monitor check failed: id=%s type=%s duration_ms=%d consecutive_failures=%d threshold=%d will_recover=%t reasons=%s", monitor.ID, monitor.Type, durationMS, consecutiveFailures, monitor.FailureThreshold, shouldRecover, joinReasons(reasons))

	if !shouldRecover {
		return
	}

	actionResults, recovered, postResults, postReasons := m.executeRecovery(ctx, monitor)

	m.mu.Lock()
	st = m.ensureMonitorStateLocked(monitor)
	now := time.Now().UTC()
	st.LastRecoveryAttemptAt = now.Format(time.RFC3339)
	st.CooldownUntil = now.Add(parseDuration(monitor.Cooldown, 5*time.Minute)).Format(time.RFC3339)
	st.LastRecoveryResults = cloneActionResults(actionResults)
	if recovered {
		st.Status = "healthy"
		st.ConsecutiveFailures = 0
		st.RecoveryCount++
		st.LastRecoverySuccessAt = now.Format(time.RFC3339)
		st.LastSuccessAt = now.Format(time.RFC3339)
		st.LastError = ""
		st.LastFailureReasons = nil
		if len(postResults) > 0 {
			st.LastCheckResults = cloneCheckResults(postResults)
		}
	} else {
		st.Status = "failed"
		st.RecoveryFailureCount++
		st.LastRecoveryFailureAt = now.Format(time.RFC3339)
		if len(postResults) > 0 {
			st.LastCheckResults = cloneCheckResults(postResults)
		}
		if len(postReasons) > 0 {
			st.LastError = joinReasons(postReasons)
			st.LastFailureReasons = append([]string(nil), postReasons...)
		}
	}
	m.persistOrLogLocked()
	m.mu.Unlock()
	if recovered {
		logf(m.logger, "monitor recovery succeeded: id=%s actions=%d", monitor.ID, len(actionResults))
		return
	}
	if len(postReasons) > 0 {
		logf(m.logger, "monitor recovery failed: id=%s actions=%d reasons=%s", monitor.ID, len(actionResults), joinReasons(postReasons))
		return
	}
	logf(m.logger, "monitor recovery failed: id=%s actions=%d", monitor.ID, len(actionResults))
}

func (m *manager) evaluateMonitor(ctx context.Context, monitor MonitorConfig) ([]CheckResult, []string) {
	results := make([]CheckResult, 0, len(monitor.Checks))
	reasons := make([]string, 0)
	for _, check := range monitor.Checks {
		result := runCheck(ctx, m.runner, monitor, check)
		results = append(results, result)
		if !result.Success {
			reasons = append(reasons, result.Message)
		}
	}
	return results, reasons
}

func (m *manager) executeRecovery(ctx context.Context, monitor MonitorConfig) ([]ActionResult, bool, []CheckResult, []string) {
	results := make([]ActionResult, 0, len(monitor.Recovery))

	m.mu.Lock()
	st := m.ensureMonitorStateLocked(monitor)
	st.Status = "recovering"
	m.persistOrLogLocked()
	m.mu.Unlock()
	logf(m.logger, "monitor recovery start: id=%s actions=%d", monitor.ID, len(monitor.Recovery))

	for _, action := range monitor.Recovery {
		actionResult := runAction(ctx, m.runner, action)
		results = append(results, actionResult)
		logf(m.logger, "monitor recovery action: id=%s action=%s type=%s success=%t message=%s", monitor.ID, actionResult.Name, actionResult.Type, actionResult.Success, actionResult.Message)
		if action.Type == "recheck" {
			checkResults, reasons := m.evaluateMonitor(ctx, monitor)
			if len(reasons) == 0 {
				return results, true, checkResults, nil
			}
			continue
		}
		if !actionResult.Success {
			return results, false, nil, []string{actionResult.Message}
		}
	}

	checkResults, reasons := m.evaluateMonitor(ctx, monitor)
	return results, len(reasons) == 0, checkResults, reasons
}

func (m *manager) Snapshot() ProcmonStatus {
	m.mu.RLock()
	defer m.mu.RUnlock()

	monitors := make([]*MonitorRuntimeState, 0, len(m.cfg.Monitors))
	status := "healthy"
	var healthy, degraded, recovering, failed, disabled int

	for _, cfgMon := range m.cfg.Monitors {
		st, ok := m.state.Monitors[cfgMon.ID]
		if !ok {
			continue
		}
		clone := cloneMonitorState(st)
		monitors = append(monitors, clone)
		switch clone.Status {
		case "healthy":
			healthy++
		case "recovering", "checking":
			recovering++
			if status == "healthy" {
				status = "recovering"
			}
		case "degraded":
			degraded++
			if status == "healthy" {
				status = "degraded"
			}
		case "failed":
			failed++
			status = "failed"
		case "disabled":
			disabled++
		default:
			if status == "healthy" {
				status = "unknown"
			}
		}
	}

	return ProcmonStatus{
		AppVersion:      appVersion,
		StartedAt:       m.state.StartedAt,
		UptimeSeconds:   int64(time.Since(m.startedAt).Seconds()),
		OverallStatus:   status,
		MonitorCount:    len(monitors),
		HealthyCount:    healthy,
		DegradedCount:   degraded,
		RecoveringCount: recovering,
		FailedCount:     failed,
		DisabledCount:   disabled,
		ConfigFile:      m.cfg.ConfigFile,
		StateFile:       m.cfg.StateFile,
		LogFile:         m.cfg.LogFile,
		ListenAddress:   m.cfg.API.ListenAddress,
		LastUpdatedAt:   m.state.LastUpdatedAt,
		Monitors:        monitors,
	}
}

func (m *manager) MonitorSnapshot(id string) (*MonitorRuntimeState, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	st, ok := m.state.Monitors[id]
	if !ok {
		return nil, false
	}
	return cloneMonitorState(st), true
}

func (m *manager) ensureMonitorStateLocked(mon MonitorConfig) *MonitorRuntimeState {
	if st, ok := m.state.Monitors[mon.ID]; ok {
		st.ID = mon.ID
		st.Name = mon.Name
		st.Type = mon.Type
		st.Enabled = mon.isEnabled()
		st.Interval = mon.Interval
		st.FailureThreshold = mon.FailureThreshold
		st.Cooldown = mon.Cooldown
		st.Metadata = cloneStringMap(mon.Metadata)
		st.ConfiguredChecks = cloneStoredCheckConfigs(mon.Checks)
		st.ConfiguredRecoveries = cloneStoredActions(mon.Recovery)
		return st
	}

	st := &MonitorRuntimeState{
		ID:                   mon.ID,
		Name:                 mon.Name,
		Type:                 mon.Type,
		Enabled:              mon.isEnabled(),
		Status:               "unknown",
		Interval:             mon.Interval,
		FailureThreshold:     mon.FailureThreshold,
		Cooldown:             mon.Cooldown,
		Metadata:             cloneStringMap(mon.Metadata),
		ConfiguredChecks:     cloneStoredCheckConfigs(mon.Checks),
		ConfiguredRecoveries: cloneStoredActions(mon.Recovery),
	}
	m.state.Monitors[mon.ID] = st
	return st
}

func (m *manager) persistOrLogLocked() {
	if err := m.persistLocked(); err != nil {
		logf(m.logger, "state persist failed: %v", err)
	}
}

func (m *manager) persistLocked() error {
	m.state.Version = 2
	m.state.AppVersion = appVersion
	m.state.LastUpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return writeProcmonState(m.cfg.StateFile, m.state)
}

func newAPIServer(cfg Config, manager *manager) *http.Server {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		snapshot := manager.Snapshot()
		ok := overallStatusHealthy(snapshot.OverallStatus)
		statusCode := http.StatusOK
		if !ok {
			statusCode = http.StatusServiceUnavailable
		}
		writeJSON(w, statusCode, map[string]any{
			"alive":          true,
			"ok":             ok,
			"overall_status": snapshot.OverallStatus,
			"app_version":    snapshot.AppVersion,
			"uptime_seconds": snapshot.UptimeSeconds,
			"monitor_count":  snapshot.MonitorCount,
		})
	})
	mux.HandleFunc("/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, manager.Snapshot())
	})
	mux.HandleFunc("/monitors", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, manager.Snapshot().Monitors)
	})
	mux.HandleFunc("/monitors/", func(w http.ResponseWriter, r *http.Request) {
		id := strings.TrimPrefix(r.URL.Path, "/monitors/")
		if id == "" {
			http.NotFound(w, r)
			return
		}
		monitor, ok := manager.MonitorSnapshot(id)
		if !ok {
			http.NotFound(w, r)
			return
		}
		writeJSON(w, http.StatusOK, monitor)
	})

	return &http.Server{
		Addr:              cfg.API.ListenAddress,
		Handler:           mux,
		ReadHeaderTimeout: parseDuration(cfg.API.ReadHeaderTimeout, 5*time.Second),
	}
}

func runCheck(ctx context.Context, runner commandRunner, monitor MonitorConfig, check CheckConfig) CheckResult {
	start := time.Now().UTC()
	result := CheckResult{
		ID:        check.ID,
		Name:      check.Name,
		Type:      check.Type,
		StartedAt: start.Format(time.RFC3339),
	}

	success := true
	message := ""
	observations := map[string]any{}

	switch check.Type {
	case "http_json":
		success, message, observations = checkHTTPJSON(check)
	case "load":
		success, message, observations = checkLoad(check)
	case "memory":
		success, message, observations = checkMemory(check)
	case "io_pressure":
		success, message, observations = checkIOPressure(check)
	case "io_paths":
		success, message, observations = checkIOPaths(check)
	case "docker_container":
		success, message, observations = checkDockerContainer(ctx, runner, check)
	case "docker_log_pattern":
		success, message, observations = checkDockerLogPattern(ctx, runner, check)
	case "command":
		success, message, observations = checkCommand(ctx, runner, check)
	default:
		success = false
		message = fmt.Sprintf("unsupported check type %q", check.Type)
	}

	finished := time.Now().UTC()
	result.Success = success
	result.Message = message
	result.FinishedAt = finished.Format(time.RFC3339)
	result.DurationMS = finished.Sub(start).Milliseconds()
	if len(observations) > 0 {
		result.Observations = observations
	}
	if success && result.Message == "" {
		result.Message = fmt.Sprintf("%s ok", monitor.ID)
	}
	return result
}

func checkHTTPJSON(check CheckConfig) (bool, string, map[string]any) {
	timeout := parseDuration(check.Timeout, 3*time.Second)
	client := &http.Client{Timeout: timeout}
	start := time.Now()
	resp, err := client.Get(check.URL)
	if err != nil {
		return false, fmt.Sprintf("health check failed: %v", err), nil
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	latencyMS := time.Since(start).Milliseconds()
	observations := map[string]any{
		"http_status":  resp.StatusCode,
		"latency_ms":   latencyMS,
		"response_len": len(body),
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Sprintf("health HTTP %d", resp.StatusCode), observations
	}
	if check.MaxLatencyMS > 0 && latencyMS > int64(check.MaxLatencyMS) {
		return false, fmt.Sprintf("health latency %dms > %dms", latencyMS, check.MaxLatencyMS), observations
	}
	if !check.RequireOK && !check.RequireSerialConnected {
		return true, "", observations
	}

	var parsed healthResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		return false, fmt.Sprintf("health response invalid JSON: %v", err), observations
	}
	if check.RequireOK && parsed.Ok != nil && !*parsed.Ok {
		if parsed.Error != "" {
			return false, fmt.Sprintf("health not ok: %s", parsed.Error), observations
		}
		return false, "health not ok", observations
	}
	if check.RequireSerialConnected {
		if parsed.SerialConnected == nil {
			return false, "health response missing serial_connected", observations
		}
		if !*parsed.SerialConnected {
			return false, "serial disconnected", observations
		}
	}
	return true, "", observations
}

func checkLoad(check CheckConfig) (bool, string, map[string]any) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return true, "", nil
	}
	fields := strings.Fields(string(data))
	if len(fields) == 0 {
		return true, "", nil
	}
	load1, err := strconv.ParseFloat(fields[0], 64)
	if err != nil {
		return true, "", nil
	}
	threshold := 0.0
	if check.MaxLoad1 > 0 {
		threshold = check.MaxLoad1
	}
	if check.MaxLoadPerCPU > 0 {
		perCPU := check.MaxLoadPerCPU * float64(runtime.NumCPU())
		if threshold == 0 || perCPU < threshold {
			threshold = perCPU
		}
	}
	obs := map[string]any{
		"load1":     load1,
		"threshold": threshold,
	}
	if threshold > 0 && load1 > threshold {
		return false, fmt.Sprintf("load1 %.2f > %.2f", load1, threshold), obs
	}
	return true, "", obs
}

func checkMemory(check CheckConfig) (bool, string, map[string]any) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return true, "", nil
	}
	var totalKB, availableKB float64
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		value, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			totalKB = value
		case "MemAvailable":
			availableKB = value
		}
	}
	if totalKB == 0 {
		return true, "", nil
	}
	usedPct := (1 - (availableKB / totalKB)) * 100
	obs := map[string]any{
		"used_pct":  usedPct,
		"threshold": check.MaxMemUsedPct,
	}
	if check.MaxMemUsedPct > 0 && usedPct > check.MaxMemUsedPct {
		return false, fmt.Sprintf("mem used %.1f%% > %.1f%%", usedPct, check.MaxMemUsedPct), obs
	}
	return true, "", obs
}

func checkIOPressure(check CheckConfig) (bool, string, map[string]any) {
	data, err := os.ReadFile("/proc/pressure/io")
	if err != nil {
		return true, "", nil
	}
	avg300, ok := parsePSIAvg(strings.Split(string(data), "\n"), "some", "avg300")
	if !ok {
		return true, "", nil
	}
	obs := map[string]any{
		"avg300":    avg300,
		"threshold": check.MaxIOPressureAvg300,
	}
	if check.MaxIOPressureAvg300 > 0 && avg300 > check.MaxIOPressureAvg300 {
		return false, fmt.Sprintf("io pressure avg300 %.2f > %.2f", avg300, check.MaxIOPressureAvg300), obs
	}
	return true, "", obs
}

func checkIOPaths(check CheckConfig) (bool, string, map[string]any) {
	for _, path := range check.Paths {
		if err := checkRWAccess(path); err != nil {
			return false, fmt.Sprintf("io access %s: %v", path, err), map[string]any{"path": path}
		}
		if len(check.AllowProcesses) == 0 {
			continue
		}
		resolved := resolvePath(path)
		openers, err := findOpenProcesses([]string{path, resolved})
		if err != nil || len(openers) == 0 {
			continue
		}
		disallowed := filterDisallowed(openers, check.AllowProcesses)
		if len(disallowed) > 0 {
			return false, fmt.Sprintf("io busy %s by %s", path, formatProcs(disallowed)), map[string]any{"path": path}
		}
	}
	return true, "", map[string]any{"paths_checked": len(check.Paths)}
}

func checkDockerContainer(ctx context.Context, runner commandRunner, check CheckConfig) (bool, string, map[string]any) {
	if strings.TrimSpace(check.Container) == "" {
		return false, "docker_container check missing container", nil
	}
	cmd := fmt.Sprintf("docker inspect -f '{{.State.Running}}' %s", shellQuote(check.Container))
	outcome, err := runner.Run(ctx, cmd)
	running := strings.TrimSpace(outcome.Output) == "true"
	obs := map[string]any{
		"container": check.Container,
		"running":   running,
		"exit_code": outcome.ExitCode,
	}
	if err != nil || !running {
		return false, fmt.Sprintf("container %s not running", check.Container), obs
	}
	return true, "", obs
}

func checkDockerLogPattern(ctx context.Context, runner commandRunner, check CheckConfig) (bool, string, map[string]any) {
	if strings.TrimSpace(check.Container) == "" {
		return false, "docker_log_pattern check missing container", nil
	}
	since := check.Since
	if strings.TrimSpace(since) == "" {
		since = "10m"
	}
	cmd := fmt.Sprintf("docker logs --since %s %s 2>&1", shellQuote(since), shellQuote(check.Container))
	outcome, err := runner.Run(ctx, cmd)
	logText := outcome.Output
	if err != nil && strings.TrimSpace(logText) == "" {
		return false, fmt.Sprintf("docker logs failed for %s: %v", check.Container, err), nil
	}
	count, matchedPatterns, regexErr := countPatternMatches(logText, check.Patterns)
	if regexErr != nil {
		return false, regexErr.Error(), nil
	}
	threshold := check.MatchCountThreshold
	if threshold <= 0 {
		threshold = 1
	}
	obs := map[string]any{
		"container":        check.Container,
		"since":            since,
		"match_count":      count,
		"threshold":        threshold,
		"matched_patterns": matchedPatterns,
	}
	if count >= threshold {
		return false, fmt.Sprintf("docker log pattern matched %d times in %s", count, since), obs
	}
	return true, "", obs
}

func checkCommand(ctx context.Context, runner commandRunner, check CheckConfig) (bool, string, map[string]any) {
	if strings.TrimSpace(check.Command) == "" {
		return false, "command check missing command", nil
	}
	outcome, err := runner.Run(ctx, check.Command)
	output := limitString(outcome.Output, 2048)
	obs := map[string]any{
		"exit_code": outcome.ExitCode,
		"output":    output,
	}
	expectedExitCode := check.ExpectedExitCode
	if outcome.ExitCode != expectedExitCode {
		return false, fmt.Sprintf("command exit code %d != %d", outcome.ExitCode, expectedExitCode), obs
	}
	if err != nil && outcome.ExitCode != expectedExitCode {
		return false, fmt.Sprintf("command failed with exit code %d", outcome.ExitCode), obs
	}
	if len(check.ExpectedOutputPatterns) > 0 {
		matched, err := matchPatterns(output, check.ExpectedOutputPatterns, check.MatchAll)
		if err != nil {
			return false, err.Error(), obs
		}
		if !matched {
			return false, "command output missing expected pattern", obs
		}
	}
	if len(check.ForbiddenOutputPatterns) > 0 {
		matched, err := matchPatterns(output, check.ForbiddenOutputPatterns, false)
		if err != nil {
			return false, err.Error(), obs
		}
		if matched {
			return false, "command output matched forbidden pattern", obs
		}
	}
	return true, "", obs
}

func runAction(ctx context.Context, runner commandRunner, action ActionConfig) ActionResult {
	start := time.Now().UTC()
	result := ActionResult{
		Name:      action.Name,
		Type:      action.Type,
		StartedAt: start.Format(time.RFC3339),
	}

	success := true
	message := ""
	output := ""

	switch action.Type {
	case "command":
		if strings.TrimSpace(action.Command) == "" {
			success = false
			message = "command action missing command"
			break
		}
		outcome, err := runner.Run(ctx, action.Command)
		output = limitString(outcome.Output, 2048)
		if err != nil {
			success = false
			message = fmt.Sprintf("command failed with exit code %d", outcome.ExitCode)
		}
	case "sleep":
		duration := parseDuration(action.Duration, 5*time.Second)
		timer := time.NewTimer(duration)
		select {
		case <-ctx.Done():
			timer.Stop()
			success = false
			message = ctx.Err().Error()
		case <-timer.C:
		}
	case "recheck":
		message = "recheck requested"
	default:
		success = false
		message = fmt.Sprintf("unsupported action type %q", action.Type)
	}

	finished := time.Now().UTC()
	result.Success = success
	result.Message = message
	result.Output = output
	result.FinishedAt = finished.Format(time.RFC3339)
	result.DurationMS = finished.Sub(start).Milliseconds()
	return result
}

func (shellRunner) Run(ctx context.Context, command string) (CommandOutcome, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	output, err := cmd.CombinedOutput()
	outcome := CommandOutcome{
		Output: strings.TrimSpace(string(output)),
	}
	if cmd.ProcessState != nil {
		outcome.ExitCode = cmd.ProcessState.ExitCode()
	}
	return outcome, err
}

func readProcmonState(path string) (ProcmonState, error) {
	var st ProcmonState
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ProcmonState{Version: 2, Monitors: make(map[string]*MonitorRuntimeState)}, nil
		}
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, err
	}
	if st.Monitors == nil {
		st.Monitors = make(map[string]*MonitorRuntimeState)
	}
	return st, nil
}

func writeProcmonState(path string, st ProcmonState) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func openLog(path string) (*os.File, func() error, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return nil, nil, err
	}
	file, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, nil, err
	}
	return file, file.Close, nil
}

func writeJSON(w http.ResponseWriter, status int, payload any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(payload)
}

func overallStatusHealthy(status string) bool {
	switch status {
	case "healthy", "recovering":
		return true
	default:
		return false
	}
}

func logf(w io.Writer, format string, args ...interface{}) {
	ts := time.Now().UTC().Format(time.RFC3339)
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(w, "%s %s\n", ts, msg)
}

func envString(key string, def string) string {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return def
	}
	return val
}

func envInt(key string, def int) int {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return def
	}
	parsed, err := strconv.Atoi(val)
	if err != nil {
		return def
	}
	return parsed
}

func envFloat(key string, def float64) float64 {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return def
	}
	parsed, err := strconv.ParseFloat(val, 64)
	if err != nil {
		return def
	}
	return parsed
}

func envBool(key string, def bool) bool {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return def
	}
	if val == "1" || strings.EqualFold(val, "true") || strings.EqualFold(val, "yes") {
		return true
	}
	if val == "0" || strings.EqualFold(val, "false") || strings.EqualFold(val, "no") {
		return false
	}
	return def
}

func envList(key string) []string {
	val := strings.TrimSpace(os.Getenv(key))
	if val == "" {
		return nil
	}
	parts := strings.Split(val, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed == "" {
			continue
		}
		out = append(out, trimmed)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func parseDuration(value string, def time.Duration) time.Duration {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return def
	}
	parsed, err := time.ParseDuration(trimmed)
	if err != nil {
		return def
	}
	return parsed
}

func cooldownActive(until string, now time.Time) bool {
	if strings.TrimSpace(until) == "" {
		return false
	}
	parsed, err := time.Parse(time.RFC3339, until)
	if err != nil {
		return false
	}
	return parsed.After(now)
}

func joinReasons(reasons []string) string {
	return strings.Join(reasons, "; ")
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneCheckResults(in []CheckResult) []CheckResult {
	if len(in) == 0 {
		return nil
	}
	out := make([]CheckResult, len(in))
	for i := range in {
		out[i] = in[i]
		if len(in[i].Observations) > 0 {
			obs := make(map[string]any, len(in[i].Observations))
			for k, v := range in[i].Observations {
				obs[k] = v
			}
			out[i].Observations = obs
		}
	}
	return out
}

func cloneActionResults(in []ActionResult) []ActionResult {
	if len(in) == 0 {
		return nil
	}
	out := make([]ActionResult, len(in))
	copy(out, in)
	return out
}

func cloneStoredCheckConfigs(in []CheckConfig) StoredCheckConfigs {
	if len(in) == 0 {
		return nil
	}
	out := make([]CheckConfig, len(in))
	copy(out, in)
	return StoredCheckConfigs(out)
}

func cloneStoredActions(in []ActionConfig) StoredActions {
	if len(in) == 0 {
		return nil
	}
	out := make([]ActionConfig, len(in))
	copy(out, in)
	return StoredActions(out)
}

func cloneMonitorState(in *MonitorRuntimeState) *MonitorRuntimeState {
	if in == nil {
		return nil
	}
	out := *in
	out.Metadata = cloneStringMap(in.Metadata)
	out.LastFailureReasons = append([]string(nil), in.LastFailureReasons...)
	out.LastCheckResults = cloneCheckResults(in.LastCheckResults)
	out.LastRecoveryResults = cloneActionResults(in.LastRecoveryResults)
	out.ConfiguredChecks = cloneStoredCheckConfigs([]CheckConfig(in.ConfiguredChecks))
	out.ConfiguredRecoveries = cloneStoredActions([]ActionConfig(in.ConfiguredRecoveries))
	return &out
}

func parsePSIAvg(lines []string, prefix string, key string) (float64, bool) {
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || !strings.HasPrefix(line, prefix+" ") {
			continue
		}
		fields := strings.Fields(line)
		for _, field := range fields[1:] {
			parts := strings.SplitN(field, "=", 2)
			if len(parts) != 2 || parts[0] != key {
				continue
			}
			value, err := strconv.ParseFloat(parts[1], 64)
			if err != nil {
				return 0, false
			}
			return value, true
		}
	}
	return 0, false
}

func checkRWAccess(path string) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("empty path")
	}
	if _, err := os.Stat(path); err != nil {
		return err
	}
	return unix.Access(path, unix.R_OK|unix.W_OK)
}

func resolvePath(path string) string {
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || strings.TrimSpace(resolved) == "" {
		return path
	}
	return resolved
}

func findOpenProcesses(paths []string) ([]procInfo, error) {
	matchPaths := make([]string, 0, len(paths))
	for _, path := range paths {
		if strings.TrimSpace(path) == "" {
			continue
		}
		matchPaths = append(matchPaths, path)
	}
	if len(matchPaths) == 0 {
		return nil, errors.New("no paths to match")
	}
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil, err
	}
	found := make([]procInfo, 0)
	seen := make(map[int]struct{})
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(entry.Name())
		if err != nil || pid <= 0 {
			continue
		}
		if _, ok := seen[pid]; ok {
			continue
		}
		fdDir := filepath.Join("/proc", entry.Name(), "fd")
		fds, err := os.ReadDir(fdDir)
		if err != nil {
			continue
		}
		matched := false
		for _, fd := range fds {
			target, err := os.Readlink(filepath.Join(fdDir, fd.Name()))
			if err != nil {
				continue
			}
			if matchesPath(target, matchPaths) {
				matched = true
				break
			}
		}
		if !matched {
			continue
		}
		seen[pid] = struct{}{}
		found = append(found, procInfo{pid: pid, cmd: readProcCmd(pid)})
	}
	return found, nil
}

func matchesPath(target string, paths []string) bool {
	for _, path := range paths {
		if target == path || strings.HasPrefix(target, path+" ") {
			return true
		}
	}
	return false
}

func readProcCmd(pid int) string {
	cmdlinePath := filepath.Join("/proc", strconv.Itoa(pid), "cmdline")
	if data, err := os.ReadFile(cmdlinePath); err == nil && len(data) > 0 {
		parts := strings.Split(string(data), "\x00")
		trimmed := make([]string, 0, len(parts))
		for _, part := range parts {
			if strings.TrimSpace(part) == "" {
				continue
			}
			trimmed = append(trimmed, part)
		}
		if len(trimmed) > 0 {
			return strings.Join(trimmed, " ")
		}
	}
	commPath := filepath.Join("/proc", strconv.Itoa(pid), "comm")
	if data, err := os.ReadFile(commPath); err == nil {
		return strings.TrimSpace(string(data))
	}
	return ""
}

func filterDisallowed(openers []procInfo, allowList []string) []procInfo {
	disallowed := make([]procInfo, 0)
	for _, opener := range openers {
		if !procAllowed(opener, allowList) {
			disallowed = append(disallowed, opener)
		}
	}
	return disallowed
}

func procAllowed(proc procInfo, allowList []string) bool {
	if len(allowList) == 0 {
		return false
	}
	cmd := strings.ToLower(proc.cmd)
	for _, allowed := range allowList {
		if strings.TrimSpace(allowed) == "" {
			continue
		}
		if strings.Contains(cmd, strings.ToLower(allowed)) {
			return true
		}
	}
	return false
}

func formatProcs(procs []procInfo) string {
	parts := make([]string, 0, len(procs))
	for _, proc := range procs {
		if proc.cmd == "" {
			parts = append(parts, fmt.Sprintf("pid=%d", proc.pid))
		} else {
			parts = append(parts, fmt.Sprintf("pid=%d cmd=%s", proc.pid, proc.cmd))
		}
	}
	return strings.Join(parts, ", ")
}

func countPatternMatches(logText string, patterns []string) (int, []string, error) {
	if len(patterns) == 0 {
		return 0, nil, nil
	}
	regexes := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return 0, nil, fmt.Errorf("invalid pattern %q: %w", pattern, err)
		}
		regexes = append(regexes, re)
	}

	scanner := bufio.NewScanner(bytes.NewBufferString(logText))
	matchCount := 0
	matchedSet := make(map[string]struct{})
	for scanner.Scan() {
		line := scanner.Text()
		for idx, re := range regexes {
			if re.MatchString(line) {
				matchCount++
				matchedSet[patterns[idx]] = struct{}{}
				break
			}
		}
	}

	matched := make([]string, 0, len(matchedSet))
	for _, pattern := range patterns {
		if _, ok := matchedSet[pattern]; ok {
			matched = append(matched, pattern)
		}
	}
	return matchCount, matched, nil
}

func matchPatterns(text string, patterns []string, requireAll bool) (bool, error) {
	if len(patterns) == 0 {
		return true, nil
	}
	if requireAll {
		for _, pattern := range patterns {
			re, err := regexp.Compile(pattern)
			if err != nil {
				return false, fmt.Errorf("invalid pattern %q: %w", pattern, err)
			}
			if !re.MatchString(text) {
				return false, nil
			}
		}
		return true, nil
	}
	for _, pattern := range patterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return false, fmt.Errorf("invalid pattern %q: %w", pattern, err)
		}
		if re.MatchString(text) {
			return true, nil
		}
	}
	return false, nil
}

func limitString(value string, max int) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	return value[:max]
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
