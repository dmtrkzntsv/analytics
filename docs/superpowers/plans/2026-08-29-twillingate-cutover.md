# Twillingate Cut-over Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Rename analytics → twillingate end to end (module, binary, paths, images, repo), add a TypeScript-built `twillingate.js` SDK, consolidate env config (`*_DSN`, `MCP_AUTH_DSN`), and restructure the docs — as a cut-over with a one-time migration runbook.

**Architecture:** Mechanical rename first (module path, cmd dir, deploy artifacts, Evidence source), then the config cut-over inside `internal/config`, then the new `sdk/` TypeScript project whose committed bundle is embedded and served next to the frozen legacy `script.js`, then MCP docs/branding, then the README/docs restructure, finally the repo rename + release push.

**Tech Stack:** Go 1.25, SQLite, TypeScript + esbuild + vitest/jsdom (sdk only), Evidence, GitHub Actions.

**Spec:** docs/superpowers/specs/2026-08-29-twillingate-rename-design.md

## Global Constraints

- Frozen wire surfaces (spec §1): `POST /api/events`, `X-Analytics-Key`, body `key`, `GET /js/script.js` byte-identical, `window.analytics`, `analytics_*` localStorage keys, `ak_`/`ar_` prefixes, `/mcp` path.
- No env fallback aliases: `DATABASE_URL`/`GEO_URL`/`MCP_AUTH_MODE`/etc. simply stop existing.
- `make check` stays Node-free; SDK build/test/drift runs as its own CI job.
- The compiled `internal/server/twillingate.js` is committed; esbuild + typescript + vitest versions pinned in `sdk/package-lock.json`.
- Conventional commits per CLAUDE.md; release notes are generated from subjects.

---

### Task 1: Mechanical rename — module, cmd, Makefile, scripts

**Files:**
- Modify: `go.mod` (module line), every `*.go` import of `github.com/dmitry/analytics`
- Rename: `cmd/analytics/` → `cmd/twillingate/`
- Modify: `Makefile` (`BIN := twillingate`, all `analytics` refs incl. LOCAL_ENV → `DATABASE_DSN`/`GEO_DSN` deferred to Task 3; here only names/paths), `scripts/*.sh`, `scripts/smokecheck/main.go`, `scripts/seed-demo.py`
- Modify: `cmd/twillingate/*.go` usage strings ("analytics …" → "twillingate …")

**Interfaces:**
- Produces: module path `github.com/dmtrkzntsv/twillingate`; binary/dir name `twillingate`; local dev state under `local/twillingate.db`.

- [ ] `git mv cmd/analytics cmd/twillingate`
- [ ] `go mod edit -module github.com/dmtrkzntsv/twillingate` and `grep -rl 'github.com/dmitry/analytics' --include='*.go' . | xargs sed -i 's|github.com/dmitry/analytics|github.com/dmtrkzntsv/twillingate|g'`
- [ ] Sweep Makefile/scripts for `analytics` names (binary, db filename, container names); keep env var names for Task 3.
- [ ] Run `make build && make test` — green.
- [ ] Commit `refactor: rename module and binary to twillingate`.

### Task 2: Mechanical rename — deploy, Dockerfile, workflows, Evidence source

**Files:**
- Rename: `deploy/systemd/analytics.service` → `twillingate.service`, `deploy/logrotate/analytics` → `deploy/logrotate/twillingate`, `install.sh` → `deploy/systemd/install.sh`, `evidence/sources/analytics/` → `evidence/sources/twillingate/`
- Modify: `Dockerfile`, `deploy/compose/*.yml`, `deploy/litestream/*`, `.github/workflows/release.yml` (image names `twillingate`/`twillingate-evidence`), `.github/workflows/ci.yml`, `evidence/**` (`from analytics.` → `from twillingate.`, `EVIDENCE_SOURCE__analytics__filename` → `…__twillingate__…`, svelte.config.js), `internal/dashboards/*` (same env var), `Makefile` dist/test-install paths, `scripts/test-install*.sh`, `scripts/test-compose.sh`
- Paths inside artifacts: `/etc/twillingate/twillingate.env`, `/var/lib/twillingate/twillingate.db`, `/var/log/twillingate`, user `twillingate`.

