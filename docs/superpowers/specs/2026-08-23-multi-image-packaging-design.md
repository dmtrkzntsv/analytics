# Packaging: two published images, a `dashboards` mode, and no replication in the app

Date: 2026-08-23
Status: draft-pending-review
Supersedes: §10 (Replication & backoffice), §12 (Repository layout) and the
`sync` parts of §3–§4 of `2026-08-22-analytics-design.md`.

## 1. Problem

Updating a deployment today means having a git checkout on the box, running
`docker compose up -d --build`, and hoping `node_modules` inside the bind mount
is still in step with `package.json`. Dashboards are not part of a release:
they are whatever happens to be in `backoffice/evidence/` on that machine, built
at runtime by a shell script that `npm ci`s on first boot and fetches
`npx http-server` from the network on every start.

Three consequences drive this design:

- **No reproducibility.** Nothing ties the dashboards a machine is serving to a
  version tag.
- **Cold start.** First boot compiles `sqlite3` from source inside a
  `node:20-alpine` container, on the deployment machine.
- **The app owns replication.** `analytics sync` shells out to `litestream
  restore` on a loop, which makes a specific backup tool part of the
  application's contract and forces every operator into one topology.

## 2. Goals

- Ship dashboards as versioned, published artifacts: `docker compose pull`.
- Remove all runtime `npm`/`npx` installation.
- Reduce the application's contract to: **`serve` writes a SQLite file,
  `dashboards` reads a SQLite file.** Everything about getting a file from one
  machine to another is the operator's, supported by documentation and scripts
  in `deploy/`, not by code in the binary.
- Make the no-backup single-server deployment a first-class path that requires
  no credentials and no object storage.

## 3. Non-goals

- No registry other than GHCR; no Docker Hub mirror.
- No runtime dashboard customisation. Dashboards are image content; authoring
  happens in a checkout via `make dashboards`, and per-deployment variants are a
  downstream `FROM ghcr.io/…/analytics-evidence:vX` image.
- No change to the bare-metal/systemd path beyond documentation.
- No query API or auth in front of the dashboards; that remains the reverse
  proxy's job.

## 4. Decisions

| Decision | Rationale |
|---|---|
| Two images, not one | `serve` is the internet-facing process; a combined image would put ~988 npm packages inside it. Two build targets in one Dockerfile keep the cost near zero, and a broken Evidence build stops shipping dashboards without blocking ingestion. |
| `analytics` / `analytics-evidence` | **Modes are named for the job, images for their contents.** `dashboards` survives replacing the renderer; the image name tells you why it is 600 MB. |
| Delete `sync` and all litestream code | Replication is per-operator and per-topology. The app should not embed one tool's CLI in its own mode list. |
| No litestream binary in either image | The systemd path is already bring-your-own (`install.sh:142`); the compose path already uses the official `litestream/litestream` image as a sidecar. The bundled copy was only ever used by `sync`. |
| The word "backoffice" is retired | It names a deployment role, not a component, and stops being true as soon as there is a second reader. Docs say "a machine that only reads a replica". |
| `dashboards` snapshots before each build | Node never opens the live or replica file; a restore that swaps the file mid-build cannot corrupt a render. |
| Data volume mounted read-write in `dashboards` | A SQLite WAL reader needs write access to `-shm`. `:ro` can fail with `unable to open database file` against a live database; it works today only because dashboards were built against an idle one. |

## 5. Images

One `Dockerfile`, four stages, two published targets.

| stage | base | purpose |
|---|---|---|
| `go-build` | `golang:1.25-alpine` | static `analytics` binary (unchanged) |
| `evidence-build` | `node:22-alpine` + `build-base python3` | `COPY evidence/package*.json` → `npm ci` → `COPY evidence/` |
| `runtime` *(target)* | `alpine:3.20` | + `analytics` |
| `evidence` *(target)* | `node:22-alpine` | + `analytics`, + `/opt/evidence` from `evidence-build` |

Node 22 rather than 20: the Evidence build imports the `node:sqlite` builtin,
which 20 does not have, and fails with `ERR_UNKNOWN_BUILTIN_MODULE`.

`npm ci` sits behind a lockfile-keyed layer, so editing a dashboard page does
not reinstall dependencies. `@evidence-dev/sqlite` depends on `sqlite3`, which
publishes no musl prebuilds and therefore compiles from source — this happens
once, in `evidence-build`, and the compilers stay out of the runtime image.

| image | modes | platforms |
|---|---|---|
| `ghcr.io/dmtrkzntsv/analytics` | `serve`, `migrate`, `version` | linux/amd64, arm64, arm/v7 |
| `ghcr.io/dmtrkzntsv/analytics-evidence` | `dashboards` | linux/amd64, arm64 |

32-bit arm is dropped for the Evidence image only: no `sqlite3` prebuilds and a
source build under QEMU. Release tarballs keep all three architectures.

Both images keep the existing non-root `analytics` user (uid 10001) and the
`ENTRYPOINT ["/usr/local/bin/analytics"]`. In the Evidence image,
`/opt/evidence` and `/var/lib/dashboards` are created and `chown`ed at build
time — Evidence writes `.evidence/` and `build/` inside the project directory.

