#!/usr/bin/env bash
set -euo pipefail

REPO_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEFAULT_CONFIG_DIR="/etc/rpi-procmon"
DEFAULT_API_ADDR="127.0.0.1:9645"
DEFAULT_LOG_FILE="/var/log/rpi-procmon.log"
DEFAULT_STATE_FILE="/var/lib/rpi-procmon/state.json"
DEFAULT_ENV_FILE="/etc/default/rpi-procmon"
DEFAULT_SERVICE_FILE="/etc/systemd/system/rpi-procmon.service"
DEFAULT_BIN_PATH="/usr/local/bin/rpi-procmon"
TMP_MONITORS="$(mktemp)"
GO_BIN=""

ensure_logrotate() {
  if command -v logrotate >/dev/null 2>&1; then
    return
  fi
  if command -v apt-get >/dev/null 2>&1 && command -v dpkg >/dev/null 2>&1; then
    echo "Installing logrotate..."
    apt-get update
    apt-get install -y logrotate
    return
  fi
  echo "logrotate not found. Install it manually to enable log rotation."
}

cleanup() {
  rm -f "$TMP_MONITORS"
}
trap cleanup EXIT

require_root() {
  if [[ "$(id -u)" -ne 0 ]]; then
    echo "Run this script with sudo on the Raspberry Pi."
    exit 1
  fi
}

require_linux() {
  if [[ "$(uname -s)" != "Linux" ]]; then
    echo "This installer is intended for Raspberry Pi Linux hosts."
    exit 1
  fi
}

prompt_default() {
  local label="$1"
  local default_value="$2"
  local reply
  read -r -p "$label [$default_value]: " reply
  if [[ -z "$reply" ]]; then
    printf '%s\n' "$default_value"
  else
    printf '%s\n' "$reply"
  fi
}

prompt_required() {
  local label="$1"
  local reply
  while true; do
    read -r -p "$label: " reply
    if [[ -n "$reply" ]]; then
      printf '%s\n' "$reply"
      return
    fi
    echo "A value is required."
  done
}

prompt_yes_no() {
  local label="$1"
  local default_value="$2"
  local reply
  local normalized_default
  normalized_default="$(printf '%s' "$default_value" | tr '[:upper:]' '[:lower:]')"

  while true; do
    read -r -p "$label [$default_value]: " reply
    if [[ -z "$reply" ]]; then
      reply="$normalized_default"
    fi
    reply="$(printf '%s' "$reply" | tr '[:upper:]' '[:lower:]')"
    case "$reply" in
      y|yes|true) return 0 ;;
      n|no|false) return 1 ;;
    esac
    echo "Please answer yes or no."
  done
}

read_json_string() {
  local path="$1"
  local key_path="$2"
  python3 - "$path" "$key_path" <<'PY'
import json
import sys
from pathlib import Path

path = Path(sys.argv[1])
key_path = sys.argv[2].split(".")
if not path.exists():
    raise SystemExit(0)

try:
    value = json.loads(path.read_text(encoding="utf-8"))
except Exception:
    raise SystemExit(0)

for key in key_path:
    if not isinstance(value, dict):
        raise SystemExit(0)
    value = value.get(key)

if isinstance(value, str) and value.strip():
    print(value.strip())
PY
}

detect_ipv4_addresses() {
  if command -v ip >/dev/null 2>&1; then
    ip -4 -o addr show up scope global 2>/dev/null | awk '{print $4}' | cut -d/ -f1 | awk '!seen[$0]++ && $0 != ""'
    return
  fi

  hostname -I 2>/dev/null | tr ' ' '\n' | awk '/^[0-9]+\.[0-9]+\.[0-9]+\.[0-9]+$/ && !seen[$0]++'
}

first_line() {
  local value="$1"
  if [[ -z "$value" ]]; then
    return 1
  fi
  printf '%s\n' "$value" | head -n 1
}

