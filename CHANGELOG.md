# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

- App revision updated to `eddc6bb`.
- Add a target-aware monitor model with `local` and `ssh` transports so one procmon instance can monitor and recover remote systemd services such as `ma352-bridge`.
- Add typed `systemd_service` checks and `restart_systemd_service` recovery actions instead of relying on raw shell snippets for service control.
- Extend bootstrap so `MA352` and generic systemd service monitors can be configured for local or remote SSH-backed service control.
- Persist and expose per-monitor target metadata in procmon state and API responses for external dashboards and app clients.
- Extend the `scrypted-arlo` monitor with a positive plugin-process check and additional Arlo discovery failure signatures, and tune the default log window/threshold for recurring connection failures.

- Fix bootstrap post-install status parsing so procmon API JSON is read correctly and no traceback is printed on successful installs.
- Bootstrap now reuses an existing compatible Go toolchain and only installs Go when it is missing or too old for the repo `go.mod` requirement.
- Bootstrap reruns now preserve unrelated monitors, replace monitors with the same id, and clean duplicate existing monitor entries during config generation.
- Bootstrap also replaces conflicting systemd-service monitors when they target the same underlying service name, so reruns can safely update `ma352`, `homebridge`, and other service monitors without duplicating coverage.
- Bootstrap now blocks duplicate monitor ids before enabling the service and reports `systemctl` state cleanly during startup.
- Bootstrap deployment now prints a post-install summary with the assigned IP, service enable/active state, and procmon API/monitor status.
- Runtime state and API responses now expose full configured checks and recovery steps for each monitor, while remaining compatible with older state files that stored only counts.
- `/health` now reports actual procmon health instead of always returning `ok=true`, and returns HTTP `503` when procmon is degraded, failed, or unknown.
- Procmon now logs per-monitor check and recovery activity to its own log file in addition to startup/shutdown events.
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
