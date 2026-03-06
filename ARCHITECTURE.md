# Architecture

## Purpose

`rpi-procmon` is a long-running Go daemon for Raspberry Pi. It monitors multiple named services in one process, runs service-specific recovery sequences, persists per-monitor runtime state, and exposes a local HTTP API for health and status.

## Runtime Flow

1. Load JSON config from `PROC_CONFIG_FILE`, or synthesize a legacy `ma352` monitor from the original `PROC_*` environment variables.
2. Open the configured log file.
3. Load the persisted procmon state file.
4. Start the HTTP API server.
5. Start one goroutine per enabled monitor.
6. For each monitor cycle:
   - mark the monitor as checking
   - run all configured checks
   - persist detailed check results
   - if healthy, clear failure state
   - if unhealthy and threshold/cooldown permit, execute the monitor's recovery steps
   - persist recovery results and updated monitor state

## Monitor Model

Each monitor has its own:
- identity: `id`, `name`, `type`
- schedule: `interval`
- policy: `failure_threshold`, `cooldown`
- `checks[]`
- `recovery[]`

This lets `ma352`, `scrypted-arlo`, `homebridge-core`, or future services each carry their own logic while sharing one engine.

## Current Check Types

- `http_json`
- `load`
- `memory`
- `io_pressure`
- `io_paths`
- `docker_container`
- `docker_log_pattern`
- `command`

## Current Recovery Step Types

- `command`
- `sleep`
- `recheck`

## State Model

The state file is keyed by monitor id and stores the full runtime picture for each monitor:
- current status
- configured interval, cooldown, check count, recovery count
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

## Files

- `main.go`: daemon entrypoint plus engine, checks, actions, persistence, and API
- `main_test.go`: core tests
- `configs/example.json`: example multi-monitor config
- `systemd/rpi-procmon.service`: long-running systemd service
- `systemd/rpi-procmon.env.example`: runtime environment example
