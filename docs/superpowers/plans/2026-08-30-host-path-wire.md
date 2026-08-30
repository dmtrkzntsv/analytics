# `$host`/`$path` Wire and Storage Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace `$url` on the `$pageview` wire with `$host` + `$path` + explicit `$utm_*`, and make host a stored, queryable dimension.

**Architecture:** The server stops parsing URLs. Five new reserved attribute keys map straight to typed fields on `resolved`, and `$pageview` requires `$path`. `web_hits` gains a `host` column, `agg_web_hosts` rolls it up daily, and `v_web_hosts` stitches aggregate history to a live half exactly as `v_web_pages` does. The read surfaces (MCP `web_breakdown`, Evidence) gain a `hosts` dimension.

**Tech Stack:** Go 1.26, SQLite (`modernc.org/sqlite`), MCP Go SDK, Evidence (SQL sources + markdown pages).

**Spec:** `docs/superpowers/specs/2026-08-30-host-path-split-design.md`

## Global Constraints

- **Migration number is `008`.** `006_attributes.sql` and `007_product_attrs_view.sql` already exist on main (spec §5.1).
- **Values are stored verbatim.** The server does no parsing, normalization or case folding of `$host`/`$path` (spec §3.1).
- **`$path` may contain `#` or `?`.** Hash routing and opt-in query routing both produce them; the server must not strip either (spec §3.1, §4.6, §4.7).
- **Rejection is per event, never per batch** — use `res.reject(i, ...)`, never an HTTP error (existing `ingest.go` contract).
- **Commit types:** `feat(server)!` for the wire change, `feat(store)` for the dimension, `feat(mcpserver)` for the breakdown. Subject in the imperative, lower case, no trailing period (`CLAUDE.md`).
- **`make check` must pass** before the final commit of each task. `test-restore.sh` needs the `sqlite3` CLI, which is absent on this machine — a pre-existing failure, unrelated to this work. Use `go vet ./... && go test ./...` as the gate and say so.

---

### Task 1: `host` column, table and view

**Files:**
- Create: `internal/store/sqlite/migrations/008_web_host.sql`
- Modify: `internal/store/store.go:13-20`
- Modify: `internal/store/sqlite/write.go:22-40`
- Modify: `internal/store/sqlite/registry.go:184-186`
- Modify: `internal/store/sqlite/prune.go:14-17`
- Test: `internal/store/sqlite/write_test.go`

**Interfaces:**
- Consumes: nothing.
- Produces: `store.WebHit.Host string`; tables `agg_web_hosts(project, day, host, visitors, pageviews)`; view `v_web_hosts(project, day, host, visitors, pageviews)`.

- [ ] **Step 1: Write the failing test**

Append to `internal/store/sqlite/write_test.go`:

```go
func TestWriteWebHitsStoresHost(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	hits := []store.WebHit{{
		ID: "h1", Project: "app", TS: ts("2026-08-10T10:00:00Z"),
		ActorID: "v1", Host: "shop.example.com", Path: "/pricing",
	}}
	if err := db.WriteWebHits(ctx, hits); err != nil {
		t.Fatal(err)
	}
	var host string
	if err := db.db.QueryRow(
		`SELECT host FROM web_hits WHERE id='h1'`).Scan(&host); err != nil {
		t.Fatal(err)
	}
	if host != "shop.example.com" {
		t.Errorf("host = %q, want %q", host, "shop.example.com")
	}
}

// A hit written without a host must read back as the empty string, not
// NULL: every consumer scans into a string and the column is NOT NULL.
func TestWriteWebHitsHostDefaultsEmpty(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.WriteWebHits(ctx, []store.WebHit{{
		ID: "h2", Project: "app", TS: ts("2026-08-10T10:00:00Z"),
		ActorID: "v1", Path: "/pricing",
	}}); err != nil {
		t.Fatal(err)
	}
	var host string
	if err := db.db.QueryRow(
		`SELECT host FROM web_hits WHERE id='h2'`).Scan(&host); err != nil {
		t.Fatal(err)
	}
	if host != "" {
		t.Errorf("host = %q, want empty", host)
	}
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test ./internal/store/sqlite/ -run TestWriteWebHitsStoresHost -v`
Expected: FAIL — `unknown field Host in struct literal`.

