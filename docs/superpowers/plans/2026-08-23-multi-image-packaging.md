# Multi-Image Packaging Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Publish `analytics` and `analytics-evidence` images to GHCR, replace the Evidence entrypoint script with an `analytics dashboards` mode, and remove replication from the application.

**Architecture:** One Dockerfile with two published targets. A new `internal/dashboards` package owns the Evidence build loop: snapshot the database with `VACUUM INTO`, run `npm run sources && npm run build` through an `execCommand` seam, rotate the output into one of two slots, and serve it with `net/http`. `internal/synccmd` and all `SYNC_*` configuration are deleted; restore becomes `deploy/litestream/restore.sh` plus docs.

**Tech Stack:** Go 1.25 (stdlib + `modernc.org/sqlite`), Docker buildx, GitHub Actions, Evidence 40 on Node 20.

**Spec:** `docs/superpowers/specs/2026-08-23-multi-image-packaging-design.md`

## Global Constraints

- stdlib only, except the two dependencies already in `go.mod` (`modernc.org/sqlite`, `github.com/google/uuid`).
- Coverage gate: `./scripts/coverage.sh` requires ≥85% per package. `internal/dashboards` is in scope.
- `gofmt`, `go vet` clean. `make check` green before every commit.
- Images: `ghcr.io/dmtrkzntsv/analytics` (amd64, arm64, arm/v7) and `ghcr.io/dmtrkzntsv/analytics-evidence` (amd64, arm64).
- Non-root user `analytics` (uid 10001) in both images. `ENTRYPOINT ["/usr/local/bin/analytics"]`.
- Defaults, verbatim: `DASHBOARDS_ADDR=0.0.0.0:3000`, `DASHBOARDS_INTERVAL=15m`, `DASHBOARDS_PROJECT_DIR=/opt/evidence`, `DASHBOARDS_WORK_DIR=/var/lib/dashboards`. Poll period is a fixed 60s.
- The word "backoffice" does not appear in new code, config, paths or docs.

---

### Task 1: Config — `DASHBOARDS_*` in, `SYNC_*` out

**Files:**
- Modify: `internal/config/config.go` (`SyncConfig` at :61, `Config` at :66, `FromEnv` at :133, `validate` at :205)
- Test: `internal/config/config_test.go`

**Interfaces:**
- Produces: `config.DashboardsConfig{DBPath, Addr string; Interval time.Duration; ProjectDir, WorkDir string}`; `config.Config.Dashboards DashboardsConfig`; `config.Load() (*Config, error)` (unchanged, requires projects); `config.LoadDashboards() (*Config, error)`; `config.FromEnv(lookup)`; `config.FromEnvDashboards(lookup)`.

- [ ] **Step 1: Write the failing tests**

```go
func TestDashboardsDefaults(t *testing.T) {
	c, err := config.FromEnvDashboards(mapLookup(map[string]string{
		"DATABASE_URL": "sqlite:///var/lib/analytics/analytics.db",
	}))
	if err != nil {
		t.Fatalf("FromEnvDashboards: %v", err)
	}
	if c.Dashboards.DBPath != "/var/lib/analytics/analytics.db" {
		t.Errorf("DBPath = %q, want the DATABASE_URL path", c.Dashboards.DBPath)
	}
	if c.Dashboards.Addr != "0.0.0.0:3000" || c.Dashboards.Interval != 15*time.Minute {
		t.Errorf("defaults = %+v", c.Dashboards)
	}
	if c.Dashboards.ProjectDir != "/opt/evidence" || c.Dashboards.WorkDir != "/var/lib/dashboards" {
		t.Errorf("dir defaults = %+v", c.Dashboards)
	}
}

func TestDashboardsDBPathWins(t *testing.T) {
	c, err := config.FromEnvDashboards(mapLookup(map[string]string{
		"DATABASE_URL":       "sqlite:///var/lib/analytics/analytics.db",
		"DASHBOARDS_DB_PATH": "/data/replica.db",
	}))
	if err != nil {
		t.Fatalf("FromEnvDashboards: %v", err)
	}
	if c.Dashboards.DBPath != "/data/replica.db" {
		t.Errorf("DBPath = %q, want the explicit override", c.Dashboards.DBPath)
	}
}

func TestDashboardsNeedsADatabase(t *testing.T) {
	if _, err := config.FromEnvDashboards(mapLookup(map[string]string{})); err == nil {
		t.Fatal("want an error with neither DASHBOARDS_DB_PATH nor DATABASE_URL")
	}
}

func TestDashboardsIgnoresProjectsFile(t *testing.T) {
	// No PROJECTS_FILE, no projects: dashboards never reads the project list.
	c, err := config.FromEnvDashboards(mapLookup(map[string]string{
		"DASHBOARDS_DB_PATH": "/data/replica.db",
		"PROJECTS_FILE":      "/nonexistent/projects.json",
	}))
	if err != nil {
		t.Fatalf("FromEnvDashboards: %v", err)
	}
	if len(c.Projects) != 0 {
		t.Errorf("Projects = %v, want none", c.Projects)
	}
}
```

