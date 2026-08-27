#!/usr/bin/env bash
# Installer for the analytics collector (spec §11).
#
# From a checkout (after `make build`):
#   sudo ./install.sh [--user NAME] [--yes]
#
# Straight from GitHub — downloads the latest release for this machine:
#   curl -fsSL https://raw.githubusercontent.com/dmtrkzntsv/analytics/main/install.sh | sudo bash
#   curl -fsSL ...install.sh | sudo bash -s -- --yes --version v26.825.1
set -euo pipefail

REPO="dmtrkzntsv/analytics"
VERSION="${ANALYTICS_VERSION:-latest}"
SERVICE_USER=""
ASSUME_YES=0
while [ $# -gt 0 ]; do
  case "$1" in
    --user)    SERVICE_USER="$2"; shift 2 ;;
    --yes)     ASSUME_YES=1; shift ;;
    --version) VERSION="$2"; shift 2 ;;
    *) echo "unknown flag: $1"; exit 2 ;;
  esac
done

die() { echo "$*" >&2; exit 1; }

[ "$(id -u)" -eq 0 ] || die "run as root (sudo ./install.sh)"

tmp=""
cleanup() { if [ -n "$tmp" ]; then rm -rf "$tmp"; fi; }
trap cleanup EXIT

arch() {
  case "$(uname -m)" in
    x86_64)        echo amd64 ;;
    aarch64|arm64) echo arm64 ;;
    armv7l|armv6l) echo arm ;;
    *) return 1 ;;
  esac
}

root="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"
if [ -f "$root/deploy/systemd/analytics.service" ]; then
  # Local mode: running from a checkout or an unpacked release tarball.
  bin="$root/analytics"
  [ -x "$bin" ] || die "analytics binary not found next to installer; run 'make build' first"
else
  # Remote mode (curl | bash): fetch a release tarball and install from it.
  [ "$(uname -s)" = Linux ] || die "only Linux is supported"
  command -v curl >/dev/null 2>&1 || die "curl is required"
  a="$(arch)" || die "unsupported architecture: $(uname -m)"
  asset="analytics-linux-$a.tar.gz"
  tmp="$(mktemp -d)"
  echo "Downloading $asset ($VERSION) from github.com/$REPO"
  case "$VERSION" in
    latest) base="https://github.com/$REPO/releases/latest/download" ;;
    *)      base="https://github.com/$REPO/releases/download/$VERSION" ;;
  esac
  curl -fsSL -o "$tmp/$asset" "$base/$asset" \
    || die "download failed — is a release published?"
  curl -fsSL -o "$tmp/SHA256SUMS" "$base/SHA256SUMS"
  (cd "$tmp" && sha256sum --check --ignore-missing --quiet SHA256SUMS) \
    || die "checksum verification failed"
  tar -xzf "$tmp/$asset" -C "$tmp"
  root="$tmp"
  bin="$tmp/analytics"
  [ -x "$bin" ] || die "release tarball did not contain the analytics binary"
fi

if [ -z "$SERVICE_USER" ]; then
  if [ "$ASSUME_YES" -eq 1 ] || ! [ -r /dev/tty ]; then
    SERVICE_USER=analytics
  else
    # stdin may be the script itself under `curl | bash`; prompt via the tty.
    read -r -p "Service account to run under [analytics]: " SERVICE_USER < /dev/tty
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

if [ ! -f /etc/analytics/analytics.env ]; then
  install -m 0640 -g "$SERVICE_USER" "$root/.env.example" /etc/analytics/analytics.env
  echo "Installed /etc/analytics/analytics.env — EDIT IT (R2 credentials, geo)."
fi
if [ ! -f /etc/analytics/projects.json ]; then
  install -m 0640 -g "$SERVICE_USER" "$root/projects.example.json" /etc/analytics/projects.json
  # The example ships a placeholder key; substitute a real one so a fresh
  # install has a usable credential rather than a value someone might keep.
  if key=$(/usr/local/bin/analytics keygen 2>/dev/null | grep -o 'ak_[0-9a-f]\{32\}' | head -1) \
     && [ -n "$key" ]; then
    sed -i "s/REPLACE_ME_RUN_ANALYTICS_KEYGEN/$key/" /etc/analytics/projects.json
    echo "Installed /etc/analytics/projects.json with a generated ingest key — EDIT IT (projects, origins)."
    echo "  ingest key: $key"
  else
    echo "Installed /etc/analytics/projects.json — EDIT IT (projects, origins, ingest_keys)."
    echo "  run 'analytics keygen' to generate an ingest key."
  fi
fi
if [ ! -f /etc/litestream.yml ] && [ -f "$root/deploy/litestream/litestream.yml" ]; then
  install -m 0644 "$root/deploy/litestream/litestream.yml" /etc/litestream.yml
fi
if [ -d /etc/logrotate.d ] && [ -f "$root/deploy/logrotate/analytics" ]; then
  install -m 0644 "$root/deploy/logrotate/analytics" /etc/logrotate.d/analytics
fi

# Litestream is not installed by us — it may live in /usr/local/bin (manual
# tarball) or /usr/bin (package manager), so bake in wherever it actually is.
# The fallback keeps the unit sensible when it is installed later by hand.
litestream_bin="$(command -v litestream || true)"
litestream_bin="${litestream_bin:-/usr/local/bin/litestream}"

for unit in analytics litestream; do
  sed -e "s/__USER__/$SERVICE_USER/g" \
      -e "s|__LITESTREAM__|$litestream_bin|g" \
      "$root/deploy/systemd/$unit.service" \
    > "/etc/systemd/system/$unit.service"
done
systemctl daemon-reload
systemctl enable analytics.service
if command -v litestream >/dev/null 2>&1; then
  systemctl enable litestream.service
else
  echo "NOTE: litestream binary not found; install it (https://litestream.io/install/)."
  echo "  If it does not land at $litestream_bin, fix ExecStart= in"
  echo "  /etc/systemd/system/litestream.service, then: systemctl enable --now litestream"
fi

cat <<EOF_DONE

Installed. Next steps:
  1. Edit /etc/analytics/projects.json (projects, allowed_origins, ingest_keys)
     Every project needs at least one ingest key or the service will not start.
     Generate more with: analytics keygen -n 3
  2. Edit /etc/analytics/analytics.env (R2 credentials, geo)
  3. systemctl start analytics   (and litestream once installed)
  4. Put Cloudflare/Caddy/nginx in front of 127.0.0.1:8080 for TLS
  5. Embed, using the ingest key from projects.json:
       <script defer src="https://YOUR_DOMAIN/js/script.js"
               data-key="ak_..." data-identity="anonymous"></script>
  6. Apps post to https://YOUR_DOMAIN/api/events — see docs/ingest-api.md
EOF_DONE