append_monitor_json() {
  python3 - "$TMP_MONITORS" <<'PY'
import json
import os
import sys
from pathlib import Path

def normalize(value):
    return str(value or "").strip().lower()

def monitor_target_keys(monitor):
    keys = []
    target = monitor.get("target") or {}
    transport = normalize(target.get("transport")) or "local"
    host = normalize(target.get("host"))
    metadata = monitor.get("metadata") or {}
    service_name = normalize(metadata.get("service_name"))
    if not service_name:
        for check in monitor.get("checks", []) or []:
            check_type = normalize(check.get("type"))
            if check_type == "systemd_service":
                service_name = normalize(check.get("service"))
                break
            if check_type == "command":
                command = normalize(check.get("command"))
                prefix = "systemctl is-active --quiet "
                if command.startswith(prefix):
                    service_name = command[len(prefix):].strip()
                    break
    if service_name:
        keys.append(f"systemd-service:{transport}:{host}:{service_name}")
    return keys

def describe_target(key):
    parts = key.split(":", 3)
    kind = parts[0]
    if kind == "systemd-service":
        transport = parts[1] if len(parts) > 1 else "local"
        host = parts[2] if len(parts) > 2 else ""
        value = parts[3] if len(parts) > 3 else ""
        if transport == "ssh" and host:
            return f"systemd service {value} on {host}"
        return f"systemd service {value}"
    return key

def upsert(monitor):
    path = Path(sys.argv[1])
    monitors = []
    if path.exists():
        for line in path.read_text(encoding="utf-8").splitlines():
            line = line.strip()
            if not line:
                continue
            monitors.append(json.loads(line))

    monitor_id = str(monitor.get("id", "")).strip()
    target_keys = set(monitor_target_keys(monitor))
    replaced_by_id = False
    replaced_targets = []
    next_monitors = []
    for existing in monitors:
        existing_id = str(existing.get("id", "")).strip()
        if monitor_id and existing_id == monitor_id:
            replaced_by_id = True
            continue
        overlap = sorted(target_keys & set(monitor_target_keys(existing)))
        if overlap:
            replaced_targets.append((existing_id, overlap))
            continue
        next_monitors.append(existing)
    monitors = next_monitors
    monitors.append(monitor)
    path.write_text("".join(json.dumps(item) + "\n" for item in monitors), encoding="utf-8")
    if replaced_by_id and monitor_id:
        print(f"Replacing pending monitor definition for id: {monitor_id}", file=sys.stderr)
    for existing_id, overlaps in replaced_targets:
        for key in overlaps:
            if existing_id:
                print(f"Replacing pending monitor definition for {describe_target(key)} (previous id: {existing_id})", file=sys.stderr)
            else:
                print(f"Replacing pending monitor definition for {describe_target(key)}", file=sys.stderr)

template = os.environ["PROC_TEMPLATE"]
if template == "ma352":
    target = {
        "transport": os.environ.get("PROC_TARGET_TRANSPORT", "local"),
        "host": os.environ.get("PROC_TARGET_HOST", ""),
        "user": os.environ.get("PROC_TARGET_USER", ""),
        "port": int(os.environ.get("PROC_TARGET_PORT", "0") or "0"),
        "identity_file": os.environ.get("PROC_TARGET_IDENTITY_FILE", ""),
    }
    monitor = {
        "id": "ma352",
        "name": "MA352 Bridge",
        "type": "systemd-service",
        "interval": os.environ["PROC_INTERVAL"],
        "failure_threshold": 1,
        "cooldown": os.environ["PROC_COOLDOWN"],
        "target": target,
        "metadata": {
            "repo_path": os.environ.get("PROC_REPO_PATH", ""),
            "service_name": os.environ["PROC_SERVICE_NAME"],
        },
        "checks": [
            {
                "id": "ma352-service-active",
                "type": "systemd_service",
                "service": os.environ["PROC_SERVICE_NAME"],
            },
            {
                "id": "ma352-health",
                "type": "http_json",
                "url": os.environ["PROC_HEALTH_URL"],
                "timeout": "3s",
                "require_ok": True,
                "require_serial_connected": True,
            },
        ],
        "recovery": [
            {
                "name": "restart-ma352-bridge",
                "type": "restart_systemd_service",
                "service": os.environ["PROC_SERVICE_NAME"],
            },
            {
                "name": "wait-after-restart",
                "type": "sleep",
                "duration": "10s",
            },
            {
                "name": "recheck-after-restart",
                "type": "recheck",
            },
        ],
    }
    upsert(monitor)
elif template == "scrypted_arlo":
    target = {
        "transport": os.environ.get("PROC_TARGET_TRANSPORT", "local"),
        "host": os.environ.get("PROC_TARGET_HOST", ""),
        "user": os.environ.get("PROC_TARGET_USER", ""),
        "port": int(os.environ.get("PROC_TARGET_PORT", "0") or "0"),
        "identity_file": os.environ.get("PROC_TARGET_IDENTITY_FILE", ""),
    }
    recovery = []
    plugin_cmd = os.environ.get("PROC_PLUGIN_RESTART_CMD", "").strip()
    if plugin_cmd:
        recovery.extend([
            {
                "name": "restart-arlo-plugin",
                "type": "docker_exec",
                "container": os.environ["PROC_CONTAINER_NAME"],
                "command": plugin_cmd,
            },
            {
                "name": "wait-after-plugin-restart",
                "type": "sleep",
                "duration": "30s",
            },
            {
                "name": "recheck-after-plugin-restart",
                "type": "recheck",
            },
        ])
    recovery.extend([
        {
            "name": "restart-scrypted-container",
            "type": "restart_docker_container",
            "container": os.environ["PROC_CONTAINER_NAME"],
        },
        {
            "name": "wait-after-container-restart",
            "type": "sleep",
            "duration": "60s",
        },
        {
            "name": "recheck-after-container-restart",
            "type": "recheck",
        },
    ])
    monitor = {
        "id": "scrypted-arlo",
        "name": "Scrypted Arlo",
        "type": "docker-log",
        "interval": os.environ["PROC_INTERVAL"],
        "failure_threshold": 1,
        "cooldown": os.environ["PROC_COOLDOWN"],
        "target": target,
        "checks": [
            {
                "id": "scrypted-running",
                "type": "docker_container",
                "container": os.environ["PROC_CONTAINER_NAME"],
            },
            {
                "id": "arlo-plugin-process",
                "type": "docker_exec",
                "container": os.environ["PROC_CONTAINER_NAME"],
                "docker_command": "ps ax -o args= | grep -F \"plugin_remote.py @scrypted/arlo\" | grep -v grep",
                "expected_output_patterns": [
                    "plugin_remote.py @scrypted/arlo",
                ],
            },
            {
                "id": "arlo-log-errors",
                "type": "docker_log_pattern",
                "container": os.environ["PROC_CONTAINER_NAME"],
                "since": os.environ["PROC_LOG_SINCE"],
                "match_count_threshold": int(os.environ["PROC_MATCH_THRESHOLD"]),
                "patterns": [
                    "Arlo Cloud snapshot failed: Failed to get snapshot URL",
                    "MQTT Event Stream .* failed to connect with return code 4",
                    "MQTT Event Stream .* disconnected with return code 5",
                    "HTTP error 401 .*fullFrameSnapshot",
                    "Error discovering devices: Arlo client not connected, cannot discover devices",
                    "Error during periodic device discovery: Arlo client not connected, cannot discover devices",
                ],
                "success_patterns": [
                    "Arlo Cloud login successful\\.",
                    "Subscribed to Arlo event stream successfully\\.",
                    "Arlo plugin initialized\\.",
                ],
            },
        ],
        "recovery": recovery,
    }
    upsert(monitor)
elif template == "systemd_service":
    target = {
        "transport": os.environ.get("PROC_TARGET_TRANSPORT", "local"),
        "host": os.environ.get("PROC_TARGET_HOST", ""),
        "user": os.environ.get("PROC_TARGET_USER", ""),
        "port": int(os.environ.get("PROC_TARGET_PORT", "0") or "0"),
        "identity_file": os.environ.get("PROC_TARGET_IDENTITY_FILE", ""),
    }
    checks = [
        {
            "id": f"{os.environ['PROC_MONITOR_ID']}-service-active",
            "type": "systemd_service",
            "service": os.environ["PROC_SERVICE_NAME"],
        }
    ]
    health_url = os.environ.get("PROC_HEALTH_URL", "").strip()
    if health_url:
        checks.append({
            "id": f"{os.environ['PROC_MONITOR_ID']}-health",
            "type": "http_json",
            "url": health_url,
            "timeout": "3s",
            "require_ok": True,
        })
    monitor = {
        "id": os.environ["PROC_MONITOR_ID"],
        "name": os.environ["PROC_MONITOR_NAME"],
        "type": "systemd-service",
        "interval": os.environ["PROC_INTERVAL"],
        "failure_threshold": 1,
        "cooldown": os.environ["PROC_COOLDOWN"],
        "target": target,
        "metadata": {
            "service_name": os.environ["PROC_SERVICE_NAME"],
        },
        "checks": checks,
        "recovery": [
            {
                "name": f"restart-{os.environ['PROC_MONITOR_ID']}",
                "type": "restart_systemd_service",
                "service": os.environ["PROC_SERVICE_NAME"],
            },
            {
                "name": "wait-after-restart",
                "type": "sleep",
                "duration": "10s",
            },
            {
                "name": "recheck-after-restart",
                "type": "recheck",
            },
        ],
    }
    upsert(monitor)
else:
    raise SystemExit(f"Unsupported template: {template}")
PY
}

