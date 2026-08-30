# Migrating from `analytics` to `twillingate`

One-time runbook for a host that ran the old `analytics` binary. This was a
cut-over rename, not a compatibility release: the new binary reads only the
new paths and variables. What did **not** change is the wire: `/api/events`,
`X-Analytics-Key`, key values (`ak_…`/`ar_…`), the database schema and
`/js/script.js` are untouched, so websites, apps and the data itself carry
over as-is. Nothing needs re-instrumenting.

What renamed:

| Old | New |
| --- | --- |
| binary `/usr/local/bin/analytics` | `/usr/local/bin/twillingate` |
| `/etc/analytics/analytics.env` | `/etc/twillingate/twillingate.env` |
| `/var/lib/analytics/analytics.db` | `/var/lib/twillingate/twillingate.db` |
| `analytics.service` | `twillingate.service` |
| `/etc/logrotate.d/analytics` | `/etc/logrotate.d/twillingate` |
| service user `analytics` | `twillingate` (the installer default) |
| `DATABASE_URL`, `GEO_URL` | `DATABASE_DSN`, `GEO_DSN` |
| `MCP_AUTH_MODE` + `MCP_TOKEN` / `MCP_AUTH_ISSUER` / `MCP_AUTH_AUDIENCE` / `MCP_RESOURCE_URL` / `MCP_CF_TEAM_DOMAIN` / `MCP_CF_AUD` | one `MCP_AUTH_DSN` |
| `ghcr.io/dmtrkzntsv/analytics[-evidence]` | `ghcr.io/dmtrkzntsv/twillingate[-evidence]` |

## Systemd host

```bash
# 1. Stop the old services.
sudo systemctl stop analytics litestream 2>/dev/null || sudo systemctl stop analytics

# 2. Install twillingate (creates user, dirs, units, logrotate; does not
#    touch the old install).
curl -fsSL https://raw.githubusercontent.com/dmtrkzntsv/twillingate/main/deploy/systemd/install.sh | sudo bash -s -- --yes

# 3. Move the database (all three files if -wal/-shm exist).
sudo mv /var/lib/analytics/analytics.db      /var/lib/twillingate/twillingate.db
sudo mv /var/lib/analytics/analytics.db-wal  /var/lib/twillingate/twillingate.db-wal 2>/dev/null || true
sudo mv /var/lib/analytics/analytics.db-shm  /var/lib/twillingate/twillingate.db-shm 2>/dev/null || true
sudo chown -R twillingate:twillingate /var/lib/twillingate

# 4. Port the env file: start from the old one, then rename the variables.
sudo cp /etc/analytics/analytics.env /etc/twillingate/twillingate.env
sudo sed -i \
  -e 's|^DATABASE_URL=.*|DATABASE_DSN=sqlite:///var/lib/twillingate/twillingate.db|' \
  -e 's|^GEO_URL=|GEO_DSN=|' \
  /etc/twillingate/twillingate.env
sudo chown root:twillingate /etc/twillingate/twillingate.env && sudo chmod 0640 /etc/twillingate/twillingate.env
# If MCP was enabled, replace the MCP_* auth lines with one MCP_AUTH_DSN:
#   token mode:      MCP_AUTH_DSN=token://<old MCP_TOKEN value>
#   cloudflare mode: MCP_AUTH_DSN=cloudflare://<team domain>?aud=<aud tag>
#   oauth mode:      MCP_AUTH_DSN=oauth://<issuer host>   (+ set PUBLIC_URL,
#                    or append ?resource=<old MCP_RESOURCE_URL>)
sudo vi /etc/twillingate/twillingate.env

# 5. Litestream: point the config at the new path. The replica is keyed by
#    path, so the new path starts a fresh replication history — old backups
#    stay in the bucket under the old key until you clean them up.
sudo sed -i 's|/var/lib/analytics/analytics.db|/var/lib/twillingate/twillingate.db|' /etc/litestream.yml
# If a restore cron exists: update paths + user in /etc/cron.d/analytics-restore
# and rename it to /etc/cron.d/twillingate-restore.

# 6. Start and verify.
sudo systemctl start twillingate litestream
curl -s localhost:8080/healthz
journalctl -u twillingate -n 20

# 7. Remove the old install.
sudo systemctl disable analytics
sudo rm -f /etc/systemd/system/analytics.service /etc/logrotate.d/analytics \
           /usr/local/bin/analytics
sudo systemctl daemon-reload
sudo rm -rf /etc/analytics /var/lib/analytics     # after verifying step 6
sudo userdel analytics                            # optional; keeps uid tidy
# If LOG_FILE pointed under /var/log/analytics, move it under
# /var/log/twillingate (the new logrotate rules watch that directory).
```

## Docker compose host

```bash
# 1. Fetch the new compose file (image names and volume paths changed).
curl -fsSLO https://raw.githubusercontent.com/dmtrkzntsv/twillingate/main/deploy/compose/docker-compose.yml

# 2. Rename the database inside the data volume, then start.
docker compose down
docker run --rm -v <project>_data:/data alpine \
  sh -c 'mv /data/analytics.db /data/twillingate.db 2>/dev/null;
         mv /data/analytics.db-wal /data/twillingate.db-wal 2>/dev/null;
         mv /data/analytics.db-shm /data/twillingate.db-shm 2>/dev/null; true'
docker compose up -d

# 3. .env: rename DATABASE_URL/GEO_URL to *_DSN, collapse MCP_* to
#    MCP_AUTH_DSN (same mapping as above), and rename ANALYTICS_VERSION
#    pins to TWILLINGATE_VERSION.
```

The volume is still named `data`; only the paths inside it and the image
names changed. Never `docker compose down -v`.

## Afterwards

- Websites keep working untouched: `/js/script.js` still serves the frozen
  legacy snippet. Migrate them to `/js/twillingate.js` at leisure —
  [sdk.md](sdk.md#migrating-a-site-from-scriptjs).
- MCP clients keep their `/mcp` URL and token values.
- Delete the old GHCR packages (`analytics`, `analytics-evidence`) in
  GitHub → Packages once the first twillingate images are published.
- GitHub redirects the renamed repo, but update any `git remote`,
  bookmark or CI reference to `dmtrkzntsv/twillingate` anyway.
