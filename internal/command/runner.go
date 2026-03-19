package command

import (
	"context"
	"os/exec"
	"strconv"
	"strings"

	"github.com/nbeathoven/rpi-procmon/internal/config"
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

func BuildSystemdIsActiveCommand(target config.TargetConfig, service string) string {
	return buildSystemdCommand(target, "systemctl is-active --quiet "+shellQuote(service))
}

func BuildSystemdRestartCommand(target config.TargetConfig, service string) string {
	command := "systemctl restart " + shellQuote(service)
	if strings.TrimSpace(target.Transport) == "ssh" {
		command = "sudo -n " + command
	}
	return buildSystemdCommand(target, command)
}

func buildSystemdCommand(target config.TargetConfig, remoteCommand string) string {
	transport := strings.TrimSpace(target.Transport)
	if transport == "" || transport == "local" {
		return remoteCommand
	}

	parts := []string{
		"ssh",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
	}
	if target.Port > 0 {
		parts = append(parts, "-p", shellQuoteInt(target.Port))
	}
	if strings.TrimSpace(target.IdentityFile) != "" {
		parts = append(parts, "-i", shellQuote(target.IdentityFile))
	}

	host := strings.TrimSpace(target.Host)
	user := strings.TrimSpace(target.User)
	if user != "" {
		host = user + "@" + host
	}
	parts = append(parts, shellQuote(host), shellQuote(remoteCommand))
	return strings.Join(parts, " ")
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func shellQuoteInt(value int) string {
	return shellQuote(strconv.Itoa(value))
}
