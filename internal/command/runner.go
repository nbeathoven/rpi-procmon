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

	hosts := targetHosts(target)
	if len(hosts) == 0 {
		return remoteCommand
	}

	if len(hosts) == 1 {
		return buildSSHCommand(target, hosts[0], remoteCommand)
	}

	parts := []string{"rc=255"}
	for _, host := range hosts {
		sshCommand := buildSSHCommand(target, host, remoteCommand)
		parts = append(parts, sshCommand+" && exit 0; rc=$?; if [ \"$rc\" -ne 255 ]; then exit \"$rc\"; fi")
	}
	parts = append(parts, "exit \"$rc\"")
	return strings.Join(parts, "; ")
}

func buildSSHCommand(target config.TargetConfig, host string, remoteCommand string) string {
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

	user := strings.TrimSpace(target.User)
	if user != "" {
		host = user + "@" + host
	}
	parts = append(parts, shellQuote(host), shellQuote(remoteCommand))
	return strings.Join(parts, " ")
}

func targetHosts(target config.TargetConfig) []string {
	hosts := make([]string, 0, 1+len(target.FallbackHosts))
	seen := map[string]struct{}{}
	appendHost := func(host string) {
		host = strings.TrimSpace(host)
		if host == "" {
			return
		}
		if _, ok := seen[host]; ok {
			return
		}
		seen[host] = struct{}{}
		hosts = append(hosts, host)
	}

	appendHost(target.Host)
	for _, host := range target.FallbackHosts {
		appendHost(host)
	}
	return hosts
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func shellQuoteInt(value int) string {
	return shellQuote(strconv.Itoa(value))
}
