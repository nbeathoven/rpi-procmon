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

type Handler func(context.Context, command.Runner, config.ActionConfig) (bool, string, string)

var registry = map[string]Handler{
	"command": runCommand,
	"sleep":   runSleep,
	"recheck": runRecheck,
}

func Run(ctx context.Context, runner command.Runner, action config.ActionConfig) state.ActionResult {
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
		success, message, output = handler(ctx, runner, action)
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

func runCommand(ctx context.Context, runner command.Runner, action config.ActionConfig) (bool, string, string) {
	if strings.TrimSpace(action.Command) == "" {
		return false, "command action missing command", ""
	}
	outcome, err := runner.Run(ctx, action.Command)
	output := limitString(outcome.Output, 2048)
	if err != nil {
		return false, fmt.Sprintf("command failed with exit code %d", outcome.ExitCode), output
	}
	return true, "", output
}

func runSleep(ctx context.Context, _ command.Runner, action config.ActionConfig) (bool, string, string) {
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

func runRecheck(context.Context, command.Runner, config.ActionConfig) (bool, string, string) {
	return true, "recheck requested", ""
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