write_config_file() {
  local config_file="$1"
  local api_addr="$2"
  local log_file="$3"
  local state_file="$4"
  python3 - "$TMP_MONITORS" "$config_file" "$api_addr" "$log_file" "$state_file" <<'PY'
import json
import sys
from pathlib import Path

monitors_path = Path(sys.argv[1])
config_path = Path(sys.argv[2])
api_addr = sys.argv[3]
log_file = sys.argv[4]
state_file = sys.argv[5]

def normalize(value):
    return str(value or "").strip().lower()

def monitor_target_keys(monitor):
    keys = []
    target = monitor.get("target") or {}
    transport = normalize(target.get("transport")) or "local"
    host = normalize(target.get("host"))
    metadata = monitor.get("metadata") or {}
    service_name = normalize(metadata.get("service_name"))
    if not service_name:
        for check in monitor.get("checks", []) or []:
            check_type = normalize(check.get("type"))
            if check_type == "systemd_service":
                service_name = normalize(check.get("service"))
                break
            if check_type == "command":
                command = normalize(check.get("command"))
                prefix = "systemctl is-active --quiet "
                if command.startswith(prefix):
                    service_name = command[len(prefix):].strip()
                    break
    if service_name:
        keys.append(f"systemd-service:{transport}:{host}:{service_name}")
    return keys

def describe_target(key):
    parts = key.split(":", 3)
    kind = parts[0]
    if kind == "systemd-service":
        transport = parts[1] if len(parts) > 1 else "local"
        host = parts[2] if len(parts) > 2 else ""
        value = parts[3] if len(parts) > 3 else ""
        if transport == "ssh" and host:
            return f"systemd service {value} on {host}"
        return f"systemd service {value}"
    return key

def remove_monitor(monitor_id, ordered_ids, monitor_by_id, target_key_to_id):
    ordered_ids[:] = [existing_id for existing_id in ordered_ids if existing_id != monitor_id]
    monitor_by_id.pop(monitor_id, None)
    for target_key, existing_id in list(target_key_to_id.items()):
        if existing_id == monitor_id:
            del target_key_to_id[target_key]

def add_or_replace(monitor, ordered_ids, monitor_by_id, target_key_to_id, announce_replace=False):
    monitor_id = str(monitor.get("id") or "").strip()
    if not monitor_id:
        raise SystemExit("Monitor id is required in generated config")
    target_keys = monitor_target_keys(monitor)

    if monitor_id in monitor_by_id:
        if announce_replace:
            print(f"Replacing existing monitor definition for id: {monitor_id}", file=sys.stderr)
    else:
        ordered_ids.append(monitor_id)

    for target_key in target_keys:
        existing_id = target_key_to_id.get(target_key)
        if existing_id and existing_id != monitor_id:
            remove_monitor(existing_id, ordered_ids, monitor_by_id, target_key_to_id)
            print(f"Replacing existing monitor definition for {describe_target(target_key)} (previous id: {existing_id})", file=sys.stderr)
    monitor_by_id[monitor_id] = monitor
    for target_key in target_keys:
        target_key_to_id[target_key] = monitor_id

existing_config = {}
existing_monitors = []
if config_path.exists():
    existing_config = json.loads(config_path.read_text(encoding="utf-8"))
    existing_monitors = existing_config.get("monitors", []) or []

selected_monitors = []
if monitors_path.exists():
    for line in monitors_path.read_text(encoding="utf-8").splitlines():
        line = line.strip()
        if not line:
            continue
        selected_monitors.append(json.loads(line))

ordered_ids = []
monitor_by_id = {}
target_key_to_id = {}
seen_existing = set()
for monitor in existing_monitors:
    monitor_id = str(monitor.get("id") or "").strip()
    if not monitor_id:
        raise SystemExit("Monitor id is required in existing config")
    if monitor_id in seen_existing:
        print(f"Cleaning duplicate existing monitor definition for id: {monitor_id}", file=sys.stderr)
    seen_existing.add(monitor_id)
    add_or_replace(monitor, ordered_ids, monitor_by_id, target_key_to_id)

for monitor in selected_monitors:
    add_or_replace(monitor, ordered_ids, monitor_by_id, target_key_to_id, announce_replace=str(monitor.get("id") or "").strip() in monitor_by_id)

monitors = [monitor_by_id[monitor_id] for monitor_id in ordered_ids]

config = dict(existing_config)
config["log_file"] = log_file
config["state_file"] = state_file
config["api"] = {
    "listen_address": api_addr,
    "read_header_timeout": "5s",
}
config["monitors"] = monitors

config_path.parent.mkdir(parents=True, exist_ok=True)
config_path.write_text(json.dumps(config, indent=2) + "\n", encoding="utf-8")
PY
}

