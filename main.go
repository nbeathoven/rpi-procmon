package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"golang.org/x/sys/unix"
)

type config struct {
	healthURL             string
	healthTimeoutSec      int
	maxHealthLatencyMs    int
	requireSerial         bool
	maxLoad1              float64
	maxLoadPerCPU         float64
	maxMemUsedPct         float64
	maxIOPressureAvg300   float64
	ioPaths               []string
	ioAllowProcs          []string
	rebootCmd             string
	logFile               string
	stateFile             string
	minRebootIntervalSec  int
	rebootOnHealthFailure bool
}

// appVersion is set at build time with:
//
//	go build -ldflags "-X main.appVersion=1.2.3"
var appVersion = "dev"

type state struct {
	RebootCount  int    `json:"reboot_count"`
	LastRebootAt string `json:"last_reboot_at"`
	LastReason   string `json:"last_reason"`
}

type healthResponse struct {
	Ok              *bool  `json:"ok"`
	SerialConnected *bool  `json:"serial_connected"`
	Error           string `json:"error"`
}

func main() {
	cfg := loadConfig()
	logger, closeLog, err := openLog(cfg.logFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "procmon: log open failed: %v\n", err)
		os.Exit(1)
	}
	defer closeLog()

	logf(logger, "start: version=%s", appVersion)

	st, _ := readState(cfg.stateFile)
	var reasons []string

	if cfg.healthURL != "" && cfg.rebootOnHealthFailure {
		ok, reason := checkHealth(cfg)
		if !ok {
			reasons = append(reasons, reason)
		}
	}

	if cfg.maxLoad1 > 0 || cfg.maxLoadPerCPU > 0 {
		ok, reason := checkLoad(cfg)
		if !ok {
			reasons = append(reasons, reason)
		}
	}

	if cfg.maxMemUsedPct > 0 {
		ok, reason := checkMem(cfg)
		if !ok {
			reasons = append(reasons, reason)
		}
	}

	if cfg.maxIOPressureAvg300 > 0 {
		ok, reason := checkIOPressure(cfg)
		if !ok {
			reasons = append(reasons, reason)
		}
	}

	if len(cfg.ioPaths) > 0 {
		ok, reason := checkIO(cfg)
		if !ok {
			reasons = append(reasons, reason)
		}
	}

	if len(reasons) == 0 {
		logf(logger, "ok: no issues detected")
		return
	}

	reason := strings.Join(reasons, "; ")
	if cfg.minRebootIntervalSec > 0 {
		if last, err := parseTime(st.LastRebootAt); err == nil {
			since := time.Since(last)
			if since < time.Duration(cfg.minRebootIntervalSec)*time.Second {
				logf(logger, "reboot suppressed (cooldown %ds): %s", cfg.minRebootIntervalSec, reason)
				return
			}
		}
	}

	if err := triggerReboot(cfg, logger, st, reason); err != nil {
		logf(logger, "reboot command failed: %v", err)
		os.Exit(1)
	}
}

func loadConfig() config {
	cfg := config{}
	cfg.healthURL = envString("PROC_HEALTH_URL", "http://127.0.0.1:5000/health")
	cfg.healthTimeoutSec = envInt("PROC_HEALTH_TIMEOUT_SEC", 3)
	cfg.maxHealthLatencyMs = envInt("PROC_MAX_HEALTH_LATENCY_MS", 0)
	cfg.requireSerial = envBool("PROC_REQUIRE_SERIAL", false)
	cfg.maxLoad1 = envFloat("PROC_MAX_LOAD1", 0)
	cfg.maxLoadPerCPU = envFloat("PROC_MAX_LOAD_PER_CPU", 0)
	cfg.maxMemUsedPct = envFloat("PROC_MAX_MEM_USED_PCT", 0)
	cfg.maxIOPressureAvg300 = envFloat("PROC_MAX_IO_PRESSURE_AVG300", 0)
	cfg.ioPaths = envList("PROC_IO_PATHS")
	cfg.ioAllowProcs = envList("PROC_IO_ALLOW_PROCS")
	cfg.rebootCmd = envString("PROC_REBOOT_CMD", "systemctl reboot")
	cfg.logFile = envString("PROC_LOG_FILE", "/var/log/ma352-procmon.log")
	cfg.stateFile = envString("PROC_STATE_FILE", "/var/lib/ma352-procmon/state.json")
	cfg.minRebootIntervalSec = envInt("PROC_MIN_REBOOT_INTERVAL_SEC", 3600)
	cfg.rebootOnHealthFailure = envBool("PROC_REBOOT_ON_HEALTH_FAIL", true)
	return cfg
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

func logf(w io.Writer, format string, args ...interface{}) {
	ts := time.Now().UTC().Format(time.RFC3339)
	msg := fmt.Sprintf(format, args...)
	fmt.Fprintf(w, "%s %s\n", ts, msg)
}

func readState(path string) (state, error) {
	var st state
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return st, nil
		}
		return st, err
	}
	if err := json.Unmarshal(data, &st); err != nil {
		return st, err
	}
	return st, nil
}

func writeState(path string, st state) error {
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

func parseTime(value string) (time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return time.Time{}, errors.New("empty time")
	}
	return time.Parse(time.RFC3339, value)
}

