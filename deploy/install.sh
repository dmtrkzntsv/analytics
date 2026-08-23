#!/usr/bin/env bash
# Installer for the analytics collector (spec §11).
#
# From a checkout (after `make build`):
#   sudo ./deploy/install.sh [--user NAME] [--yes]
#
# Straight from GitHub — downloads the latest release for this machine:
#   curl -fsSL https://raw.githubusercontent.com/dmtrkzntsv/analytics/main/deploy/install.sh | sudo bash
#   curl -fsSL ...install.sh | sudo bash -s -- --yes --version v0.2.0
#
# While the repository is private, both the curl above and the release
# download need a token with repo read access:
#   curl -fsSL -H "Authorization: Bearer $GITHUB_TOKEN" \
#     https://raw.githubusercontent.com/dmtrkzntsv/analytics/main/deploy/install.sh \
#     | sudo GITHUB_TOKEN="$GITHUB_TOKEN" bash
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

gh_curl() {
  curl -fsSL ${GITHUB_TOKEN:+-H "Authorization: Bearer $GITHUB_TOKEN"} "$@"
}

arch() {
  case "$(uname -m)" in
    x86_64)        echo amd64 ;;
    aarch64|arm64) echo arm64 ;;
    armv7l|armv6l) echo arm ;;
    *) return 1 ;;
  esac
}

here="$(cd "$(dirname "${BASH_SOURCE[0]:-$0}")" && pwd)"
if [ -f "$here/systemd/analytics.service" ]; then
  # Local mode: running from a checkout or an unpacked release tarball.
  bin="$here/../analytics"
  [ -x "$bin" ] || bin="$here/analytics"
  [ -x "$bin" ] || die "analytics binary not found next to installer; run 'make build' first"
else
  # Remote mode (curl | bash): fetch a release tarball and install from it.
  [ "$(uname -s)" = Linux ] || die "only Linux is supported"
  command -v curl >/dev/null 2>&1 || die "curl is required"
  a="$(arch)" || die "unsupported architecture: $(uname -m)"
  asset="analytics-linux-$a.tar.gz"
  tmp="$(mktemp -d)"
  echo "Downloading $asset ($VERSION) from github.com/$REPO"
  if [ -n "${GITHUB_TOKEN:-}" ]; then
    # Private repo: release assets are only reachable through the API,
    # by asset id, with an octet-stream Accept header.
    command -v python3 >/dev/null 2>&1 || die "python3 is required for token-authenticated downloads"
    api="https://api.github.com/repos/$REPO/releases"
    case "$VERSION" in
      latest) rel="$api/latest" ;;
      *)      rel="$api/tags/$VERSION" ;;
    esac
    json="$(gh_curl "$rel")" || die "release $VERSION not found (is one published?)"
    for name in "$asset" SHA256SUMS; do
      id="$(printf '%s' "$json" | python3 -c '
import json, sys
rel = json.load(sys.stdin)
ids = [a["id"] for a in rel["assets"] if a["name"] == sys.argv[1]]
if not ids: sys.exit(1)
print(ids[0])' "$name")" || die "release has no asset named $name"
      gh_curl -H "Accept: application/octet-stream" -o "$tmp/$name" "$api/assets/$id"
    done
  else
    case "$VERSION" in
      latest) base="https://github.com/$REPO/releases/latest/download" ;;
      *)      base="https://github.com/$REPO/releases/download/$VERSION" ;;
    esac
    gh_curl -o "$tmp/$asset" "$base/$asset" \
      || die "download failed — no release published yet, or the repo is private (set GITHUB_TOKEN)"
    gh_curl -o "$tmp/SHA256SUMS" "$base/SHA256SUMS"
  fi
  (cd "$tmp" && sha256sum --check --ignore-missing --quiet SHA256SUMS) \
    || die "checksum verification failed"
  tar -xzf "$tmp/$asset" -C "$tmp"
  here="$tmp/deploy"
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
  install -m 0640 -g "$SERVICE_USER" "$here/analytics.example.env" /etc/analytics/analytics.env
  echo "Installed /etc/analytics/analytics.env — EDIT IT (R2 credentials, geo)."
fi
if [ ! -f /etc/analytics/projects.json ]; then
  install -m 0640 -g "$SERVICE_USER" "$here/projects.example.json" /etc/analytics/projects.json
  echo "Installed /etc/analytics/projects.json — EDIT IT (projects, origins)."
fi
if [ -f /etc/analytics/config.json ]; then
  echo "WARNING: /etc/analytics/config.json is no longer read. Move its infra"
  echo "  settings into analytics.env and its projects array into projects.json."
fi
if [ ! -f /etc/litestream.yml ] && [ -f "$here/litestream/litestream.yml" ]; then
  install -m 0644 "$here/litestream/litestream.yml" /etc/litestream.yml
fi
if [ -d /etc/logrotate.d ] && [ -f "$here/logrotate/analytics" ]; then
  install -m 0644 "$here/logrotate/analytics" /etc/logrotate.d/analytics
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
  1. Edit /etc/analytics/projects.json (projects, allowed_origins)
  2. Edit /etc/analytics/analytics.env (R2 credentials, geo)
  3. systemctl start analytics   (and litestream once installed)
  4. Put Cloudflare/Caddy/nginx in front of 127.0.0.1:8080 for TLS
  5. Embed: <script defer src="https://YOUR_DOMAIN/js/script.js" data-project="myapp"></script>
EOF_DONE
