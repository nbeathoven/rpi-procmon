package state

import "github.com/nbeathoven/rpi-procmon/internal/config"

const SchemaVersion = 3

type ProcmonState struct {
	Version       int                             `json:"version"`
	AppVersion    string                          `json:"app_version"`
	StartedAt     string                          `json:"started_at"`
	LastUpdatedAt string                          `json:"last_updated_at"`
	Monitors      map[string]*MonitorRuntimeState `json:"monitors"`
}

type MonitorRuntimeState struct {
	ID                    string                `json:"id"`
	Name                  string                `json:"name"`
	Type                  string                `json:"type"`
	Enabled               bool                  `json:"enabled"`
	Status                string                `json:"status"`
	Interval              string                `json:"interval"`
	FailureThreshold      int                   `json:"failure_threshold"`
	Cooldown              string                `json:"cooldown"`
	Target                config.TargetConfig   `json:"target,omitempty"`
	Metadata              map[string]string     `json:"metadata,omitempty"`
	ConfiguredChecks      []config.CheckConfig  `json:"configured_checks"`
	ConfiguredRecoveries  []config.ActionConfig `json:"configured_recoveries"`
	LastCheckStartedAt    string                `json:"last_check_started_at,omitempty"`
	LastCheckFinishedAt   string                `json:"last_check_finished_at,omitempty"`
	LastCheckDurationMS   int64                 `json:"last_check_duration_ms,omitempty"`
	NextCheckAt           string                `json:"next_check_at,omitempty"`
	LastSuccessAt         string                `json:"last_success_at,omitempty"`
	LastFailureAt         string                `json:"last_failure_at,omitempty"`
	LastRecoveryAttemptAt string                `json:"last_recovery_attempt_at,omitempty"`
	LastRecoverySuccessAt string                `json:"last_recovery_success_at,omitempty"`
	LastRecoveryFailureAt string                `json:"last_recovery_failure_at,omitempty"`
	CooldownUntil         string                `json:"cooldown_until,omitempty"`
	ConsecutiveFailures   int                   `json:"consecutive_failures"`
	CheckRunCount         int                   `json:"check_run_count"`
	SuccessCount          int                   `json:"success_count"`
	FailureCount          int                   `json:"failure_count"`
	RecoveryCount         int                   `json:"recovery_count"`
	RecoveryFailureCount  int                   `json:"recovery_failure_count"`
	LastError             string                `json:"last_error,omitempty"`
	LastFailureReasons    []string              `json:"last_failure_reasons,omitempty"`
	LastCheckResults      []CheckResult         `json:"last_check_results,omitempty"`
	LastRecoveryResults   []ActionResult        `json:"last_recovery_results,omitempty"`
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

type ProcmonStatus struct {
	AppVersion        string                 `json:"app_version"`
	StartedAt         string                 `json:"started_at"`
	UptimeSeconds     int64                  `json:"uptime_seconds"`
	OverallStatus     string                 `json:"overall_status"`
	ControlAPIEnabled bool                   `json:"control_api_enabled"`
	MonitorCount      int                    `json:"monitor_count"`
	HealthyCount      int                    `json:"healthy_count"`
	DegradedCount     int                    `json:"degraded_count"`
	RecoveringCount   int                    `json:"recovering_count"`
	FailedCount       int                    `json:"failed_count"`
	DisabledCount     int                    `json:"disabled_count"`
	ConfigFile        string                 `json:"config_file"`
	StateFile         string                 `json:"state_file"`
	EventsFile        string                 `json:"events_file"`
	LogFile           string                 `json:"log_file"`
	ListenAddress     string                 `json:"listen_address"`
	LastUpdatedAt     string                 `json:"last_updated_at"`
	Monitors          []*MonitorRuntimeState `json:"monitors"`
}

func CloneMonitorState(in *MonitorRuntimeState) *MonitorRuntimeState {
	if in == nil {
		return nil
	}
	out := *in
	out.Target = cloneTargetConfig(in.Target)
	out.Metadata = cloneStringMap(in.Metadata)
	out.ConfiguredChecks = cloneCheckConfigs(in.ConfiguredChecks)
	out.ConfiguredRecoveries = cloneActionConfigs(in.ConfiguredRecoveries)
	out.LastFailureReasons = append([]string(nil), in.LastFailureReasons...)
	out.LastCheckResults = CloneCheckResults(in.LastCheckResults)
	out.LastRecoveryResults = CloneActionResults(in.LastRecoveryResults)
	return &out
}

func cloneTargetConfig(in config.TargetConfig) config.TargetConfig {
	out := in
	out.FallbackHosts = append([]string(nil), in.FallbackHosts...)
	return out
}

func CloneCheckResults(in []CheckResult) []CheckResult {
	if len(in) == 0 {
		return nil
	}
	out := make([]CheckResult, len(in))
	for i := range in {
		out[i] = in[i]
		if len(in[i].Observations) > 0 {
			observations := make(map[string]any, len(in[i].Observations))
			for key, value := range in[i].Observations {
				observations[key] = value
			}
			out[i].Observations = observations
		}
	}
	return out
}

func CloneActionResults(in []ActionResult) []ActionResult {
	if len(in) == 0 {
		return nil
	}
	out := make([]ActionResult, len(in))
	copy(out, in)
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]string, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func cloneCheckConfigs(in []config.CheckConfig) []config.CheckConfig {
	if len(in) == 0 {
		return nil
	}
	out := make([]config.CheckConfig, len(in))
	copy(out, in)
	return out
}

func cloneActionConfigs(in []config.ActionConfig) []config.ActionConfig {
	if len(in) == 0 {
		return nil
	}
	out := make([]config.ActionConfig, len(in))
	copy(out, in)
	return out
}
