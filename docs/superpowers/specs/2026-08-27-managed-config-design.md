# Managed configuration — design

Date: 2026-08-27
Status: proposed
Companion to: 2026-08-24-mcp-endpoint-design.md (amended by this spec)

## 1. Purpose

Move project configuration from `projects.json` into the database and make
it manageable at runtime — by an AI through MCP management tools, and by
the operator through CLI subcommands — without editing files or restarting
the server.

The concrete capabilities: create, archive, restore and (CLI-only) delete
projects; issue, disable and re-enable ingest keys; import and export the
whole registry as JSON.

## 2. Non-goals

- No multi-user model, roles, or per-project authorization. One operator,
  one authorization tier (§6).
- No DB-issued MCP access tokens. Access tokens live only in
  `analytics.env`; the management surface cannot mint access to itself.
- No delete through MCP. Irreversible operations require a shell (§7.3).
- No HTTP admin API outside MCP. The CLI talks to the database directly.
- No hot-reload of infra settings (`LISTEN_ADDR`, retention defaults,
  buffer sizes). Those stay env-only, read at boot, 12-factor as today.

## 3. The registry

### 3.1 Schema (migration 005)

`projects` gains the per-project settings that lived in JSON:

    ALTER TABLE projects ADD COLUMN identity            TEXT NOT NULL DEFAULT 'anonymous';
    ALTER TABLE projects ADD COLUMN allowed_origins     TEXT NOT NULL DEFAULT '[]';  -- JSON array
    ALTER TABLE projects ADD COLUMN retention           TEXT;                        -- JSON object or NULL
    ALTER TABLE projects ADD COLUMN product_aggregation TEXT;                        -- JSON object or NULL

The two JSON-typed columns reuse the exact shapes `projects.json` defines
today (`RetentionOverride`, `ProductAggregation`), so import/export and the
Go structs need no translation layer.

Ingest keys become rows, replacing the `ingest_keys` config array:

    CREATE TABLE ingest_keys (
        key         TEXT PRIMARY KEY,
        project     TEXT NOT NULL REFERENCES projects(alias),
        label       TEXT NOT NULL,
        created_at  TEXT NOT NULL DEFAULT (datetime('now')),
        disabled_at TEXT                                      -- NULL = active
    );

`disabled_at` replaces the boolean `disabled` for the same reason
`archived_at` did on projects: the flag doubles as a timestamp for the
audit trail, and re-enabling is setting it back to NULL.

Every write is recorded:

    CREATE TABLE audit_log (
        ts      TEXT NOT NULL DEFAULT (datetime('now')),
        actor   TEXT NOT NULL,          -- 'mcp' or 'cli'
        action  TEXT NOT NULL,          -- 'project.create', 'key.disable', …
        subject TEXT NOT NULL,          -- alias or key label
        detail  TEXT NOT NULL DEFAULT ''
    );

And `meta` gains a `config_version` row, incremented in the same
transaction as every registry write.

### 3.2 Source of truth, and what gets deleted

The database is the sole runtime source of project configuration.
Consequently:

