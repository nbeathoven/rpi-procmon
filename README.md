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
- `scripts/bootstrap-rpi.sh`: first-time Raspberry Pi deployment helper.
- `scripts/install.sh`: one-line remote installer for fresh Raspberry Pi hosts.
- `scripts/uninstall.sh`: safe uninstall script with optional `--purge` cleanup.
- `systemd/rpi-procmon.service`: long-running systemd service.
- `systemd/rpi-procmon.env.example`: environment file example.
- `systemd/rpi-procmon.logrotate`: log retention policy installed under `/etc/logrotate.d/`.

## Fresh Raspberry Pi Deployment

For a turnkey first install on a fresh Raspberry Pi, run:

```bash
curl -fsSL https://raw.githubusercontent.com/nbeathoven/rpi-procmon/main/scripts/install.sh | sudo bash
```

That remote installer will:
- install required base packages such as `git`, `curl`, `python3`, and `tar`
- clone or update `rpi-procmon` under `/opt/rpi-procmon`
- launch the interactive bootstrap flow from the local checkout

If you already have the repo cloned locally, you can run the interactive bootstrap script directly:

```bash
sudo ./scripts/bootstrap-rpi.sh
```

The script will:
- install `logrotate` if it is missing
- install Go if it is missing
- build `rpi-procmon`
- ask which services you want to monitor
- generate `/etc/rpi-procmon/config.json`
- install `/etc/default/rpi-procmon`
- install and start `rpi-procmon.service`
- print a post-install summary with the assigned IP, service enable/active state, and procmon API status

Current monitor templates in the bootstrap flow:
- `MA352 bridge`
- `Scrypted Arlo`
- `Generic systemd service`

The script also prints the final config directory and config file path after deployment.

The bootstrap enables `rpi-procmon.service`, so the daemon starts automatically at boot through `systemd`.

## MA352 Template

The `ma352` monitor is intended for the McIntosh amplifier bridge service from [homebridge-mcintosh-rs232](https://github.com/nbeathoven/homebridge-mcintosh-rs232).

The template assumes:
- systemd service name similar to `ma352-bridge`
- local health endpoint similar to `http://127.0.0.1:5000/health`
- recovery by restarting the bridge service and rechecking health

## Manual Build

```bash
go build -ldflags "-X main.appVersion=$(git rev-parse --short HEAD)" -o /usr/local/bin/rpi-procmon
```

## Manual Configure

1. Create config and env locations:

```bash
sudo mkdir -p /etc/rpi-procmon
sudo cp configs/example.json /etc/rpi-procmon/config.json
sudo cp systemd/rpi-procmon.env.example /etc/default/rpi-procmon
```

2. Edit `/etc/rpi-procmon/config.json` for your monitors.

The Scrypted example intentionally ships with a failing placeholder plugin restart command. Replace it with your real Arlo plugin restart command before enabling automated recovery.

## Manual Install Service

```bash
sudo cp systemd/rpi-procmon.service /etc/systemd/system/rpi-procmon.service
sudo systemctl daemon-reload
sudo systemctl enable --now rpi-procmon.service
```

## Logging

`rpi-procmon` writes its own daemon log to the configured `PROC_LOG_FILE`, which defaults to `/var/log/rpi-procmon.log`. That file is append-only and contains procmon startup, status, and recovery events.

Installed deployments also install a `logrotate` policy. The default retention policy is:
- rotate only when the log exceeds `5M`
- keep `3` rotated files
- do not compress rotated logs

So the active log is capped by rotation and does not grow forever under the default deployment flow.

The monitored services keep their own logs separately:
- `Scrypted` logs still come from `docker logs scrypted`
- `homebridge-mcintosh-rs232` bridge logs still come from its own service/app logging

`rpi-procmon` reads those services through checks, but it does not replace or centralize their native logs.

Because the daemon runs as a `systemd` service, you can also inspect service-level lifecycle output with:

```bash
journalctl -u rpi-procmon.service -f
```

## Uninstall

Safe uninstall removes the procmon service, env file, and binary, but keeps config, logs, state, and the repo checkout:

```bash
sudo ./scripts/uninstall.sh
```

Purge uninstall also removes config, state, log files, and the default repo checkout under `/opt/rpi-procmon`:

```bash
sudo ./scripts/uninstall.sh --purge
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
