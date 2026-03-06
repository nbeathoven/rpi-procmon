# rpi-procmon

`rpi-procmon` is a long-running Raspberry Pi monitoring daemon for multiple services. It executes named monitors on their own intervals, persists per-monitor state, runs ordered recovery actions, and exposes a local HTTP API for overall and per-service status.

## What It Does

- Runs multiple monitors in one process.
- Supports service-specific checks such as HTTP health, Docker container state, Docker log pattern matching, command checks, system load, memory, I/O pressure, and file path access.
- Persists per-monitor runtime state including last check times, last failures, last recoveries, cooldowns, counters, and detailed check/action results.
- Exposes API endpoints for external apps:
  - `GET /health`
  - `GET /status`
  - `GET /monitors`
  - `GET /monitors/{id}`
- Supports a legacy MA352 mode when no JSON config file exists and the original `PROC_*` environment variables are set.

## Main Files

- `main.go`: monitor engine, checks, recovery actions, persistence, and API server.
- `main_test.go`: coverage for config loading, recovery flow, and API exposure.
- `configs/example.json`: example multi-monitor configuration for `ma352` and `scrypted-arlo`.
- `systemd/rpi-procmon.service`: long-running systemd service.
- `systemd/rpi-procmon.env.example`: environment file example.

## Build

```bash
go build -ldflags "-X main.appVersion=$(git rev-parse --short HEAD)" -o /usr/local/bin/rpi-procmon
```

## Configure

1. Create config and env locations:

```bash
sudo mkdir -p /etc/rpi-procmon
sudo cp configs/example.json /etc/rpi-procmon/config.json
sudo cp systemd/rpi-procmon.env.example /etc/default/rpi-procmon
```

2. Edit `/etc/rpi-procmon/config.json` for your monitors.

The Scrypted example intentionally ships with a failing placeholder plugin restart command. Replace it with your real Arlo plugin restart command before enabling automated recovery.

## Install Service

```bash
sudo cp systemd/rpi-procmon.service /etc/systemd/system/rpi-procmon.service
sudo systemctl daemon-reload
sudo systemctl enable --now rpi-procmon.service
```

## API

By default the API listens on `127.0.0.1:9645`.

Examples:

```bash
curl http://127.0.0.1:9645/health
curl http://127.0.0.1:9645/status
curl http://127.0.0.1:9645/monitors
curl http://127.0.0.1:9645/monitors/scrypted-arlo
```

## Monitor Model

Each monitor defines:
- `id`, `name`, `type`
- `interval`
- `failure_threshold`
- `cooldown`
- `checks[]`
- `recovery[]`

Checks currently supported:
- `http_json`
- `load`
- `memory`
- `io_pressure`
- `io_paths`
- `docker_container`
- `docker_log_pattern`
- `command`

Recovery steps currently supported:
- `command`
- `sleep`
- `recheck`

## Per-Monitor State

The state file stores everything the daemon tracks for each monitor, including:
- status
- configured interval and cooldown
- next check time
- last check start/finish and duration
- last success and failure timestamps
- last recovery attempt, success, and failure timestamps
- cooldown expiry
- consecutive failures
- run counters and recovery counters
- last error and failure reasons
- last check results with observations
- last recovery action results with command output

## Test

```bash
go test ./...
```