Delete the existing `SYNC_*` tests in the same file.

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/config/ -run Dashboards -v`
Expected: FAIL, `undefined: config.FromEnvDashboards`.

- [ ] **Step 3: Implement**

Replace `SyncConfig` with:

```go
type DashboardsConfig struct {
	DBPath     string
	Addr       string
	Interval   time.Duration
	ProjectDir string
	WorkDir    string
}
```

Change `Config.Sync SyncConfig` to `Config.Dashboards DashboardsConfig`. Split `FromEnv`:

```go
func Load() (*Config, error)           { return FromEnv(os.LookupEnv) }
func LoadDashboards() (*Config, error) { return FromEnvDashboards(os.LookupEnv) }

func FromEnv(lookup func(string) (string, bool)) (*Config, error) {
	return parse(lookup, true)
}

// FromEnvDashboards parses the environment for `analytics dashboards`, which
// renders whatever database it is pointed at and never reads the project list.
func FromEnvDashboards(lookup func(string) (string, bool)) (*Config, error) {
	return parse(lookup, false)
}
```

In `parse`, replace the `Sync:` block with the `Dashboards:` block reading the five variables, then:

```go
	if !withProjects {
		if c.Dashboards.DBPath == "" {
			if c.Database == "" {
				return nil, fmt.Errorf("config: DASHBOARDS_DB_PATH or DATABASE_URL is required")
			}
			c.Dashboards.DBPath = strings.TrimPrefix(c.Database, "sqlite://")
		}
		return c, nil
	}
```
before the projects file is opened.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/config/`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/config
git commit -m "feat(config): DASHBOARDS_* settings, drop SYNC_*"
```

---

### Task 2: `internal/dashboards` — snapshot and Evidence source path

**Files:**
- Create: `internal/dashboards/dashboards.go`, `internal/dashboards/snapshot.go`
- Test: `internal/dashboards/snapshot_test.go`

**Interfaces:**
- Produces: `func snapshot(ctx context.Context, dbPath, dest string) error`; `func sourceFilename(projectDir, snapshotPath string) (string, error)`.

- [ ] **Step 1: Write the failing tests**

```go
func TestSnapshotCopiesData(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	db, err := sql.Open("sqlite", src)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("create table t(x); insert into t values (42)"); err != nil {
		t.Fatal(err)
	}
	db.Close()

	dest := filepath.Join(dir, "snapshot.db")
	if err := snapshot(context.Background(), src, dest); err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	out, err := sql.Open("sqlite", dest)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	var x int
	if err := out.QueryRow("select x from t").Scan(&x); err != nil || x != 42 {
		t.Fatalf("snapshot content: x=%d err=%v", x, err)
	}
}

func TestSnapshotOverwritesPrevious(t *testing.T) {
	// VACUUM INTO refuses an existing target; a second run must still work.
	dir := t.TempDir()
	src := filepath.Join(dir, "src.db")
	db, _ := sql.Open("sqlite", src)
	db.Exec("create table t(x)")
	db.Close()
	dest := filepath.Join(dir, "snapshot.db")
	if err := snapshot(context.Background(), src, dest); err != nil {
		t.Fatal(err)
	}
	if err := snapshot(context.Background(), src, dest); err != nil {
		t.Fatalf("second snapshot: %v", err)
	}
}