- [ ] **Step 3: Create the migration**

`internal/store/sqlite/migrations/008_web_host.sql`:

```sql
-- Host was computed from $url and discarded: it existed only to suppress
-- self-referrals. Storing it lets a project spanning a marketing site and
-- an app keep their /pricing rows apart.
--
-- Rows written before this migration carry host='' and cannot be
-- backfilled -- the value was never persisted. The empty bucket in
-- v_web_hosts is that history, not a bug.
ALTER TABLE web_hits ADD COLUMN host TEXT NOT NULL DEFAULT '';

CREATE TABLE agg_web_hosts (
    project TEXT NOT NULL, day TEXT NOT NULL, host TEXT NOT NULL,
    visitors INTEGER NOT NULL, pageviews INTEGER NOT NULL,
    PRIMARY KEY (project, day, host)
) WITHOUT ROWID;

-- Mirrors v_web_pages (004_app_views.sql): aggregated history stitched to
-- a live half over raw rows, so today and yesterday are included. Raw and
-- aggregated days are disjoint (aggregation deletes raw in the same
-- transaction), so the live half needs no day exclusion.
CREATE VIEW v_web_hosts AS
SELECT project, day, host, visitors, pageviews FROM agg_web_hosts
UNION ALL
SELECT project, substr(ts,1,10), host, COUNT(DISTINCT actor_id), COUNT(*)
FROM web_hits GROUP BY project, substr(ts,1,10), host;
```

- [ ] **Step 4: Add the struct field**

`internal/store/store.go`, in `type WebHit struct`, change the location line:

```go
	Path, ReferrerSource                string
```

to:

```go
	Host, Path, ReferrerSource          string
```

- [ ] **Step 5: Add the column to the insert**

`internal/store/sqlite/write.go` — add `host` to the column list, one more `?`, and `h.Host` to the args in the matching position:

```go
		stmt, err := tx.PrepareContext(ctx, `INSERT OR IGNORE INTO web_hits
			(id, project, ts, received_at, actor_id, user_id, group_id,
			 host, path, referrer_source, utm_source, utm_medium, utm_campaign,
			 country, device, browser, os)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`)
```

```go
			if _, err := stmt.ExecContext(ctx, h.ID, h.Project,
				h.TS.UTC().Format(tsFormat), h.ReceivedAt.UTC().Format(tsFormat),
				h.ActorID, h.UserID, h.GroupID,
				h.Host, h.Path, h.ReferrerSource, h.UTMSource, h.UTMMedium, h.UTMCampaign,
				h.Country, h.Device, h.Browser, h.OS); err != nil {
```

Count the `?` marks: 17. Miscounting here fails at runtime, not compile time.

- [ ] **Step 6: Register the new table**

`internal/store/sqlite/registry.go`, in `projectTables`, change:

```go
	"agg_web_daily", "agg_web_pages", "agg_web_referrers", "agg_web_countries",
```

to:

```go
	"agg_web_daily", "agg_web_pages", "agg_web_hosts", "agg_web_referrers", "agg_web_countries",
```

`TestProjectTablesMatchesSchema` cross-checks this list against the live schema in both directions, so omitting it fails loudly rather than silently orphaning rows on `DeleteProjectData`.

- [ ] **Step 7: Register the table for pruning**

`internal/store/sqlite/prune.go` keeps a **separate** list from `projectTables`. A table missing here never honours retention and grows without bound. In `webAggTables`:

```go
var webAggTables = []string{
	"agg_web_daily", "agg_web_pages", "agg_web_hosts", "agg_web_referrers", "agg_web_countries",
	"agg_web_devices", "agg_web_browsers", "agg_web_os", "agg_web_utm",
}
```

