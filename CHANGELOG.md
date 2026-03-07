# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

- Bootstrap now blocks duplicate monitor ids before enabling the service and reports `systemctl` state cleanly during startup.
- Bootstrap deployment now prints a post-install summary with the assigned IP, service enable/active state, and procmon API/monitor status.
- Add log rotation defaults via `logrotate`: rotate only above `5M`, keep `3` files, and do not compress.
- Add a safe uninstall script plus optional `--purge` cleanup for config, state, logs, and the default repo checkout.
- Add a one-line remote Raspberry Pi installer that bootstraps required packages, clones the repo, and launches the interactive deployment flow.

## [0.2.0] - 2026-03-06

- Refactor the app from a one-shot timer-driven MA352 monitor into a long-running multi-monitor `rpi-procmon` daemon.
- Add JSON-driven monitor configuration with support for multiple named services, per-monitor intervals, thresholds, cooldowns, checks, and recovery steps.
- Add persistent per-monitor runtime state including status, timestamps, counters, failure reasons, last check results, and last recovery results.
- Add an embedded HTTP API with `/health`, `/status`, `/monitors`, and `/monitors/{id}` for external visibility.
- Preserve legacy `PROC_*` MA352 environment variable behavior by synthesizing a `ma352` monitor when no JSON config file is present.
- Add built-in monitor checks for HTTP JSON health, system load, memory, I/O pressure, file path access, Docker container state, Docker log pattern matching, and command execution.
- Add recovery step types for command execution, sleep, and recheck.
- Replace the old `ma352-procmon` systemd timer model with a long-running `rpi-procmon.service` deployment model.
- Add example configuration for `ma352` and `scrypted-arlo` monitors.
- Expand tests to cover config loading, recovery flow, and status API responses.

## [0.1.0] - 2026-02-08

- Initial public release.
- Periodic proc/health checks with optional reboot.
- Systemd service and timer units.
- Build-time app revision support via `-ldflags "-X main.appVersion=..."`.