**Interfaces:**
- Produces: installable tarball layout with `deploy/systemd/install.sh`; images tagged `ghcr.io/dmtrkzntsv/twillingate{,-evidence}`.

- [ ] git mv the four artifacts; sed sweeps for names/paths above.
- [ ] Update `install.sh` internals (REPO, asset names, unit list, prompts) and Makefile `dist` to copy `deploy/systemd/install.sh` into the stage root as before (tarball keeps `install.sh` at top level for `curl | bash` parity? No — spec moves it; tarball carries it under `deploy/systemd/`, install docs updated).
- [ ] `make build && make test`, `bash -n deploy/systemd/install.sh`, `docker compose -f deploy/compose/docker-compose.yml config -q`.
- [ ] Commit `refactor(deploy): rename artifacts, paths and images to twillingate`.

### Task 3: Config cut-over — DATABASE_DSN, GEO_DSN

**Files:**
- Modify: `internal/config/config.go` (`e.str("DATABASE_DSN", "")`, `e.str("GEO_DSN", "cloudflare://")`, error strings), `internal/config/config_test.go`, `configtest`, `example_config_test.go`
- Modify: every non-config reference: `Makefile` LOCAL_ENV, `.env.example`, `Dockerfile` ENV, compose files, `scripts/*`, docs mentions (docs proper rewritten in Task 8).

**Interfaces:**
- Produces: `config.FromEnv` reads only the new names; `Config` struct unchanged.

- [ ] Update tests first (rename keys in test env maps; add a test asserting `DATABASE_URL` alone yields the "DATABASE_DSN is required" error).
- [ ] `go test ./internal/config/...` fails; flip config.go; passes.
- [ ] Sweep remaining `DATABASE_URL`/`GEO_URL` occurrences repo-wide (excluding legacy spec/plan docs).
- [ ] `make check` green. Commit `feat(config)!: rename DATABASE_URL and GEO_URL to DATABASE_DSN and GEO_DSN`.

### Task 4: Config cut-over — MCP_AUTH_DSN

**Files:**
- Modify: `internal/config/config.go`, `internal/config/config_test.go`
- Modify: `cmd/twillingate/serve.go` (unchanged logic; error text), `internal/app/app.go` if it switches on AuthMode (check), `.env.example`

**Interfaces:**
- Produces: `MCPConfig` fields as today, populated from `MCP_AUTH_DSN`; `parseMCPAuthDSN(dsn, publicURL string) (mode, token, issuer, audience, resource, cfTeam, cfAud string, err error)` internal to config.

Parsing rules (spec §3):

```go
scheme, rest, ok := strings.Cut(dsn, "://")
switch scheme {
case "token":   Token = rest (must be non-empty)
case "cloudflare": u, _ := url.Parse(dsn); CFTeamDomain = u.Host; CFAud = u.Query().Get("aud") (both required)
case "oauth":   u := url.Parse(dsn); Issuer = "https://" + u.Host + u.Path (trim trailing /)
                Resource = q["resource"], default PublicURL+"/mcp" (PublicURL required if resource absent)
                Audience = q["audience"], default Resource
}
```

- [ ] Table-driven tests: each scheme happy path, missing token, missing aud, oauth without resource and without PUBLIC_URL (error), oauth default resource/audience derivation, unknown scheme, empty DSN + `-mcp` behavior via `ValidateMCP`.
- [ ] Implement; `ValidateMCP` messages name `MCP_AUTH_DSN`.
- [ ] `make check` green. Commit `feat(config)!: replace the MCP_* auth variables with a single MCP_AUTH_DSN`.

### Task 5: twillingate.js SDK

