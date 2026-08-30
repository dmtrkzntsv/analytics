# CLAUDE.md

## Commit messages

Use [Conventional Commits](https://www.conventionalcommits.org/). The release
notes are generated from the log, so the message is the changelog entry — write
the subject for someone reading the release page, not the diff.

```
<type>(<scope>): <subject>
```

`feat`, `fix` and `perf` are the only types that appear in the release notes,
each under its own heading. Everything else (`docs`, `refactor`, `test`,
`build`, `ci`, `chore`, `style`) is still valid and still expected, but is
omitted from the notes entirely — so anything a user should read about on the
release page needs one of the three published types.

The scope is the package or area the change lands in, matching the tree:
`store`, `server`, `jobs`, `config`, `mcpserver`, `manage`, `pipeline`, `geo`,
`dashboards`, `sdk`, `cmd`, `deploy`, `ci`. Omit it when a change is genuinely
repo-wide.

Breaking changes take a `!` before the colon (`feat(store)!: ...`), or a
`BREAKING CHANGE:` footer — both surface under 🚨 Breaking Changes.

Subject in the imperative, lower case, no trailing period:

```
feat(mcpserver): expose retention cohorts as a tool
fix(jobs): stop the daily pass double-counting identities
ci: do not cut a release for prose-only commits
```

## Releases

Pushing to `main` cuts a release: the workflow tags `vYY.MMDD.{build}`, runs
`make check`, builds the tarballs and hands off to `npx changelogithub`, which
creates the GitHub release, groups the notes by commit type and uploads the
artifacts. Container images publish from a separate job.

Two consequences worth remembering:

- A push touching only `docs/**` or `**.md` is excluded via `paths-ignore` and
  publishes nothing. A push that mixes prose with code still releases.
- Release notes are only as good as the commit subjects, and they are not
  hand-edited afterwards.

## Checks

`make check` (vet + coverage + restore test) is what CI runs — run it before
pushing. `make build` compiles the binary; `make test` runs the race-enabled
suite on its own.

## Documentation

`docs/twillingate.md` is the single normative document and the only prose
the MCP endpoint serves to agents. It is not a summary of the code — it is
the contract. Update it **in the same commit** as any change to:

- reserved attribute keys or event names (`internal/server/ingest.go`)
- the ingest wire format or its responses (`internal/server/handlers.go`)
- the JS SDK's public API, `data-` attributes or defaults
  (`sdk/src/twillingate.ts`)
- config keys, env vars or project fields (`internal/config/`)
- install, upgrade or restore procedure (`deploy/`, `Makefile`)
- MCP auth modes, client setup, or the set of tools offered
  (`internal/mcpserver/`)
- the Evidence dashboards (`internal/dashboards/`, `evidence/`)

Update `schemaViews` in `internal/mcpserver/resources.go` in the same commit
as any migration that adds or changes a queryable view.

Everything else under `docs/` that it absorbed is a pointer, not a source —
never add content there. `docs/plausible/README.md` is the exception: it
documents bytes the collector serves at `/js/plausible-shim.js` and a test
binds it to them.

`docs_sync_test.go` enforces part of this — reserved keys in both
directions, the SDK's public symbols, and every queryable web view. The rest
is on you. A `docs`-only push publishes nothing (`paths-ignore`), so a
same-commit update costs no extra release.
