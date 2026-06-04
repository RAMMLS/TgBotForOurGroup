#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ENV_FILE="${ROOT_DIR}/.env"
ENV_EXAMPLE_FILE="${ROOT_DIR}/.env.example"
DATA_DIR="${ROOT_DIR}/data"

if [[ "$(uname -s)" != "Linux" ]]; then
  echo "This script is intended to run on a Linux server."
  exit 1
fi

if [[ "${EUID}" -eq 0 ]]; then
  SUDO=""
else
  SUDO="sudo"
fi

log() {
  echo "[build_all] $*"
}

fail() {
  echo "[build_all] ERROR: $*" >&2
  exit 1
}

require_command() {
  local cmd="$1"
  command -v "${cmd}" >/dev/null 2>&1 || fail "Required command not found: ${cmd}"
}

install_docker_if_needed() {
  if command -v docker >/dev/null 2>&1 && docker compose version >/dev/null 2>&1; then
    log "Docker and Docker Compose plugin are already installed."
    return
  fi

  require_command apt-get
  require_command curl

  log "Installing Docker and Docker Compose plugin..."
  ${SUDO} apt-get update
  ${SUDO} apt-get install -y ca-certificates curl gnupg
  require_command gpg

  ${SUDO} install -m 0755 -d /etc/apt/keyrings
  if [[ ! -f /etc/apt/keyrings/docker.asc ]]; then
    curl -fsSL https://download.docker.com/linux/ubuntu/gpg | ${SUDO} gpg --dearmor -o /etc/apt/keyrings/docker.asc
    ${SUDO} chmod a+r /etc/apt/keyrings/docker.asc
  fi

  local arch codename
  arch="$(dpkg --print-architecture)"
  codename="$(
    . /etc/os-release
    echo "${VERSION_CODENAME}"
  )"

  echo "deb [arch=${arch} signed-by=/etc/apt/keyrings/docker.asc] https://download.docker.com/linux/ubuntu ${codename} stable" \
    | ${SUDO} tee /etc/apt/sources.list.d/docker.list >/dev/null

  ${SUDO} apt-get update
  ${SUDO} apt-get install -y docker-ce docker-ce-cli containerd.io docker-buildx-plugin docker-compose-plugin

  ${SUDO} systemctl enable docker
  ${SUDO} systemctl restart docker

  if [[ -n "${SUDO}" ]]; then
    ${SUDO} usermod -aG docker "${USER}" || true
  fi
}

ensure_env_file() {
  if [[ ! -f "${ENV_FILE}" ]]; then
    if [[ -f "${ENV_EXAMPLE_FILE}" ]]; then
      cp "${ENV_EXAMPLE_FILE}" "${ENV_FILE}"
      log "Created ${ENV_FILE} from .env.example."
      fail "Fill in ${ENV_FILE} with real tokens and IDs, then run the script again."
    fi

    fail ".env file not found."
  fi
}

validate_env_file() {
  grep -Eq '^DISCORD_BOT_TOKEN=' "${ENV_FILE}" || fail "DISCORD_BOT_TOKEN is missing in .env"
  grep -Eq '^DISCORD_TARGET_GUILD_ID=' "${ENV_FILE}" || fail "DISCORD_TARGET_GUILD_ID is missing in .env"
  grep -Eq '^TELEGRAM_BOT_TOKEN=' "${ENV_FILE}" || fail "TELEGRAM_BOT_TOKEN is missing in .env"
  grep -Eq '^TELEGRAM_BOT_USERNAME=' "${ENV_FILE}" || fail "TELEGRAM_BOT_USERNAME is missing in .env"

  if grep -Eq 'your_discord_bot_token|your_telegram_bot_token|123456789012345678|YourTelegramBot' "${ENV_FILE}"; then
    fail ".env still contains placeholder values. Fill in real tokens and IDs first."
  fi
}

main() {
  cd "${ROOT_DIR}"

  install_docker_if_needed
  ensure_env_file
  validate_env_file

  mkdir -p "${DATA_DIR}"

  log "Building and starting the bot..."
  ${SUDO} docker compose --env-file "${ENV_FILE}" up -d --build

  log "Current container status:"
  ${SUDO} docker compose ps

  log "Done."
  log "Logs: ${SUDO} docker compose logs -f bot"
}

main "$@"