- `SyncProjects` and its boot-time archiving ("absent from config ⇒
  archived") are **deleted**. That rule is exactly backwards under DB
  truth, and nothing replaces it: projects change state only through
  explicit operations.
- `config.Load` stops reading `PROJECTS_FILE`. The variable, the default
  path, `projects.example.json` and the config-file documentation are
  retired. For one release, a boot-time warning fires if the file still
  exists and names the import command (§9).
- `config.Config` keeps only infra settings. `Projects`, `keys`, and
  `RetentionFor` move behind the registry (§3.3). Consumers change
  accordingly: `internal/server` (key lookup, origins, identity mode) and
  `internal/jobs` (retention, aggregation settings, identified check) read
  the registry snapshot; `internal/pipeline` is write-only and unaffected;
  the dashboards already read the database.

### 3.3 Hot path: the snapshot

`internal/manage` exposes a `Registry` whose current state is an
immutable snapshot behind an `atomic.Pointer`: the flat constant-time key
slice (unchanged semantics from `config.keys`), the origin sets, and the
per-project settings. Readers pay one atomic load; no locks on the ingest
path.

Invalidation covers all three writer paths with one mechanism:

- In-process writes (MCP tools in the same `serve`) rebuild the snapshot
  synchronously after commit.
- Out-of-process writes (a second `serve -mcp` process, or the CLI) are
  noticed by polling `config_version` — a single-row point read — at most
  once per second on the ingest path, amortized to noise. A stale window
  of ≤1s for a key disable is acceptable; the README already treats key
  retirement as a watch-the-logs process, not an instant one.

WAL handles the multi-process writes themselves; writers serialize on the
existing `busy_timeout(5000)`.

## 4. One core, three frontends

`internal/manage` owns every operation; MCP tools, CLI subcommands and the
importer are thin frontends that parse input, call the core, and render
output. No business logic lives in a frontend.

    CreateProject(alias, name, identity, origins, …)
    UpdateProject(alias, fields…)         // name, identity, origins, retention, aggregation
    ArchiveProject(alias)                 // sets archived_at; ingest rejects; data kept
    RestoreProject(alias)
    DeleteProject(alias)                  // CLI-only frontend exposure, §7.3
    IssueIngestKey(project, label)        // mints ak_…, registers, returns snippet
    DisableIngestKey / EnableIngestKey(key or project+label)
    ListProjects / ListKeys
    Export(w io.Writer) / Import(r io.Reader)

Every mutating operation writes its `audit_log` row (actor supplied by the
frontend) and bumps `config_version`, in one transaction.

`UpdateProject` exists because origins changes are routine once file
editing is gone; without it there is no way to point a project at a new
domain. Changing `identity` from `anonymous` to `identified` is
privacy-significant and the tool description says so, but it is allowed —
the caller is the operator.

## 5. MCP management tools

Added to the endpoint from the companion spec, same transport, same auth:

| Tool | Core op | Annotations |
| --- | --- | --- |
| `create_project` | CreateProject | `readOnlyHint: false` |
| `update_project` | UpdateProject | `readOnlyHint: false` |
| `archive_project` | ArchiveProject | `readOnlyHint: false`, `idempotentHint: true` |
| `restore_project` | RestoreProject | `readOnlyHint: false`, `idempotentHint: true` |
| `issue_ingest_key` | IssueIngestKey | `readOnlyHint: false` |
| `disable_ingest_key` | DisableIngestKey | `readOnlyHint: false`, `idempotentHint: true` |
| `enable_ingest_key` | EnableIngestKey | `readOnlyHint: false`, `idempotentHint: true` |
| `list_ingest_keys` | ListKeys | read-only |

There is no `delete_project` tool and no import/export tool. The
companion spec's `list_projects` reads the registry and gains the
per-project settings in its output.

`create_project` and `issue_ingest_key` return the embed snippet alongside
the key, so "create a project for my blog" yields something paste-ready in
one round trip.

## 6. Authorization: one tier, eyes open

The same token that reads analytics authorizes management. Two modes, as
in the companion spec, one verifier seam:

- `token` mode: **`MCP_TOKEN`** — a single static bearer token in
  `analytics.env`. One operator, one token; the labeled multi-token list
  (`MCP_READ_TOKENS`) is dropped from the companion spec, and with it the
  `label:token` parsing rules. `analytics keygen -mcp` mints it (256-bit,
  `ar_` prefix) and prints the env line.
- `oauth` mode: unchanged — external IdP, resource-server-only validation.
  No scope distinction is required between read and management tools.

The risk this accepts, stated as in the companion spec's §5.3 so it is not
rediscovered: ingest keys are public, so anyone on the internet can write
strings into the database (paths, event names, `$user_name`), and those
strings return through tool results into the context of a model holding
write authority. A crafted `$user_name` is a prompt-injection vector
against the management tools. Three guardrails cap it:

1. **Annotations** (§5) — clients that honor `readOnlyHint: false`
   interpose the operator before any write executes. In a single-user app
   the human in the loop is the right human.
2. **No irreversible primitive over MCP** — the worst uninterrupted
   outcome is reversible config churn plus a minted *ingest* key, which is
   public-class by design. Access tokens cannot be minted at all: they
   live only in env.
3. **Audit trail** — every write is visible after the fact.

Internally every tool registration is tagged read/write; that tag drives
the MCP annotations and is the seam where a two-tier mode could become a
config knob later without redesign. No such knob ships now.

## 7. CLI

### 7.1 Commands

    analytics project create -alias blog -name "My blog" [-identity anonymous] [-origin https://…]…
    analytics project update -alias blog [-name …] [-identity …] [-origin …]…
    analytics project list
    analytics project archive|restore -alias blog
    analytics project delete -alias blog [-force]
    analytics key issue   -project blog -label web
    analytics key list    [-project blog]
    analytics key disable|enable -project blog -label web
    analytics config export [> registry.json]
    analytics config import registry.json

Same binary, same subcommand table as `serve`/`migrate`/`keygen`. Direct
database access, not HTTP: it matches `migrate`, works as break-glass when
the server is down or the token is lost, and the running server notices
every write via `config_version` (§3.3).

Env resolution is identical to `migrate` — the documented pattern is
`sudo -u analytics sh -ac '. /etc/analytics/analytics.env; analytics …'`
(docs/deployment.md) — plus an `-env-file` flag on the management commands
to spare that incantation now that they are routine rather than one-time.

### 7.2 keygen

`keygen` is absorbed: `analytics key issue` mints, registers and prints
the snippet in one step, which the old print-only flow cannot do once keys
live in the DB. The bare `keygen` ingest-key mode is deprecated with a
pointer to `key issue`; `keygen -mcp` (renamed from the companion spec's
`-read`, matching the `MCP_TOKEN` rename) remains, because it generates an
env value and correctly never touches the database.

### 7.3 Delete

`project delete` hard-deletes the project row and every row keyed by the
alias — raw hits, events, app views, all `agg_*` tables, identities,
actors, retention, ingest keys — in a single transaction, then runs
`PRAGMA incremental_vacuum` to return the pages (auto_vacuum is already
INCREMENTAL). It requires `-force` or an interactive typed-alias
confirmation.

Delete is deliberately CLI-only. The MCP no-irreversible-ops guardrail
(§6) exists to cap prompt injection; the operator at a shell is outside
that threat model and can delete the database file anyway. This is also
the complete-erasure lever the README's GDPR section can point to.

## 8. Import / export

`Export` writes the full registry — projects with settings, ingest keys
with state — as one JSON document that round-trips through `Import`
losslessly. The format is the natural JSON of the registry structs and is
versioned with a top-level `"version": 1`.

`Import` is declarative and **never destructive**: it creates missing
projects and keys and updates listed fields, but a project or key absent
from the file is left untouched — not archived, not disabled, not deleted.
The old boot-archiving footgun is not rebuilt in a new place. Disabling
things via import works only explicitly (`"disabled": true` on a key).

Import also accepts the legacy `projects.json` array format, detected by
shape, which makes it the one-time migration path for existing installs.
Export gives the operator back a versionable artifact: run it after
changes and commit the output, restoring the gitops property the file
used to provide.

## 9. Migration and deploy story

Existing installs:

    analytics config import /etc/analytics/projects.json

once. The file is then inert; for one release, boot logs a warning naming
that command if the file still exists, after which the warning is removed.

New installs lose the config-file step entirely: `docker compose up -d`,
then either `analytics project create …` over SSH or connect an MCP client
and say "create a project for my blog" — both return a paste-ready
snippet. `install.sh` stops installing `projects.example.json`;
`PROJECTS_FILE` leaves `.env.example`, the README table and the compose
files; the README quickstarts, Embedding, Configuration and GDPR sections
are rewritten accordingly.

Litestream now replicates configuration with the data, so a restore is
complete rather than data-minus-config.

## 10. Amendments to the companion spec

Applied to `2026-08-24-mcp-endpoint-design.md` in the same branch:

- §2 non-goals: management is no longer excluded; it is specified here.
  The endpoint spec's own tools remain read-only.
- §4: `MCP_READ_TOKENS` → `MCP_TOKEN` (single value); the `projects.json
  is unchanged` paragraph now defers to this spec.
- §4.1: rewritten for the single token; `label:token` parsing rules and
  their tests dropped; `keygen -read` → `keygen -mcp`.
- §5.2 token verifier: constant-time compare against one value;
  `TokenInfo.UserID` is `"mcp"`.
- §5.3: notes the same token now authorizes management (this spec §6).
- §7 `list_projects`: source is the registry, not "projects ⋈ config".

## 11. Dependencies

None new. Everything here is stdlib plus what the companion spec already
adds.

## 12. Testing

- **Registry**: snapshot swap on in-process write; `config_version` poll
  picks up an out-of-process write within a second; constant-time key
  lookup preserved; origins/identity/retention read correctly by server
  and jobs after a live change.
- **Core ops**: each operation against a temp DB, including audit row and
  version bump in the same transaction; archive/restore round trip;
  key disable → ingest 401s within the poll window.
- **Delete**: cascade leaves zero rows for the alias across every table;
  pages reclaimed; refuses without `-force`/confirmation.
- **Import/export**: round-trip equality; import of the legacy
  `projects.json` shape; import never archives/disables anything absent
  from the file.
- **CLI**: each subcommand exit code and output against a temp DB;
  `-env-file`.
- **MCP tools**: annotations present (`readOnlyHint`, `idempotentHint`)
  on every management tool; snippet returned by `create_project` and
  `issue_ingest_key`.
- **Migration 005**: applies to a populated v004 database; existing
  projects get defaults; boot warning fires iff the legacy file exists.

The 85% per-package floor, `make check`, `make build-all`, `go vet`,
`gofmt` all hold.

## 13. Sequencing

This spec lands **before** implementation of the companion spec begins:
it reshapes `internal/config` and supplies the registry that
`list_projects` and the auth layer read. Building the endpoint first would
mean building its config plumbing twice.

## 14. Package layout

    internal/manage/
        registry.go     snapshot, atomic swap, config_version poll
        ops.go          core operations + audit
        importexport.go JSON round-trip + legacy projects.json import
    internal/store/sqlite/
        migrations/005_registry.sql
        registry.go     row access for internal/manage
    cmd/analytics/
        project.go      project create|update|list|archive|restore|delete
        key.go          key issue|list|disable|enable
        configcmd.go    config import|export
        keygen.go       -mcp; ingest-key mode deprecated
    internal/mcpserver/
        tools_manage.go the eight management tools
