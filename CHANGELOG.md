# Changelog

All notable changes to this project will be documented in this file.

## [Unreleased]

- Fix reboot cooldown state so failed reboot commands do not suppress later retries.
- Require valid JSON with `serial_connected` when `PROC_REQUIRE_SERIAL=true`.
- Add unit tests for reboot state handling and serial health validation.
- Expand the repository documentation with a code walkthrough and architecture note.

## [0.1.0] - 2026-02-08

- Initial public release.
- Periodic proc/health checks with optional reboot.
- Systemd service and timer units.
- Build-time app revision support via `-ldflags "-X main.appVersion=..."`.
