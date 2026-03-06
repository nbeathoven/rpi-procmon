#!/usr/bin/env bash
set -euo pipefail

REPO_URL="${RPI_PROCMON_REPO_URL:-https://github.com/nbeathoven/rpi-procmon.git}"
INSTALL_ROOT="${RPI_PROCMON_INSTALL_ROOT:-/opt/rpi-procmon}"
CHECKOUT_DIR="${RPI_PROCMON_CHECKOUT_DIR:-$INSTALL_ROOT/repo}"
BRANCH="${RPI_PROCMON_BRANCH:-main}"
APT_UPDATED=""

require_root() {
  if [[ "$(id -u)" -ne 0 ]]; then
    echo "Run this installer with sudo."
    exit 1
  fi
}

require_linux() {
  if [[ "$(uname -s)" != "Linux" ]]; then
    echo "This installer is intended for Raspberry Pi Linux hosts."
    exit 1
  fi
}

ensure_apt_package() {
  local package="$1"
  if dpkg -s "$package" >/dev/null 2>&1; then
    return
  fi
  if [[ -z "$APT_UPDATED" ]]; then
    apt-get update
    APT_UPDATED="1"
  fi
  apt-get install -y "$package"
}

ensure_base_packages() {
  ensure_apt_package ca-certificates
  ensure_apt_package git
  ensure_apt_package curl
  ensure_apt_package python3
  ensure_apt_package tar
  ensure_apt_package logrotate
}

clone_or_update_repo() {
  install -d -m 0755 "$INSTALL_ROOT"
  if [[ -d "$CHECKOUT_DIR/.git" ]]; then
    git -C "$CHECKOUT_DIR" fetch origin "$BRANCH"
    git -C "$CHECKOUT_DIR" checkout "$BRANCH"
    git -C "$CHECKOUT_DIR" pull --ff-only origin "$BRANCH"
    return
  fi

  if [[ -e "$CHECKOUT_DIR" ]]; then
    echo "Install path exists but is not a git checkout: $CHECKOUT_DIR"
    exit 1
  fi

  git clone --branch "$BRANCH" "$REPO_URL" "$CHECKOUT_DIR"
}

main() {
  require_root
  require_linux
  ensure_base_packages
  clone_or_update_repo

  echo
  echo "Launching interactive bootstrap from $CHECKOUT_DIR"
  echo
  exec "$CHECKOUT_DIR/scripts/bootstrap-rpi.sh"
}

main "$@"