`TestPruneAggregatesCoversAllAggTables` fails if a migration adds an agg table missing from these lists, so this is caught — but catch it here rather than in CI.

- [ ] **Step 8: Run the tests to make sure they pass**

Run: `go test ./internal/store/... -v -run 'TestWriteWebHits|TestProjectTables|TestPruneAggregatesCovers'`
Expected: PASS, including `TestProjectTablesMatchesSchema` and `TestPruneAggregatesCoversAllAggTables`.

- [ ] **Step 9: Run the whole store suite**

Run: `go test ./internal/store/...`
Expected: PASS. If `TestViews*` fails, the view SQL in Step 3 has a typo — compare it column by column with `v_web_pages` in `migrations/004_app_views.sql:47-51`.

- [ ] **Step 10: Commit**

```bash
git add internal/store/
git commit -m "feat(store): store the pageview host as a queryable dimension"
```

---

### Task 2: roll `host` up daily

**Files:**
- Modify: `internal/store/sqlite/aggregate_web.go:93-102`
- Test: `internal/store/sqlite/aggregate_web_test.go`

**Interfaces:**
- Consumes: `store.WebHit.Host`, table `agg_web_hosts` (Task 1).
- Produces: `AggregateWebDay` writes `agg_web_hosts` rows.

- [ ] **Step 1: Write the failing test**

In `internal/store/sqlite/aggregate_web_test.go`, extend the fixture and add a test. First change `seedWebDay`'s `mk` helper to set a host, so every existing fixture row carries one:

```go
	mk := func(id, vis, path, tsS, source, country, device, browser, osN, us, um, uc string) store.WebHit {
		return store.WebHit{ID: id, Project: "app", TS: ts(tsS), ActorID: vis,
			Host: "shop.example.com", Path: path,
			ReferrerSource: source, Country: country, Device: device, Browser: browser, OS: osN,
			UTMSource: us, UTMMedium: um, UTMCampaign: uc}
	}
```

Then append:

```go
// Two hosts in one project must stay apart: the whole point of storing
// host is that a marketing site and an app do not collapse into one row.
func TestAggregateWebDayHosts(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedWebDay(t, db)
	// v2 visits a second host on the same day.
	if err := db.WriteWebHits(ctx, []store.WebHit{{
		ID: "5", Project: "app", TS: ts("2026-08-10T13:00:00Z"),
		ActorID: "v2", Host: "app.example.com", Path: "/dashboard",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateWebDay(ctx, "app", day("2026-08-10")); err != nil {
		t.Fatal(err)
	}
	rows, err := db.db.Query(`SELECT host, visitors, pageviews FROM agg_web_hosts
		WHERE project='app' AND day='2026-08-10' ORDER BY host`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	type row struct {
		host             string
		visitors, views  int
	}
	var got []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.host, &r.visitors, &r.views); err != nil {
			t.Fatal(err)
		}
		got = append(got, r)
	}
	want := []row{
		{"app.example.com", 1, 1},
		{"shop.example.com", 2, 4},
	}
	if len(got) != len(want) {
		t.Fatalf("got %d rows (%v), want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// Rows predating migration 008 have host='' and must still aggregate --
// dropping them would lose the pageview counts entirely, not just the host.
func TestAggregateWebDayEmptyHostBucket(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.WriteWebHits(ctx, []store.WebHit{{
		ID: "h0", Project: "app", TS: ts("2026-08-10T10:00:00Z"),
		ActorID: "v1", Path: "/legacy",
	}}); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateWebDay(ctx, "app", day("2026-08-10")); err != nil {
		t.Fatal(err)
	}
	var views int
	if err := db.db.QueryRow(`SELECT pageviews FROM agg_web_hosts
		WHERE project='app' AND day='2026-08-10' AND host=''`).Scan(&views); err != nil {
		t.Fatal(err)
	}
	if views != 1 {
		t.Errorf("empty-host pageviews = %d, want 1", views)
	}
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test ./internal/store/sqlite/ -run TestAggregateWebDayHosts -v`
Expected: FAIL — `no such table: agg_web_hosts` is impossible after Task 1, so expect `sql: no rows in result set` / `got 0 rows`.

