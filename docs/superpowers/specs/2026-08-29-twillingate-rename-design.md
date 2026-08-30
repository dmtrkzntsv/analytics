# Twillingate cut-over design

2026-08-29. Renames the project from **analytics** to **twillingate**, adds a
TypeScript-built JS SDK, consolidates configuration, and simplifies the docs.
This is a cut-over, not a compatibility layer: the only backwards-compatible
surfaces are the ones deployed in the field on other people's machines —
browsers holding `/js/script.js` and apps speaking the ingest wire format.
Everything the operator controls (env vars, paths, unit names, images) renames
in one move, with a one-time manual migration.

## 1. What stays compatible (the wire)

These are frozen because existing websites and shipped app binaries use them:

- `POST /api/events`, the `X-Analytics-Key` header, the body `key` field, and
  the whole ingest wire format (docs/ingest-api.md).
- `GET /js/script.js` — the legacy snippet is served unchanged, byte for byte,
  including its `window.analytics` global and `analytics_*` localStorage keys.
- Key prefixes `ak_` (ingest) and `ar_` (MCP token).
- `/mcp` endpoint path and the MCP tool surface.

## 2. Renames

| Old | New |
| --- | --- |
| module `github.com/dmitry/analytics` | `github.com/dmtrkzntsv/twillingate` |
| `cmd/analytics`, binary `analytics` | `cmd/twillingate`, binary `twillingate` |
| repo `dmtrkzntsv/analytics` | `dmtrkzntsv/twillingate` (`gh repo rename`) |
| `ghcr.io/dmtrkzntsv/analytics[-evidence]` | `ghcr.io/dmtrkzntsv/twillingate[-evidence]` |
| `/etc/analytics/analytics.env` | `/etc/twillingate/twillingate.env` |
| `/var/lib/analytics/analytics.db` | `/var/lib/twillingate/twillingate.db` |
| service user `analytics` (host + containers) | `twillingate` |
| `analytics.service`, `deploy/logrotate/analytics` | `twillingate.service`, `deploy/logrotate/twillingate` |
| `install.sh` (repo root) | `deploy/systemd/install.sh` |
| Evidence source `analytics` (`EVIDENCE_SOURCE__analytics__filename`, `from analytics.…`) | `twillingate` |

Old GHCR packages are deleted after the first twillingate release publishes
(manual: the CLI token lacks `delete:packages`; use the GitHub packages UI).

## 3. Configuration cut-over

Renamed, no fallback aliases:

- `DATABASE_URL` → `DATABASE_DSN`
- `GEO_URL` → `GEO_DSN`

MCP auth collapses from seven variables (`MCP_AUTH_MODE`, `MCP_TOKEN`,
`MCP_AUTH_ISSUER`, `MCP_AUTH_AUDIENCE`, `MCP_RESOURCE_URL`,
`MCP_CF_TEAM_DOMAIN`, `MCP_CF_AUD`) into one DSN:

```
MCP_AUTH_DSN=token://<token>
MCP_AUTH_DSN=cloudflare://<team>.cloudflareaccess.com?aud=<aud-tag>
MCP_AUTH_DSN=oauth://idp.example.com[/path][?resource=<url>][&audience=<aud>]
```

- `token://` — everything after the scheme separator is the literal token
  (split on `://` once; not URL-parsed, so no escaping surprises).
- `cloudflare://` — host is the Access team domain; `aud` query param is the
  application AUD tag. Both required.
- `oauth://` — host+path form the issuer as `https://<host>[/path]`.
  `resource` defaults to `PUBLIC_URL` + `/mcp` (a new derivation — most
  deployments already set `PUBLIC_URL`); `audience` defaults to the resource
  URL. Issuer plus a resolvable resource URL are required.

Internally the DSN parses into the existing `MCPConfig` fields; verifiers are
untouched. `ValidateMCP` errors name `MCP_AUTH_DSN` and show the expected
form. Unset `MCP_AUTH_DSN` keeps today's semantics: bare `serve` warns and
skips MCP, `serve -mcp` hard-fails.

Kept as-is: `MCP_ADDR`, `MCP_DB_PATH` (now defaulting from `DATABASE_DSN`),
`MCP_QUERY_TIMEOUT`, `MCP_QUERY_MAX_ROWS`, and every non-MCP variable.

