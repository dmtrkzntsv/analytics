# Public MCP endpoint — design

Date: 2026-08-24
Status: proposed

## 1. Purpose

Let a person connect an MCP client (Claude.ai, Claude Code, Cursor) to a
running `analytics` instance and ask questions about their analytics in
natural language, without opening the Evidence dashboards.

The endpoint is internet-facing and authenticated. It is read-only: no MCP
tool can write to the database, by construction rather than by convention.

## 2. Non-goals

- No built-in authorization server. The binary is an OAuth 2.1 *resource
  server* only; issuing tokens is somebody else's job.
- No per-project authorization. A valid token reads every project (§5.3).
- No write, configuration or administration tools. Ingestion stays on
  `POST /api/events`; project configuration stays in `projects.json`.
- No replacement for the Evidence dashboards. This is a second read path
  over the same views, not a migration away from them.

## 3. Process model

### 3.1 Flags

`serve` gains two boolean flags naming the surfaces to run:

    analytics serve -api            # ingestion only (today's behaviour)
    analytics serve -mcp            # MCP only
    analytics serve -api -mcp       # both

**Bare `serve` is a usage error.** It exits non-zero with:

    serve: specify at least one surface: -api (ingestion), -mcp (MCP endpoint)

There is no implicit default. Running a surface is always an explicit act.

No new subcommand is added; `analytics <serve|dashboards|migrate|version>`
is unchanged.

### 3.2 Addressing

One rule: **each enabled surface binds its configured address; if the
addresses are equal, they share a single listener and mux.** `MCP_ADDR`
defaults to `LISTEN_ADDR`.

| Invocation | Result |
| --- | --- |
| `serve -api` | ingestion on `LISTEN_ADDR` |
| `serve -mcp` | MCP on `MCP_ADDR` (= `LISTEN_ADDR` unless set) |
| `serve -api -mcp` | both on `LISTEN_ADDR`, one port |
| `serve -api -mcp` with `MCP_ADDR` set | two listeners, two ports |
| `serve -api` and `serve -mcp` as two processes | full isolation |

This covers every topology without a special case. The operator chooses
whether ingest and MCP share a process, a port, both, or neither.

When the two surfaces share a mux, `GET /healthz` is registered once:
Go's `http.ServeMux` panics on a duplicate pattern.

### 3.3 Shutdown

Both listeners shut down before the jobs runner and the pipeline,
preserving the existing ordering invariant in `internal/app`: HTTP drains
first so no new events arrive, jobs stop next, and the pipeline is
cancelled last because its cancellation triggers the final buffer flush.

## 4. Configuration

New environment variables, following the existing 12-factor pattern
(`.env.example`, the README config table, `EnvironmentFile=` on installed
hosts):

| Variable | Meaning |
| --- | --- |
| `MCP_ADDR` | Address the MCP surface binds. Defaults to `LISTEN_ADDR`. |
| `MCP_DB_PATH` | Database to read. Defaults to the `DATABASE_URL` path. |
| `MCP_AUTH_MODE` | `oauth` or `token`. **No default**; required with `-mcp`. |
| `MCP_RESOURCE_URL` | Canonical public URI of this server, e.g. `https://analytics.example.com/mcp`. Required in `oauth` mode. |
| `MCP_AUTH_ISSUER` | Authorization server issuer URL. Required in `oauth` mode; optional in `token` mode, where setting it enables the metadata document. |
| `MCP_AUTH_AUDIENCE` | Expected `aud`. Defaults to `MCP_RESOURCE_URL`. |
| `MCP_READ_TOKENS` | Comma-separated `label:token` pairs. Required in `token` mode. See §4.1. |
| `MCP_QUERY_TIMEOUT` | Per-query deadline. Default `10s`. |
| `MCP_QUERY_MAX_ROWS` | Row cap for the `query` tool. Default `1000`. |