- [ ] **Step 3: Add the dimension**

`internal/store/sqlite/aggregate_web.go`, in the `dims` slice, add one line after `agg_web_pages`:

```go
		dims := []dim{
			{"agg_web_pages", "path", "path", ""},
			{"agg_web_hosts", "host", "host", ""},
			{"agg_web_referrers", "source", "referrer_source", ""},
```

No `where` clause: unlike `agg_web_utm`, an empty host is a real bucket (pre-migration rows), not an absent dimension.

- [ ] **Step 4: Run the tests to make sure they pass**

Run: `go test ./internal/store/sqlite/ -run TestAggregateWebDay -v`
Expected: PASS, all four `TestAggregateWebDay*` tests.

- [ ] **Step 5: Commit**

```bash
git add internal/store/sqlite/
git commit -m "feat(store): roll pageview hosts up into agg_web_hosts"
```

---

### Task 3: the wire contract

**Files:**
- Modify: `internal/server/ingest.go:73-104`
- Modify: `internal/server/handlers.go:110-137`
- Modify: `internal/enrich/url.go:1-33`
- Modify: `internal/enrich/url_test.go`
- Test: `internal/server/ingest_test.go`, `internal/server/server_test.go`

**Interfaces:**
- Consumes: `store.WebHit.Host` (Task 1).
- Produces: reserved keys `$host`, `$path`, `$utm_source`, `$utm_medium`, `$utm_campaign`; `resolved.Host/Path/UTMSource/UTMMedium/UTMCampaign`. `resolved.URL` and `enrich.ParsePageURL`/`enrich.PageInfo` are **removed**.

- [ ] **Step 1: Write the failing tests**

Append to `internal/server/ingest_test.go`:

```go
func TestResolveAttributesLocationKeys(t *testing.T) {
	r, unknown := resolveAttributes(map[string]any{
		"$host":         "shop.example.com",
		"$path":         "/account/[id]/edit",
		"$utm_source":   "newsletter",
		"$utm_medium":   "email",
		"$utm_campaign": "spring",
		"$referrer":     "https://news.ycombinator.com/",
	})
	if len(unknown) != 0 {
		t.Errorf("unknown = %v, want none", unknown)
	}
	if r.Host != "shop.example.com" || r.Path != "/account/[id]/edit" {
		t.Errorf("host/path = %q/%q", r.Host, r.Path)
	}
	if r.UTMSource != "newsletter" || r.UTMMedium != "email" || r.UTMCampaign != "spring" {
		t.Errorf("utm = %q/%q/%q", r.UTMSource, r.UTMMedium, r.UTMCampaign)
	}
	if len(r.Custom) != 0 {
		t.Errorf("custom = %v, want empty", r.Custom)
	}
}

// $url is gone from the contract. It must warn as an unknown reserved key
// rather than being stored as a custom attribute, so a stale client sees
// the reason in the response body.
func TestResolveAttributesRejectsURL(t *testing.T) {
	r, unknown := resolveAttributes(map[string]any{"$url": "https://a.example.com/x"})
	if len(unknown) != 1 || unknown[0] != "$url" {
		t.Errorf("unknown = %v, want [$url]", unknown)
	}
	if len(r.Custom) != 0 {
		t.Errorf("custom = %v, want empty", r.Custom)
	}
}

// The server stores what it is told. A path carrying a hash route or an
// opt-in query string must survive untouched.
func TestResolveAttributesPathVerbatim(t *testing.T) {
	for _, path := range []string{
		"/app/#/account/[id]/edit",
		"/settings?tab=billing",
		"/plain",
	} {
		r, _ := resolveAttributes(map[string]any{"$path": path})
		if r.Path != path {
			t.Errorf("path %q stored as %q", path, r.Path)
		}
	}
}
```