detect_ma352_repo() {
  local candidates=(
    "$HOME/homebridge-mcintosh-rs232"
    "/home/pi/homebridge-mcintosh-rs232"
    "/opt/homebridge-mcintosh-rs232"
    "/srv/homebridge-mcintosh-rs232"
  )
  local path
  for path in "${candidates[@]}"; do
    if [[ -d "$path/bridge-service" ]]; then
      printf '%s\n' "$path"
      return
    fi
  done
  printf '%s\n' ""
}

map_go_arch() {
  case "$(uname -m)" in
    aarch64|arm64) printf '%s\n' "arm64" ;;
    x86_64) printf '%s\n' "amd64" ;;
    armv7l|armv6l) printf '%s\n' "armv6l" ;;
    *)
      echo "Unsupported CPU architecture: $(uname -m)" >&2
      exit 1
      ;;
  esac
}

find_go_binary() {
  local candidate
  if candidate="$(command -v go 2>/dev/null)"; then
    if [[ -x "$candidate" ]]; then
      printf '%s\n' "$candidate"
      return 0
    fi
  fi

  for candidate in \
    "/usr/local/go/bin/go" \
    "/usr/bin/go" \
    "/usr/local/bin/go"
  do
    if [[ -x "$candidate" ]]; then
      printf '%s\n' "$candidate"
      return 0
    fi
  done

  return 1
}