Validation is fail-fast at startup: `-mcp` without `MCP_AUTH_MODE`, an
unknown mode, or a mode missing its required variables all exit non-zero
with a message naming the missing variable. There is no unauthenticated
mode and no way to reach one by omission.

`projects.json` is unchanged. Read tokens live in the environment, not the
JSON file, for the same reason litestream credentials do: secrets should
not sit in a file that is routinely edited and shared. The split is a rule,
not a convenience — `projects.json` holds only values that are public by
design (`ingest_keys` ship in page source and app binaries;
`allowed_origins` is public), and every actual secret lives in
`analytics.env` beside the R2 credentials.

### 4.1 Read token format and minting

`MCP_READ_TOKENS` is a comma-separated list of `label:token` pairs. Each
entry is split on its **first** `:` so a token may itself contain colons;
an entry with an empty label or an empty token is a startup error naming
the offending position. A token containing a comma cannot be expressed and
is rejected at startup rather than silently truncated.

The label is never compared and never logged as a credential — it is what
appears as `TokenInfo.UserID`, so an operator can tell which token is in
use from the logs without the token itself ever being written.

`keygen` gains `-read` to mint them:

    analytics keygen -read -n 2

Unlike ingest keys, read tokens are true secrets: they grant read access to
every project including identified-mode personal data. So they use 256 bits
of entropy rather than 128, carry an `ar_` prefix to distinguish them from
`ak_` ingest keys at a glance, and print an **env line for
`analytics.env`** rather than a `projects.json` block:

    Add to analytics.env:

      MCP_READ_TOKENS=laptop:ar_…,ci:ar_…

The two output shapes are the visible form of the rule above: `keygen`
prints JSON for public identifiers and an env line for secrets.

## 5. Authentication and authorization

### 5.1 Shape

The MCP server is an OAuth 2.1 resource server per the MCP authorization
specification. Concretely it MUST, and does:

- implement RFC 9728 Protected Resource Metadata at
  `GET /.well-known/oauth-protected-resource`, served **unauthenticated**,
  whenever `MCP_AUTH_ISSUER` is configured;
- return `401` with a `WWW-Authenticate` header naming that metadata URL
  when a token is absent or invalid;
- validate that a token was issued for *this* resource (RFC 8707 audience
  binding) before processing any request;
- return `403` for a well-formed token lacking required scopes.

RFC 9728 requires `authorization_servers` to name at least one server, so
the document cannot be emitted honestly without an issuer. `MCP_AUTH_ISSUER`
is therefore *required* in `oauth` mode and *optional* in `token` mode: set
it in `token` mode and clients can discover the OAuth path before the
operator switches over; leave it unset and the well-known path returns 404
while `401` responses carry a bare `WWW-Authenticate: Bearer` with no
`resource_metadata` parameter.

The SDK supplies `auth.ProtectedResourceMetadataHandler` and the
`auth.RequireBearerToken` middleware. We supply the `TokenVerifier`.

### 5.2 The two verifiers

`auth.TokenVerifier` is `func(ctx, token, req) (*auth.TokenInfo, error)`,
so both modes are the same seam with different bodies.

**`oauth`** — at startup, fetch
`MCP_AUTH_ISSUER/.well-known/oauth-authorization-server` (RFC 8414) and
fail fast on a typo or an unreachable issuer. Fetch and cache the JWKS
with a TTL, refetching on an unknown `kid` to survive key rotation. Per
request, validate:

- signature, against an **asymmetric algorithm allowlist** (`RS*`, `ES*`,
  `PS*`). `none` and every HMAC algorithm are rejected before verification,
  so a JWKS public key can never be used as an HMAC secret;
- `iss` equals `MCP_AUTH_ISSUER`;
- `aud` contains `MCP_AUTH_AUDIENCE`;
- `exp` and `nbf`.

Returns `auth.TokenInfo{UserID: sub, Scopes: …, Expiration: exp}`. Setting
`UserID` also enables the SDK's session-hijacking check, which binds all
requests in a session to one user.

