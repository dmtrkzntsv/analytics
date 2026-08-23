#!/usr/bin/env bash
# Installer for the analytics collector (spec §11).
# Usage: sudo ./install.sh [--user NAME] [--yes]
set -euo pipefail

SERVICE_USER=""
ASSUME_YES=0
while [ $# -gt 0 ]; do
  case "$1" in
    --user) SERVICE_USER="$2"; shift 2 ;;
    --yes)  ASSUME_YES=1; shift ;;
    *) echo "unknown flag: $1"; exit 2 ;;
  esac
done

[ "$(id -u)" -eq 0 ] || { echo "run as root (sudo ./install.sh)"; exit 1; }

here="$(cd "$(dirname "$0")" && pwd)"
bin="$here/../analytics"
[ -x "$bin" ] || bin="$here/analytics"
[ -x "$bin" ] || { echo "analytics binary not found next to installer; run 'make build' first"; exit 1; }

if [ -z "$SERVICE_USER" ]; then
  if [ "$ASSUME_YES" -eq 1 ]; then
    SERVICE_USER=analytics
  else
    read -r -p "Service account to run under [analytics]: " SERVICE_USER
    SERVICE_USER=${SERVICE_USER:-analytics}
  fi
fi

if ! id "$SERVICE_USER" >/dev/null 2>&1; then
  echo "Creating system user $SERVICE_USER"
  useradd --system --home-dir /var/lib/analytics --shell /usr/sbin/nologin "$SERVICE_USER"
fi

install -m 0755 "$bin" /usr/local/bin/analytics
install -d -m 0750 -o "$SERVICE_USER" -g "$SERVICE_USER" /var/lib/analytics
install -d -m 0755 /etc/analytics

if [ ! -f /etc/analytics/config.json ]; then
  install -m 0640 -g "$SERVICE_USER" "$here/config.example.json" /etc/analytics/config.json
  echo "Installed /etc/analytics/config.json — EDIT IT (projects, origins)."
fi
if [ ! -f /etc/analytics/litestream.env ]; then
  cat > /etc/analytics/litestream.env <<'EOF_ENV'
# R2/S3 credentials for litestream (referenced by litestream.service)
LITESTREAM_ACCESS_KEY_ID=CHANGE_ME
LITESTREAM_SECRET_ACCESS_KEY=CHANGE_ME
R2_BUCKET=CHANGE_ME
R2_ENDPOINT=https://ACCOUNT_ID.r2.cloudflarestorage.com
EOF_ENV
  chmod 0640 /etc/analytics/litestream.env
  chgrp "$SERVICE_USER" /etc/analytics/litestream.env
  echo "Installed /etc/analytics/litestream.env — EDIT IT (R2 credentials)."
fi
if [ ! -f /etc/litestream.yml ] && [ -f "$here/litestream/litestream.yml" ]; then
  install -m 0644 "$here/litestream/litestream.yml" /etc/litestream.yml
fi

for unit in analytics litestream; do
  sed "s/__USER__/$SERVICE_USER/g" "$here/systemd/$unit.service" \
    > "/etc/systemd/system/$unit.service"
done
systemctl daemon-reload
systemctl enable analytics.service
if command -v litestream >/dev/null 2>&1; then
  systemctl enable litestream.service
else
  echo "NOTE: litestream binary not found; install it (https://litestream.io/install/) then: systemctl enable --now litestream"
fi

cat <<EOF_DONE

Installed. Next steps:
  1. Edit /etc/analytics/config.json (projects, allowed_origins, geo)
  2. Edit /etc/analytics/litestream.env (R2 credentials)
  3. systemctl start analytics   (and litestream once installed)
  4. Put Cloudflare/Caddy/nginx in front of 127.0.0.1:8080 for TLS
  5. Embed: <script defer src="https://YOUR_DOMAIN/js/script.js" data-project="myapp"></script>
EOF_DONE
