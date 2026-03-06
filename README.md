# rpi-procmon

A lightweight, periodic proc/health monitor for Raspberry Pi. It runs as a systemd timer, checks system health and a health endpoint, and can reboot the device when configured thresholds are exceeded.

## Files and Locations

- App source: `main.go`
- Systemd unit files: `systemd/ma352-procmon.service`, `systemd/ma352-procmon.timer`
- Default config file (optional): `/etc/default/ma352-procmon`
- Log file: `/var/log/ma352-procmon.log`
- State file: `/var/lib/ma352-procmon/state.json`

## App Revision (Version Tracking)

The app exposes a build-time version string. By default it logs `version=dev` on each run. Set a real revision at build time:

```bash
go build -ldflags "-X main.appVersion=1.0.0" -o /usr/local/bin/ma352-procmon
```

You can use a Git-based revision string as well:

```bash
go build -ldflags "-X main.appVersion=$(git rev-parse --short HEAD)" -o /usr/local/bin/ma352-procmon
```

The version is logged at startup in `/var/log/ma352-procmon.log`.

## Quick Start

1. Build and install:

```bash
go build -ldflags "-X main.appVersion=1.0.0" -o /usr/local/bin/ma352-procmon
```

2. Install systemd units:

```bash
sudo cp systemd/ma352-procmon.service /etc/systemd/system/ma352-procmon.service
sudo cp systemd/ma352-procmon.timer /etc/systemd/system/ma352-procmon.timer
sudo systemctl daemon-reload
sudo systemctl enable --now ma352-procmon.timer
```

3. (Optional) configure `/etc/default/ma352-procmon`.

## Configuration

All settings are environment variables loaded from `/etc/default/ma352-procmon` (if present). Defaults are shown here:

- `PROC_HEALTH_URL` = `http://127.0.0.1:5000/health`
- `PROC_HEALTH_TIMEOUT_SEC` = `3`
- `PROC_MAX_HEALTH_LATENCY_MS` = `0` (disabled)
- `PROC_REQUIRE_SERIAL` = `false`
- `PROC_REBOOT_ON_HEALTH_FAIL` = `true`
- `PROC_MAX_LOAD1` = `0` (disabled)
- `PROC_MAX_LOAD_PER_CPU` = `0` (disabled)
- `PROC_MAX_MEM_USED_PCT` = `0` (disabled)
- `PROC_MAX_IO_PRESSURE_AVG300` = `0` (disabled)
- `PROC_IO_PATHS` = empty (disabled)
- `PROC_IO_ALLOW_PROCS` = empty
- `PROC_REBOOT_CMD` = `systemctl reboot`
- `PROC_LOG_FILE` = `/var/log/ma352-procmon.log`
- `PROC_STATE_FILE` = `/var/lib/ma352-procmon/state.json`
- `PROC_MIN_REBOOT_INTERVAL_SEC` = `3600`

## What It Monitors

- Health endpoint check (HTTP status + optional latency).
- Health JSON fields `ok` and `serial_connected` (required when `PROC_REQUIRE_SERIAL=true`).
- 1-minute load average (absolute or per-CPU threshold).
- Memory usage percentage based on `/proc/meminfo`.
- IO pressure `avg300` from `/proc/pressure/io`.
- Read/write access to specific paths, plus optional allowlist of processes that can keep those paths open.

## Monitoring Any Given Service

To monitor an arbitrary service, point the health check at a service-specific health endpoint or provide a tiny local health endpoint that validates the service. Examples:

- If the service exposes `http://127.0.0.1:PORT/health`, set `PROC_HEALTH_URL` to that endpoint.
- If the service does not expose a health endpoint, run a small local health handler that checks `systemctl is-active your-service` and returns JSON `{ "ok": true }` when active.
- If the service uses a device file or critical path, set `PROC_IO_PATHS` to that path and use `PROC_IO_ALLOW_PROCS` to ensure only expected processes have it open.

## Code Walkthrough

The implementation lives in `main.go` and follows a simple one-shot flow:

1. Load environment-driven config.
2. Open the log file and read the previous reboot state.
3. Run the enabled checks for HTTP health, load, memory, I/O pressure, and path access.
4. Aggregate any failures into a single reboot reason.
5. Apply the minimum reboot interval guard.
6. Execute the reboot command and persist reboot state only after the command succeeds.

For a code-oriented overview of the main functions and runtime behavior, see `ARCHITECTURE.md`.

## Logging and State

- Logs: `/var/log/ma352-procmon.log` (append-only, UTC timestamps)
- State: `/var/lib/ma352-procmon/state.json` (updated only after a successful reboot command handoff)

## Revision History

See `CHANGELOG.md`.