**`token`** — constant-time compare against the configured tokens, using
`crypto/subtle` over a slice, matching how `config.keys` already avoids
map indexing on a credential. Returns
`auth.TokenInfo{UserID: label}` with the middleware constructed
`AllowMissingExpiration: true`, since a static token carries no `exp`.

### 5.3 Authorization

A valid token reads every non-archived project. There is no per-project
scoping and no filtering of identified-mode data.

This is a deliberate decision with a real consequence, recorded here so it
is not rediscovered later: **the operator's IdP is the only control point
between a token and the personal data in identified-mode projects.** An
audience misconfiguration that causes tokens minted for another service to
validate here exposes user ids and display names. §10 covers the resulting
documentation obligations. Narrowing this later is a breaking change to
tool signatures, so it should be revisited before the first release rather
than after.

## 6. Data access

### 6.1 Read-only connection

The MCP surface opens the database file itself rather than going through
`store.Store`, which is a write and aggregate interface whose `*sql.DB` is
pinned to `SetMaxOpenConns(1)` for the single-writer pipeline.

    file:<path>?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)

Read-only at the driver *and* connection level, with a normal pool. No
tool — curated or SQL — can write, whatever the model emits. Works
unchanged against a live WAL database on the collector or a
Litestream-restored replica.

### 6.2 What the views guarantee

Two properties of the existing schema that the tools and the schema
resource depend on:

- **Every `v_*` view includes yesterday.** `v_web_daily`, `v_app_daily`,
  `v_product_daily` and the breakdown views are stitch views: an `agg_*`
  half UNION ALL a live half computed from raw rows, disjoint by
  construction because aggregation deletes raw rows in the same
  transaction. `v_identity_daily` likewise has a live half.
- **`v_retention` is the exception, in two specific ways.** It has no live
  half, so it is refreshed only by the 03:00 UTC daily pass (plus the
  catch-up pass on boot); yesterday's cohort appears after that pass, not
  at midnight. And it is populated **only for `identified` projects** —
  under anonymous identity `actor_id` rotates at midnight, so every cohort
  would contain nothing but offset 0, and `jobs.go` skips them.

Curated tools therefore read the `v_*` views, never the `agg_*` tables
directly: bypassing the views would silently drop recent days.

### 6.3 Cost

The live halves are potentially expensive. `v_web_daily` sessionizes
`web_hits` through window-function CTEs, and window functions block
predicate pushdown, so a `WHERE project=… AND day>=…` may not prune the
work. `v_app_daily` has the same shape and `v_identity_daily` unions six
branches.

This is why `MCP_QUERY_TIMEOUT` and `MCP_QUERY_MAX_ROWS` are load-bearing
rather than decorative. Actual cost against a seeded database is to be
measured during implementation; if a curated tool is unacceptably slow,
the fix is a narrower date-bounded query, not bypassing the view.

## 7. Tool surface

Ten tools, registered with `mcp.AddTool[In, Out]` so the SDK infers both
input and output JSON Schema from Go structs and they cannot drift from
the handlers.

Every range tool takes `project`, `from`, `to`, the latter two as
`YYYY-MM-DD` strings matching the TEXT `day` columns.

| Tool | Source | Returns |
| --- | --- | --- |
| `list_projects` | `projects` ⋈ config | `alias`, `name`, `identity`, `archived`, and first/last day present per surface |
| `web_overview` | `v_web_daily` | `visitors`, `pageviews`, `sessions`, `bounces`, `duration_sec`, plus derived `bounce_rate` and `avg_session_sec` |
| `web_breakdown` | `v_web_{pages,referrers,countries,devices,browsers,os,utm}` | `dimension` enum + `limit`; rows of value, `visitors`, `pageviews` |
| `app_overview` | `v_app_daily` | `actives`, `views`, `sessions`, `duration_sec` |
| `app_breakdown` | `v_app_{screens,versions,os,devices,countries}` | `dimension` enum + `limit`; rows of value, `actives`, `views` |
| `product_events` | `v_product_daily`, `v_product_totals` | per-event `count` and `unique_users`; optional event-name filter; daily `total_events` and `active_users` |
| `product_attributes` | `agg_product_attrs` | `event_name`, `attr_key`, `attr_value`, `count`, `unique_users` |
| `retention` | `v_retention` | `surface` ∈ `web`\|`app`; `cohort_day`, `day_offset`, `actors`, `cohort_size` |
| `identities` | `v_identity_daily` ⋈ `identities` | `kind` ∈ `user`\|`group`; `id`, `name`, `actors`, `users`, `hits`, `views`, `events`. **This is the personal-data surface** (§5.3, §11) |
| `query` | guarded SQL | `columns`, `rows`, `truncated` |

