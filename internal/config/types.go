package config

type Config struct {
	ConfigFile       string          `json:"-"`
	LogFile          string          `json:"log_file"`
	StateFile        string          `json:"state_file"`
	EventsFile       string          `json:"events_file"`
	EventsMaxEntries int             `json:"events_max_entries"`
	API              APIConfig       `json:"api"`
	Monitors         []MonitorConfig `json:"monitors"`
}

type APIConfig struct {
	ListenAddress     string `json:"listen_address"`
	ReadHeaderTimeout string `json:"read_header_timeout"`
}

type TargetConfig struct {
	Transport     string   `json:"transport,omitempty"`
	Host          string   `json:"host,omitempty"`
	FallbackHosts []string `json:"fallback_hosts,omitempty"`
	User          string   `json:"user,omitempty"`
	Port          int      `json:"port,omitempty"`
	IdentityFile  string   `json:"identity_file,omitempty"`
}

type MonitorConfig struct {
	ID               string            `json:"id"`
	Name             string            `json:"name"`
	Type             string            `json:"type"`
	Enabled          *bool             `json:"enabled,omitempty"`
	Interval         string            `json:"interval"`
	FailureThreshold int               `json:"failure_threshold"`
	Cooldown         string            `json:"cooldown"`
	Target           TargetConfig      `json:"target,omitempty"`
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
	DockerCommand           string   `json:"docker_command,omitempty"`
	Since                   string   `json:"since,omitempty"`
	Patterns                []string `json:"patterns,omitempty"`
	SuccessPatterns         []string `json:"success_patterns,omitempty"`
	MatchCountThreshold     int      `json:"match_count_threshold,omitempty"`
	Command                 string   `json:"command,omitempty"`
	Service                 string   `json:"service,omitempty"`
	ExpectedExitCode        int      `json:"expected_exit_code,omitempty"`
	ExpectedOutputPatterns  []string `json:"expected_output_patterns,omitempty"`
	ForbiddenOutputPatterns []string `json:"forbidden_output_patterns,omitempty"`
	MatchAll                bool     `json:"match_all,omitempty"`
}

type ActionConfig struct {
	Name      string `json:"name,omitempty"`
	Type      string `json:"type"`
	Command   string `json:"command,omitempty"`
	Service   string `json:"service,omitempty"`
	Container string `json:"container,omitempty"`
	Duration  string `json:"duration,omitempty"`
}

func (m MonitorConfig) IsEnabled() bool {
	if m.Enabled == nil {
		return true
	}
	return *m.Enabled
}
