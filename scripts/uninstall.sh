#!/usr/bin/env bash
set -euo pipefail

SERVICE_NAME="rpi-procmon.service"
DEFAULT_SERVICE_FILE="/etc/systemd/system/rpi-procmon.service"
DEFAULT_ENV_FILE="/etc/default/rpi-procmon"
DEFAULT_BIN_PATH="/usr/local/bin/rpi-procmon"
DEFAULT_CONFIG_FILE="/etc/rpi-procmon/config.json"
DEFAULT_LOG_FILE="/var/log/rpi-procmon.log"
DEFAULT_STATE_FILE="/var/lib/rpi-procmon/state.json"
DEFAULT_CHECKOUT_DIR="/opt/rpi-procmon/repo"
DEFAULT_INSTALL_ROOT="/opt/rpi-procmon"
DEFAULT_LOGROTATE_FILE="/etc/logrotate.d/rpi-procmon"
PURGE=0
ASSUME_YES=0
CHECKOUT_DIR="$DEFAULT_CHECKOUT_DIR"
INSTALL_ROOT="$DEFAULT_INSTALL_ROOT"

usage() {
  cat <<'EOF_USAGE'
Usage: sudo ./scripts/uninstall.sh [--purge] [--yes] [--service-name NAME] [--checkout-dir PATH]

Safe uninstall removes:
- systemd service unit
- procmon environment file
- procmon binary

It preserves by default:
- config file and config directory
- state file and state directory
- log file
- repo checkout

With --purge it also removes:
- config file and empty parent directories
- state file and empty parent directories
- log file
- default checkout at /opt/rpi-procmon/repo
- default install root at /opt/rpi-procmon if empty
EOF_USAGE
}

require_root() {
  if [[ "$(id -u)" -ne 0 ]]; then
    echo "Run this script with sudo."
    exit 1
  fi
}

parse_args() {
  while [[ $# -gt 0 ]]; do
    case "$1" in
      --purge)
        PURGE=1
        shift
        ;;
      --yes|-y)
        ASSUME_YES=1
        shift
        ;;
      --service-name)
        SERVICE_NAME="$2"
        shift 2
        ;;
      --checkout-dir)
        CHECKOUT_DIR="$2"
        shift 2
        ;;
      --help|-h)
        usage
        exit 0
        ;;
      *)
        echo "Unknown argument: $1"
        usage
        exit 1
        ;;
    esac
  done
}

confirm() {
  local prompt="$1"
  if [[ "$ASSUME_YES" -eq 1 ]]; then
    return 0
  fi
  local reply
  read -r -p "$prompt [y/N]: " reply
  reply="$(printf '%s' "$reply" | tr '[:upper:]' '[:lower:]')"
  [[ "$reply" == "y" || "$reply" == "yes" ]]
}

get_fragment_path() {
  local path
  path="$(systemctl show -p FragmentPath --value "$SERVICE_NAME" 2>/dev/null || true)"
  if [[ -n "$path" ]]; then
    printf '%s\n' "$path"
    return
  fi
  printf '%s\n' "$DEFAULT_SERVICE_FILE"
}

extract_service_value() {
  local file="$1"
  local key="$2"
  if [[ ! -f "$file" ]]; then
    return
  fi
  sed -n "s/^${key}=//p" "$file" | tail -n 1
}

extract_env_file_path() {
  local service_file="$1"
  local raw
  raw="$(sed -n 's/^EnvironmentFile=-\?//p' "$service_file" | tail -n 1)"
  printf '%s\n' "${raw:-$DEFAULT_ENV_FILE}"
}

extract_execstart_path() {
  local service_file="$1"
  local raw
  raw="$(sed -n 's/^ExecStart=//p' "$service_file" | tail -n 1)"
  printf '%s\n' "${raw:-$DEFAULT_BIN_PATH}"
}

remove_if_exists() {
  local path="$1"
  if [[ -e "$path" || -L "$path" ]]; then
    rm -rf "$path"
    echo "Removed: $path"
  fi
}

remove_file_if_exists() {
  local path="$1"
  if [[ -f "$path" || -L "$path" ]]; then
    rm -f "$path"
    echo "Removed: $path"
  fi
}

remove_empty_dir() {
  local path="$1"
  if [[ -d "$path" ]] && [[ -z "$(ls -A "$path" 2>/dev/null)" ]]; then
    rmdir "$path"
    echo "Removed empty directory: $path"
  fi
}

main() {
  require_root
  parse_args "$@"

  local service_file env_file bin_path config_file log_file state_file config_dir state_dir
  service_file="$(get_fragment_path)"
  env_file="$DEFAULT_ENV_FILE"
  bin_path="$DEFAULT_BIN_PATH"
  config_file="$DEFAULT_CONFIG_FILE"
  log_file="$DEFAULT_LOG_FILE"
  state_file="$DEFAULT_STATE_FILE"

  if [[ -f "$service_file" ]]; then
    env_file="$(extract_env_file_path "$service_file")"
    bin_path="$(extract_execstart_path "$service_file")"
  fi

  if [[ -f "$env_file" ]]; then
    config_file="$(extract_service_value "$env_file" PROC_CONFIG_FILE)"
    log_file="$(extract_service_value "$env_file" PROC_LOG_FILE)"
    state_file="$(extract_service_value "$env_file" PROC_STATE_FILE)"
    config_file="${config_file:-$DEFAULT_CONFIG_FILE}"
    log_file="${log_file:-$DEFAULT_LOG_FILE}"
    state_file="${state_file:-$DEFAULT_STATE_FILE}"
  fi

  config_dir="$(dirname "$config_file")"
  state_dir="$(dirname "$state_file")"

  echo "Service name: $SERVICE_NAME"
  echo "Service file: $service_file"
  echo "Environment file: $env_file"
  echo "Binary: $bin_path"
  echo "Config file: $config_file"
  echo "State file: $state_file"
  echo "Log file: $log_file"
  if [[ "$PURGE" -eq 1 ]]; then
    echo "Repo checkout (purge): $CHECKOUT_DIR"
  fi
  echo

  if [[ "$PURGE" -eq 1 ]]; then
    if ! confirm "Proceed with uninstall and purge?"; then
      echo "Cancelled."
      exit 1
    fi
  else
    if ! confirm "Proceed with safe uninstall?"; then
      echo "Cancelled."
      exit 1
    fi
  fi

  if systemctl list-unit-files | grep -Fq "$SERVICE_NAME"; then
    systemctl disable --now "$SERVICE_NAME" >/dev/null 2>&1 || true
  else
    systemctl stop "$SERVICE_NAME" >/dev/null 2>&1 || true
  fi

  remove_file_if_exists "$service_file"
  remove_file_if_exists "$env_file"
  remove_file_if_exists "$bin_path"
  remove_file_if_exists "$DEFAULT_LOGROTATE_FILE"

  systemctl daemon-reload
  systemctl reset-failed "$SERVICE_NAME" >/dev/null 2>&1 || true

  if [[ "$PURGE" -eq 1 ]]; then
    remove_file_if_exists "$log_file"
    remove_file_if_exists "$config_file"
    remove_file_if_exists "$state_file"
    remove_empty_dir "$config_dir"
    remove_empty_dir "$state_dir"
    remove_if_exists "$CHECKOUT_DIR"
    remove_empty_dir "$INSTALL_ROOT"
  fi

  echo
  echo "Uninstall complete."
  if [[ "$PURGE" -eq 0 ]]; then
    echo "Config, state, log, and repo files were preserved."
  fi
}

main "$@"
