#!/bin/sh

set -eu

mkdir -p /data
chown -R 10001:10001 /data

exec su-exec 10001:10001 "$@"
