# Architecture

## Purpose

`rpi-procmon` is a long-running Go daemon for Raspberry Pi. It monitors multiple named services in one process, runs service-specific recovery sequences, persists per-monitor runtime state, and exposes a local HTTP API for health and status.

## Runtime Flow

1. Load canonical JSON config from `PROC_CONFIG_FILE`.
2. Open the configured log file.
3. Load the persisted procmon state file.
4. Load the persisted monitor event history.
5. Start the HTTP API server.
6. Start one goroutine per enabled monitor.
7. For each monitor cycle:
   - mark the monitor as checking
   - run all configured checks
   - persist detailed check results
   - append failure and recovery events to the history store when monitor state changes
   - if healthy, clear failure state
   - if unhealthy and threshold/cooldown permit, execute the monitor's recovery steps
   - persist recovery results and updated monitor state

## Monitor Model

Each monitor has its own:
- identity: `id`, `name`, `type`
- target: `target.transport`, `target.host`, `target.fallback_hosts`, `target.user`, `target.port`
- schedule: `interval`
- policy: `failure_threshold`, `cooldown`
- `checks[]`
- `recovery[]`

This lets `ma352`, `scrypted-arlo`, `homebridge-core`, or future services each carry their own logic while sharing one engine. The target model keeps local and remote service control explicit without turning procmon into a distributed controller mesh.

## Current Check Types

- `http_json`
- `load`
- `memory`
- `io_pressure`
- `io_paths`
- `docker_container`
- `docker_log_pattern`
- `command`
- `systemd_service`

## Current Recovery Step Types

- `command`
- `sleep`
- `recheck`
- `restart_systemd_service`

## State Model

The state file is keyed by monitor id and stores the full runtime picture for each monitor:
- current status
- configured interval, cooldown, checks, and recovery steps
- last and next check timestamps
- last success and failure timestamps
- last recovery attempt, success, and failure timestamps
- cooldown expiration
- consecutive failure streak
- cumulative run, failure, and recovery counters
- last error and failure reasons
- last check results with observations
- last recovery action results with command output

## API

The embedded HTTP API provides:
- `/health`: procmon liveness and overall status
- `/status`: global snapshot and all monitor states
- `/monitors`: list of all monitor states
- `/monitors/{id}`: one monitor's full state
- `/events`: reverse-chronological monitor history for app clients

Per-monitor API responses include the configured `target` object so external apps can distinguish local services from remotely managed ones.

## Files

- `main.go`: daemon entrypoint
- `main_test.go`: core tests
- `internal/config`: config loading and validation
- `internal/engine`: monitor scheduler and recovery flow
- `internal/checks`: check registry and handlers
- `internal/actions`: recovery registry and handlers
- `internal/state`: runtime state types and atomic storage
- `internal/events`: persisted event history and query filtering
- `internal/api`: HTTP status endpoints
- `internal/command`: shell runner abstraction
- `internal/logging`: procmon log writer
- `configs/example.json`: example multi-monitor config
- `systemd/rpi-procmon.service`: long-running systemd service
- `systemd/rpi-procmon.env.example`: runtime environment example