`list_projects` is the orientation call. Without it a model guesses alias
strings, and its `identity` field is what tells the model which projects
can answer retention questions at all.

## 8. The `query` tool

Four independent layers, no SQL parser:

1. **Driver-level read-only** — `mode=ro` plus `query_only(1)`. Writes are
   impossible regardless of input.
2. **Wrap, do not parse** — the submitted query is embedded as
   `SELECT * FROM (<query>) LIMIT <max+1>`. This is a parser-free statement
   gate: a `DELETE`, a `PRAGMA`, or a `a; b` multi-statement is a syntax
   error in subquery position. It enforces the row cap in the same move,
   and `max+1` makes truncation detectable rather than silently reported as
   a complete answer.
3. **Reject `ATTACH`** by token scan. Read-only does not prevent attaching
   and reading another file on disk.
4. **Deadline** — `context.WithTimeout(MCP_QUERY_TIMEOUT)` on
   `QueryContext`; cancellation interrupts the SQLite statement.

Submitted SQL is logged at `debug` only, never at `info`: a query may
contain user ids from an identified-mode project.

## 9. Resources

- `schema://views` — the `v_*` DDL with column meanings, plus the three
  facts a model cannot infer: `day` is TEXT `YYYY-MM-DD`; every view
  includes yesterday; `v_retention` refreshes at 03:00 UTC and exists only
  for `identified` projects.
- `schema://projects` — the current project list with identity modes.

The schema resource is what makes `query` usable rather than a guessing
game.

## 10. Errors

Error text is written for a model to recover from, not for a human to read.

| Condition | Response |
| --- | --- |
| Missing/invalid token | HTTP 401 + `WWW-Authenticate` naming the metadata URL |
| Valid token, missing scope | HTTP 403. Not reachable in this design — the middleware is configured with no required scopes (§5.3); listed because the semantics must hold if scoping is ever added |
| Unknown project | Tool error listing the valid aliases |
| `retention` on an anonymous project | Tool error explaining that retention requires `identity: identified`, naming the setting — **not** an empty curve, which would present a configuration choice as a data gap |
| `product_attributes` where aggregation is off | Explains the `product_aggregation.enabled` flag rather than returning empty |
| Query timeout | "exceeded Ns; narrow the date range" |
| Row cap hit | Not an error: `truncated: true` plus a note, so partial data is never presented as complete |

## 11. Privacy and GDPR

Per §5.3, MCP reaches identified-mode personal data — stored user ids,
`$user_name` display names, and group ids — with no additional gate beyond
a valid token.

The README's "Privacy and GDPR" section gains a paragraph stating this
plainly: enabling `-mcp` on an instance with identified projects exposes
personal data to every holder of a valid token, and the IdP (or the token
list) is the whole of the access control. The existing operator
responsibilities around paths and `user_id` choice apply unchanged.

Anonymous projects are unaffected: there is nothing personal to expose,
because the daily-rotating salt has already destroyed the linkage.

## 12. Dependencies

Measured against `go.mod`, not estimated:

- Direct, new: `github.com/modelcontextprotocol/go-sdk` v1.7.0,
  `github.com/golang-jwt/jwt/v5`.
