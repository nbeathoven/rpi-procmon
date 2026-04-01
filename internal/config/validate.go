package config

import (
	"fmt"
	"strings"
	"time"
)

var supportedCheckTypes = map[string]struct{}{
	"http_json":          {},
	"load":               {},
	"memory":             {},
	"io_pressure":        {},
	"io_paths":           {},
	"docker_container":   {},
	"docker_log_pattern": {},
	"command":            {},
	"systemd_service":    {},
}

var supportedActionTypes = map[string]struct{}{
	"command":                 {},
	"sleep":                   {},
	"recheck":                 {},
	"restart_systemd_service": {},
}

func validateTarget(monitorID string, target TargetConfig) error {
	transport := strings.TrimSpace(target.Transport)
	if transport == "" {
		transport = "local"
	}
	switch transport {
	case "local":
		return nil
	case "ssh":
		if strings.TrimSpace(target.Host) == "" {
			return fmt.Errorf("monitor %q target with transport ssh missing host", monitorID)
		}
		for _, host := range target.FallbackHosts {
			if strings.TrimSpace(host) == "" {
				return fmt.Errorf("monitor %q target has empty fallback host", monitorID)
			}
		}
		if target.Port < 0 {
			return fmt.Errorf("monitor %q target port %d is invalid", monitorID, target.Port)
		}
		return nil
	default:
		return fmt.Errorf("monitor %q has unsupported target transport %q", monitorID, target.Transport)
	}
}

func validateCheck(monitorID string, check CheckConfig) error {
	if _, ok := supportedCheckTypes[check.Type]; !ok {
		return fmt.Errorf("monitor %q check %q has unsupported type %q", monitorID, check.ID, check.Type)
	}

	switch check.Type {
	case "http_json":
		if strings.TrimSpace(check.URL) == "" {
			return fmt.Errorf("monitor %q check %q missing url", monitorID, check.ID)
		}
		if strings.TrimSpace(check.Timeout) != "" {
			if _, err := time.ParseDuration(strings.TrimSpace(check.Timeout)); err != nil {
				return fmt.Errorf("monitor %q check %q timeout %q is invalid: %w", monitorID, check.ID, check.Timeout, err)
			}
		}
	case "docker_container":
		if strings.TrimSpace(check.Container) == "" {
			return fmt.Errorf("monitor %q check %q missing container", monitorID, check.ID)
		}
	case "docker_log_pattern":
		if strings.TrimSpace(check.Container) == "" {
			return fmt.Errorf("monitor %q check %q missing container", monitorID, check.ID)
		}
		if len(check.Patterns) == 0 {
			return fmt.Errorf("monitor %q check %q has no patterns", monitorID, check.ID)
		}
		if strings.TrimSpace(check.Since) != "" {
			if _, err := time.ParseDuration(strings.TrimSpace(check.Since)); err != nil {
				return fmt.Errorf("monitor %q check %q since %q is invalid: %w", monitorID, check.ID, check.Since, err)
			}
		}
	case "io_paths":
		if len(check.Paths) == 0 {
			return fmt.Errorf("monitor %q check %q has no paths", monitorID, check.ID)
		}
	case "command":
		if strings.TrimSpace(check.Command) == "" {
			return fmt.Errorf("monitor %q check %q missing command", monitorID, check.ID)
		}
	case "systemd_service":
		if strings.TrimSpace(check.Service) == "" {
			return fmt.Errorf("monitor %q check %q missing service", monitorID, check.ID)
		}
	}

	return nil
}

func validateAction(monitorID string, action ActionConfig) error {
	if _, ok := supportedActionTypes[action.Type]; !ok {
		return fmt.Errorf("monitor %q recovery action %q has unsupported type %q", monitorID, action.Name, action.Type)
	}

	switch action.Type {
	case "command":
		if strings.TrimSpace(action.Command) == "" {
			return fmt.Errorf("monitor %q recovery action %q missing command", monitorID, action.Name)
		}
	case "restart_systemd_service":
		if strings.TrimSpace(action.Service) == "" {
			return fmt.Errorf("monitor %q recovery action %q missing service", monitorID, action.Name)
		}
	case "sleep":
		if strings.TrimSpace(action.Duration) == "" {
			return fmt.Errorf("monitor %q recovery action %q missing duration", monitorID, action.Name)
		}
		if _, err := time.ParseDuration(strings.TrimSpace(action.Duration)); err != nil {
			return fmt.Errorf("monitor %q recovery action %q duration %q is invalid: %w", monitorID, action.Name, action.Duration, err)
		}
	}

	return nil
}