## 6. `analytics dashboards`

New package `internal/dashboards`, structured after `internal/synccmd`: an
`execCommand` seam for the external tool, a `RunOnce` and a `Run` loop.

### Configuration

| var | default | meaning |
|---|---|---|
| `DASHBOARDS_DB_PATH` | path from `DATABASE_URL` | database to render |
| `DASHBOARDS_ADDR` | `0.0.0.0:3000` | listen address |
| `DASHBOARDS_INTERVAL` | `15m` | minimum spacing between builds |
| `DASHBOARDS_PROJECT_DIR` | `/opt/evidence` | Evidence project |
| `DASHBOARDS_WORK_DIR` | `/var/lib/dashboards` | snapshot location |

`DASHBOARDS_DB_PATH` wins when both are set; `dashboards` refuses to start with
neither. `serve` and `migrate` keep requiring `DATABASE_URL` as they do today.

### Change detection

Every 60 seconds the loop stats the database file and its `-wal` sibling. If the
`(size, mtime)` pair of either has changed since the last successful build, and
at least `DASHBOARDS_INTERVAL` has elapsed, it rebuilds.

Stat is deliberate: `PRAGMA data_version` cannot see a file replaced by an
atomic rename (the held connection keeps the old inode), and the main file's
mtime alone cannot see WAL writes between checkpoints. The pair covers a live
database under traffic, a checkpoint, and a wholesale file swap by a restore
job, without holding a connection open.

### Build cycle

1. `VACUUM INTO $DASHBOARDS_WORK_DIR/snapshot.db` (removing any previous
   snapshot first) via `modernc.org/sqlite` — a consistent copy that node
   alone owns.
2. Compute Evidence's connection path as
   `filepath.Rel($PROJECT_DIR/sources/analytics, snapshot)` and pass it as
   `EVIDENCE_SOURCE__analytics__filename`. The sqlite plugin resolves
   `filename` with `path.join(<source dir>, …)`, so an absolute path is
   rewritten and a hand-written `../../../` breaks the moment the project moves.
   Computing it removes the knob from the compose files and the comment from
   `connection.yaml`.
3. `npm run sources && npm run build`, cwd `$PROJECT_DIR`.
4. On success, rotate: rename `build/` to the unused slot of `site.a`/`site.b`,
   point the handler at it atomically, remove the other slot. Renames stay
   within `$PROJECT_DIR`, so no cross-device failure. Serving straight out of
   `build/` would expose a half-written tree during every rebuild.

### Serving

`net/http` `FileServer` over the current slot, root held in an
`atomic.Pointer[string]`. Before the first successful build, every request gets
`503` and a one-line "dashboards are building" body. A failed build logs the
error and leaves the previous slot serving — the current behaviour, kept.

No `npx --yes http-server`: that is an unpinned network fetch on every container
start, and the standard library covers it.

## 7. Configuration changes

Removed: `SYNC_INTERVAL`, `SYNC_LITESTREAM_CONFIG`, `SYNC_REPLICA_PATH`.

Added: the `DASHBOARDS_*` block above.

Changed: `config.FromEnv` currently opens `PROJECTS_FILE` and fails when it is
missing or empty (`config.go:169`, `config.go:219`), for *every* command. That
is why a replica-only host has to mount a projects file it never reads.
The projects file becomes a requirement of `serve` and `migrate` only;
`dashboards` loads without one.

## 8. Repository layout

```
evidence/                                  was backoffice/evidence/
deploy/compose/docker-compose.yml          analytics + dashboards
deploy/compose/docker-compose.evidence.yml dashboards alone (reader host)
deploy/compose/docker-compose.litestream.yml   replicate sidecar (overlay)
deploy/litestream/litestream.yml           unchanged
deploy/litestream/restore.sh               restore → quick_check → atomic swap
deploy/litestream/restore.cron             */5 example
docs/litestream.md                         new
```

Deleted: `backoffice/` (both compose files and `evidence-entrypoint.sh`),
`internal/synccmd/`, `cmd/analytics/sync.go`.

`make dashboards` follows the move to `evidence/`.

## 9. Compose files

**`docker-compose.yml`** — single server, no credentials, no object storage:

```yaml
services:
  analytics:
    image: ghcr.io/dmtrkzntsv/analytics:${ANALYTICS_VERSION:-latest}
    restart: unless-stopped
    ports: ["8080:8080"]
    volumes:
      - ./projects.json:/etc/analytics/projects.json:ro
      - data:/var/lib/analytics
    env_file: [{ path: .env, required: false }]
    environment: [GOMEMLIMIT=128MiB]

  dashboards:
    image: ghcr.io/dmtrkzntsv/analytics-evidence:${ANALYTICS_VERSION:-latest}
    restart: unless-stopped
    command: ["dashboards"]
    ports: ["3000:3000"]
    volumes: [data:/var/lib/analytics]
    environment:
      - DASHBOARDS_DB_PATH=/var/lib/analytics/analytics.db

volumes:
  data:
```