- Indirect, new: `google/jsonschema-go`, `segmentio/asm`,
  `segmentio/encoding`, `yosida95/uritemplate/v3`, `golang.org/x/oauth2`,
  `golang.org/x/sync`, `golang.org/x/time`. (`golang.org/x/sys` is already
  present.)

Direct dependencies go from 3 to 5; the total module count from 11 to 20.
That is a real cost to a deliberately small list. The justification is
interoperability: external MCP clients must speak to this
server, and hand-rolling Streamable HTTP sessions, JSON Schema inference
and spec-revision drift is where interop bugs live. The alternative was
considered and rejected.

All analytics logic stays behind our own interfaces so the protocol layer
remains replaceable.

## 13. Breaking change and migration

Bare `analytics serve` stops working. Call sites to update:

| File | Change |
| --- | --- |
| `deploy/systemd/analytics.service:9` | `ExecStart=… analytics serve -api` |
| `Dockerfile:52` | `CMD ["serve", "-api"]` |
| `Makefile:118` | `./$(BIN) serve -api` |
| `scripts/smoke.sh:33` | `./analytics serve -api` |
| `README.md`, `docs/deployment.md` | prose and examples |

Both documented upgrade paths self-heal: `deploy/compose/docker-compose.yml`
has no `command:` override so it picks up the new image `CMD` on
`docker compose pull`, and `deploy/install.sh:147` unconditionally rewrites
the unit file. Only a hand-rolled binary-only swap breaks, which is why the
error message names the fix explicitly.

Historical plan documents under `docs/superpowers/plans/` are archives and
are not edited.

## 14. Testing

- **Verifier, `oauth`** — locally generated RSA key plus an `httptest`
  JWKS server. Cases: valid; expired; `nbf` in the future; wrong `aud`;
  wrong `iss`; `alg: none`; HMAC-signed with the public key as secret;
  unknown `kid`; `kid` rotation triggering a refetch.
- **Verifier, `token`** — match, non-match, and that comparison does not
  short-circuit on a prefix.
- **`MCP_READ_TOKENS` parsing** — multiple pairs; a token containing a
  colon; empty label; empty token; a token containing a comma. The last
  three are startup errors.
- **`keygen -read`** — `ar_` prefix, 256 bits, prints an env line and no
  `projects.json` block.
- **Startup validation** — `-mcp` without `MCP_AUTH_MODE`, unknown mode,
  and each mode missing a required variable all exit non-zero.
- **Flags** — bare `serve` exits non-zero with the usage message; each of
  the three valid combinations binds what it should; equal addresses share
  one listener and register `/healthz` once.
- **Curated tools** — against a seeded database, reusing the approach in
  `zz_seed_test.go`. Includes the anonymous-project retention path and the
  aggregation-off `product_attributes` path.
- **`query` guard** — write attempt, `ATTACH`, multi-statement, row cap
  with truncation flag, timeout.
- **Integration** — the SDK's in-memory transport drives a real MCP client
  through `initialize` → `tools/list` → `tools/call` against the assembled
  handler.
- **Privacy** — bearer tokens and submitted SQL never appear in logs at
  `info`, in the spirit of the existing test that scans the database file
  and log output for IPs and User-Agents.

The 85% per-package coverage floor holds; `make check`, `make build-all`,
`go vet` and `gofmt` stay clean.

## 15. Package layout

    cmd/analytics/serve.go        -api / -mcp flags, usage error
    cmd/analytics/keygen.go       -read: 256-bit ar_ tokens, env output
    internal/app/app.go           two listeners, existing shutdown order
    internal/config/config.go     MCP_* loading and fail-fast validation
    internal/mcpserver/
        server.go                 mcp.Server assembly, routes, middleware
        auth.go                   both TokenVerifiers, JWKS cache
        readdb.go                 read-only connection, row scanning
        tools_web.go              web_overview, web_breakdown
        tools_app.go              app_overview, app_breakdown
        tools_product.go          product_events, product_attributes
        tools_identity.go         retention, list_projects
        query.go                  guarded SQL
        resources.go              schema resources
