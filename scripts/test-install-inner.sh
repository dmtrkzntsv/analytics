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
cp /src/twillingate /src/.env.example /tmp/

/tmp/deploy/systemd/install.sh --yes

echo "--- assertions"
test -x /usr/local/bin/twillingate       && echo "ok: binary installed"
/usr/local/bin/twillingate > /dev/null 2>&1 || [ $? -lt 126 ]
echo "ok: binary executes"
id twillingate > /dev/null               && echo "ok: service user created"
d="$(stat -c '%a %U' /var/lib/twillingate)"
[ "$d" = "750 twillingate" ]             && echo "ok: data dir 0750 twillingate"
grep -q 'User=twillingate' /etc/systemd/system/twillingate.service \
                                       && echo "ok: twillingate.service templated"
grep -q 'User=twillingate' /etc/systemd/system/litestream.service \
                                       && echo "ok: litestream.service templated"
[ "$(stat -c '%a' /etc/twillingate/twillingate.env)" = 640 ] \
                                       && echo "ok: twillingate.env 0640"
grep -q '^DATABASE_URL=' /etc/twillingate/twillingate.env \
                                       && echo "ok: twillingate.env has DATABASE_URL"
grep -q 'EnvironmentFile=/etc/twillingate/twillingate.env' /etc/systemd/system/twillingate.service \
                                       && echo "ok: unit loads twillingate.env"
test -f /etc/litestream.yml            && echo "ok: litestream.yml installed"
test -f /etc/logrotate.d/twillingate     && echo "ok: logrotate installed"

# Projects live in the database, not a shipped file: the installer's own
# hint (as the service user, sourcing twillingate.env) must actually create
# one. No sudo in this minimal image, so drop privileges with su instead;
# `sh -ac` (allexport), exactly like the hint install.sh prints, matters
# here — a plain `. file` only sets shell variables, it does not export
# them, so DATABASE_URL would never reach the twillingate process.
out="$(su -s /bin/sh twillingate -c "sh -ac '. /etc/twillingate/twillingate.env; /usr/local/bin/twillingate project create -alias myapp'")"
echo "$out" | grep -q '"myapp" created' && echo "ok: project create via installer hint works"

# Re-running must not clobber an edited config file.
echo 'DATABASE_URL=sqlite:///edited.db' > /etc/twillingate/twillingate.env
/tmp/deploy/systemd/install.sh --yes > /dev/null
grep -q edited /etc/twillingate/twillingate.env \
  && echo "ok: rerun preserves config"

echo "INSTALL TEST OK"
