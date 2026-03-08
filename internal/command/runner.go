package command

import (
	"context"
	"os/exec"
	"strings"
)

type Runner interface {
	Run(context.Context, string) (Outcome, error)
}

type Outcome struct {
	Output   string
	ExitCode int
}

type ShellRunner struct{}

func (ShellRunner) Run(ctx context.Context, command string) (Outcome, error) {
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	output, err := cmd.CombinedOutput()
	outcome := Outcome{
		Output: strings.TrimSpace(string(output)),
	}
	if cmd.ProcessState != nil {
		outcome.ExitCode = cmd.ProcessState.ExitCode()
	}
	return outcome, err
}
