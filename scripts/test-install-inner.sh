#!/usr/bin/env bash
# Container side of scripts/test-install.sh. Runs as root inside a throwaway
# Debian container with the repo mounted read-only at /src.
set -euo pipefail

# The container has no systemd; stub systemctl so enable/daemon-reload succeed.
printf '#!/bin/sh\necho "systemctl $*"\n' > /usr/local/bin/systemctl
chmod +x /usr/local/bin/systemctl
mkdir -p /etc/logrotate.d

# Copy out of the read-only mount, mimicking an unpacked checkout.
cp -r /src/deploy /tmp/deploy
cp /src/analytics /tmp/analytics

/tmp/deploy/install.sh --yes

echo "--- assertions"
test -x /usr/local/bin/analytics       && echo "ok: binary installed"
/usr/local/bin/analytics > /dev/null 2>&1 || [ $? -lt 126 ]
echo "ok: binary executes"
id analytics > /dev/null               && echo "ok: service user created"
d="$(stat -c '%a %U' /var/lib/analytics)"
[ "$d" = "750 analytics" ]             && echo "ok: data dir 0750 analytics"
grep -q 'User=analytics' /etc/systemd/system/analytics.service \
                                       && echo "ok: analytics.service templated"
grep -q 'User=analytics' /etc/systemd/system/litestream.service \
                                       && echo "ok: litestream.service templated"
[ "$(stat -c '%a' /etc/analytics/analytics.env)" = 640 ] \
                                       && echo "ok: analytics.env 0640"
[ "$(stat -c '%a' /etc/analytics/projects.json)" = 640 ] \
                                       && echo "ok: projects.json 0640"
grep -q '^DATABASE_URL=' /etc/analytics/analytics.env \
                                       && echo "ok: analytics.env has DATABASE_URL"
grep -q '"alias"' /etc/analytics/projects.json \
                                       && echo "ok: projects.json has a project"
grep -q 'EnvironmentFile=/etc/analytics/analytics.env' /etc/systemd/system/analytics.service \
                                       && echo "ok: unit loads analytics.env"
test -f /etc/litestream.yml            && echo "ok: litestream.yml installed"
test -f /etc/logrotate.d/analytics     && echo "ok: logrotate installed"

# Re-running must not clobber edited config files.
echo '[{"alias": "edited", "name": "E", "allowed_origins": ["https://e.com"]}]' > /etc/analytics/projects.json
echo 'DATABASE_URL=sqlite:///edited.db' > /etc/analytics/analytics.env
/tmp/deploy/install.sh --yes > /dev/null
grep -q edited /etc/analytics/projects.json && grep -q edited /etc/analytics/analytics.env \
  && echo "ok: rerun preserves config"

echo "INSTALL TEST OK"