**Files:**
- Create: `sdk/package.json`, `sdk/tsconfig.json`, `sdk/src/twillingate.ts`, `sdk/src/*.test.ts`, `sdk/build.mjs` (esbuild IIFE → `../internal/server/twillingate.js`), `sdk/README.md`
- Create: `internal/server/twillingate_script.go` (embed + route `GET /js/twillingate.js`), `internal/server/twillingate_script_test.go`
- Modify: `.github/workflows/ci.yml` (sdk job: `npm ci && npm test && npm run build && git diff --exit-code`), `.gitignore` (`sdk/node_modules`)

**Interfaces:**
- Produces: `window.twillingate` API per spec §4 (`init`, `page`, `screen`, `track`, `identify`, `group`, `reset`, `flush`); served at `/js/twillingate.js`; legacy `/js/script.js` untouched.

- [ ] Scaffold sdk project; write vitest suites first (init modes, identity precedence + persistence + `analytics_*` migration, ignore rules incl. legacy opt-out, batching timer + pagehide flush, offline queue replay bounded, SPA dedupe, payload shape: `{key, attributes, events:[{id, ts, name, attributes}]}`).
- [ ] Implement `twillingate.ts` until green; build bundle; commit bundle.
- [ ] Go: failing test for `/js/twillingate.js` 200 + cache header + `window.twillingate` marker; embed+route; green.
- [ ] `make check` green; sdk `npm test` green; drift check clean.
- [ ] Commit `feat(server): twillingate.js SDK for web, product and app analytics`.

### Task 6: MCP server update

**Files:**
- Modify: `internal/mcpserver/server.go` (server name/branding), `docs_content.go` (docs://js-sdk rewritten for twillingate.js + legacy note), `guide.go` (snippets use `/js/twillingate.js`; server/mobile SDK-only guidance), `docs_sync_test.go` (anchor to `sdk/src/twillingate.ts`), affected `*_test.go`.

**Interfaces:**
- Consumes: `sdk/src/twillingate.ts` as drift-check source of truth.

- [ ] Update sync tests to read the TS source; watch them fail; rewrite docs content + guide; green.
- [ ] `make check` green. Commit `feat(mcpserver): document and integrate the twillingate.js SDK`.

### Task 7: install.sh + logrotate + units polish

**Files:**
- Modify: `deploy/systemd/install.sh` (already moved; verify unit names, `/etc/twillingate`, logrotate install, next-steps text), `scripts/test-install-inner.sh` assertions.

- [ ] `make test-install` (docker) green.
- [ ] Commit `fix(deploy): finish installer rename` (fold into Task 2 if no drift found).

### Task 8: README + docs restructure

**Files:**
- Rewrite: `README.md` (short: intro, privacy para, compose quickstarts, embed snippet incl. twillingate.js, pointers table)
- Create: `docs/configuration.md`, `docs/sdk.md`, `docs/migration.md`
- Modify: `docs/deployment.md`, `docs/mcp-auth.md`, `docs/litestream.md`, `docs/ingest-api.md`, `docs/plausible/README.md` (names/paths/DSNs)

- [ ] Write each doc; verify every env var mentioned exists in config.go (grep).
- [ ] Commit `docs: restructure around twillingate — slim README, detailed docs/, migration runbook`.

### Task 9: Full verification

- [ ] `make check`, `make build`, `make build-all`, sdk `npm test` + drift, `make smoke`, `make test-install`, `make test-compose` (docker available?), `go vet`, `gofmt -l`.
- [ ] Fix fallout; commit.

### Task 10: Cut-over — repo rename, merge, push, images

- [ ] `gh repo rename twillingate -R dmtrkzntsv/analytics --yes`; `git remote set-url origin git@github.com:dmtrkzntsv/twillingate.git`.
- [ ] Merge worktree branch to `main` (ff if possible), push — release workflow publishes tarballs + twillingate images.
- [ ] Watch the release run (`gh run watch`); confirm images exist.
- [ ] Attempt old package deletion via API; expected 403 → record as manual step.
- [ ] Report manual migration steps to the user.
