#!/usr/bin/env bash
# Container side of scripts/test-install.sh. Runs as root inside a throwaway
# Debian container with the repo mounted read-only at /src.
set -euo pipefail

# The container has no systemd; stub systemctl so enable/daemon-reload succeed.
printf '#!/bin/sh\necho "systemctl $*"\n' > /usr/local/bin/systemctl
chmod +x /usr/local/bin/systemctl
mkdir -p /etc/logrotate.d

# Copy out of the read-only mount, mimicking an unpacked checkout: the
# installer resolves the examples and binary relative to its own directory.
cp -r /src/deploy /tmp/deploy
cp /src/install.sh /src/analytics /src/.env.example /tmp/

/tmp/install.sh --yes

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
grep -q '^DATABASE_URL=' /etc/analytics/analytics.env \
                                       && echo "ok: analytics.env has DATABASE_URL"
grep -q 'EnvironmentFile=/etc/analytics/analytics.env' /etc/systemd/system/analytics.service \
                                       && echo "ok: unit loads analytics.env"
test -f /etc/litestream.yml            && echo "ok: litestream.yml installed"
test -f /etc/logrotate.d/analytics     && echo "ok: logrotate installed"

# Projects live in the database, not a shipped file: the installer's own
# hint (as the service user, sourcing analytics.env) must actually create
# one. No sudo in this minimal image, so drop privileges with su instead;
# `sh -ac` (allexport), exactly like the hint install.sh prints, matters
# here — a plain `. file` only sets shell variables, it does not export
# them, so DATABASE_URL would never reach the analytics process.
out="$(su -s /bin/sh analytics -c "sh -ac '. /etc/analytics/analytics.env; /usr/local/bin/analytics project create -alias myapp'")"
echo "$out" | grep -q '"myapp" created' && echo "ok: project create via installer hint works"

# Re-running must not clobber an edited config file.
echo 'DATABASE_URL=sqlite:///edited.db' > /etc/analytics/analytics.env
/tmp/install.sh --yes > /dev/null
grep -q edited /etc/analytics/analytics.env \
  && echo "ok: rerun preserves config"

echo "INSTALL TEST OK"