- [ ] **Step 2: Run them to make sure they fail**

Run: `go test ./internal/server/ -run TestResolveAttributes -v`
Expected: FAIL — `r.Host undefined`.

- [ ] **Step 3: Change `resolved` and `reservedKeys`**

`internal/server/ingest.go`. Replace the location line of `resolved`:

```go
	URL, Referrer, Screen          string
```

with:

```go
	Host, Path, Referrer, Screen   string
	UTMSource, UTMMedium           string
	UTMCampaign                    string
```

Update the doc comment above `reservedKeys`, which currently describes the old parsing behaviour:

```go
// reservedKeys maps every system-defined attribute key to its destination.
// Location attributes are stored verbatim: the client owns normalization
// (masking, routing mode), so the server does no URL parsing at all.
// $screen populates its own column.
```

Replace the `"$url"` entry with the five location keys:

```go
	"$host":         func(r *resolved, v string) { r.Host = v },
	"$path":         func(r *resolved, v string) { r.Path = v },
	"$utm_source":   func(r *resolved, v string) { r.UTMSource = v },
	"$utm_medium":   func(r *resolved, v string) { r.UTMMedium = v },
	"$utm_campaign": func(r *resolved, v string) { r.UTMCampaign = v },
	"$referrer":     func(r *resolved, v string) { r.Referrer = v },
```

- [ ] **Step 4: Run the unit tests to verify they pass**

Run: `go test ./internal/server/ -run TestResolveAttributes -v`
Expected: PASS. The package will not build yet — `handlers.go` still references `rv.URL`. That is Step 6.

- [ ] **Step 5: Write the failing handler test**

Append to `internal/server/server_test.go` (follow the existing helpers in that file for building a request; `postEvents` is the idiom used by the tests already there — read one before writing this):

```go
// A pageview without $path is rejected per event, and a stale client
// sending $url gets both halves of the diagnosis in one response.
func TestPageviewRequiresPath(t *testing.T) {
	srv, h := testServer(t)
	_ = srv
	res := postEvents(t, h, `{"key":"ak_test","events":[
		{"id":"018f0000-0000-7000-8000-000000000001","ts":"2026-08-10T10:00:00Z",
		 "name":"$pageview","attributes":{"$url":"https://a.example.com/x"}}]}`)
	if res.Accepted != 0 || res.Rejected != 1 {
		t.Fatalf("accepted=%d rejected=%d, want 0/1", res.Accepted, res.Rejected)
	}
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Reason, "$path") {
		t.Errorf("errors = %+v, want one mentioning $path", res.Errors)
	}
	if len(res.Warnings) != 1 || !strings.Contains(res.Warnings[0].Reason, "$url") {
		t.Errorf("warnings = %+v, want one mentioning $url", res.Warnings)
	}
}

```

And append this one to `internal/enrich/url_test.go` — it exercises `CleanReferrer` directly, so it belongs in that package rather than in a handler test:

```go
// Referrer cleaning uses the supplied host. Same host => self-referral,
// suppressed. No host => nothing to compare, so take the referrer as given.
func TestCleanReferrerAgainstSuppliedHost(t *testing.T) {
	for _, tc := range []struct {
		name, host, referrer, want string
	}{
		{"self", "shop.example.com", "https://shop.example.com/a", ""},
		{"external", "shop.example.com", "https://news.ycombinator.com/", "hackernews"},
		{"no host", "", "https://shop.example.com/a", "shop.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := CleanReferrer(tc.referrer, tc.host)
			if got != tc.want {
				t.Errorf("CleanReferrer(%q, %q) = %q, want %q",
					tc.referrer, tc.host, got, tc.want)
			}
		})
	}
}
```

- [ ] **Step 6: Rewrite the pageview branch**

`internal/server/handlers.go`. Replace the whole `case namePageview:` block:

