package actions

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/nbeathoven/rpi-procmon/internal/command"
	"github.com/nbeathoven/rpi-procmon/internal/config"
	"github.com/nbeathoven/rpi-procmon/internal/state"
)

type Handler func(context.Context, command.Runner, config.MonitorConfig, config.ActionConfig) (bool, string, string)

var registry = map[string]Handler{
	"command":                  runCommand,
	"docker_exec":              runDockerExec,
	"sleep":                    runSleep,
	"recheck":                  runRecheck,
	"restart_docker_container": runRestartDockerContainer,
	"restart_systemd_service":  runRestartSystemdService,
}

func Run(ctx context.Context, runner command.Runner, monitor config.MonitorConfig, action config.ActionConfig) state.ActionResult {
	start := time.Now().UTC()
	result := state.ActionResult{
		Name:      action.Name,
		Type:      action.Type,
		StartedAt: start.Format(time.RFC3339),
	}

	handler, ok := registry[action.Type]
	success := false
	message := ""
	output := ""
	if ok {
		success, message, output = handler(ctx, runner, monitor, action)
	} else {
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

func runCommand(ctx context.Context, runner command.Runner, _ config.MonitorConfig, action config.ActionConfig) (bool, string, string) {
	if strings.TrimSpace(action.Command) == "" {
		return false, "command action missing command", ""
	}
	outcome, err := runner.Run(ctx, action.Command)
	output := limitString(outcome.Output, 2048)
	if err != nil {
		return false, command.FailureMessage("command", outcome.ExitCode), output
	}
	return true, "", output
}

func runDockerExec(ctx context.Context, runner command.Runner, monitor config.MonitorConfig, action config.ActionConfig) (bool, string, string) {
	if strings.TrimSpace(action.Container) == "" {
		return false, "docker_exec action missing container", ""
	}
	if strings.TrimSpace(action.Command) == "" {
		return false, "docker_exec action missing command", ""
	}
	outcome, err := runner.Run(ctx, command.BuildDockerExecCommand(monitor.Target, action.Container, action.Command))
	output := limitString(outcome.Output, 2048)
	if err != nil {
		return false, command.FailureMessage("docker_exec", outcome.ExitCode), output
	}
	return true, "", output
}

func runSleep(ctx context.Context, _ command.Runner, _ config.MonitorConfig, action config.ActionConfig) (bool, string, string) {
	duration := parseDuration(action.Duration, 5*time.Second)
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false, ctx.Err().Error(), ""
	case <-timer.C:
		return true, "", ""
	}
}

func runRecheck(context.Context, command.Runner, config.MonitorConfig, config.ActionConfig) (bool, string, string) {
	return true, "recheck requested", ""
}

func runRestartSystemdService(ctx context.Context, runner command.Runner, monitor config.MonitorConfig, action config.ActionConfig) (bool, string, string) {
	service := strings.TrimSpace(action.Service)
	if service == "" {
		return false, "restart_systemd_service missing service", ""
	}
	outcome, err := runner.Run(ctx, command.BuildSystemdRestartCommand(monitor.Target, service))
	output := limitString(outcome.Output, 2048)
	if err != nil {
		return false, command.SSHFailureMessage("restart_systemd_service", outcome.ExitCode), output
	}
	return true, "", output
}

func runRestartDockerContainer(ctx context.Context, runner command.Runner, monitor config.MonitorConfig, action config.ActionConfig) (bool, string, string) {
	container := strings.TrimSpace(action.Container)
	if container == "" {
		return false, "restart_docker_container missing container", ""
	}
	outcome, err := runner.Run(ctx, command.BuildDockerRestartCommand(monitor.Target, container))
	output := limitString(outcome.Output, 2048)
	if err != nil {
		return false, command.FailureMessage("restart_docker_container", outcome.ExitCode), output
	}
	return true, "", output
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

func limitString(value string, max int) string {
	if max <= 0 || len(value) <= max {
		return value
	}
	return value[:max]
}
