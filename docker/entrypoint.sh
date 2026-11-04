#!/bin/sh
set -e

PUID=${PUID:-1000}
PGID=${PGID:-1000}
DOWNLOAD_DIR=${DOWNLOAD_DIR:-/downloads}
CONFIG_PATH=${CONFIG_PATH:-/config/config.json}

if [ "$(id -u)" = "0" ]; then
    mkdir -p "$DOWNLOAD_DIR" "$(dirname "$CONFIG_PATH")"
    chown -R "$PUID:$PGID" "$DOWNLOAD_DIR" "$(dirname "$CONFIG_PATH")" 2>/dev/null || true
    exec su-exec "$PUID:$PGID" /usr/local/bin/m3u8-downloader "$@"
fi

exec /usr/local/bin/m3u8-downloader "$@"