## 4. The twillingate.js SDK

New `sdk/` directory: TypeScript source, esbuild bundle (IIFE, pinned
versions), vitest + jsdom tests. The build output is **committed** at
`internal/server/twillingate.js`, embedded with `go:embed`, and served at
`GET /js/twillingate.js` alongside the legacy `/js/script.js`.

Two usage modes, one file:

1. **Snippet mode** — drop-in like the old script: auto-init from
   `data-key` / `data-identity` / `data-user` / `data-group`, automatic
   pageviews including pushState/popstate.
2. **SDK-only mode** — load the file (or bundle it) without `data-key` and
   drive everything through the API: web, product and app analytics from
   code alone.

Public API on `window.twillingate`:

- `init({url, key, identity, user, group, platform, appVersion, installId, autoPageviews})`
  — explicit initialisation; `url` defaults to the script's own origin.
- `page(attrs?)` — manual `$pageview`.
- `screen(name, attrs?)` — `$screen_view` for app analytics; batch
  attributes carry `$platform` / `$app_version` / `$install_id` from init.
- `track(name, attrs?)` — product event.
- `identify(user, group?)`, `group(id)`, `reset()` — identity lifecycle,
  same persistence semantics as the legacy snippet.
- `flush()` — force-send the queue.

Transport: events accumulate in a small queue flushed on a short timer, on
`pagehide`/`visibilitychange` (sendBeacon, fetch-keepalive fallback), and on
`flush()`. Failed sends persist to `twillingate_queue` in localStorage
(bounded) and replay on next load or `online`. Each event carries an `id`
(crypto.randomUUID) and client `ts`, so replays dedupe server-side.

Continuity with the legacy snippet: storage keys are `twillingate_visitor`,
`twillingate_user`, `twillingate_group`; on first run any `analytics_*`
values migrate over, and both `twillingate_ignore` and the legacy
`analytics_ignore` opt-outs are honoured. Ignore rules (localhost, `file:`,
webdriver) match the old snippet.

Testing: vitest covers init modes, identity precedence and persistence,
migration from `analytics_*`, ignore rules, batching/flush triggers, offline
queue replay, SPA pageview dedupe, and payload shape against
docs/ingest-api.md. A CI job builds the bundle and fails on
`git diff --exit-code` (committed artifact drift) — `make check` stays
Go-only so the release job needs no Node for the Go path. A Go test asserts
the embedded bundle is present and version-stamped.

## 5. MCP server update

- Server identity/branding renamed to twillingate.
- `docs://js-sdk` rewritten to document twillingate.js (both modes), with a
  short legacy note about script.js.
- `integration_guide` emits `/js/twillingate.js` snippets; the `server` and
  `mobile` platforms gain the SDK-only guidance.
- docs-sync tests re-anchored to `sdk/src` so the resource cannot drift from
  the shipped SDK.

## 6. Docs restructure

README shrinks to: what it is (short), privacy stance (short), the two
docker compose recipes, the embed snippet, and a table of pointers into
`docs/`. Everything else moves:

- `docs/configuration.md` — the full env var and project-registry reference
  (from README).
- `docs/deployment.md` — systemd/tarball install, two-server topology,
  Raspberry Pi notes (absorbs README sections; installer now at
  `deploy/systemd/install.sh`).
- `docs/mcp-auth.md` — updated for `MCP_AUTH_DSN`.
- `docs/sdk.md` — twillingate.js reference (superset of `docs://js-sdk`).
- `docs/migration.md` — the one-time analytics → twillingate migration
  runbook (systemd hosts and compose hosts).
- Existing `docs/ingest-api.md`, `docs/litestream.md`, `docs/plausible/`
  stay, path-corrected.

## 7. Release / cut-over sequence

1. Land everything on `main` locally; `make check`, `make build`, SDK tests,
   `make test-install`, `make test-compose` green.
2. `gh repo rename twillingate`; update the local remote.
3. Push `main` — CI cuts the first twillingate release: tarballs plus
   `ghcr.io/dmtrkzntsv/twillingate{,-evidence}` images.
4. Operator runs docs/migration.md once per host; deletes old GHCR packages
   in the UI.