resolve_go_versions() {
  local minimum_version preferred_version
  minimum_version="$(awk '/^go / { print "go" $2; exit }' "$REPO_DIR/go.mod")"
  preferred_version="$(awk '/^toolchain / { print $2; exit }' "$REPO_DIR/go.mod")"
  if [[ -z "$minimum_version" ]]; then
    echo "Unable to determine minimum Go version from go.mod" >&2
    exit 1
  fi
  if [[ -z "$preferred_version" ]]; then
    preferred_version="$minimum_version"
  fi
  printf '%s %s\n' "$minimum_version" "$preferred_version"
}

normalize_go_version() {
  local version="$1"
  version="${version#go}"
  IFS='.' read -r major minor patch <<< "$version"
  major="${major:-0}"
  minor="${minor:-0}"
  patch="${patch:-0}"
  printf '%d %d %d\n' "$major" "$minor" "$patch"
}

version_gte() {
  local left="$1"
  local right="$2"
  local l_major l_minor l_patch r_major r_minor r_patch
  read -r l_major l_minor l_patch <<< "$(normalize_go_version "$left")"
  read -r r_major r_minor r_patch <<< "$(normalize_go_version "$right")"

  if (( l_major > r_major )); then
    return 0
  fi
  if (( l_major < r_major )); then
    return 1
  fi
  if (( l_minor > r_minor )); then
    return 0
  fi
  if (( l_minor < r_minor )); then
    return 1
  fi
  if (( l_patch >= r_patch )); then
    return 0
  fi
  return 1
}

ensure_go() {
  local minimum_version preferred_version
  read -r minimum_version preferred_version <<< "$(resolve_go_versions)"

  local installed_go installed_version
  if installed_go="$(find_go_binary)"; then
    installed_version="$("$installed_go" version | awk '{print $3}')"
    if version_gte "$installed_version" "$minimum_version"; then
      GO_BIN="$installed_go"
      echo "Using existing Go ${installed_version} from ${installed_go}. Minimum required is ${minimum_version}; preferred toolchain is ${preferred_version}."
      return
    fi
    echo "Installed Go ${installed_version} is older than required ${minimum_version}. Installing ${preferred_version}."
  fi

  if ! command -v curl >/dev/null 2>&1; then
    echo "curl is required for first-time Go installation."
    exit 1
  fi
  if ! command -v tar >/dev/null 2>&1; then
    echo "tar is required for first-time Go installation."
    exit 1
  fi

  local go_arch archive_url archive_path
  go_arch="$(map_go_arch)"
  archive_url="https://go.dev/dl/${preferred_version}.linux-${go_arch}.tar.gz"
  archive_path="/tmp/${preferred_version}.linux-${go_arch}.tar.gz"

  echo "Installing Go ${preferred_version} for ${go_arch}..."
  curl -fsSL "$archive_url" -o "$archive_path"
  if [[ -d /usr/local/go ]]; then
    mv /usr/local/go "/usr/local/go.backup.$(date +%s)"
  fi
  tar -C /usr/local -xzf "$archive_path"
  GO_BIN="/usr/local/go/bin/go"
}

build_binary() {
  local revision
  revision="$(git -C "$REPO_DIR" rev-parse --short HEAD 2>/dev/null || printf 'dev')"
  echo "Building rpi-procmon (${revision})..."
  "$GO_BIN" build -ldflags "-X main.appVersion=${revision}" -o "$BIN_PATH" "$REPO_DIR"
}

install_service_files() {
  local config_file="$1"
  local env_file="$2"
  local service_file="$3"
  local log_file="$4"
  local state_file="$5"
  local api_addr="$6"
  local logrotate_file="/etc/logrotate.d/rpi-procmon"

  install -d -m 0755     "$(dirname "$BIN_PATH")"     "$(dirname "$env_file")"     "$(dirname "$service_file")"     "$(dirname "$config_file")"     "$(dirname "$state_file")"     "$(dirname "$log_file")"

  cat > "$service_file" <<EOF_SERVICE
[Unit]
Description=rpi-procmon daemon
After=network-online.target docker.service
Wants=network-online.target docker.service

[Service]
Type=simple
User=root
Group=root
EnvironmentFile=-$env_file
ExecStart=$BIN_PATH
Restart=always
RestartSec=5
Nice=5

[Install]
WantedBy=multi-user.target
EOF_SERVICE

  cat > "$env_file" <<EOF_ENV
PROC_CONFIG_FILE=$config_file
PROC_LOG_FILE=$log_file
PROC_STATE_FILE=$state_file
PROC_API_LISTEN_ADDR=$api_addr
PROC_API_READ_HEADER_TIMEOUT=5s
# Optional: enable admin control endpoints after setting a long random token.
# PROC_API_ADMIN_TOKEN=
EOF_ENV
  touch "$log_file"
  if [[ -f "$REPO_DIR/systemd/rpi-procmon.logrotate" ]]; then
    install -m 0644 "$REPO_DIR/systemd/rpi-procmon.logrotate" "$logrotate_file"
    sed -i "s#/var/log/rpi-procmon.log#$log_file#" "$logrotate_file"
  fi
}

