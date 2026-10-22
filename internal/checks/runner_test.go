package checks

import (
	"context"
	"testing"

	"github.com/nbeathoven/rpi-procmon/internal/command"
	"github.com/nbeathoven/rpi-procmon/internal/config"
)

type failedRunner struct{}

func (failedRunner) Run(context.Context, string) (command.Outcome, error) {
	return command.Outcome{ExitCode: 0, Output: "2026-10-21T01:00:00Z initialized"}, context.DeadlineExceeded
}

func TestRunnerFailureCannotPassWithMatchingExitCode(t *testing.T) {
	for _, check := range []config.CheckConfig{
		{Type: "command", Command: "true"},
		{Type: "docker_exec", Container: "test", DockerCommand: "true"},
		{Type: "docker_log_pattern", Container: "test", Patterns: []string{"error"}, SuccessPatterns: []string{"initialized"}},
	} {
		result := Run(context.Background(), failedRunner{}, config.MonitorConfig{}, check)
		if result.Success {
			t.Fatalf("execution error accepted as healthy: %s", check.Type)
		}
	}
}

func TestExpectedNonzeroProcessExitStillPasses(t *testing.T) {
	result := Run(context.Background(), command.ShellRunner{}, config.MonitorConfig{}, config.CheckConfig{Type: "command", Command: "exit 3", ExpectedExitCode: 3})
	if !result.Success {
		t.Fatalf("expected exit code rejected: %+v", result)
	}
}