func checkHealth(cfg config) (bool, string) {
	client := &http.Client{Timeout: time.Duration(cfg.healthTimeoutSec) * time.Second}
	start := time.Now()
	resp, err := client.Get(cfg.healthURL)
	if err != nil {
		return false, fmt.Sprintf("health check failed: %v", err)
	}
	defer resp.Body.Close()
	latencyMs := int(time.Since(start) / time.Millisecond)

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return false, fmt.Sprintf("health HTTP %d", resp.StatusCode)
	}

	if cfg.maxHealthLatencyMs > 0 && latencyMs > cfg.maxHealthLatencyMs {
		return false, fmt.Sprintf("health latency %dms > %dms", latencyMs, cfg.maxHealthLatencyMs)
	}

	var parsed healthResponse
	if err := json.Unmarshal(body, &parsed); err != nil {
		if cfg.requireSerial {
			return false, fmt.Sprintf("health response invalid JSON: %v", err)
		}
		return true, ""
	}

	if parsed.Ok != nil && !*parsed.Ok {
		if parsed.Error != "" {
			return false, fmt.Sprintf("health not ok: %s", parsed.Error)
		}
		return false, "health not ok"
	}

	if cfg.requireSerial {
		if parsed.SerialConnected == nil {
			return false, "health response missing serial_connected"
		}
		if !*parsed.SerialConnected {
			return false, "serial disconnected"
		}
	}

	return true, ""
}

func triggerReboot(cfg config, logger io.Writer, st state, reason string) error {
	nextCount := st.RebootCount + 1
	logf(logger, "REBOOTING: %s (count=%d)", reason, nextCount)
	if err := runReboot(cfg.rebootCmd); err != nil {
		return err
	}

	st.RebootCount = nextCount
	st.LastRebootAt = time.Now().UTC().Format(time.RFC3339)
	st.LastReason = reason
	if err := writeState(cfg.stateFile, st); err != nil {
		logf(logger, "state write failed: %v", err)
	}
	return nil
}

func checkLoad(cfg config) (bool, string) {
	data, err := os.ReadFile("/proc/loadavg")
	if err != nil {
		return true, ""
	}
	parts := strings.Fields(string(data))
	if len(parts) < 1 {
		return true, ""
	}
	load1, err := strconv.ParseFloat(parts[0], 64)
	if err != nil {
		return true, ""
	}
	threshold := 0.0
	if cfg.maxLoad1 > 0 {
		threshold = cfg.maxLoad1
	}
	if cfg.maxLoadPerCPU > 0 {
		perCPU := cfg.maxLoadPerCPU * float64(runtime.NumCPU())
		if threshold == 0 || perCPU < threshold {
			threshold = perCPU
		}
	}
	if threshold > 0 && load1 > threshold {
		return false, fmt.Sprintf("load1 %.2f > %.2f", load1, threshold)
	}
	return true, ""
}

func checkMem(cfg config) (bool, string) {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return true, ""
	}
	var totalKB, availableKB float64
	for _, line := range strings.Split(string(data), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		key := strings.TrimSuffix(fields[0], ":")
		val, err := strconv.ParseFloat(fields[1], 64)
		if err != nil {
			continue
		}
		switch key {
		case "MemTotal":
			totalKB = val
		case "MemAvailable":
			availableKB = val
		}
	}
	if totalKB == 0 {
		return true, ""
	}
	usedPct := (1 - (availableKB / totalKB)) * 100
	if usedPct > cfg.maxMemUsedPct {
		return false, fmt.Sprintf("mem used %.1f%% > %.1f%%", usedPct, cfg.maxMemUsedPct)
	}
	return true, ""
}

func checkIOPressure(cfg config) (bool, string) {
	data, err := os.ReadFile("/proc/pressure/io")
	if err != nil {
		return true, ""
	}
	avg300, ok := parsePSIAvg(strings.Split(string(data), "\n"), "some", "avg300")
	if !ok {
		return true, ""
	}
	if avg300 > cfg.maxIOPressureAvg300 {
		return false, fmt.Sprintf("io pressure avg300 %.2f > %.2f", avg300, cfg.maxIOPressureAvg300)
	}
	return true, ""
}

func checkIO(cfg config) (bool, string) {
	for _, path := range cfg.ioPaths {
		if err := checkRWAccess(path); err != nil {
			return false, fmt.Sprintf("io access %s: %v", path, err)
		}
		if len(cfg.ioAllowProcs) == 0 {
			continue
		}
		resolved := resolvePath(path)
		openers, err := findOpenProcesses([]string{path, resolved})
		if err != nil || len(openers) == 0 {
			continue
		}
		disallowed := filterDisallowed(openers, cfg.ioAllowProcs)
		if len(disallowed) > 0 {
			return false, fmt.Sprintf("io busy %s by %s", path, formatProcs(disallowed))
		}
	}
	return true, ""
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
			if len(parts) != 2 {
				continue
			}
			if parts[0] != key {
				continue
			}
			val, err := strconv.ParseFloat(parts[1], 64)
			if err != nil {
				return 0, false
			}
			return val, true
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

type procInfo struct {
	pid int
	cmd string
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
		found = append(found, procInfo{
			pid: pid,
			cmd: readProcCmd(pid),
		})
	}
	return found, nil
}

func matchesPath(target string, paths []string) bool {
	for _, path := range paths {
		if target == path {
			return true
		}
		if strings.HasPrefix(target, path+" ") {
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

func runReboot(cmd string) error {
	if strings.TrimSpace(cmd) == "" {
		return errors.New("empty reboot command")
	}
	c := exec.Command("sh", "-c", cmd)
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}