add_ma352_monitor() {
  local detected_repo repo_path service_name health_url interval cooldown
  local service_host target_transport ssh_user ssh_port ssh_identity_file default_health_host
  detected_repo="$(detect_ma352_repo)"
  repo_path="$(prompt_default "MA352 repo path (homebridge-mcintosh-rs232)" "${detected_repo:-/opt/homebridge-mcintosh-rs232}")"
  service_host="$(prompt_default "MA352 host/IP" "127.0.0.1")"
  if [[ "$service_host" == "127.0.0.1" || "$service_host" == "localhost" ]]; then
    target_transport="local"
  else
    target_transport="ssh"
  fi
  target_transport="$(prompt_default "MA352 service control transport (local or ssh)" "$target_transport")"
  target_transport="$(printf '%s' "$target_transport" | tr '[:upper:]' '[:lower:]')"
  ssh_user=""
  ssh_port="22"
  ssh_identity_file=""
  if [[ "$target_transport" == "ssh" ]]; then
    ssh_user="$(prompt_default "MA352 SSH user" "nima")"
    ssh_port="$(prompt_default "MA352 SSH port" "22")"
    ssh_identity_file="$(prompt_default "MA352 SSH identity file on procmon host" "/home/nima/.ssh/id_ed25519")"
  fi
  service_name="$(prompt_default "MA352 bridge systemd service name" "ma352-bridge")"
  default_health_host="$service_host"
  if [[ "$default_health_host" == "localhost" ]]; then
    default_health_host="127.0.0.1"
  fi
  health_url="$(prompt_default "MA352 health URL" "http://${default_health_host}:5000/health")"
  interval="$(prompt_default "MA352 check interval" "1m")"
  cooldown="$(prompt_default "MA352 recovery cooldown" "15m")"

  export PROC_TEMPLATE="ma352"
  export PROC_REPO_PATH="$repo_path"
  export PROC_TARGET_TRANSPORT="$target_transport"
  if [[ "$target_transport" == "ssh" ]]; then
    export PROC_TARGET_HOST="$service_host"
  else
    export PROC_TARGET_HOST=""
  fi
  export PROC_TARGET_USER="$ssh_user"
  export PROC_TARGET_PORT="$ssh_port"
  export PROC_TARGET_IDENTITY_FILE="$ssh_identity_file"
  export PROC_SERVICE_NAME="$service_name"
  export PROC_HEALTH_URL="$health_url"
  export PROC_INTERVAL="$interval"
  export PROC_COOLDOWN="$cooldown"
  append_monitor_json
}

add_scrypted_arlo_monitor() {
  local container_name interval cooldown log_since match_threshold plugin_restart_cmd recovery_target
  local service_host target_transport ssh_user ssh_port ssh_identity_file
  echo
  echo "Scrypted Arlo monitor setup:"
  echo "  - Log scan window: how far back procmon scans Scrypted logs, for example 30m or 1h."
  echo "  - Failure threshold: how many matching Arlo error lines inside that window trigger recovery."
  echo "  - Primary recovery target: default is plugin restart first."
  echo "    Type scrypted if you want container restart to be the first recovery action instead."
  service_host="$(prompt_default "Scrypted host/IP" "127.0.0.1")"
  if [[ "$service_host" == "127.0.0.1" || "$service_host" == "localhost" ]]; then
    target_transport="local"
  else
    target_transport="ssh"
  fi
  target_transport="$(prompt_default "Scrypted control transport (local or ssh)" "$target_transport")"
  target_transport="$(printf '%s' "$target_transport" | tr '[:upper:]' '[:lower:]')"
  ssh_user=""
  ssh_port="22"
  ssh_identity_file=""
  if [[ "$target_transport" == "ssh" ]]; then
    ssh_user="$(prompt_default "Scrypted SSH user" "nima")"
    ssh_port="$(prompt_default "Scrypted SSH port" "22")"
    ssh_identity_file="$(prompt_default "Scrypted SSH identity file on procmon host" "/home/nima/.ssh/id_ed25519")"
  fi
  container_name="$(prompt_default "Scrypted container name" "scrypted")"
  interval="$(prompt_default "Scrypted Arlo check interval" "5m")"
  cooldown="$(prompt_default "Scrypted Arlo recovery cooldown" "20m")"
  log_since="$(prompt_default "Scrypted Arlo log scan window (duration like 30m or 1h)" "30m")"
  match_threshold="$(prompt_default "Scrypted Arlo matching error lines required before recovery" "2")"
  recovery_target="$(prompt_default "Primary recovery target (plugin or scrypted)" "plugin")"
  recovery_target="$(printf '%s' "$recovery_target" | tr '[:upper:]' '[:lower:]')"

  plugin_restart_cmd=""
  if [[ "$recovery_target" != "scrypted" ]]; then
    while true; do
      plugin_restart_cmd="$(prompt_default "Arlo plugin restart command (or type scrypted to restart the whole container instead)" "")"
      if [[ -n "$plugin_restart_cmd" ]]; then
        if [[ "$(printf '%s' "$plugin_restart_cmd" | tr '[:upper:]' '[:lower:]')" == "scrypted" ]]; then
          plugin_restart_cmd=""
        fi
        break
      fi
      echo "Plugin-first recovery needs a plugin restart command. Type scrypted if you want container restart instead."
    done
  fi

  export PROC_TEMPLATE="scrypted_arlo"
  export PROC_CONTAINER_NAME="$container_name"
  export PROC_TARGET_TRANSPORT="$target_transport"
  if [[ "$target_transport" == "ssh" ]]; then
    export PROC_TARGET_HOST="$service_host"
  else
    export PROC_TARGET_HOST=""
  fi
  export PROC_TARGET_USER="$ssh_user"
  export PROC_TARGET_PORT="$ssh_port"
  export PROC_TARGET_IDENTITY_FILE="$ssh_identity_file"
  export PROC_INTERVAL="$interval"
  export PROC_COOLDOWN="$cooldown"
  export PROC_LOG_SINCE="$log_since"
  export PROC_MATCH_THRESHOLD="$match_threshold"
  export PROC_PLUGIN_RESTART_CMD="$plugin_restart_cmd"
  append_monitor_json
}