```go
		case namePageview:
			if rv.Path == "" {
				res.reject(i, "$pageview requires $path")
				continue
			}
			if botUA {
				// Accepted and silently ignored: the client did nothing
				// wrong, so it must not retry.
				res.Accepted++
				continue
			}
			// CleanReferrer with an empty host cannot detect a
			// self-referral, so the referrer is taken at face value.
			source := enrich.CleanReferrer(rv.Referrer, rv.Host)
			device, browser, osName := enrich.ParseUserAgent(ua)
			s.queue.EnqueueHit(store.WebHit{
				ID: id, Project: p.Alias, TS: ts, ReceivedAt: received,
				ActorID: actor, UserID: user, GroupID: group,
				Host: rv.Host, Path: rv.Path, ReferrerSource: source,
				UTMSource: rv.UTMSource, UTMMedium: rv.UTMMedium,
				UTMCampaign: rv.UTMCampaign,
				Country:     country, Device: device, Browser: browser, OS: osName,
			})
			res.Accepted++
```

Two removals inside this block are deliberate and must not be reinstated: the `enrich.ParsePageURL` call, and the `?ref=` fallback (`if source == "" && page.Ref != ""`). Both existed only because the server had the query string in hand; it no longer does.

- [ ] **Step 7: Delete the dead URL parser**

`internal/enrich/url.go` — delete `PageInfo` and `ParsePageURL` entirely, and the now-unused `fmt` and `net/url` imports if nothing else in the file uses them (`CleanReferrer` uses `net/url` and `strings`, so only `fmt` goes). Keep `CleanReferrer`, `referrerNames` and `referrerPrefixes` untouched.

`internal/enrich/url_test.go` — delete `TestParsePageURL`. Leave every `CleanReferrer` test.

- [ ] **Step 8: Run the whole suite**

Run: `go vet ./... && go test ./...`
Expected: PASS. Expect failures in `internal/server` fixtures that still send `$url` — update each to send `$host`/`$path`. Do not add a compatibility shim to make them pass.

- [ ] **Step 9: Commit**

```bash
git add internal/server/ internal/enrich/
git commit -m "$(cat <<'EOF'
feat(server)!: take $host and $path instead of parsing $url

A pageview now carries its location already split, so the collector stores
what it is told: no URL parsing, no normalization, no case folding. That is
what lets a client mask /account/8812/edit to /account/[id]/edit without the
raw path ever leaving the browser.

$url is gone from reservedKeys, so a stale client gets both halves of the
diagnosis in one response: a warning that $url was ignored and a rejection
naming $path. The ?ref= referrer fallback goes with it -- it existed only
because the server already had the query string in hand.

BREAKING CHANGE: $pageview requires $path and no longer accepts $url. UTM
parameters travel as $utm_source, $utm_medium and $utm_campaign. The ?ref=
query-parameter referrer fallback is removed; set $referrer instead.
EOF
)"
```

---

### Task 4: the `hosts` breakdown

**Files:**
- Modify: `internal/mcpserver/tools_read.go:158-176`
- Modify: `internal/mcpserver/resources.go:29-30`
- Test: `internal/mcpserver/tools_read_test.go`

**Interfaces:**
- Consumes: view `v_web_hosts` (Task 1).
- Produces: `web_breakdown` accepts `dimension: "hosts"`.

- [ ] **Step 1: Write the failing test**

Append to `internal/mcpserver/tools_read_test.go` (read a neighbouring `web_breakdown` test first — it shows how the seeded fixture and host are constructed):

```go
func TestWebBreakdownHosts(t *testing.T) {
	h := newTestHost(t)
	_, out, err := h.webBreakdown(context.Background(), nil, breakdownIn{
		rangeIn:   rangeIn{Project: "app", From: "2026-08-10", To: "2026-08-10"},
		Dimension: "hosts",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Rows) == 0 {
		t.Fatal("no rows for the hosts dimension")
	}
}
```

- [ ] **Step 2: Run it to make sure it fails**

