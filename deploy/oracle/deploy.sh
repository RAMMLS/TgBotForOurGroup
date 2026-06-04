#!/usr/bin/env bash

set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
ENV_FILE="${ROOT_DIR}/.env"
DATA_DIR="${ROOT_DIR}/data"

if [[ ! -f "${ENV_FILE}" ]]; then
  echo ".env was not found. Create it from .env.example first."
  exit 1
fi

mkdir -p "${DATA_DIR}"

docker compose -f "${ROOT_DIR}/docker-compose.yml" --env-file "${ENV_FILE}" up -d --build

echo
echo "Bot is deployed."
echo "Useful commands:"
echo "  docker compose -f ${ROOT_DIR}/docker-compose.yml logs -f bot"
echo "  docker compose -f ${ROOT_DIR}/docker-compose.yml ps"
echo "  docker compose -f ${ROOT_DIR}/docker-compose.yml restart bot"
