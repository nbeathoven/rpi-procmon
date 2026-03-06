# Architecture

## Purpose

`rpi-procmon` is a small Go daemon-style utility intended to run from a systemd timer on a Raspberry Pi. Each run performs a fixed set of health checks and can trigger a reboot when one or more configured thresholds are exceeded.

## Runtime Flow

1. Load configuration from environment variables.
2. Open the configured log file and write a startup line with the build version.
3. Read the persisted reboot state from disk.
4. Run the enabled checks:
   - HTTP health endpoint check
   - 1-minute load average check
   - memory usage check
   - Linux PSI I/O pressure check
   - read/write path access and optional open-process allowlist check
5. If no checks fail, log success and exit.
6. If checks fail, combine all failure reasons into a single message.
7. Apply the reboot cooldown window using the last reboot timestamp from the state file.
8. Persist the reboot attempt state and execute the configured reboot command.

## Main Components

- `loadConfig`
  Reads all supported environment variables and applies defaults.

- `checkHealth`
  Sends an HTTP GET to `PROC_HEALTH_URL`, enforces timeout and optional latency, and optionally evaluates JSON fields such as `ok` and `serial_connected`.

- `checkLoad`
  Reads `/proc/loadavg` and compares the 1-minute load average against either an absolute threshold or a per-CPU threshold.

- `checkMem`
  Reads `/proc/meminfo` and computes used memory percentage from `MemTotal` and `MemAvailable`.

- `checkIOPressure`
  Reads `/proc/pressure/io` and parses the `some avg300` PSI metric.

- `checkIO`
  Confirms configured paths exist and are readable/writable, then optionally scans `/proc/*/fd` to identify processes holding those paths open.

- `readState` / `writeState`
  Manage the JSON file that tracks reboot count, last reboot time, and the last reboot reason.

- `runReboot`
  Executes `PROC_REBOOT_CMD` through `sh -c`.

## Operational Notes

- The tool is stateless unless it reaches a reboot-triggering path, in which case it updates the state file.
- Logs are append-only with UTC timestamps.
- All checks are optional and controlled by environment variables, so the same binary can monitor different services or devices.
- The shipped systemd timer runs the monitor once per minute after boot.

## Files

- `main.go`: application entry point and all monitor logic
- `systemd/ma352-procmon.service`: one-shot systemd service unit
- `systemd/ma352-procmon.timer`: periodic timer unit
- `README.md`: installation, configuration, and operations guide
