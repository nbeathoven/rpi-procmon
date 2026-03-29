package checks

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
	"path/filepath"
	"regexp"
	stdruntime "runtime"
	"strconv"
	"strings"
	"time"

	"github.com/nbeathoven/rpi-procmon/internal/command"
	"github.com/nbeathoven/rpi-procmon/internal/config"
	"github.com/nbeathoven/rpi-procmon/internal/state"
	"golang.org/x/sys/unix"
)

type Handler func(context.Context, command.Runner, config.MonitorConfig, config.CheckConfig) (bool, string, map[string]any)

var registry = map[string]Handler{
	"http_json":          runHTTPJSON,
	"load":               runLoad,
	"memory":             runMemory,
	"io_pressure":        runIOPressure,
	"io_paths":           runIOPaths,
	"docker_container":   runDockerContainer,
	"docker_log_pattern": runDockerLogPattern,
	"command":            runCommand,
	"systemd_service":    runSystemdService,
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

func Run(ctx context.Context, runner command.Runner, monitor config.MonitorConfig, check config.CheckConfig) state.CheckResult {
	start := time.Now().UTC()
	result := state.CheckResult{
		ID:        check.ID,
		Name:      check.Name,
		Type:      check.Type,
		StartedAt: start.Format(time.RFC3339),
	}

	handler, ok := registry[check.Type]
	success := false
	message := ""
	observations := map[string]any{}
	if ok {
		success, message, observations = handler(ctx, runner, monitor, check)
	} else {
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

func runHTTPJSON(_ context.Context, _ command.Runner, _ config.MonitorConfig, check config.CheckConfig) (bool, string, map[string]any) {
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

func runLoad(_ context.Context, _ command.Runner, _ config.MonitorConfig, check config.CheckConfig) (bool, string, map[string]any) {
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
		perCPU := check.MaxLoadPerCPU * float64(stdruntime.NumCPU())
		if threshold == 0 || perCPU < threshold {
			threshold = perCPU
		}
	}
	observations := map[string]any{
		"load1":     load1,
		"threshold": threshold,
	}
	if threshold > 0 && load1 > threshold {
		return false, fmt.Sprintf("load1 %.2f > %.2f", load1, threshold), observations
	}
	return true, "", observations
}

func runMemory(_ context.Context, _ command.Runner, _ config.MonitorConfig, check config.CheckConfig) (bool, string, map[string]any) {
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
	observations := map[string]any{
		"used_pct":  usedPct,
		"threshold": check.MaxMemUsedPct,
	}
	if check.MaxMemUsedPct > 0 && usedPct > check.MaxMemUsedPct {
		return false, fmt.Sprintf("mem used %.1f%% > %.1f%%", usedPct, check.MaxMemUsedPct), observations
	}
	return true, "", observations
}

func runIOPressure(_ context.Context, _ command.Runner, _ config.MonitorConfig, check config.CheckConfig) (bool, string, map[string]any) {
	data, err := os.ReadFile("/proc/pressure/io")
	if err != nil {
		return true, "", nil
	}
	avg300, ok := parsePSIAvg(strings.Split(string(data), "\n"), "some", "avg300")
	if !ok {
		return true, "", nil
	}
	observations := map[string]any{
		"avg300":    avg300,
		"threshold": check.MaxIOPressureAvg300,
	}
	if check.MaxIOPressureAvg300 > 0 && avg300 > check.MaxIOPressureAvg300 {
		return false, fmt.Sprintf("io pressure avg300 %.2f > %.2f", avg300, check.MaxIOPressureAvg300), observations
	}
	return true, "", observations
}

func runIOPaths(_ context.Context, _ command.Runner, _ config.MonitorConfig, check config.CheckConfig) (bool, string, map[string]any) {
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

func runDockerContainer(ctx context.Context, runner command.Runner, _ config.MonitorConfig, check config.CheckConfig) (bool, string, map[string]any) {
	if strings.TrimSpace(check.Container) == "" {
		return false, "docker_container check missing container", nil
	}
	cmd := fmt.Sprintf("docker inspect -f '{{.State.Running}}' %s", shellQuote(check.Container))
	outcome, err := runner.Run(ctx, cmd)
	running := strings.TrimSpace(outcome.Output) == "true"
	observations := map[string]any{
		"container": check.Container,
		"running":   running,
		"exit_code": outcome.ExitCode,
	}
	if err != nil || !running {
		return false, fmt.Sprintf("container %s not running", check.Container), observations
	}
	return true, "", observations
}

func runDockerLogPattern(ctx context.Context, runner command.Runner, _ config.MonitorConfig, check config.CheckConfig) (bool, string, map[string]any) {
	if strings.TrimSpace(check.Container) == "" {
		return false, "docker_log_pattern check missing container", nil
	}
	since := check.Since
	if strings.TrimSpace(since) == "" {
		since = "10m"
	}
	cmd := fmt.Sprintf("docker logs --timestamps --since %s %s 2>&1", shellQuote(since), shellQuote(check.Container))
	outcome, err := runner.Run(ctx, cmd)
	logText := outcome.Output
	if err != nil && strings.TrimSpace(logText) == "" {
		return false, fmt.Sprintf("docker logs failed for %s: %v", check.Container, err), nil
	}
	count, matchedPatterns, regexErr := countPatternMatches(logText, check.Patterns)
	if regexErr != nil {
		return false, regexErr.Error(), nil
	}
	successCount, matchedSuccessPatterns, regexErr := countPatternMatches(logText, check.SuccessPatterns)
	if regexErr != nil {
		return false, regexErr.Error(), nil
	}
	threshold := check.MatchCountThreshold
	if threshold <= 0 {
		threshold = 1
	}
	observations := map[string]any{
		"container":                check.Container,
		"since":                    since,
		"match_count":              count,
		"threshold":                threshold,
		"matched_patterns":         matchedPatterns,
		"success_match_count":      successCount,
		"matched_success_patterns": matchedSuccessPatterns,
	}
	if len(check.SuccessPatterns) > 0 {
		lastFailureAt, lastSuccessAt, timelineErr := findLatestPatternTimestamps(logText, check.Patterns, check.SuccessPatterns)
		if timelineErr != nil {
			return false, timelineErr.Error(), observations
		}
		if !lastFailureAt.IsZero() {
			observations["last_failure_match_at"] = lastFailureAt.UTC().Format(time.RFC3339Nano)
		}
		if !lastSuccessAt.IsZero() {
			observations["last_success_match_at"] = lastSuccessAt.UTC().Format(time.RFC3339Nano)
		}
		if count >= threshold && !lastSuccessAt.IsZero() && lastSuccessAt.After(lastFailureAt) {
			observations["superseded_by_success"] = true
			return true, "", observations
		}
	}
	if count >= threshold {
		return false, fmt.Sprintf("docker log pattern matched %d times in %s", count, since), observations
	}
	return true, "", observations
}

func runCommand(ctx context.Context, runner command.Runner, _ config.MonitorConfig, check config.CheckConfig) (bool, string, map[string]any) {
	if strings.TrimSpace(check.Command) == "" {
		return false, "command check missing command", nil
	}
	outcome, err := runner.Run(ctx, check.Command)
	output := limitString(outcome.Output, 2048)
	observations := map[string]any{
		"exit_code": outcome.ExitCode,
		"output":    output,
	}
	expectedExitCode := check.ExpectedExitCode
	if outcome.ExitCode != expectedExitCode {
		return false, fmt.Sprintf("command exit code %d != %d", outcome.ExitCode, expectedExitCode), observations
	}
	if err != nil && outcome.ExitCode != expectedExitCode {
		return false, fmt.Sprintf("command failed with exit code %d", outcome.ExitCode), observations
	}
	if len(check.ExpectedOutputPatterns) > 0 {
		matched, err := matchPatterns(output, check.ExpectedOutputPatterns, check.MatchAll)
		if err != nil {
			return false, err.Error(), observations
		}
		if !matched {
			return false, "command output missing expected pattern", observations
		}
	}
	if len(check.ForbiddenOutputPatterns) > 0 {
		matched, err := matchPatterns(output, check.ForbiddenOutputPatterns, false)
		if err != nil {
			return false, err.Error(), observations
		}
		if matched {
			return false, "command output matched forbidden pattern", observations
		}
	}
	return true, "", observations
}

func runSystemdService(ctx context.Context, runner command.Runner, monitor config.MonitorConfig, check config.CheckConfig) (bool, string, map[string]any) {
	service := strings.TrimSpace(check.Service)
	if service == "" {
		return false, "systemd_service check missing service", nil
	}
	cmd := command.BuildSystemdIsActiveCommand(monitor.Target, service)
	outcome, err := runner.Run(ctx, cmd)
	observations := map[string]any{
		"service":   service,
		"transport": normalizedTransport(monitor.Target.Transport),
		"host":      strings.TrimSpace(monitor.Target.Host),
		"exit_code": outcome.ExitCode,
	}
	if err != nil || outcome.ExitCode != 0 {
		return false, fmt.Sprintf("systemd service %s is not active", service), observations
	}
	return true, "", observations
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

func findLatestPatternTimestamps(logText string, failurePatterns []string, successPatterns []string) (time.Time, time.Time, error) {
	failureRegexes := make([]*regexp.Regexp, 0, len(failurePatterns))
	for _, pattern := range failurePatterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid pattern %q: %w", pattern, err)
		}
		failureRegexes = append(failureRegexes, re)
	}
	successRegexes := make([]*regexp.Regexp, 0, len(successPatterns))
	for _, pattern := range successPatterns {
		re, err := regexp.Compile(pattern)
		if err != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid pattern %q: %w", pattern, err)
		}
		successRegexes = append(successRegexes, re)
	}

	var lastFailureAt time.Time
	var lastSuccessAt time.Time
	scanner := bufio.NewScanner(bytes.NewBufferString(logText))
	for scanner.Scan() {
		line := scanner.Text()
		ts, msg, ok := splitDockerTimestampLine(line)
		if !ok {
			continue
		}
		for _, re := range failureRegexes {
			if re.MatchString(msg) {
				if ts.After(lastFailureAt) {
					lastFailureAt = ts
				}
				break
			}
		}
		for _, re := range successRegexes {
			if re.MatchString(msg) {
				if ts.After(lastSuccessAt) {
					lastSuccessAt = ts
				}
				break
			}
		}
	}
	return lastFailureAt, lastSuccessAt, nil
}

func splitDockerTimestampLine(line string) (time.Time, string, bool) {
	idx := strings.IndexByte(line, ' ')
	if idx <= 0 {
		return time.Time{}, "", false
	}
	ts, err := time.Parse(time.RFC3339Nano, line[:idx])
	if err != nil {
		return time.Time{}, "", false
	}
	return ts, line[idx+1:], true
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

func normalizedTransport(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return "local"
	}
	return trimmed
}
