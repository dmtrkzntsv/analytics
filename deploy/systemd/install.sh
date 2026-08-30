#!/usr/bin/env bash
# Installer for the twillingate collector (spec §11).
#
# From a checkout (after `make build`):
#   sudo ./install.sh [--user NAME] [--yes]
#
# Straight from GitHub — downloads the latest release for this machine:
#   curl -fsSL https://raw.githubusercontent.com/dmtrkzntsv/twillingate/main/deploy/systemd/install.sh | sudo bash
#   curl -fsSL ...install.sh | sudo bash -s -- --yes --version v26.825.1
set -euo pipefail

REPO="dmtrkzntsv/twillingate"
VERSION="${TWILLINGATE_VERSION:-latest}"
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

# The script lives at deploy/systemd/install.sh, so the checkout (or the
# unpacked tarball) root is two directories up. Under `curl | bash` there is
# no script file and the ../.. walk lands somewhere without the unit file,
# which is exactly the remote-mode signal.
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"
root="$(cd "$script_dir/../.." 2>/dev/null && pwd || echo "$script_dir")"
if [ -f "$root/deploy/systemd/twillingate.service" ] && [ -e "$root/twillingate" ]; then
  # Local mode: running from a checkout or an unpacked release tarball.
  bin="$root/twillingate"
  [ -x "$bin" ] || die "twillingate binary not found at the repo root; run 'make build' first"
else
  # Remote mode (curl | bash): fetch a release tarball and install from it.
  [ "$(uname -s)" = Linux ] || die "only Linux is supported"
  command -v curl >/dev/null 2>&1 || die "curl is required"
  a="$(arch)" || die "unsupported architecture: $(uname -m)"
  asset="twillingate-linux-$a.tar.gz"
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
  bin="$tmp/twillingate"
  [ -x "$bin" ] || die "release tarball did not contain the twillingate binary"
fi

if [ -z "$SERVICE_USER" ]; then
  if [ "$ASSUME_YES" -eq 1 ] || ! [ -r /dev/tty ]; then
    SERVICE_USER=twillingate
  else
    # stdin may be the script itself under `curl | bash`; prompt via the tty.
    read -r -p "Service account to run under [twillingate]: " SERVICE_USER < /dev/tty
    SERVICE_USER=${SERVICE_USER:-twillingate}
  fi
fi

if ! id "$SERVICE_USER" >/dev/null 2>&1; then
  echo "Creating system user $SERVICE_USER"
  useradd --system --home-dir /var/lib/twillingate --shell /usr/sbin/nologin "$SERVICE_USER"
fi

install -m 0755 "$bin" /usr/local/bin/twillingate
install -d -m 0750 -o "$SERVICE_USER" -g "$SERVICE_USER" /var/lib/twillingate
install -d -m 0755 /etc/twillingate

if [ ! -f /etc/twillingate/twillingate.env ]; then
  install -m 0640 -g "$SERVICE_USER" "$root/.env.example" /etc/twillingate/twillingate.env
  echo "Installed /etc/twillingate/twillingate.env — EDIT IT (R2 credentials, geo)."
fi
if [ ! -f /etc/litestream.yml ] && [ -f "$root/deploy/litestream/litestream.yml" ]; then
  install -m 0644 "$root/deploy/litestream/litestream.yml" /etc/litestream.yml
fi
if [ -d /etc/logrotate.d ] && [ -f "$root/deploy/logrotate/twillingate" ]; then
  install -m 0644 "$root/deploy/logrotate/twillingate" /etc/logrotate.d/twillingate
fi

# Litestream is not installed by us — it may live in /usr/local/bin (manual
# tarball) or /usr/bin (package manager), so bake in wherever it actually is.
# The fallback keeps the unit sensible when it is installed later by hand.
litestream_bin="$(command -v litestream || true)"
litestream_bin="${litestream_bin:-/usr/local/bin/litestream}"

for unit in twillingate litestream; do
  sed -e "s/__USER__/$SERVICE_USER/g" \
      -e "s|__LITESTREAM__|$litestream_bin|g" \
      "$root/deploy/systemd/$unit.service" \
    > "/etc/systemd/system/$unit.service"
done
systemctl daemon-reload
systemctl enable twillingate.service
if command -v litestream >/dev/null 2>&1; then
  systemctl enable litestream.service
else
  echo "NOTE: litestream binary not found; install it (https://litestream.io/install/)."
  echo "  If it does not land at $litestream_bin, fix ExecStart= in"
  echo "  /etc/systemd/system/litestream.service, then: systemctl enable --now litestream"
fi

cat <<EOF_DONE

Installed. Next steps:
  1. Edit /etc/twillingate/twillingate.env (R2 credentials, geo)
  2. systemctl start twillingate   (and litestream once installed)
  3. Put Cloudflare/Caddy/nginx in front of 127.0.0.1:8080 for TLS
  4. Create your first project:
       sudo -u $SERVICE_USER sh -ac '. /etc/twillingate/twillingate.env; twillingate project create -alias myapp'
     Then issue an ingest key (prints a ready-to-paste web snippet):
       sudo -u $SERVICE_USER sh -ac '. /etc/twillingate/twillingate.env; twillingate key issue -project myapp -label web'
  5. Apps post to https://YOUR_DOMAIN/api/events — see docs/ingest-api.md
EOF_DONE