**`docker-compose.evidence.yml`** — a machine that only reads a replica: the
`dashboards` service alone, `DASHBOARDS_DB_PATH=/data/replica.db`, with a
commented `restore` service for hosts that would rather loop `restore.sh` in a
container than use host cron.

**`docker-compose.litestream.yml`** — an overlay adding one
`litestream/litestream:0.3.13` service on the shared volume, reading
`LITESTREAM_ACCESS_KEY_ID`, `LITESTREAM_SECRET_ACCESS_KEY`, `R2_BUCKET`,
`R2_ENDPOINT` from `.env` and bind-mounting `./litestream.yml`. Used as
`docker compose -f docker-compose.yml -f docker-compose.litestream.yml up -d`.

The config is bind-mounted rather than inlined with `configs: content:` so that
one canonical `litestream.yml` serves compose, systemd and `restore.sh`. Its
`path:` key is what identifies a replica in the bucket; a second copy could
drift silently and break restores.

## 10. Release and CI

`release.yml` gains a job after the existing tarball job, on `v*` tags, with
`permissions: packages: write`:

- `docker/setup-qemu-action`, `docker/setup-buildx-action`,
  `docker/login-action` against `ghcr.io` with `GITHUB_TOKEN`.
- Two `docker/build-push-action` steps, `target: runtime` and
  `target: evidence`, each tagged `vX.Y.Z` and `latest`, with
  `cache-from/to: type=gha` so the `npm ci` layer survives between releases.

`make docker` builds both targets locally for the host architecture.

**GHCR visibility:** the repository is private, so packages default to private
and every deployment host would need `docker login ghcr.io` with a PAT. The
packages should be set public unless that is the intent; the docs must state
which.

## 11. Documentation

- `docs/litestream.md` (new): R2 bucket and two credentials (read-write for the
  writer, **read-only for the reader**); `litestream.yml` and why both sides
  must agree on `path:` byte-for-byte; the writer sidecar and systemd unit; the
  reader's cron restore; verifying with `litestream snapshots` /
  `generations`; disaster recovery onto a fresh server.
- `README.md`: quickstarts become "curl the compose file, edit `.env` and
  `projects.json`, `up -d`" — no clone, no build.
- `docs/deployment.md`: an *Upgrade* row for the compose path
  (`docker compose pull && docker compose up -d`, and never `down -v`), and a
  pointer to `docs/litestream.md` instead of inline replication prose.

## 12. Testing

- `internal/dashboards`, with a fake `npm` through the `execCommand` seam:
  successful build rotates slots; failed build keeps the previous slot serving;
  unchanged database skips the rebuild; a changed `-wal` triggers one; `503`
  before the first build; `filepath.Rel` path computation across project
  depths; `VACUUM INTO` produces a readable snapshot.
- `internal/config`: `DASHBOARDS_*` parsing and defaults; `dashboards` loads
  with no projects file; `serve`/`migrate` still refuse without one.
- `shellcheck` on `restore.sh` (CI already does this for `install.sh`).
- `scripts/test-compose.sh`: boots both images, posts a hit, polls `:3000` for a
  rendered page. Local/manual, following the `test-install.sh` precedent rather
  than adding a heavy CI job.
- The coverage gate (85% per package) applies to `internal/dashboards`.

## 13. Risks

| Risk | Mitigation |
|---|---|
| `npm ci` compiling `sqlite3` for arm64 under QEMU may take 15–30 min per release | Validate early in the plan. Fallback: publish `analytics-evidence` for amd64 only at first; the reader host is usually the beefier machine. |
| Evidence image is ~1.0 GB against ~35 MB for the collector (measured; `node_modules` alone is 650 MB, of which `@duckdb` is 138 MB) | Only the reader pulls it, once per release, and it replaces a `node:20-alpine` pull plus a runtime `npm ci` that were already happening. |
| `VACUUM INTO` doubles database size transiently | Acceptable at the design point (a few hundred MB); documented in `docs/deployment.md` alongside the existing "database size" guidance. |
| Deleting `sync` removes verified restore for existing users | `restore.sh` reproduces it exactly — temp file, `PRAGMA quick_check`, atomic rename, `flock` against overlapping cron runs. |
| GHCR packages private by default | Section 10; decide and document before the first tag. |

## 14. Migrating an existing deployment

1. `docker compose down` (never `-v`; the named `data` volume is reused).
2. Replace the compose file with `deploy/compose/docker-compose.yml`; drop the
   `./evidence` and `./evidence-entrypoint.sh` bind mounts and any
   `EVIDENCE_SOURCE__analytics__filename` or `SYNC_*` variables.
3. For a reader host, install `deploy/litestream/restore.sh` plus a cron entry
   in place of the `sync` service, and use
   `deploy/compose/docker-compose.evidence.yml`.
4. `docker compose pull && docker compose up -d`.

The database schema is untouched; `serve` migrates on startup as it already
does. The VPS/systemd path is unchanged.
