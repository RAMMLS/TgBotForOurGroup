#!/usr/bin/env bash

set -euo pipefail

BOT_HOME="${BOT_HOME:-/opt/tgbotforourgroup}"
APP_DIR="${APP_DIR:-${BOT_HOME}/app}"
BIN_PATH="${BIN_PATH:-${BOT_HOME}/bin/tgbot}"
GO_BIN="${GO_BIN:-/usr/local/go/bin/go}"

cd "${APP_DIR}"

git pull --ff-only
"${GO_BIN}" build -o "${BIN_PATH}" ./cmd/bot

sudo systemctl restart tgbotforourgroup
sudo systemctl status tgbotforourgroup --no-pager