add_systemd_service_monitor() {
  local monitor_id monitor_name service_name health_url interval cooldown
  local service_host target_transport ssh_user ssh_port ssh_identity_file
  service_name="$(prompt_required "Systemd service name to monitor")"
  monitor_id="$(prompt_default "Custom monitor id" "$service_name")"
  monitor_name="$(prompt_default "Custom monitor name" "$monitor_id")"
  service_host="$(prompt_default "Target host/IP for service control" "127.0.0.1")"
  if [[ "$service_host" == "127.0.0.1" || "$service_host" == "localhost" ]]; then
    target_transport="local"
  else
    target_transport="ssh"
  fi
  target_transport="$(prompt_default "Service control transport (local or ssh)" "$target_transport")"
  target_transport="$(printf '%s' "$target_transport" | tr '[:upper:]' '[:lower:]')"
  ssh_user=""
  ssh_port="22"
  ssh_identity_file=""
  if [[ "$target_transport" == "ssh" ]]; then
    ssh_user="$(prompt_default "SSH user" "nima")"
    ssh_port="$(prompt_default "SSH port" "22")"
    ssh_identity_file="$(prompt_default "SSH identity file on procmon host" "/home/nima/.ssh/id_ed25519")"
  fi
  health_url="$(prompt_default "Optional health URL (leave blank for none)" "")"
  interval="$(prompt_default "Custom service check interval" "1m")"
  cooldown="$(prompt_default "Custom service recovery cooldown" "10m")"

  export PROC_TEMPLATE="systemd_service"
  export PROC_MONITOR_ID="$monitor_id"
  export PROC_MONITOR_NAME="$monitor_name"
  export PROC_SERVICE_NAME="$service_name"
  export PROC_TARGET_TRANSPORT="$target_transport"
  if [[ "$target_transport" == "ssh" ]]; then
    export PROC_TARGET_HOST="$service_host"
  else
    export PROC_TARGET_HOST=""
  fi
  export PROC_TARGET_USER="$ssh_user"
  export PROC_TARGET_PORT="$ssh_port"
  export PROC_TARGET_IDENTITY_FILE="$ssh_identity_file"
  export PROC_HEALTH_URL="$health_url"
  export PROC_INTERVAL="$interval"
  export PROC_COOLDOWN="$cooldown"
  append_monitor_json
}

systemctl_state() {
  local state_type="$1"
  local service_name="$2"
  local state_output

  state_output="$(systemctl "$state_type" "$service_name" 2>/dev/null || true)"
  state_output="$(printf '%s' "$state_output" | head -n 1 | tr -d '\r')"
  if [[ -z "$state_output" ]]; then
    printf '%s\n' "unknown"
    return
  fi
  printf '%s\n' "$state_output"
}