Run: `go test ./internal/mcpserver/ -run TestWebBreakdownHosts -v`
Expected: FAIL — `unknown dimension "hosts"; valid: browsers, countries, devices, os, pages, referrers, utm`.

- [ ] **Step 3: Add the dimension**

`internal/mcpserver/tools_read.go`, in `webDimensions`:

```go
	"pages":     {"v_web_pages", "path"},
	"hosts":     {"v_web_hosts", "host"},
	"referrers": {"v_web_referrers", "source"},
```

- [ ] **Step 4: Update the schema tag and the tool description**

The comment above `webDimensions` claims the enum is generated from the map, but `breakdownIn`'s struct tag is a hand-written string. Both must be edited, or the tool advertises a dimension list that does not match its behaviour:

```go
	Dimension string `json:"dimension" jsonschema:"one of: pages, hosts, referrers, countries, devices, browsers, os, utm"`
```

And at `tools_read.go:269`:

```go
		Description: "Top pages, hosts, referrers, countries, devices, browsers, os or utm for one project over a date range."},
```

- [ ] **Step 5: Add the view to the query schema**

`internal/mcpserver/resources.go`, in `schemaViews`, after the `v_web_pages` line:

```
  v_web_hosts(project, day, host, visitors, pageviews)
```

- [ ] **Step 6: Run the tests to make sure they pass**

Run: `go test ./internal/mcpserver/`
Expected: PASS. If a coverage or seed test fails on the dimension count, update its expectation — the new dimension is intended.

- [ ] **Step 7: Commit**

```bash
git add internal/mcpserver/
git commit -m "feat(mcpserver): break web traffic down by host"
```

---

### Task 5: the Evidence hosts tile

**Files:**
- Create: `evidence/sources/twillingate/v_web_hosts.sql`
- Modify: `evidence/pages/web/[project].md`

**Interfaces:**
- Consumes: view `v_web_hosts` (Task 1).
- Produces: an Evidence source named `v_web_hosts` and a tile on the web page.

- [ ] **Step 1: Create the source**

`evidence/sources/twillingate/v_web_hosts.sql`. The empty-database guard is not optional — a zero-row result throws in the sqlite connector and takes down every other query on the page:

```sql
-- Empty-database guard: the sqlite connector infers column types from the
-- first row, so a zero-row result throws "Cannot convert undefined or null to
-- object" and fails the whole source build -- taking every other query on the
-- page down with it. A fresh install has no traffic yet, so emit a sentinel
-- row when the view is empty; pages filter it out via their project clause.
select project, day, host, visitors, pageviews
from v_web_hosts
union all
select '', '1970-01-01', '', 0, 0
where not exists (select 1 from v_web_hosts)
```

- [ ] **Step 2: Add the tile**

`evidence/pages/web/[project].md` — read the existing pages tile at line 49 and mirror its structure exactly (the `where` clause with the project param and the date range is the part that must match). Place the hosts tile immediately after it.

- [ ] **Step 3: Verify the dashboards build**

Run: `go test ./internal/dashboards/`
Expected: PASS. This is the guard that catches a malformed source or page.

- [ ] **Step 4: Commit**

```bash
git add evidence/
git commit -m "feat(dashboards): show a hosts breakdown on the web page"
```

---

### Task 6: full gate

- [ ] **Step 1: Run the full check**

Run: `go vet ./... && go test ./...`
Expected: PASS.

- [ ] **Step 2: Run make check and record the known gap**

Run: `make check`
Expected: vet and coverage pass; `test-restore.sh` fails with `sqlite3 is required`. That is a pre-existing environment gap on this machine, not a regression — confirm with `command -v sqlite3` returning nothing, and say so plainly rather than claiming a clean run.

- [ ] **Step 3: Confirm no stragglers**

Run: `grep -rn '\$url\|ParsePageURL\|PageInfo' --include=*.go internal/ cmd/`
Expected: no matches outside comments describing the removal.