func TestSourceFilenameIsRelativeToTheSourceDir(t *testing.T) {
	got, err := sourceFilename("/opt/evidence", "/var/lib/dashboards/snapshot.db")
	if err != nil {
		t.Fatal(err)
	}
	// The sqlite plugin resolves filename with path.join(<source dir>, …).
	want := "../../../../var/lib/dashboards/snapshot.db"
	if got != want {
		t.Errorf("sourceFilename = %q, want %q", got, want)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/dashboards/`
Expected: FAIL, `undefined: snapshot`.

- [ ] **Step 3: Implement**

```go
// snapshot writes a consistent copy of dbPath to dest. Evidence's node
// process then owns a file nobody else writes: a live database can be
// checkpointed mid-build, and a replica can be replaced by a restore job.
func snapshot(ctx context.Context, dbPath, dest string) error {
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	if err := os.Remove(dest); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		return err
	}
	defer db.Close()
	_, err = db.ExecContext(ctx, "VACUUM INTO ?", dest)
	return err
}

// sourceFilename is what the sqlite datasource plugin needs in
// EVIDENCE_SOURCE__analytics__filename: it resolves the value with
// path.join(<source dir>, filename), so an absolute path is rewritten and a
// hand-written ../../.. breaks whenever the project directory moves.
func sourceFilename(projectDir, snapshotPath string) (string, error) {
	return filepath.Rel(filepath.Join(projectDir, "sources", "analytics"), snapshotPath)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/dashboards/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/dashboards
git commit -m "feat(dashboards): database snapshot and Evidence source path"
```

---

### Task 3: `internal/dashboards` — build cycle and slot rotation

**Files:**
- Modify: `internal/dashboards/dashboards.go`
- Test: `internal/dashboards/build_test.go`

**Interfaces:**
- Consumes: `snapshot`, `sourceFilename` (Task 2).
- Produces: `var execCommand = exec.CommandContext`; `type Builder struct{ Cfg config.DashboardsConfig; Log *slog.Logger; site atomic.Pointer[string] }`; `func (b *Builder) Build(ctx context.Context) error`; `func (b *Builder) ServeHTTP(w http.ResponseWriter, r *http.Request)`.

- [ ] **Step 1: Write the failing tests**

`fakeNPM` replaces `execCommand` with a helper-process pattern already used by `internal/synccmd/sync_test.go`; the fake writes `build/index.html` containing the value of `EVIDENCE_SOURCE__analytics__filename` so the test can assert the path handed to Evidence.

```go
func TestBuildRotatesSlotsAndServes(t *testing.T) {
	b, dir := newTestBuilder(t, fakeNPMSucceeds)
	if err := b.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}
	first := *b.site.Load()
	if filepath.Base(first) != "site.a" {
		t.Errorf("first slot = %q, want site.a", first)
	}
	if err := b.Build(context.Background()); err != nil {
		t.Fatalf("second Build: %v", err)
	}
	if second := *b.site.Load(); filepath.Base(second) != "site.b" {
		t.Errorf("second slot = %q, want site.b", second)
	}
	if _, err := os.Stat(filepath.Join(dir, "build")); !errors.Is(err, fs.ErrNotExist) {
		t.Error("build/ should have been renamed into a slot")
	}
}

func TestBuildFailureKeepsServingPreviousSlot(t *testing.T) {
	b, _ := newTestBuilder(t, fakeNPMSucceeds)
	if err := b.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	good := *b.site.Load()
	b.npm = fakeNPMFails
	if err := b.Build(context.Background()); err == nil {
		t.Fatal("want an error from a failing npm")
	}
	if now := *b.site.Load(); now != good {
		t.Errorf("site = %q, want the previous good build %q", now, good)
	}
}

func TestServeHTTPBeforeFirstBuild(t *testing.T) {
	b, _ := newTestBuilder(t, fakeNPMSucceeds)
	rec := httptest.NewRecorder()
	b.ServeHTTP(rec, httptest.NewRequest("GET", "/", nil))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

func TestServeHTTPServesTheBuild(t *testing.T) {
	b, _ := newTestBuilder(t, fakeNPMSucceeds)
	if err := b.Build(context.Background()); err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	b.ServeHTTP(rec, httptest.NewRequest("GET", "/index.html", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "snapshot.db") {
		t.Errorf("body = %q, want the Evidence source path the fake recorded", rec.Body.String())
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/dashboards/ -run Build -v`
Expected: FAIL, `undefined: Builder`.

- [ ] **Step 3: Implement**

`Build`: snapshot → `sourceFilename` → `npm run sources` → `npm run build`, both with cwd `Cfg.ProjectDir` and `EVIDENCE_SOURCE__analytics__filename` in the environment → rotate. Rotation picks the slot that is *not* current, `os.RemoveAll`s it, renames `build/` onto it, stores the pointer, then removes the slot that was current before. Both slots live inside `ProjectDir`, so the rename never crosses a filesystem.

```go
func (b *Builder) slots() (string, string) {
	return filepath.Join(b.Cfg.ProjectDir, "site.a"), filepath.Join(b.Cfg.ProjectDir, "site.b")
}

func (b *Builder) rotate() error {
	a, c := b.slots()
	next, prev := a, ""
	if cur := b.site.Load(); cur != nil {
		if *cur == a {
			next = c
		}
		prev = *cur
	}
	if err := os.RemoveAll(next); err != nil {
		return err
	}
	if err := os.Rename(filepath.Join(b.Cfg.ProjectDir, "build"), next); err != nil {
		return err
	}
	b.site.Store(&next)
	if prev != "" {
		_ = os.RemoveAll(prev)
	}
	return nil
}

func (b *Builder) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	root := b.site.Load()
	if root == nil {
		http.Error(w, "dashboards: building", http.StatusServiceUnavailable)
		return
	}
	http.FileServer(http.Dir(*root)).ServeHTTP(w, r)
}
```

- [ ] **Step 4: Run tests**

Run: `go test ./internal/dashboards/ -v`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add internal/dashboards
git commit -m "feat(dashboards): build cycle, slot rotation, static serving"
```

---

### Task 4: `internal/dashboards` — change detection and the run loop

**Files:**
- Create: `internal/dashboards/run.go`
- Test: `internal/dashboards/run_test.go`

**Interfaces:**
- Consumes: `Builder` (Task 3).
- Produces: `func stamp(dbPath string) string`; `func Run(ctx context.Context, cfg config.DashboardsConfig, logger *slog.Logger) error`.

- [ ] **Step 1: Write the failing tests**

```go
func TestStampChangesWithTheWAL(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "a.db")
	os.WriteFile(db, []byte("x"), 0o644)
	before := stamp(db)
	os.WriteFile(db+"-wal", []byte("y"), 0o644)
	if after := stamp(db); after == before {
		t.Error("stamp ignored the -wal file; WAL writes between checkpoints would be invisible")
	}
}

func TestStampOfAMissingDatabaseIsStable(t *testing.T) {
	if a, b := stamp("/nonexistent/x.db"), stamp("/nonexistent/x.db"); a != b {
		t.Errorf("stamp is not stable for a missing file: %q vs %q", a, b)
	}
}

func TestRunBuildsOnceImmediatelyThenStops(t *testing.T) {
	// ctx cancelled after the first build; a second build must not happen even
	// though the database keeps changing, because the interval has not elapsed.
	...
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./internal/dashboards/ -run 'Stamp|Run' -v`
Expected: FAIL, `undefined: stamp`.

- [ ] **Step 3: Implement**

```go
// pollInterval is how often the database is stat-ed. It is deliberately not
// configurable: two stat calls a minute cost nothing, and the knob that
// matters — how often Evidence rebuilds — is DASHBOARDS_INTERVAL.
const pollInterval = time.Minute

// stamp fingerprints the database for change detection. The -wal sibling is
// included because a live database under traffic only has its main file
// touched at checkpoints; a missing file contributes an empty entry, so a
// database that appears later registers as a change.
func stamp(dbPath string) string {
	var sb strings.Builder
	for _, p := range []string{dbPath, dbPath + "-wal"} {
		if fi, err := os.Stat(p); err == nil {
			fmt.Fprintf(&sb, "%d:%d|", fi.Size(), fi.ModTime().UnixNano())
		} else {
			sb.WriteString("-|")
		}
	}
	return sb.String()
}
```

`Run` starts the HTTP server on `cfg.Addr`, builds immediately, then ticks every `pollInterval`: rebuild when `stamp` differs from the last built stamp *and* `cfg.Interval` has elapsed since the last build. Build errors are logged, not returned. Shutdown on `ctx.Done()` via `srv.Shutdown`.

- [ ] **Step 4: Run tests**

Run: `go test ./internal/dashboards/ -v && ./scripts/coverage.sh`
Expected: PASS, `internal/dashboards` ≥85%.

- [ ] **Step 5: Commit**

```bash
git add internal/dashboards
git commit -m "feat(dashboards): stat-based change detection and run loop"
```

---

### Task 5: CLI — add `dashboards`, delete `sync`

**Files:**
- Create: `cmd/analytics/dashboards.go`
- Delete: `cmd/analytics/sync.go`, `internal/synccmd/`
- Modify: `cmd/analytics/main.go:25,30` (usage strings)
- Test: `cmd/analytics/commands_test.go`

**Interfaces:**
- Consumes: `dashboards.Run` (Task 4), `config.LoadDashboards` (Task 1).

- [ ] **Step 1: Write the failing test**

```go
func TestUsageListsTheModes(t *testing.T) {
	var out bytes.Buffer
	if code := run(nil, &out); code != 2 {
		t.Errorf("exit = %d, want 2", code)
	}
	if got := out.String(); !strings.Contains(got, "dashboards") || strings.Contains(got, "sync") {
		t.Errorf("usage = %q, want dashboards and no sync", got)
	}
}
```

- [ ] **Step 2: Run to verify failure**

Run: `go test ./cmd/analytics/ -run Usage -v`
Expected: FAIL (usage still says `sync`).

- [ ] **Step 3: Implement**

`cmd/analytics/dashboards.go` mirrors `serve.go`: parse flags, `config.LoadDashboards()`, `app.NewLogger`, `signal.NotifyContext`, `dashboards.Run`. Usage becomes `usage: analytics <serve|dashboards|migrate|version> [flags]`. Delete `sync.go` and `internal/synccmd/`, and any `sync` cases in `commands_test.go`.

- [ ] **Step 4: Run tests**

Run: `make check`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add -A cmd internal
git commit -m "feat(cli): add dashboards mode, remove sync"
```

---

### Task 6: Dockerfile, two targets, and the `evidence/` move

**Files:**
- Modify: `Dockerfile`, `Makefile` (`docker`, `dashboards` targets), `.dockerignore`
- Move: `backoffice/evidence/` → `evidence/`
- Delete: `backoffice/evidence-entrypoint.sh`

- [ ] **Step 1: Move the project and update the Makefile**

```bash
git mv backoffice/evidence evidence
git rm backoffice/evidence-entrypoint.sh
```

`make dashboards` becomes `cd evidence && npm install && …`.

- [ ] **Step 2: Write the Dockerfile**

Stages `go-build`, `evidence-build`, and targets `runtime` and `evidence` exactly as specified in §5 of the spec. `evidence-build` installs `build-base python3` because `sqlite3` has no musl prebuilds; the runtime stages carry no compilers. The `evidence` target creates and `chown`s `/opt/evidence` and `/var/lib/dashboards`, and sets `DASHBOARDS_PROJECT_DIR`/`DASHBOARDS_WORK_DIR`.

- [ ] **Step 3: Build both targets**

Run: `docker build --target runtime -t analytics:dev . && docker build --target evidence -t analytics-evidence:dev .`
Expected: both succeed; `docker run --rm analytics:dev version` prints a version.

- [ ] **Step 4: Boot-test the Evidence image**

Run a container against a seeded database and curl `:3000`; expect `503` first, then `200` with a rendered page.

- [ ] **Step 5: Commit**

```bash
git add -A Dockerfile Makefile .dockerignore evidence backoffice
git commit -m "feat(docker): two build targets, move evidence project to the repo root"
```

---

### Task 7: Compose files and the restore script

**Files:**
- Create: `deploy/compose/docker-compose.yml`, `deploy/compose/docker-compose.evidence.yml`, `deploy/compose/docker-compose.litestream.yml`, `deploy/litestream/restore.sh`, `deploy/litestream/restore.cron`
- Delete: `backoffice/docker-compose.yml`, `backoffice/docker-compose.aio.yml`
- Modify: `.env.example` (drop `SYNC_*`, add `DASHBOARDS_*`)

- [ ] **Step 1: Write the three compose files** exactly as in §9 of the spec.

- [ ] **Step 2: Write `restore.sh`**

`set -eu`, `flock` on a lock file so overlapping cron runs cannot collide, `litestream restore -config … -o "$REPLICA.tmp" "$SOURCE_DB"`, `sqlite3 "$REPLICA.tmp" 'PRAGMA quick_check'` (must print `ok`), then `mv` into place. Any failure exits non-zero leaving the previous replica untouched. Configuration by environment with documented defaults; `sqlite3` is a stated prerequisite.

- [ ] **Step 3: Verify**

Run: `shellcheck deploy/litestream/restore.sh`
Expected: clean.

Run: `docker compose -f deploy/compose/docker-compose.yml config`
Expected: renders without error. Same for the overlay pair.

- [ ] **Step 4: Commit**

```bash
git add -A deploy .env.example backoffice
git commit -m "feat(deploy): compose files per topology, cron restore script"
```

---

### Task 8: Release workflow

**Files:**
- Modify: `.github/workflows/release.yml`

- [ ] **Step 1: Add the image job** — `permissions: packages: write`, QEMU + buildx + GHCR login, two `build-push-action` steps (`target: runtime` → `analytics`, `target: evidence` → `analytics-evidence`), tags `vX.Y.Z` and `latest`, `cache-from/to: type=gha`, platforms per the Global Constraints.

- [ ] **Step 2: Validate the workflow**

Run: `python3 -c "import yaml,sys; yaml.safe_load(open('.github/workflows/release.yml'))"`
Expected: no error.

- [ ] **Step 3: Commit**

```bash
git add .github/workflows/release.yml
git commit -m "ci: publish analytics and analytics-evidence images to GHCR"
```

---

### Task 9: Documentation

**Files:**
- Create: `docs/litestream.md`
- Modify: `README.md`, `docs/deployment.md`

- [ ] **Step 1: Write `docs/litestream.md`** — bucket and two credentials (read-write writer, read-only reader), the `path:` key agreement, writer sidecar and systemd unit, reader cron restore, verification with `litestream snapshots`/`generations`, disaster recovery onto a fresh server.

- [ ] **Step 2: Rewrite the README quickstarts** — curl the compose file, edit `.env`/`projects.json`, `up -d`; a second section for a reader-only host; embedding and privacy sections unchanged.

- [ ] **Step 3: Update `docs/deployment.md`** — an Upgrade row for the compose path (`docker compose pull && docker compose up -d`, never `down -v`), the `VACUUM INTO` transient disk note, and a pointer to `docs/litestream.md` replacing the inline replication prose.

- [ ] **Step 4: Check** that no file outside `docs/superpowers/` still says "backoffice".

Run: `grep -ril backoffice --exclude-dir=.git --exclude-dir=node_modules --exclude-dir=superpowers .`
Expected: no output.

- [ ] **Step 5: Commit**

```bash
git add -A README.md docs
git commit -m "docs: litestream guide, compose-based install and upgrade"
```

---

### Task 10: Compose smoke test and final verification

**Files:**
- Create: `scripts/test-compose.sh`
- Modify: `Makefile` (`test-compose` target)

- [ ] **Step 1: Write the script** — build both images locally, `docker compose up -d` with a temporary project directory, POST a hit to `:8080` with a non-bot User-Agent (the smoke test learned this the hard way), poll `:3000` until it leaves `503`, assert the page renders, tear down.

- [ ] **Step 2: Run it**

Run: `make test-compose`
Expected: PASS.

- [ ] **Step 3: Full check**

Run: `make check && make build-all`
Expected: PASS, coverage gate green.

- [ ] **Step 4: Commit**

```bash
git add -A scripts Makefile
git commit -m "test: end-to-end compose smoke test"
```