show_post_install_status() {
  local service_name="$1"
  local api_addr="$2"
  local primary_ip enabled_state active_state status_json

  primary_ip="$(hostname -I 2>/dev/null | awk '{print $1}')"
  enabled_state="$(systemctl_state is-enabled "$service_name")"
  active_state="$(systemctl_state is-active "$service_name")"

  echo
  echo "Install OK"
  echo "Assigned IP: ${primary_ip:-unknown}"
  echo "Service enabled: $enabled_state"
  echo "Service active: $active_state"

  for _ in 1 2 3 4 5; do
    if status_json="$(curl -fsS --max-time 5 "http://$api_addr/status" 2>/dev/null)"; then
      break
    fi
    sleep 1
  done

  if [[ -n "${status_json:-}" ]]; then
    if PROC_STATUS_JSON="$status_json" python3 - <<'PY_STATUS'
import json
import os

payload = json.loads(os.environ["PROC_STATUS_JSON"])
print("Procmon API status check: PASS")
print(f"Overall status: {payload.get('overall_status', 'unknown')}")
for monitor in payload.get('monitors', []):
    monitor_id = monitor.get('id', 'unknown')
    status = monitor.get('status', 'unknown')
    print(f"  - {monitor_id}: {status}")
PY_STATUS
    then
      :
    else
      echo "Procmon API status check: FAIL"
      echo "  Procmon API responded, but bootstrap could not parse the JSON payload."
    fi
  else
    echo "Procmon API status check: FAIL"
    echo "  Unable to reach http://$api_addr/status yet."
  fi
}

monitor_menu() {
  while true; do
    echo
    echo "Select a monitor template to add:"
    echo "  1) MA352 bridge (homebridge-mcintosh-rs232)"
    echo "  2) Scrypted Arlo"
    echo "  3) Generic systemd service"
    echo "  4) Finish"
    local choice
    read -r -p "Choice [4]: " choice
    choice="${choice:-4}"
    case "$choice" in
      1) add_ma352_monitor ;;
      2) add_scrypted_arlo_monitor ;;
      3) add_systemd_service_monitor ;;
      4) break ;;
      *) echo "Invalid choice." ;;
    esac
  done
}

main() {
  require_root
  require_linux
  if ! command -v python3 >/dev/null 2>&1; then
    echo "python3 is required for config generation."
    exit 1
  fi

  local config_dir config_file env_file service_file bin_path api_addr log_file state_file
  local api_addr_default log_file_default state_file_default
  local detected_ipv4s detected_primary_ipv4
  config_dir="$(prompt_default "rpi-procmon config directory" "$DEFAULT_CONFIG_DIR")"
  config_file="$config_dir/config.json"
  if [[ -f "$config_file" ]]; then
    echo "Existing config detected at $config_file. Matching monitor ids will be replaced; other monitors will be preserved."
  fi
  env_file="$(prompt_default "Environment file path" "$DEFAULT_ENV_FILE")"
  service_file="$(prompt_default "systemd service path" "$DEFAULT_SERVICE_FILE")"
  bin_path="$(prompt_default "Binary install path" "$DEFAULT_BIN_PATH")"
  api_addr_default="$(read_json_string "$config_file" "api.listen_address")"
  log_file_default="$(read_json_string "$config_file" "log_file")"
  state_file_default="$(read_json_string "$config_file" "state_file")"
  detected_ipv4s="$(detect_ipv4_addresses || true)"
  detected_primary_ipv4="$(first_line "$detected_ipv4s" || true)"
  if [[ -n "$detected_ipv4s" ]]; then
    echo "Detected IPv4 addresses:"
    printf '%s\n' "$detected_ipv4s" | sed 's/^/  - /'
  fi
  if [[ -z "$api_addr_default" && -n "$detected_primary_ipv4" ]]; then
    api_addr_default="${detected_primary_ipv4}:9645"
  fi
  api_addr="$(prompt_default "Procmon API listen address" "${api_addr_default:-$DEFAULT_API_ADDR}")"
  log_file="$(prompt_default "Procmon log file" "${log_file_default:-$DEFAULT_LOG_FILE}")"
  state_file="$(prompt_default "Procmon state file" "${state_file_default:-$DEFAULT_STATE_FILE}")"
  BIN_PATH="$bin_path"

  monitor_menu
  if [[ ! -s "$TMP_MONITORS" ]]; then
    echo "No monitors selected. Nothing to deploy."
    exit 1
  fi

  ensure_logrotate
  ensure_go
  build_binary
  write_config_file "$config_file" "$api_addr" "$log_file" "$state_file"
  install_service_files "$config_file" "$env_file" "$service_file" "$log_file" "$state_file" "$api_addr"

  systemctl daemon-reload
  systemctl enable "$(basename "$service_file")"
  if systemctl is-active --quiet "$(basename "$service_file")"; then
    systemctl restart "$(basename "$service_file")"
  else
    systemctl start "$(basename "$service_file")"
  fi

  show_post_install_status "$(basename "$service_file")" "$api_addr"

  echo
  echo "Deployment complete."
  echo "Binary: $BIN_PATH"
  echo "Config directory: $config_dir"
  echo "Config file: $config_file"
  echo "Environment file: $env_file"
  echo "Service: $(basename "$service_file")"
  echo "API: http://$api_addr/health"
  echo
  echo "Useful commands:"
  echo "  systemctl status $(basename "$service_file")"
  echo "  journalctl -u $(basename "$service_file") -f"
  echo "  cat $config_file"
}

main "$@"
