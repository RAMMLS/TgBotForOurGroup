#!/usr/bin/env bash

set -euo pipefail

GO_VERSION="${GO_VERSION:-1.25.0}"
BOT_USER="${BOT_USER:-tgbot}"
BOT_HOME="${BOT_HOME:-/opt/tgbotforourgroup}"
BOT_GROUP="${BOT_GROUP:-${BOT_USER}}"

if [[ "${EUID}" -ne 0 ]]; then
  echo "Run this script as root: sudo bash deploy/vdsina/install-server.sh"
  exit 1
fi

export DEBIAN_FRONTEND=noninteractive

apt-get update
apt-get install -y ca-certificates curl git sqlite3

ARCH="$(dpkg --print-architecture)"
case "${ARCH}" in
  amd64)
    GO_ARCH="amd64"
    ;;
  arm64)
    GO_ARCH="arm64"
    ;;
  *)
    echo "Unsupported architecture: ${ARCH}"
    exit 1
    ;;
esac

curl -fsSL "https://go.dev/dl/go${GO_VERSION}.linux-${GO_ARCH}.tar.gz" -o /tmp/go.tar.gz
rm -rf /usr/local/go
tar -C /usr/local -xzf /tmp/go.tar.gz
rm -f /tmp/go.tar.gz

if ! getent group "${BOT_GROUP}" >/dev/null 2>&1; then
  groupadd --system "${BOT_GROUP}"
fi

if ! id -u "${BOT_USER}" >/dev/null 2>&1; then
  useradd --system --gid "${BOT_GROUP}" --home-dir "${BOT_HOME}" --create-home --shell /usr/sbin/nologin "${BOT_USER}"
fi

mkdir -p "${BOT_HOME}/app" "${BOT_HOME}/bin" "${BOT_HOME}/data"
chown -R "${BOT_USER}:${BOT_GROUP}" "${BOT_HOME}"
chmod 750 "${BOT_HOME}" "${BOT_HOME}/app" "${BOT_HOME}/bin" "${BOT_HOME}/data"

cat >/etc/profile.d/go.sh <<'EOF'
export PATH=/usr/local/go/bin:$PATH
EOF
chmod 644 /etc/profile.d/go.sh

echo
echo "Server is ready."
echo "Next steps:"
echo "1. Clone the repository into ${BOT_HOME}/app"
echo "2. Create ${BOT_HOME}/app/.env and set SQLITE_PATH=${BOT_HOME}/data/bot.db"
echo "3. Build the bot:"
echo "   cd ${BOT_HOME}/app && /usr/local/go/bin/go build -o ${BOT_HOME}/bin/tgbot ./cmd/bot"
echo "4. Copy deploy/vdsina/tgbotforourgroup.service to /etc/systemd/system/"
echo "5. Run: sudo systemctl daemon-reload && sudo systemctl enable --now tgbotforourgroup"
