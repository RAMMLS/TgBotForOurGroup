#!/usr/bin/env bash

set -euo pipefail

DEFAULT_APP_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
APP_DIR="${APP_DIR:-${DEFAULT_APP_DIR}}"
ENV_FILE="${ENV_FILE:-${APP_DIR}/.env}"
DEPLOY_REMOTE="${DEPLOY_REMOTE:-origin}"
DEPLOY_BRANCH="${DEPLOY_BRANCH:-main}"
LOCK_FILE="${LOCK_FILE:-${APP_DIR}/.deploy.lock}"

log() {
  echo "[selfhosted-update] $*"
}

fail() {
  echo "[selfhosted-update] ERROR: $*" >&2
  exit 1
}

ensure_clean_git_state() {
  local current_branch

  current_branch="$(git branch --show-current)"
  if [[ -z "${current_branch}" ]]; then
    fail "Cannot determine current git branch in ${APP_DIR}"
  fi
  if [[ "${current_branch}" != "${DEPLOY_BRANCH}" ]]; then
    fail "Expected branch ${DEPLOY_BRANCH}, got ${current_branch}. Switch the server clone to ${DEPLOY_BRANCH} first."
  fi
  if [[ -n "$(git status --porcelain)" ]]; then
    fail "Working tree is dirty in ${APP_DIR}. Commit/stash local changes on the server clone before enabling auto deploy."
  fi
}

main() {
  [[ -d "${APP_DIR}" ]] || fail "APP_DIR does not exist: ${APP_DIR}"
  [[ -f "${ENV_FILE}" ]] || fail ".env file was not found: ${ENV_FILE}"

  mkdir -p "$(dirname "${LOCK_FILE}")"
  exec 9>"${LOCK_FILE}"
  if command -v flock >/dev/null 2>&1; then
    flock -n 9 || fail "Another deploy is already running."
  fi

  cd "${APP_DIR}"

  if git rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    ensure_clean_git_state
    log "Fetching ${DEPLOY_REMOTE}/${DEPLOY_BRANCH}..."
    git fetch "${DEPLOY_REMOTE}" "${DEPLOY_BRANCH}"
    log "Pulling latest changes..."
    git pull --ff-only "${DEPLOY_REMOTE}" "${DEPLOY_BRANCH}"
  fi

  log "Building and starting containers..."
  docker compose --env-file "${ENV_FILE}" up -d --build

  log "Current container status:"
  docker compose ps
  log "Deploy finished successfully."
}

main "$@"
