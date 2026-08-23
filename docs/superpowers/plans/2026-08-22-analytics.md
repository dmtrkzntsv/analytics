# Ultra-Lite Analytics Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the complete self-hosted analytics system (cookieless web + product analytics, multi-project, SQLite + Litestream/R2, Evidence dashboards) defined in the spec.

**Architecture:** One Go binary (`analytics`) with subcommands `serve` / `sync` / `migrate` / `version`; DSN-selected storage behind a thin `Store` interface (SQLite now); buffered-channel batch ingestion; daily aggregation with tiered retention; deployment via hardened systemd (install.sh) or docker compose; Evidence.dev preconfigured dashboards.

**Tech Stack:** Go 1.23+, `modernc.org/sqlite`, `github.com/google/uuid`, `github.com/oschwald/maxminddb-golang/v2` (spec §6 contingency, exercised), Litestream, Evidence.dev, Docker.

**Spec:** `docs/superpowers/specs/2026-08-22-analytics-design.md` — read it first; every task below argues from it.

## Global Constraints

- Go module: `github.com/dmitry/analytics`. No CGO anywhere (`CGO_ENABLED=0`).
- Dependencies limited to: `modernc.org/sqlite`, `github.com/google/uuid`, `github.com/oschwald/maxminddb-golang/v2`. Everything else stdlib.
- Coverage gate: `make check` fails below **80% total**; core packages (`internal/store/...`, `internal/enrich`, `internal/pipeline`, `internal/identity`, `internal/config`) each ≥ **85%**. `cmd/` excluded.
- All tests run with `-race`. TDD: test first, watch it fail, implement, watch it pass, commit.
- All timestamps UTC, ISO-8601 `2006-01-02T15:04:05Z`; days are `YYYY-MM-DD` strings in SQL, `civil.Date` in Go.
- IDs are UUIDv7 (`uuid.NewV7()`).
- SQLite: single writer connection (`SetMaxOpenConns(1)`), WAL, `synchronous=NORMAL`, `cache_size=-16000` default, never full `VACUUM`.
- IP addresses and full User-Agents must never be stored or logged.
- slog structured logging; no per-request log lines on the hot path.
- Commit after every green test cycle with a conventional message; end commit messages with the Claude co-author trailer.

---

### Task 1: Repo scaffold, Makefile, coverage gate, CI

**Files:**
- Create: `go.mod`, `.gitignore`, `Makefile`, `scripts/coverage.sh`, `.github/workflows/ci.yml`, `cmd/analytics/main.go`, `cmd/analytics/main_test.go`

**Interfaces:**
- Produces: `main()` dispatching subcommands via `run(args []string, stdout io.Writer) int`; `version` subcommand prints `analytics <version>` where `version` is a package-level `var version = "dev"` (set by `-ldflags`). Later tasks register subcommands in the `commands` map: `var commands = map[string]func(args []string, stdout io.Writer) int{}`.

- [ ] **Step 1: Init module and failing test**

```bash
go mod init github.com/dmitry/analytics
```

`.gitignore`:
```
/analytics
coverage.out
*.db
*.db-wal
*.db-shm
node_modules/
```

`cmd/analytics/main_test.go`:
```go
package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestVersionSubcommand(t *testing.T) {
	var out bytes.Buffer
	code := run([]string{"version"}, &out)
	if code != 0 {
		t.Fatalf("exit code = %d, want 0", code)
	}
	if !strings.HasPrefix(out.String(), "analytics ") {
		t.Fatalf("output = %q, want prefix 'analytics '", out.String())
	}
}

func TestUnknownSubcommand(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"bogus"}, &out); code == 0 {
		t.Fatal("unknown subcommand must return non-zero")
	}
}

func TestNoSubcommandPrintsUsage(t *testing.T) {
	var out bytes.Buffer
	if code := run(nil, &out); code == 0 {
		t.Fatal("missing subcommand must return non-zero")
	}
	if !strings.Contains(out.String(), "usage") {
		t.Fatalf("output %q must contain usage", out.String())
	}
}
```

- [ ] **Step 2: Run test to verify it fails**

Run: `go test ./cmd/... -run Test -v` — Expected: FAIL (run undefined).

- [ ] **Step 3: Implement main dispatch**

`cmd/analytics/main.go`:
```go
// Command analytics is the single binary for the ultra-lite analytics
// system: `serve` (ingestion server), `sync` (backoffice replica loop),
// `migrate`, and `version`.
package main

import (
	"fmt"
	"io"
	"os"
)

var version = "dev" // overridden via -ldflags "-X main.version=..."

var commands = map[string]func(args []string, stdout io.Writer) int{}

func init() {
	commands["version"] = func(_ []string, stdout io.Writer) int {
		fmt.Fprintf(stdout, "analytics %s\n", version)
		return 0
	}
}

func run(args []string, stdout io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stdout, "usage: analytics <serve|sync|migrate|version> [flags]")
		return 2
	}
	cmd, ok := commands[args[0]]
	if !ok {
		fmt.Fprintf(stdout, "unknown command %q\nusage: analytics <serve|sync|migrate|version> [flags]\n", args[0])
		return 2
	}
	return cmd(args[1:], stdout)
}

func main() {
	os.Exit(run(os.Args[1:], os.Stdout))
}
```

- [ ] **Step 4: Run test to verify it passes**

Run: `go test ./cmd/... -v` — Expected: PASS.

- [ ] **Step 5: Add Makefile, coverage script, CI**

`Makefile`:
```make
BIN := analytics
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: build test check vet build-all docker

build:
	CGO_ENABLED=0 go build -trimpath -ldflags '$(LDFLAGS)' -o $(BIN) ./cmd/analytics

test:
	go test -race ./...

vet:
	go vet ./...

check: vet
	./scripts/coverage.sh

build-all:
	for target in linux/amd64 linux/arm64 linux/arm; do \
		GOOS=$${target%/*} GOARCH=$${target#*/} CGO_ENABLED=0 \
		go build -trimpath -ldflags '$(LDFLAGS)' -o dist/$(BIN)-$${target%/*}-$${target#*/} ./cmd/analytics || exit 1; \
	done

docker:
	docker build -t analytics:$(VERSION) .
```

`scripts/coverage.sh` (make executable):
```bash
#!/usr/bin/env bash
# Coverage gate per spec §14: total >= 80%, core packages >= 85%.
set -euo pipefail

go test -race -coverprofile=coverage.out $(go list ./... | grep -v '/cmd/')

total=$(go tool cover -func=coverage.out | awk '/^total:/ {gsub(/%/,"",$3); print $3}')
echo "total coverage: ${total}%"
awk -v t="$total" 'BEGIN { exit (t+0 >= 80.0) ? 0 : 1 }' \
  || { echo "FAIL: total coverage ${total}% < 80%"; exit 1; }

core="internal/store internal/enrich internal/pipeline internal/identity internal/config"
fail=0
for pkg in $core; do
  pct=$(go tool cover -func=coverage.out \
    | awk -v p="$pkg/" 'index($1, p) {gsub(/%/,"",$3); s+=$3; n++} END {if (n) printf "%.1f", s/n; else print "-"}')
  [ "$pct" = "-" ] && continue  # package not written yet
  echo "  $pkg: ${pct}%"
  awk -v t="$pct" 'BEGIN { exit (t+0 >= 85.0) ? 0 : 1 }' \
    || { echo "FAIL: $pkg coverage ${pct}% < 85%"; fail=1; }
done
exit $fail
```

`.github/workflows/ci.yml`:
```yaml
name: ci
on:
  push: { branches: [main] }
  pull_request:
jobs:
  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with: { go-version: "1.23" }
      - run: make check
      - run: make build
```

- [ ] **Step 6: Verify and commit**

Run: `make build && make test && ./analytics version` — Expected: builds, tests pass, prints version.

```bash
git add -A && git commit -m "feat: repo scaffold with subcommand dispatch, coverage gate, CI"
```

---

### Task 2: `internal/civil` — Date type

**Files:**
- Create: `internal/civil/date.go`, `internal/civil/date_test.go`

**Interfaces:**
- Produces:
  - `type Date struct { Year int; Month time.Month; Day int }`
  - `func DateOf(t time.Time) Date` — UTC calendar date of t
  - `func Parse(s string) (Date, error)` — from `YYYY-MM-DD`
  - `func (d Date) String() string` — `YYYY-MM-DD`
  - `func (d Date) AddDays(n int) Date`
  - `func (d Date) Before(o Date) bool`
  - `func (d Date) Time() time.Time` — midnight UTC
  - `func Today(now time.Time) Date` — alias of DateOf, reads at call sites

- [ ] **Step 1: Write failing tests**

`internal/civil/date_test.go`:
```go
package civil

import (
	"testing"
	"time"
)

func TestDateOfUsesUTC(t *testing.T) {
	// 23:30 in UTC-5 is 04:30 next day UTC.
	loc := time.FixedZone("m5", -5*3600)
	d := DateOf(time.Date(2026, 8, 22, 23, 30, 0, 0, loc))
	if d.String() != "2026-08-23" {
		t.Fatalf("got %s, want 2026-08-23", d)
	}
}

func TestParseRoundTrip(t *testing.T) {
	d, err := Parse("2026-02-28")
	if err != nil {
		t.Fatal(err)
	}
	if d.String() != "2026-02-28" {
		t.Fatalf("round trip got %s", d)
	}
	if _, err := Parse("2026-13-01"); err == nil {
		t.Fatal("invalid month must error")
	}
	if _, err := Parse("garbage"); err == nil {
		t.Fatal("garbage must error")
	}
}

func TestAddDaysCrossesMonth(t *testing.T) {
	d, _ := Parse("2026-01-30")
	if got := d.AddDays(3).String(); got != "2026-02-02" {
		t.Fatalf("got %s, want 2026-02-02", got)
	}
	if got := d.AddDays(-30).String(); got != "2025-12-31" {
		t.Fatalf("got %s, want 2025-12-31", got)
	}
}

func TestBefore(t *testing.T) {
	a, _ := Parse("2026-08-01")
	b, _ := Parse("2026-08-02")
	if !a.Before(b) || b.Before(a) || a.Before(a) {
		t.Fatal("Before ordering wrong")
	}
}

func TestTimeIsMidnightUTC(t *testing.T) {
	d, _ := Parse("2026-08-22")
	want := time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)
	if !d.Time().Equal(want) {
		t.Fatalf("got %v", d.Time())
	}
}
```

- [ ] **Step 2: Run to verify failure** — `go test ./internal/civil/` fails to build.

- [ ] **Step 3: Implement**

`internal/civil/date.go`:
```go
// Package civil provides a timezone-free calendar date. All analytics
// bucketing happens on UTC calendar days (spec §8, §9).
package civil

import (
	"fmt"
	"time"
)

type Date struct {
	Year  int
	Month time.Month
	Day   int
}

func DateOf(t time.Time) Date {
	u := t.UTC()
	return Date{u.Year(), u.Month(), u.Day()}
}

func Today(now time.Time) Date { return DateOf(now) }

func Parse(s string) (Date, error) {
	t, err := time.Parse("2006-01-02", s)
	if err != nil {
		return Date{}, fmt.Errorf("civil: parse %q: %w", s, err)
	}
	return DateOf(t), nil
}

func (d Date) String() string {
	return fmt.Sprintf("%04d-%02d-%02d", d.Year, d.Month, d.Day)
}

func (d Date) Time() time.Time {
	return time.Date(d.Year, d.Month, d.Day, 0, 0, 0, 0, time.UTC)
}

func (d Date) AddDays(n int) Date { return DateOf(d.Time().AddDate(0, 0, n)) }

func (d Date) Before(o Date) bool { return d.Time().Before(o.Time()) }
```

- [ ] **Step 4: Run tests** — `go test -race ./internal/civil/ -v` — PASS.

- [ ] **Step 5: Commit** — `git add -A && git commit -m "feat: civil.Date UTC calendar type"`

---

### Task 3: `internal/config` — JSON config, validation, DSNs, retention merge

**Files:**
- Create: `internal/config/config.go`, `internal/config/config_test.go`

**Interfaces:**
- Produces:
  - `type Duration struct{ time.Duration }` with JSON unmarshal from `"5s"` strings
  - `type Config struct { Listen string; Database string; Geo string; Log LogConfig; Buffer BufferConfig; Retention Retention; Sync SyncConfig; Projects []Project }`
  - `type LogConfig struct { Level string; Format string; File string }`
  - `type BufferConfig struct { FlushMaxEvents int; FlushInterval Duration; Capacity int }`
  - `type Retention struct { Web RetentionClass; Product RetentionClass }`
  - `type RetentionClass struct { RawDays int; AggregateDays int }`
  - `type SyncConfig struct { Interval Duration; LitestreamConfig string; ReplicaPath string }`
  - `type Project struct { ID string; Name string; AllowedOrigins []string; Retention *RetentionOverride; ProductAggregation *ProductAggregation }`
  - `type RetentionOverride struct { Web *RetentionClassOverride; Product *RetentionClassOverride }`, `type RetentionClassOverride struct { RawDays *int; AggregateDays *int }`
  - `type ProductAggregation struct { Enabled bool; Attributes map[string][]string; TopN int }` (TopN default 50 applied in Load)
  - `func Load(path string) (*Config, error)` — reads file, applies defaults, validates
  - `func Parse(r io.Reader) (*Config, error)` — same from a reader (tests use this)
  - `func (c *Config) Project(id string) *Project` — nil if unknown
  - `func (c *Config) RetentionFor(projectID string) Retention` — global merged with project override
  - JSON field names are snake_case per the spec §4 example (`flush_max_events`, `allowed_origins`, `product_aggregation`, ...)

Defaults (applied when fields are zero): Listen `127.0.0.1:8080`, Geo `cloudflare://`, Buffer `{1000, 5s, 10000}`, Retention web `{7, 365}` product `{30, 365}`, Sync interval `5m`, ProductAggregation.TopN `50`, Log `{info, json}`.

Validation errors (returned, not warned): missing/empty `database`; no projects; duplicate project IDs; project without ID; empty `allowed_origins` entry; unparsable DSN scheme in `database`/`geo`; negative retention days; `raw_days` = 0.

- [ ] **Step 1: Write failing tests**

`internal/config/config_test.go`:
```go
package config

import (
	"strings"
	"testing"
	"time"
)

const minimal = `{
  "database": "sqlite:///tmp/a.db",
  "projects": [{"id": "app", "name": "App", "allowed_origins": ["https://app.com"]}]
}`

func TestDefaultsApplied(t *testing.T) {
	c, err := Parse(strings.NewReader(minimal))
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:8080" {
		t.Errorf("Listen = %q", c.Listen)
	}
	if c.Geo != "cloudflare://" {
		t.Errorf("Geo = %q", c.Geo)
	}
	if c.Buffer.FlushMaxEvents != 1000 || c.Buffer.FlushInterval.Duration != 5*time.Second || c.Buffer.Capacity != 10000 {
		t.Errorf("Buffer = %+v", c.Buffer)
	}
	if c.Retention.Web.RawDays != 7 || c.Retention.Product.RawDays != 30 ||
		c.Retention.Web.AggregateDays != 365 || c.Retention.Product.AggregateDays != 365 {
		t.Errorf("Retention = %+v", c.Retention)
	}
	if c.Sync.Interval.Duration != 5*time.Minute {
		t.Errorf("Sync = %+v", c.Sync)
	}
}

func TestRetentionOverrideMerge(t *testing.T) {
	c, err := Parse(strings.NewReader(`{
	  "database": "sqlite:///tmp/a.db",
	  "projects": [{
	    "id": "app", "name": "App", "allowed_origins": ["https://app.com"],
	    "retention": {"product": {"raw_days": 60}}
	  }]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	r := c.RetentionFor("app")
	if r.Product.RawDays != 60 {
		t.Errorf("override lost: %+v", r)
	}
	if r.Product.AggregateDays != 365 || r.Web.RawDays != 7 {
		t.Errorf("non-overridden fields must inherit: %+v", r)
	}
	if u := c.RetentionFor("unknown"); u.Web.RawDays != 7 {
		t.Errorf("unknown project must get defaults: %+v", u)
	}
}

func TestProductAggregationDefaults(t *testing.T) {
	c, err := Parse(strings.NewReader(`{
	  "database": "sqlite:///tmp/a.db",
	  "projects": [
	    {"id": "a", "name": "A", "allowed_origins": ["https://a.com"]},
	    {"id": "b", "name": "B", "allowed_origins": ["https://b.com"],
	     "product_aggregation": {"enabled": true, "attributes": {"subscribed": ["plan"]}}}
	  ]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if c.Project("a").ProductAggregation != nil {
		t.Error("absent block must stay nil (aggregation off by default, spec §4)")
	}
	pa := c.Project("b").ProductAggregation
	if pa == nil || !pa.Enabled || pa.TopN != 50 {
		t.Errorf("TopN default not applied: %+v", pa)
	}
}

func TestValidationErrors(t *testing.T) {
	cases := map[string]string{
		"no database":      `{"projects":[{"id":"a","name":"A","allowed_origins":["https://a.com"]}]}`,
		"no projects":      `{"database":"sqlite:///tmp/a.db"}`,
		"dup project":      `{"database":"sqlite:///tmp/a.db","projects":[{"id":"a","name":"A","allowed_origins":["https://a.com"]},{"id":"a","name":"A2","allowed_origins":["https://b.com"]}]}`,
		"bad geo scheme":   `{"database":"sqlite:///tmp/a.db","geo":"???","projects":[{"id":"a","name":"A","allowed_origins":["https://a.com"]}]}`,
		"zero raw_days":    `{"database":"sqlite:///tmp/a.db","retention":{"web":{"raw_days":0,"aggregate_days":365}},"projects":[{"id":"a","name":"A","allowed_origins":["https://a.com"]}]}`,
		"empty origin":     `{"database":"sqlite:///tmp/a.db","projects":[{"id":"a","name":"A","allowed_origins":[""]}]}`,
		"invalid duration": `{"database":"sqlite:///tmp/a.db","buffer":{"flush_interval":"fast"},"projects":[{"id":"a","name":"A","allowed_origins":["https://a.com"]}]}`,
	}
	for name, in := range cases {
		if _, err := Parse(strings.NewReader(in)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}
```

- [ ] **Step 2: Run to verify failure** — build error, Parse undefined.

- [ ] **Step 3: Implement**

`internal/config/config.go`:
```go
// Package config loads and validates /etc/analytics/config.json (spec §4).
// stdlib only: encoding/json + net/url for DSN scheme checks.
package config

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"time"
)

type Duration struct{ time.Duration }

func (d *Duration) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	v, err := time.ParseDuration(s)
	if err != nil {
		return fmt.Errorf("config: invalid duration %q: %w", s, err)
	}
	d.Duration = v
	return nil
}

type LogConfig struct {
	Level  string `json:"level"`
	Format string `json:"format"`
	File   string `json:"file"`
}

type BufferConfig struct {
	FlushMaxEvents int      `json:"flush_max_events"`
	FlushInterval  Duration `json:"flush_interval"`
	Capacity       int      `json:"capacity"`
}

type RetentionClass struct {
	RawDays       int `json:"raw_days"`
	AggregateDays int `json:"aggregate_days"`
}

type Retention struct {
	Web     RetentionClass `json:"web"`
	Product RetentionClass `json:"product"`
}

type RetentionClassOverride struct {
	RawDays       *int `json:"raw_days"`
	AggregateDays *int `json:"aggregate_days"`
}

type RetentionOverride struct {
	Web     *RetentionClassOverride `json:"web"`
	Product *RetentionClassOverride `json:"product"`
}

type ProductAggregation struct {
	Enabled    bool                `json:"enabled"`
	Attributes map[string][]string `json:"attributes"`
	TopN       int                 `json:"top_n"`
}

type Project struct {
	ID                 string              `json:"id"`
	Name               string              `json:"name"`
	AllowedOrigins     []string            `json:"allowed_origins"`
	Retention          *RetentionOverride  `json:"retention"`
	ProductAggregation *ProductAggregation `json:"product_aggregation"`
}

type SyncConfig struct {
	Interval         Duration `json:"interval"`
	LitestreamConfig string   `json:"litestream_config"`
	ReplicaPath      string   `json:"replica_path"`
}

type Config struct {
	Listen    string       `json:"listen"`
	Database  string       `json:"database"`
	Geo       string       `json:"geo"`
	Log       LogConfig    `json:"log"`
	Buffer    BufferConfig `json:"buffer"`
	Retention Retention    `json:"retention"`
	Sync      SyncConfig   `json:"sync"`
	Projects  []Project    `json:"projects"`
}

func Load(path string) (*Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	defer f.Close()
	return Parse(f)
}

func Parse(r io.Reader) (*Config, error) {
	c := &Config{}
	dec := json.NewDecoder(r)
	if err := dec.Decode(c); err != nil {
		return nil, fmt.Errorf("config: %w", err)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

func (c *Config) applyDefaults() {
	if c.Listen == "" {
		c.Listen = "127.0.0.1:8080"
	}
	if c.Geo == "" {
		c.Geo = "cloudflare://"
	}
	if c.Log.Level == "" {
		c.Log.Level = "info"
	}
	if c.Log.Format == "" {
		c.Log.Format = "json"
	}
	if c.Buffer.FlushMaxEvents == 0 {
		c.Buffer.FlushMaxEvents = 1000
	}
	if c.Buffer.FlushInterval.Duration == 0 {
		c.Buffer.FlushInterval.Duration = 5 * time.Second
	}
	if c.Buffer.Capacity == 0 {
		c.Buffer.Capacity = 10000
	}
	if c.Retention.Web == (RetentionClass{}) {
		c.Retention.Web = RetentionClass{RawDays: 7, AggregateDays: 365}
	}
	if c.Retention.Product == (RetentionClass{}) {
		c.Retention.Product = RetentionClass{RawDays: 30, AggregateDays: 365}
	}
	if c.Sync.Interval.Duration == 0 {
		c.Sync.Interval.Duration = 5 * time.Minute
	}
	for i := range c.Projects {
		if pa := c.Projects[i].ProductAggregation; pa != nil && pa.TopN == 0 {
			pa.TopN = 50
		}
	}
}

func (c *Config) validate() error {
	if c.Database == "" {
		return fmt.Errorf("config: database DSN is required")
	}
	for _, dsn := range []string{c.Database, c.Geo} {
		u, err := url.Parse(dsn)
		if err != nil || u.Scheme == "" {
			return fmt.Errorf("config: invalid DSN %q", dsn)
		}
	}
	if len(c.Projects) == 0 {
		return fmt.Errorf("config: at least one project is required")
	}
	seen := map[string]bool{}
	for _, p := range c.Projects {
		if p.ID == "" {
			return fmt.Errorf("config: project with empty id")
		}
		if seen[p.ID] {
			return fmt.Errorf("config: duplicate project id %q", p.ID)
		}
		seen[p.ID] = true
		for _, o := range p.AllowedOrigins {
			if o == "" {
				return fmt.Errorf("config: project %q has an empty allowed_origin", p.ID)
			}
		}
	}
	for _, rc := range []RetentionClass{c.Retention.Web, c.Retention.Product} {
		if rc.RawDays <= 0 || rc.AggregateDays < 0 {
			return fmt.Errorf("config: retention days out of range: %+v", rc)
		}
	}
	return nil
}

func (c *Config) Project(id string) *Project {
	for i := range c.Projects {
		if c.Projects[i].ID == id {
			return &c.Projects[i]
		}
	}
	return nil
}

func (c *Config) RetentionFor(projectID string) Retention {
	r := c.Retention
	p := c.Project(projectID)
	if p == nil || p.Retention == nil {
		return r
	}
	apply := func(dst *RetentionClass, o *RetentionClassOverride) {
		if o == nil {
			return
		}
		if o.RawDays != nil {
			dst.RawDays = *o.RawDays
		}
		if o.AggregateDays != nil {
			dst.AggregateDays = *o.AggregateDays
		}
	}
	apply(&r.Web, p.Retention.Web)
	apply(&r.Product, p.Retention.Product)
	return r
}
```

- [ ] **Step 4: Run tests** — `go test -race ./internal/config/ -v` — PASS. Also test `Load` with a temp file in one extra test if coverage of Load is 0 (write the minimal JSON to `t.TempDir()`).

- [ ] **Step 5: Commit** — `git add -A && git commit -m "feat: JSON config with defaults, validation, retention merge"`

---

### Task 4: `internal/store` — models, Store interface, DSN registry

**Files:**
- Create: `internal/store/store.go`, `internal/store/store_test.go`

**Interfaces:**
- Consumes: `civil.Date`, `config.ProductAggregation`.
- Produces (later tasks implement/use exactly these):

```go
type WebHit struct {
	ID, ProjectID                       string
	TS                                  time.Time
	VisitorHash, Path, ReferrerSource   string
	UTMSource, UTMMedium, UTMCampaign   string
	Country, Device, Browser, OS        string
}

type ProductEvent struct {
	ID, ProjectID, EventName, UserID string
	TS                               time.Time
	Attributes                       map[string]string
}

type ProjectInfo struct{ ID, Name string }

// ProductAggSettings mirrors config.ProductAggregation; zero value =
// aggregation disabled (raw rows deleted without rollup, spec §9).
type ProductAggSettings struct {
	Enabled    bool
	Attributes map[string][]string // event name (or "*") -> attr keys
	TopN       int
}

type Store interface {
	Migrate(ctx context.Context) error
	SyncProjects(ctx context.Context, ps []ProjectInfo) error
	WriteWebHits(ctx context.Context, hits []WebHit) error
	WriteProductEvents(ctx context.Context, evs []ProductEvent) error
	WebDaysBefore(ctx context.Context, projectID string, before civil.Date) ([]civil.Date, error)
	ProductDaysBefore(ctx context.Context, projectID string, before civil.Date) ([]civil.Date, error)
	AggregateWebDay(ctx context.Context, projectID string, day civil.Date) error
	AggregateProductDay(ctx context.Context, projectID string, day civil.Date, agg ProductAggSettings) error
	PruneAggregates(ctx context.Context, projectID string, webBefore, productBefore civil.Date) error
	IncrementalVacuum(ctx context.Context) error
	ProjectIDs(ctx context.Context) ([]string, error) // all rows incl. archived
	KnownAttributeKeys(ctx context.Context) ([]string, error)
	RebuildFlatView(ctx context.Context, keys []string) error
	GetMeta(ctx context.Context, key string) (string, error) // "" if absent
	SetMeta(ctx context.Context, key, value string) error
	Close() error
}

// Open selects a backend by DSN scheme. sqlite:// is registered by the
// sqlite package's init via Register.
func Open(dsn string) (Store, error)
func Register(scheme string, fn func(dsn string) (Store, error))
```

- [ ] **Step 1: Write failing tests** — registry behavior only (interface has no logic):

```go
package store

import "testing"

func TestOpenUnknownScheme(t *testing.T) {
	if _, err := Open("bogus:///x"); err == nil {
		t.Fatal("unknown scheme must error")
	}
}

func TestRegisterAndOpen(t *testing.T) {
	called := ""
	Register("fake", func(dsn string) (Store, error) { called = dsn; return nil, nil })
	if _, err := Open("fake:///db"); err != nil {
		t.Fatal(err)
	}
	if called != "fake:///db" {
		t.Fatalf("factory got %q", called)
	}
}

func TestOpenInvalidDSN(t *testing.T) {
	if _, err := Open("://"); err == nil {
		t.Fatal("invalid DSN must error")
	}
}
```

- [ ] **Step 2: Run — fails.** **Step 3: Implement** models + interface above plus:

```go
var registry = map[string]func(string) (Store, error){}

func Register(scheme string, fn func(string) (Store, error)) { registry[scheme] = fn }

func Open(dsn string) (Store, error) {
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme == "" {
		return nil, fmt.Errorf("store: invalid DSN %q", dsn)
	}
	fn, ok := registry[u.Scheme]
	if !ok {
		return nil, fmt.Errorf("store: unknown backend %q (supported: sqlite)", u.Scheme)
	}
	return fn(dsn)
}
```

- [ ] **Step 4: Run tests — PASS.** **Step 5: Commit** — `git commit -m "feat: store interface, models, DSN-scheme backend registry"`

---

### Task 5: `internal/store/sqlite` — open, pragmas, migration runner, schema 001

**Files:**
- Create: `internal/store/sqlite/sqlite.go`, `internal/store/sqlite/migrate.go`, `internal/store/sqlite/migrations/001_init.sql`, `internal/store/sqlite/sqlite_test.go`

**Interfaces:**
- Consumes: `store.Register`, `store.Store`.
- Produces: `type DB struct { db *sql.DB }` implementing `store.Store` (methods stubbed with `errors.New("not implemented")` where later tasks fill in); `func open(dsn string) (store.Store, error)` registered as scheme `sqlite` in `init()`; `func openAt(path string) (*DB, error)` used by tests. Test helper `newTestDB(t *testing.T) *DB` opening a `t.TempDir()` file and running `Migrate` — later sqlite tasks reuse it.

- [ ] **Step 1: Write failing tests**

```go
package sqlite

import (
	"context"
	"path/filepath"
	"testing"

	"github.com/dmitry/analytics/internal/store"
)

func newTestDB(t *testing.T) *DB {
	t.Helper()
	db, err := openAt(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestOpenViaRegistry(t *testing.T) {
	s, err := store.Open("sqlite://" + filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestPragmas(t *testing.T) {
	db := newTestDB(t)
	for pragma, want := range map[string]string{
		"journal_mode": "wal",
		"synchronous":  "1", // NORMAL
		"auto_vacuum":  "2", // INCREMENTAL
	} {
		var got string
		if err := db.db.QueryRow("PRAGMA " + pragma).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Errorf("PRAGMA %s = %q, want %q", pragma, got, want)
		}
	}
}

func TestMigrateIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	if err := db.Migrate(context.Background()); err != nil {
		t.Fatalf("second migrate: %v", err)
	}
	var n int
	if err := db.db.QueryRow("SELECT COUNT(*) FROM schema_migrations").Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n < 1 {
		t.Fatalf("schema_migrations rows = %d", n)
	}
}

func TestSchemaTablesExist(t *testing.T) {
	db := newTestDB(t)
	for _, table := range []string{
		"meta", "projects", "web_hits", "product_events",
		"agg_web_daily", "agg_web_pages", "agg_web_referrers", "agg_web_countries",
		"agg_web_devices", "agg_web_browsers", "agg_web_os", "agg_web_utm",
		"agg_product_daily", "agg_product_totals", "agg_product_attrs",
	} {
		var name string
		err := db.db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name=?`, table).Scan(&name)
		if err != nil {
			t.Errorf("table %s missing: %v", table, err)
		}
	}
}
```

- [ ] **Step 2: Run — fails.** **Step 3: Implement**

`migrations/001_init.sql` — the exact DDL from spec §8 (copy it verbatim: `meta`, `projects` with `archived_at`, `web_hits` + 2 indexes, `product_events` + 2 indexes, `agg_web_daily`, the seven dimension tables `agg_web_pages/referrers/countries/devices/browsers/os/utm` each `WITHOUT ROWID` with the PKs listed in the spec, `agg_product_daily`, `agg_product_totals`, `agg_product_attrs`). Note: `schema_migrations` is created by the runner, not the migration file. Dimension tables all have columns `(project_id TEXT NOT NULL, day TEXT NOT NULL, <dim> TEXT NOT NULL, visitors INTEGER NOT NULL, pageviews INTEGER NOT NULL)` where `<dim>` is `path`/`source`/`country`/`device`/`browser`/`os`; `agg_web_utm` has the three utm columns.

`sqlite.go`:
```go
// Package sqlite implements store.Store on modernc.org/sqlite (spec §7.2).
package sqlite

import (
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	"github.com/dmitry/analytics/internal/store"
	_ "modernc.org/sqlite"
)

func init() { store.Register("sqlite", open) }

type DB struct{ db *sql.DB }

func open(dsn string) (store.Store, error) {
	u, err := url.Parse(dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: %w", err)
	}
	// sqlite:///var/lib/analytics/a.db -> /var/lib/analytics/a.db
	// sqlite://relative.db            -> relative.db (host part)
	path := u.Path
	if u.Host != "" {
		path = u.Host + u.Path
	}
	path = strings.TrimPrefix(path, "//")
	return openAt(path)
}

func openAt(path string) (*DB, error) {
	// _pragma values applied on every new connection by the driver.
	dsn := "file:" + path + "?" + strings.Join([]string{
		"_pragma=busy_timeout(5000)",
		"_pragma=journal_mode(WAL)",
		"_pragma=synchronous(NORMAL)",
		"_pragma=cache_size(-16000)",
		"_pragma=temp_store(2)",
	}, "&")
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("sqlite: open %s: %w", path, err)
	}
	db.SetMaxOpenConns(1) // single-writer pipeline (spec §7.2)
	db.SetMaxIdleConns(1)
	db.SetConnMaxLifetime(0)
	// auto_vacuum must be set before the first table is created.
	if _, err := db.Exec("PRAGMA auto_vacuum=INCREMENTAL"); err != nil {
		db.Close()
		return nil, fmt.Errorf("sqlite: auto_vacuum: %w", err)
	}
	return &DB{db: db}, nil
}

func (d *DB) Close() error { return d.db.Close() }
```

`migrate.go`:
```go
package sqlite

import (
	"context"
	"embed"
	"fmt"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

func (d *DB) Migrate(ctx context.Context) error {
	if _, err := d.db.ExecContext(ctx,
		`CREATE TABLE IF NOT EXISTS schema_migrations (
		   version INTEGER PRIMARY KEY,
		   applied_at TEXT NOT NULL DEFAULT (datetime('now')))`); err != nil {
		return fmt.Errorf("sqlite: migrations table: %w", err)
	}
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		version, err := strconv.Atoi(strings.SplitN(name, "_", 2)[0])
		if err != nil {
			return fmt.Errorf("sqlite: bad migration name %q", name)
		}
		var done int
		if err := d.db.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM schema_migrations WHERE version=?`, version).Scan(&done); err != nil {
			return err
		}
		if done > 0 {
			continue
		}
		body, err := migrationFS.ReadFile("migrations/" + name)
		if err != nil {
			return err
		}
		tx, err := d.db.BeginTx(ctx, nil)
		if err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, string(body)); err != nil {
			tx.Rollback()
			return fmt.Errorf("sqlite: migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO schema_migrations (version) VALUES (?)`, version); err != nil {
			tx.Rollback()
			return err
		}
		if err := tx.Commit(); err != nil {
			return err
		}
	}
	return nil
}
```

Add stub methods for every remaining `store.Store` method returning `fmt.Errorf("sqlite: not implemented")` so the package compiles as a `store.Store` (each later sqlite task replaces its stubs). Run `go get modernc.org/sqlite` and `go mod tidy`.

- [ ] **Step 4: Run tests** — `go test -race ./internal/store/... -v` — PASS.
- [ ] **Step 5: Commit** — `git commit -m "feat: sqlite backend with pragmas and embedded migration runner"`

---

### Task 6: sqlite writes, project sync (archiving), meta

**Files:**
- Modify: `internal/store/sqlite/sqlite.go` (replace stubs)
- Create: `internal/store/sqlite/write.go`, `internal/store/sqlite/write_test.go`

**Interfaces:**
- Produces working: `WriteWebHits`, `WriteProductEvents`, `SyncProjects`, `ProjectIDs`, `GetMeta`, `SetMeta`.
- `WriteProductEvents` serializes `Attributes` with `json.Marshal` (empty map → `{}`).
- `SyncProjects` semantics (spec §8): upsert by ID; present ⇒ `archived_at=NULL`, name updated; absent ⇒ `archived_at=datetime('now')` if not already set.

- [ ] **Step 1: Write failing tests**

```go
package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/dmitry/analytics/internal/store"
)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestWriteWebHitsRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	hits := []store.WebHit{{
		ID: "h1", ProjectID: "app", TS: ts("2026-08-22T10:00:00Z"),
		VisitorHash: "v1", Path: "/x", ReferrerSource: "google",
		UTMSource: "hn", Country: "DE", Device: "desktop", Browser: "Firefox", OS: "Linux",
	}}
	if err := db.WriteWebHits(ctx, hits); err != nil {
		t.Fatal(err)
	}
	var path, tsCol string
	if err := db.db.QueryRow(`SELECT path, ts FROM web_hits WHERE id='h1'`).Scan(&path, &tsCol); err != nil {
		t.Fatal(err)
	}
	if path != "/x" || tsCol != "2026-08-22T10:00:00Z" {
		t.Fatalf("got %q %q", path, tsCol)
	}
	if err := db.WriteWebHits(ctx, nil); err != nil {
		t.Fatal("empty batch must be a no-op")
	}
}

func TestWriteProductEventsAttributesJSON(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	err := db.WriteProductEvents(ctx, []store.ProductEvent{
		{ID: "e1", ProjectID: "app", EventName: "sub", UserID: "u1",
			TS: ts("2026-08-22T10:00:00Z"), Attributes: map[string]string{"plan": "pro"}},
		{ID: "e2", ProjectID: "app", EventName: "sub", UserID: "u2",
			TS: ts("2026-08-22T10:01:00Z")}, // nil attributes
	})
	if err != nil {
		t.Fatal(err)
	}
	var attrs string
	if err := db.db.QueryRow(`SELECT attributes->>'plan' FROM product_events WHERE id='e1'`).Scan(&attrs); err != nil {
		t.Fatal(err)
	}
	if attrs != "pro" {
		t.Fatalf("attrs = %q", attrs)
	}
	if err := db.db.QueryRow(`SELECT attributes FROM product_events WHERE id='e2'`).Scan(&attrs); err != nil {
		t.Fatal(err)
	}
	if attrs != "{}" {
		t.Fatalf("nil attributes must store {}, got %q", attrs)
	}
}

func TestSyncProjectsArchiving(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	must := func(err error) { if err != nil { t.Fatal(err) } }
	must(db.SyncProjects(ctx, []store.ProjectInfo{{ID: "a", Name: "A"}, {ID: "b", Name: "B"}}))
	// b disappears from config -> archived
	must(db.SyncProjects(ctx, []store.ProjectInfo{{ID: "a", Name: "A2"}}))
	var name string
	var archived *string
	must(db.db.QueryRow(`SELECT name, archived_at FROM projects WHERE id='a'`).Scan(&name, &archived))
	if name != "A2" || archived != nil {
		t.Fatalf("a: name=%q archived=%v", name, archived)
	}
	must(db.db.QueryRow(`SELECT name, archived_at FROM projects WHERE id='b'`).Scan(&name, &archived))
	if archived == nil {
		t.Fatal("b must be archived")
	}
	// b returns -> unarchived
	must(db.SyncProjects(ctx, []store.ProjectInfo{{ID: "a", Name: "A2"}, {ID: "b", Name: "B"}}))
	must(db.db.QueryRow(`SELECT archived_at FROM projects WHERE id='b'`).Scan(&archived))
	if archived != nil {
		t.Fatal("b must be unarchived after reappearing")
	}
	ids, err := db.ProjectIDs(ctx)
	if err != nil || len(ids) != 2 {
		t.Fatalf("ProjectIDs = %v, %v", ids, err)
	}
}

func TestMetaRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if v, err := db.GetMeta(ctx, "missing"); err != nil || v != "" {
		t.Fatalf("missing key: %q %v", v, err)
	}
	if err := db.SetMeta(ctx, "salt", "s1"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetMeta(ctx, "salt", "s2"); err != nil {
		t.Fatal(err) // overwrite
	}
	if v, _ := db.GetMeta(ctx, "salt"); v != "s2" {
		t.Fatalf("got %q", v)
	}
}
```

- [ ] **Step 2: Run — fails (stubs error).** **Step 3: Implement** in `write.go`:

```go
package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/dmitry/analytics/internal/store"
)

const tsFormat = "2006-01-02T15:04:05Z"

func (d *DB) WriteWebHits(ctx context.Context, hits []store.WebHit) error {
	if len(hits) == 0 {
		return nil
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO web_hits
			(id, project_id, ts, visitor_hash, path, referrer_source,
			 utm_source, utm_medium, utm_campaign, country, device, browser, os)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, h := range hits {
			if _, err := stmt.ExecContext(ctx, h.ID, h.ProjectID,
				h.TS.UTC().Format(tsFormat), h.VisitorHash, h.Path, h.ReferrerSource,
				h.UTMSource, h.UTMMedium, h.UTMCampaign, h.Country, h.Device, h.Browser, h.OS); err != nil {
				return fmt.Errorf("web hit %s: %w", h.ID, err)
			}
		}
		return nil
	})
}

func (d *DB) WriteProductEvents(ctx context.Context, evs []store.ProductEvent) error {
	if len(evs) == 0 {
		return nil
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		stmt, err := tx.PrepareContext(ctx, `INSERT INTO product_events
			(id, project_id, event_name, user_id, ts, attributes) VALUES (?,?,?,?,?,?)`)
		if err != nil {
			return err
		}
		defer stmt.Close()
		for _, e := range evs {
			attrs := e.Attributes
			if attrs == nil {
				attrs = map[string]string{}
			}
			blob, err := json.Marshal(attrs)
			if err != nil {
				return fmt.Errorf("event %s attributes: %w", e.ID, err)
			}
			if _, err := stmt.ExecContext(ctx, e.ID, e.ProjectID, e.EventName,
				e.UserID, e.TS.UTC().Format(tsFormat), string(blob)); err != nil {
				return fmt.Errorf("event %s: %w", e.ID, err)
			}
		}
		return nil
	})
}

func (d *DB) SyncProjects(ctx context.Context, ps []store.ProjectInfo) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		ids := make([]string, 0, len(ps))
		for _, p := range ps {
			ids = append(ids, p.ID)
			if _, err := tx.ExecContext(ctx, `INSERT INTO projects (id, name) VALUES (?,?)
				ON CONFLICT(id) DO UPDATE SET name=excluded.name, archived_at=NULL`,
				p.ID, p.Name); err != nil {
				return err
			}
		}
		q := `UPDATE projects SET archived_at=datetime('now') WHERE archived_at IS NULL`
		args := []any{}
		if len(ids) > 0 {
			q += ` AND id NOT IN (?` + strings.Repeat(",?", len(ids)-1) + `)`
			for _, id := range ids {
				args = append(args, id)
			}
		}
		_, err := tx.ExecContext(ctx, q, args...)
		return err
	})
}

func (d *DB) ProjectIDs(ctx context.Context) ([]string, error) {
	rows, err := d.db.QueryContext(ctx, `SELECT id FROM projects ORDER BY id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

func (d *DB) GetMeta(ctx context.Context, key string) (string, error) {
	var v string
	err := d.db.QueryRowContext(ctx, `SELECT value FROM meta WHERE key=?`, key).Scan(&v)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return v, err
}

func (d *DB) SetMeta(ctx context.Context, key, value string) error {
	_, err := d.db.ExecContext(ctx,
		`INSERT INTO meta (key, value) VALUES (?,?)
		 ON CONFLICT(key) DO UPDATE SET value=excluded.value`, key, value)
	return err
}

// tx runs fn in a transaction with commit/rollback handling; shared by all
// sqlite write paths.
func (d *DB) tx(ctx context.Context, fn func(*sql.Tx) error) error {
	tx, err := d.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	if err := fn(tx); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

var _ = time.Now // keep import if unused elsewhere
```

Remove the corresponding stubs from `sqlite.go`.

- [ ] **Step 4: Run tests — PASS.** **Step 5: Commit** — `git commit -m "feat: sqlite batch writes, project sync with archiving, meta kv"`

---

### Task 7: `internal/identity` — visitor hash + salt lifecycle

**Files:**
- Create: `internal/identity/identity.go`, `internal/identity/identity_test.go`

**Interfaces:**
- Consumes: `store.Store` (only `GetMeta`/`SetMeta`) via local interface `type MetaStore interface { GetMeta(ctx context.Context, key string) (string, error); SetMeta(ctx context.Context, key, value string) error }`.
- Produces:
  - `func VisitorHash(salt, ip, userAgent, projectID string) string` — hex SHA-256 of `salt+"\x00"+ip+"\x00"+userAgent+"\x00"+projectID`, first 16 hex chars
  - `type Salter struct{ ... }`, `func NewSalter(m MetaStore, now func() time.Time) *Salter`
  - `func (s *Salter) Current(ctx context.Context) (string, error)` — loads from meta; generates+persists if absent or rotated_at older than 24h (old salt is overwritten — destroyed, spec §5.4)
  - `func (s *Salter) Rotate(ctx context.Context) error` — force new salt now
  - Meta keys: `visitor_salt`, `visitor_salt_rotated_at` (RFC3339)

- [ ] **Step 1: Write failing tests**

```go
package identity

import (
	"context"
	"strings"
	"testing"
	"time"
)

type fakeMeta struct{ m map[string]string }

func (f *fakeMeta) GetMeta(_ context.Context, k string) (string, error) { return f.m[k], nil }
func (f *fakeMeta) SetMeta(_ context.Context, k, v string) error        { f.m[k] = v; return nil }

func TestVisitorHashProperties(t *testing.T) {
	h1 := VisitorHash("salt", "1.2.3.4", "UA", "app")
	if len(h1) != 16 || strings.ToLower(h1) != h1 {
		t.Fatalf("hash %q must be 16 lowercase hex chars", h1)
	}
	if h1 != VisitorHash("salt", "1.2.3.4", "UA", "app") {
		t.Fatal("must be deterministic")
	}
	for _, other := range []string{
		VisitorHash("salt2", "1.2.3.4", "UA", "app"),
		VisitorHash("salt", "1.2.3.5", "UA", "app"),
		VisitorHash("salt", "1.2.3.4", "UA2", "app"),
		VisitorHash("salt", "1.2.3.4", "UA", "app2"),
	} {
		if other == h1 {
			t.Fatal("any component change must change the hash")
		}
	}
	// Concatenation ambiguity: (a,bc) vs (ab,c) must differ.
	if VisitorHash("s", "ab", "c", "p") == VisitorHash("s", "a", "bc", "p") {
		t.Fatal("components must be delimited")
	}
}

func TestSalterGeneratesAndPersists(t *testing.T) {
	meta := &fakeMeta{m: map[string]string{}}
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	s := NewSalter(meta, func() time.Time { return now })
	salt, err := s.Current(context.Background())
	if err != nil || len(salt) < 32 {
		t.Fatalf("salt %q err %v", salt, err)
	}
	again, _ := s.Current(context.Background())
	if again != salt {
		t.Fatal("salt must be stable within a day")
	}
}

func TestSalterRotatesAfter24h(t *testing.T) {
	meta := &fakeMeta{m: map[string]string{}}
	now := time.Date(2026, 8, 22, 10, 0, 0, 0, time.UTC)
	s := NewSalter(meta, func() time.Time { return now })
	first, _ := s.Current(context.Background())
	now = now.Add(25 * time.Hour)
	second, err := s.Current(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second == first {
		t.Fatal("salt must rotate after 24h")
	}
	if meta.m["visitor_salt"] != second {
		t.Fatal("new salt must be persisted (old destroyed)")
	}
}
```

- [ ] **Step 2: Run — fails.** **Step 3: Implement**

```go
// Package identity implements Plausible-style cookieless visitor identity:
// hash(daily_salt, ip, user_agent, project). The salt is destroyed on
// rotation so cross-day linking is impossible (spec §5.4/§5.4a).
package identity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"
)

const (
	saltKey      = "visitor_salt"
	saltTimeKey  = "visitor_salt_rotated_at"
	rotateEvery  = 24 * time.Hour
)

func VisitorHash(salt, ip, userAgent, projectID string) string {
	h := sha256.New()
	for _, part := range []string{salt, ip, userAgent, projectID} {
		h.Write([]byte(part))
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

type MetaStore interface {
	GetMeta(ctx context.Context, key string) (string, error)
	SetMeta(ctx context.Context, key, value string) error
}

type Salter struct {
	meta MetaStore
	now  func() time.Time

	mu        sync.Mutex
	salt      string
	rotatedAt time.Time
}

func NewSalter(m MetaStore, now func() time.Time) *Salter {
	return &Salter{meta: m, now: now}
}

func (s *Salter) Current(ctx context.Context) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.salt == "" {
		if err := s.loadLocked(ctx); err != nil {
			return "", err
		}
	}
	if s.salt == "" || s.now().Sub(s.rotatedAt) >= rotateEvery {
		if err := s.rotateLocked(ctx); err != nil {
			return "", err
		}
	}
	return s.salt, nil
}

func (s *Salter) Rotate(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rotateLocked(ctx)
}

func (s *Salter) loadLocked(ctx context.Context) error {
	salt, err := s.meta.GetMeta(ctx, saltKey)
	if err != nil {
		return err
	}
	at, err := s.meta.GetMeta(ctx, saltTimeKey)
	if err != nil {
		return err
	}
	if salt != "" && at != "" {
		if ts, err := time.Parse(time.RFC3339, at); err == nil {
			s.salt, s.rotatedAt = salt, ts
		}
	}
	return nil
}

func (s *Salter) rotateLocked(ctx context.Context) error {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return fmt.Errorf("identity: entropy: %w", err)
	}
	salt := hex.EncodeToString(buf)
	if err := s.meta.SetMeta(ctx, saltKey, salt); err != nil {
		return err
	}
	if err := s.meta.SetMeta(ctx, saltTimeKey, s.now().UTC().Format(time.RFC3339)); err != nil {
		return err
	}
	s.salt, s.rotatedAt = salt, s.now()
	return nil
}
```

- [ ] **Step 4: Run tests — PASS.** **Step 5: Commit** — `git commit -m "feat: visitor hashing with rotating destroyed salt"`

---

### Task 8: `internal/enrich` — User-Agent parsing + bot filter

**Files:**
- Create: `internal/enrich/ua.go`, `internal/enrich/ua_test.go`

**Interfaces:**
- Produces:
  - `func ParseUserAgent(ua string) (device, browser, os string)` — device ∈ `desktop|mobile|tablet` (default desktop), browser ∈ `Chrome|Firefox|Safari|Edge|Opera|Samsung Internet|""`, os ∈ `Windows|macOS|Linux|Android|iOS|ChromeOS|""`
  - `func IsBot(ua string) bool` — true for empty UA or UAs containing (case-insensitive) any of: `bot`, `crawler`, `spider`, `crawling`, `headless`, `lighthouse`, `slurp`, `curl/`, `wget/`, `python-requests`, `facebookexternalhit`, `preview`
- Hot path constraint (spec §12a): `strings.Contains`/`strings.Index` only — no regexp.

- [ ] **Step 1: Write failing tests** (table-driven with real UA strings)

```go
package enrich

import "testing"

func TestParseUserAgent(t *testing.T) {
	cases := []struct{ ua, device, browser, os string }{
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
			"desktop", "Chrome", "Windows"},
		{"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.4 Safari/605.1.15",
			"desktop", "Safari", "macOS"},
		{"Mozilla/5.0 (X11; Linux x86_64; rv:127.0) Gecko/20100101 Firefox/127.0",
			"desktop", "Firefox", "Linux"},
		{"Mozilla/5.0 (iPhone; CPU iPhone OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
			"mobile", "Safari", "iOS"},
		{"Mozilla/5.0 (Linux; Android 14; Pixel 8) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Mobile Safari/537.36",
			"mobile", "Chrome", "Android"},
		{"Mozilla/5.0 (iPad; CPU OS 17_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Mobile/15E148 Safari/604.1",
			"tablet", "Safari", "iOS"},
		{"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36 Edg/126.0.0.0",
			"desktop", "Edge", "Windows"},
		{"Mozilla/5.0 (Linux; Android 13; SM-G991B) AppleWebKit/537.36 (KHTML, like Gecko) SamsungBrowser/25.0 Chrome/121.0.0.0 Mobile Safari/537.36",
			"mobile", "Samsung Internet", "Android"},
		{"Mozilla/5.0 (X11; CrOS x86_64 14541.0.0) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
			"desktop", "Chrome", "ChromeOS"},
		{"weird thing", "desktop", "", ""},
	}
	for _, c := range cases {
		d, b, o := ParseUserAgent(c.ua)
		if d != c.device || b != c.browser || o != c.os {
			t.Errorf("%q => (%s,%s,%s), want (%s,%s,%s)", c.ua, d, b, o, c.device, c.browser, c.os)
		}
	}
}

func TestIsBot(t *testing.T) {
	bots := []string{
		"", "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)",
		"Mozilla/5.0 (compatible; bingbot/2.0)", "curl/8.5.0", "Wget/1.21",
		"python-requests/2.32", "Mozilla/5.0 (X11; Linux x86_64) HeadlessChrome/126.0.0.0",
		"Screaming Frog SEO Spider/19.0", "facebookexternalhit/1.1",
	}
	for _, ua := range bots {
		if !IsBot(ua) {
			t.Errorf("IsBot(%q) = false, want true", ua)
		}
	}
	humans := []string{
		"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36",
	}
	for _, ua := range humans {
		if IsBot(ua) {
			t.Errorf("IsBot(%q) = true, want false", ua)
		}
	}
}
```

- [ ] **Step 2: Run — fails.** **Step 3: Implement**

```go
// Package enrich derives coarse device/browser/os classes and cleans
// referrers/URLs at ingest. Only substring matching on the hot path
// (spec §12a) — order of checks matters and is documented inline.
package enrich

import "strings"

var botMarkers = []string{
	"bot", "crawler", "spider", "crawling", "headless", "lighthouse",
	"slurp", "curl/", "wget/", "python-requests", "facebookexternalhit", "preview",
}

func IsBot(ua string) bool {
	if ua == "" {
		return true
	}
	l := strings.ToLower(ua)
	for _, m := range botMarkers {
		if strings.Contains(l, m) {
			return true
		}
	}
	return false
}

func ParseUserAgent(ua string) (device, browser, os string) {
	// OS first — some browser checks depend on it.
	switch {
	case strings.Contains(ua, "CrOS"):
		os = "ChromeOS"
	case strings.Contains(ua, "Windows"):
		os = "Windows"
	case strings.Contains(ua, "iPhone"), strings.Contains(ua, "iPad"):
		os = "iOS"
	case strings.Contains(ua, "Android"):
		os = "Android"
	case strings.Contains(ua, "Mac OS X"):
		os = "macOS"
	case strings.Contains(ua, "Linux"):
		os = "Linux"
	}
	// Browser: check derivatives before their bases (Edge/Samsung before
	// Chrome, Chrome before Safari — every Chrome UA contains "Safari").
	switch {
	case strings.Contains(ua, "Edg/"), strings.Contains(ua, "Edge/"):
		browser = "Edge"
	case strings.Contains(ua, "SamsungBrowser/"):
		browser = "Samsung Internet"
	case strings.Contains(ua, "OPR/"), strings.Contains(ua, "Opera/"):
		browser = "Opera"
	case strings.Contains(ua, "Firefox/"):
		browser = "Firefox"
	case strings.Contains(ua, "Chrome/"), strings.Contains(ua, "CriOS/"):
		browser = "Chrome"
	case strings.Contains(ua, "Safari/"):
		browser = "Safari"
	}
	switch {
	case strings.Contains(ua, "iPad"), strings.Contains(ua, "Tablet"):
		device = "tablet"
	case strings.Contains(ua, "Mobile"), strings.Contains(ua, "iPhone"):
		device = "mobile"
	default:
		device = "desktop"
	}
	return device, browser, os
}
```

- [ ] **Step 4: Run tests — PASS.** **Step 5: Commit** — `git commit -m "feat: UA classifier and bot filter (no regex on hot path)"`

---

### Task 9: `internal/enrich` — page URL parsing, query stripping, referrer cleaning

**Files:**
- Create: `internal/enrich/url.go`, `internal/enrich/url_test.go`

**Interfaces:**
- Produces:
  - `type PageInfo struct { Host, Path string; UTMSource, UTMMedium, UTMCampaign, Ref string }`
  - `func ParsePageURL(raw string) (PageInfo, error)` — extracts path, keeps ONLY `utm_source/utm_medium/utm_campaign/ref` query params, discards everything else (spec §5.4: never stored); error on unparsable/relative URLs
  - `func CleanReferrer(referrer, ownHost string) string` — "" for empty/own-host/unparsable; known-engine hostnames normalized via embedded map (`google.` prefix match → `google`, likewise `bing`, `duckduckgo`, `yahoo`, `baidu`, `yandex`, `t.co`→`twitter`, `twitter.com`/`x.com`→`twitter`, `facebook.com`/`l.facebook.com`/`lm.facebook.com`→`facebook`, `linkedin.com`/`lnkd.in`→`linkedin`, `reddit.com`/`old.reddit.com`→`reddit`, `news.ycombinator.com`→`hackernews`, `instagram.com`→`instagram`, `youtube.com`/`youtu.be`→`youtube`); otherwise bare hostname without `www.`

- [ ] **Step 1: Write failing tests**

```go
package enrich

import "testing"

func TestParsePageURL(t *testing.T) {
	p, err := ParsePageURL("https://app.com/pricing?utm_source=hn&utm_medium=social&utm_campaign=launch&ref=newsletter&session_token=SECRET&email=a@b.c")
	if err != nil {
		t.Fatal(err)
	}
	if p.Host != "app.com" || p.Path != "/pricing" {
		t.Errorf("host/path: %+v", p)
	}
	if p.UTMSource != "hn" || p.UTMMedium != "social" || p.UTMCampaign != "launch" || p.Ref != "newsletter" {
		t.Errorf("campaign params: %+v", p)
	}
	// Privacy: nothing else from the query may survive anywhere in the struct.
	if p.Path != "/pricing" {
		t.Errorf("query must not leak into path: %q", p.Path)
	}
	if _, err := ParsePageURL("not a url"); err == nil {
		t.Error("garbage must error")
	}
	if _, err := ParsePageURL("/relative/only"); err == nil {
		t.Error("relative URL must error (no host)")
	}
	if p, _ := ParsePageURL("https://app.com"); p.Path != "/" {
		t.Errorf("empty path must normalize to /: %q", p.Path)
	}
}

func TestCleanReferrer(t *testing.T) {
	cases := []struct{ ref, own, want string }{
		{"", "app.com", ""},
		{"https://app.com/other-page", "app.com", ""},               // own-domain
		{"https://www.google.com/search?q=x", "app.com", "google"},  // engine
		{"https://google.de/url", "app.com", "google"},
		{"https://news.ycombinator.com/item?id=1", "app.com", "hackernews"},
		{"https://x.com/somebody", "app.com", "twitter"},
		{"https://t.co/abc", "app.com", "twitter"},
		{"https://www.example.org/blog", "app.com", "example.org"},  // generic: bare host
		{"::not-a-url::", "app.com", ""},
	}
	for _, c := range cases {
		if got := CleanReferrer(c.ref, c.own); got != c.want {
			t.Errorf("CleanReferrer(%q) = %q, want %q", c.ref, got, c.want)
		}
	}
}
```

- [ ] **Step 2: Run — fails.** **Step 3: Implement**

```go
package enrich

import (
	"fmt"
	"net/url"
	"strings"
)

type PageInfo struct {
	Host, Path                              string
	UTMSource, UTMMedium, UTMCampaign, Ref  string
}

func ParsePageURL(raw string) (PageInfo, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		return PageInfo{}, fmt.Errorf("enrich: not an absolute http(s) URL: %q", raw)
	}
	q := u.Query()
	path := u.Path
	if path == "" {
		path = "/"
	}
	// Only campaign parameters survive; the rest of the query (and any
	// fragment) is discarded here and never stored (spec §5.4/§5.4a).
	return PageInfo{
		Host:        u.Hostname(),
		Path:        path,
		UTMSource:   q.Get("utm_source"),
		UTMMedium:   q.Get("utm_medium"),
		UTMCampaign: q.Get("utm_campaign"),
		Ref:         q.Get("ref"),
	}, nil
}

// referrerNames maps exact hostnames (after www-stripping) to sources.
var referrerNames = map[string]string{
	"t.co": "twitter", "twitter.com": "twitter", "x.com": "twitter",
	"facebook.com": "facebook", "l.facebook.com": "facebook", "lm.facebook.com": "facebook",
	"linkedin.com": "linkedin", "lnkd.in": "linkedin",
	"reddit.com": "reddit", "old.reddit.com": "reddit",
	"news.ycombinator.com": "hackernews",
	"instagram.com":        "instagram",
	"youtube.com":          "youtube", "youtu.be": "youtube",
	"duckduckgo.com": "duckduckgo",
}

// referrerPrefixes catches localized engine domains (google.de, yahoo.co.jp).
var referrerPrefixes = map[string]string{
	"google.": "google", "bing.": "bing", "yahoo.": "yahoo",
	"baidu.": "baidu", "yandex.": "yandex",
}

func CleanReferrer(referrer, ownHost string) string {
	if referrer == "" {
		return ""
	}
	u, err := url.Parse(referrer)
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	if host == strings.TrimPrefix(strings.ToLower(ownHost), "www.") {
		return ""
	}
	if name, ok := referrerNames[host]; ok {
		return name
	}
	for prefix, name := range referrerPrefixes {
		if strings.HasPrefix(host, prefix) {
			return name
		}
	}
	return host
}
```

- [ ] **Step 4: Run tests — PASS.** **Step 5: Commit** — `git commit -m "feat: URL parsing with privacy query stripping, referrer cleaning"`

---

### Task 10: `internal/geo` — provider registry, cloudflare, none

**Files:**
- Create: `internal/geo/geo.go`, `internal/geo/geo_test.go`

**Interfaces:**
- Produces:
  - `type Provider interface { Country(r *http.Request, ip string) string; Close() error }`
  - `func New(dsn, dataDir string, logger *slog.Logger) (Provider, error)` — scheme registry (`cloudflare`, `none`, `maxmind` added in Task 11); unknown scheme errors
  - cloudflare: returns upper-cased `CF-IPCountry` header; `""` for missing/`XX`/`T1` (Cloudflare's unknown/Tor markers)
  - none: always `""`

- [ ] **Step 1: Write failing tests**

```go
package geo

import (
	"log/slog"
	"net/http/httptest"
	"testing"
)

func TestUnknownScheme(t *testing.T) {
	if _, err := New("nope://", t.TempDir(), slog.Default()); err == nil {
		t.Fatal("unknown scheme must error")
	}
}

func TestCloudflareProvider(t *testing.T) {
	p, err := New("cloudflare://", t.TempDir(), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	cases := map[string]string{"DE": "DE", "de": "DE", "XX": "", "T1": "", "": ""}
	for header, want := range cases {
		r := httptest.NewRequest("POST", "/api/hit", nil)
		if header != "" {
			r.Header.Set("CF-IPCountry", header)
		}
		if got := p.Country(r, "1.2.3.4"); got != want {
			t.Errorf("header %q => %q, want %q", header, got, want)
		}
	}
}

func TestNoneProvider(t *testing.T) {
	p, err := New("none://", t.TempDir(), slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/hit", nil)
	if p.Country(r, "8.8.8.8") != "" {
		t.Fatal("none provider must return empty")
	}
}
```

- [ ] **Step 2: Run — fails.** **Step 3: Implement**

```go
// Package geo resolves a request's country. Providers are selected by DSN
// scheme (spec §6): cloudflare:// (default), maxmind://KEY, none://.
package geo

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
)

type Provider interface {
	Country(r *http.Request, ip string) string
	Close() error
}

type factory func(u *url.URL, dataDir string, logger *slog.Logger) (Provider, error)

var providers = map[string]factory{
	"cloudflare": func(*url.URL, string, *slog.Logger) (Provider, error) { return cloudflare{}, nil },
	"none":       func(*url.URL, string, *slog.Logger) (Provider, error) { return none{}, nil },
}

func New(dsn, dataDir string, logger *slog.Logger) (Provider, error) {
	u, err := url.Parse(dsn)
	if err != nil || u.Scheme == "" {
		return nil, fmt.Errorf("geo: invalid DSN %q", dsn)
	}
	f, ok := providers[u.Scheme]
	if !ok {
		return nil, fmt.Errorf("geo: unknown provider %q (supported: cloudflare, maxmind, none)", u.Scheme)
	}
	return f(u, dataDir, logger)
}

type cloudflare struct{}

func (cloudflare) Country(r *http.Request, _ string) string {
	c := strings.ToUpper(r.Header.Get("CF-IPCountry"))
	if c == "" || c == "XX" || c == "T1" {
		return ""
	}
	return c
}
func (cloudflare) Close() error { return nil }

type none struct{}

func (none) Country(*http.Request, string) string { return "" }
func (none) Close() error                         { return nil }
```

- [ ] **Step 4: Run tests — PASS.** **Step 5: Commit** — `git commit -m "feat: geo provider registry with cloudflare and none"`

---

### Task 11: `internal/geo` — maxmind provider with auto-download

**Files:**
- Create: `internal/geo/maxmind.go`, `internal/geo/maxmind_test.go`, `internal/geo/testdata/GeoIP2-Country-Test.mmdb`

**Interfaces:**
- Consumes: `github.com/oschwald/maxminddb-golang/v2` (spec §6 contingency, flagged and accepted in this plan's header).
- Produces: scheme `maxmind` registered; DSN `maxmind://LICENSE_KEY`. Behavior:
  - DB path: `<dataDir>/GeoLite2-Country.mmdb`. On construction: if file missing or older than 30 days, download `https://download.maxmind.com/app/geoip_download?edition_id=GeoLite2-Country&license_key=KEY&suffix=tar.gz`, extract the `.mmdb` member from the tar.gz to the path atomically (tmp + rename). Downloader is `func(url, dest string) error` stored in a package var `downloadDB` so tests stub it. A background goroutine re-checks weekly (spec §9); it must stop on `Close()`.
  - `Country`: lookup ip; struct `struct{ Country struct{ ISOCode string ` + "`maxminddb:\"iso_code\"`" + ` } ` + "`maxminddb:\"country\"`" + ` }`; any error → `""` (never drop a hit, spec §6).
  - Test fixture: vendor `GeoIP2-Country-Test.mmdb` from the MaxMind-DB test data repo (github.com/maxmind/MaxMind-DB, `test-data/GeoIP2-Country-Test.mmdb`, public test file, commit it). Known mapping in it: `2.125.160.216` → `GB`, `50.114.0.0` → `US`.

- [ ] **Step 1: Write failing tests**

```go
package geo

import (
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func fixture(t *testing.T, dataDir string) {
	t.Helper()
	src, err := os.ReadFile("testdata/GeoIP2-Country-Test.mmdb")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dataDir, "GeoLite2-Country.mmdb"), src, 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestMaxmindLookup(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir)
	p, err := New("maxmind://testkey", dir, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	r := httptest.NewRequest("POST", "/api/hit", nil)
	if got := p.Country(r, "2.125.160.216"); got != "GB" {
		t.Errorf("GB ip => %q", got)
	}
	if got := p.Country(r, "999.invalid"); got != "" {
		t.Errorf("invalid ip must degrade to empty, got %q", got)
	}
}

func TestMaxmindDownloadsWhenMissing(t *testing.T) {
	dir := t.TempDir()
	called := ""
	old := downloadDB
	downloadDB = func(url, dest string) error {
		called = url
		src, _ := os.ReadFile("testdata/GeoIP2-Country-Test.mmdb")
		return os.WriteFile(dest, src, 0o644)
	}
	defer func() { downloadDB = old }()
	p, err := New("maxmind://KEY123", dir, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if called == "" || !contains(called, "license_key=KEY123") {
		t.Fatalf("download URL = %q, must contain license key", called)
	}
}

func TestMaxmindStaleTriggersDownload(t *testing.T) {
	dir := t.TempDir()
	fixture(t, dir)
	path := filepath.Join(dir, "GeoLite2-Country.mmdb")
	stale := time.Now().Add(-31 * 24 * time.Hour)
	os.Chtimes(path, stale, stale)
	called := false
	old := downloadDB
	downloadDB = func(url, dest string) error { called = true; return nil }
	defer func() { downloadDB = old }()
	p, err := New("maxmind://k", dir, slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	defer p.Close()
	if !called {
		t.Fatal("stale DB must trigger re-download")
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && strings.Contains(s, sub) }
```

(add `"strings"` import)

- [ ] **Step 2: Run — fails.** **Step 3: Implement**

```go
package geo

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/oschwald/maxminddb-golang/v2"
)

const maxAge = 30 * 24 * time.Hour

func init() {
	providers["maxmind"] = newMaxmind
}

// downloadDB fetches url into dest atomically (tmp file + rename),
// extracting the .mmdb member from MaxMind's tar.gz. Package var so tests
// stub the network.
var downloadDB = func(rawURL, dest string) error {
	resp, err := http.Get(rawURL)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("geo: maxmind download HTTP %d", resp.StatusCode)
	}
	gz, err := gzip.NewReader(resp.Body)
	if err != nil {
		return err
	}
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return fmt.Errorf("geo: no .mmdb in archive")
		}
		if err != nil {
			return err
		}
		if strings.HasSuffix(hdr.Name, ".mmdb") {
			tmp := dest + ".tmp"
			f, err := os.Create(tmp)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
			return os.Rename(tmp, dest)
		}
	}
}

type maxmind struct {
	path   string
	reader *maxminddb.Reader
	logger *slog.Logger
	stop   chan struct{}
}

func newMaxmind(u *url.URL, dataDir string, logger *slog.Logger) (Provider, error) {
	key := u.Host // maxmind://LICENSE_KEY
	if key == "" {
		return nil, fmt.Errorf("geo: maxmind DSN requires a license key (maxmind://KEY)")
	}
	m := &maxmind{path: filepath.Join(dataDir, "GeoLite2-Country.mmdb"), logger: logger, stop: make(chan struct{})}
	if err := m.ensureFresh(key); err != nil {
		return nil, err
	}
	r, err := maxminddb.Open(m.path)
	if err != nil {
		return nil, fmt.Errorf("geo: open mmdb: %w", err)
	}
	m.reader = r
	go m.refreshLoop(key)
	return m, nil
}

func (m *maxmind) ensureFresh(key string) error {
	st, err := os.Stat(m.path)
	if err == nil && time.Since(st.ModTime()) < maxAge {
		return nil
	}
	dl := fmt.Sprintf("https://download.maxmind.com/app/geoip_download?edition_id=GeoLite2-Country&license_key=%s&suffix=tar.gz", url.QueryEscape(key))
	if derr := downloadDB(dl, m.path); derr != nil {
		if err == nil {
			m.logger.Warn("geo: refresh failed, keeping stale db", "error", derr)
			return nil // stale but usable
		}
		return fmt.Errorf("geo: initial GeoLite2 download failed: %w", derr)
	}
	return nil
}

func (m *maxmind) refreshLoop(key string) {
	t := time.NewTicker(7 * 24 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-t.C:
			if err := m.ensureFresh(key); err != nil {
				m.logger.Warn("geo: weekly refresh failed", "error", err)
				continue
			}
			if r, err := maxminddb.Open(m.path); err == nil {
				old := m.reader
				m.reader = r
				old.Close()
			}
		}
	}
}

func (m *maxmind) Country(_ *http.Request, ip string) string {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return ""
	}
	var rec struct {
		Country struct {
			ISOCode string `maxminddb:"iso_code"`
		} `maxminddb:"country"`
	}
	if err := m.reader.Lookup(addr).Decode(&rec); err != nil {
		return ""
	}
	return rec.Country.ISOCode
}

func (m *maxmind) Close() error {
	close(m.stop)
	if m.reader != nil {
		return m.reader.Close()
	}
	return nil
}
```

Fetch the fixture once during this task (`curl -L -o internal/geo/testdata/GeoIP2-Country-Test.mmdb https://github.com/maxmind/MaxMind-DB/raw/main/test-data/GeoIP2-Country-Test.mmdb`) and commit it. Run `go get github.com/oschwald/maxminddb-golang/v2 && go mod tidy`. Note: `maxminddb-golang/v2` API — if `Lookup(addr).Decode(&rec)` doesn't match the installed version's API, adapt to that version's documented lookup call (v1 is `r.Lookup(netIP, &rec)`), keeping behavior identical; check `go doc github.com/oschwald/maxminddb-golang/v2 Reader.Lookup` first.

- [ ] **Step 4: Run tests — PASS** (including a data-race check on refreshLoop swap under `-race`; if the race detector flags `m.reader`, guard it with a `sync.RWMutex` — write lock in refreshLoop, read lock in Country).
- [ ] **Step 5: Commit** — `git commit -m "feat: maxmind geo provider with auto-download and weekly refresh"`

---

### Task 12: `internal/pipeline` — bounded buffer + batch flush worker

**Files:**
- Create: `internal/pipeline/pipeline.go`, `internal/pipeline/pipeline_test.go`

**Interfaces:**
- Consumes: `store.WebHit`, `store.ProductEvent`; sink interface (subset of Store): `type Sink interface { WriteWebHits(ctx context.Context, hits []store.WebHit) error; WriteProductEvents(ctx context.Context, evs []store.ProductEvent) error }`
- Produces:
  - `func New(cfg config.BufferConfig, sink Sink, logger *slog.Logger) *Buffer`
  - `func (b *Buffer) EnqueueHit(h store.WebHit)` / `EnqueueEvent(e store.ProductEvent)` — never block: on full channel, drop the OLDEST queued item (non-blocking receive then send) and increment a dropped counter
  - `func (b *Buffer) Run(ctx context.Context)` — worker loop: flush when buffered count reaches `FlushMaxEvents` or `FlushInterval` elapsed; on ctx cancellation drain channel and final-flush; flush errors retried 3× with backoff 1s/5s/25s then batch dropped with error log (spec §5.3). Backoff sleeps use a package var `retryDelays = []time.Duration{...}` so tests shrink them.
  - `func (b *Buffer) Dropped() uint64`

- [ ] **Step 1: Write failing tests**

```go
package pipeline

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/store"
)

type fakeSink struct {
	mu     sync.Mutex
	hits   []store.WebHit
	events []store.ProductEvent
	fail   int // fail this many calls before succeeding
	calls  int
}

func (f *fakeSink) WriteWebHits(_ context.Context, h []store.WebHit) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.fail > 0 {
		f.fail--
		return errors.New("boom")
	}
	f.hits = append(f.hits, h...)
	return nil
}

func (f *fakeSink) WriteProductEvents(_ context.Context, e []store.ProductEvent) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, e...)
	return nil
}

func cfg(max int, interval time.Duration, cap int) config.BufferConfig {
	return config.BufferConfig{FlushMaxEvents: max, FlushInterval: config.Duration{Duration: interval}, Capacity: cap}
}

func TestFlushBySize(t *testing.T) {
	sink := &fakeSink{}
	b := New(cfg(3, time.Hour, 100), sink, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Run(ctx); close(done) }()
	for i := 0; i < 3; i++ {
		b.EnqueueHit(store.WebHit{ID: "h"})
	}
	waitFor(t, func() bool { sink.mu.Lock(); defer sink.mu.Unlock(); return len(sink.hits) == 3 })
	cancel()
	<-done
}

func TestFlushByInterval(t *testing.T) {
	sink := &fakeSink{}
	b := New(cfg(1000, 30*time.Millisecond, 100), sink, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Run(ctx); close(done) }()
	b.EnqueueEvent(store.ProductEvent{ID: "e"})
	waitFor(t, func() bool { sink.mu.Lock(); defer sink.mu.Unlock(); return len(sink.events) == 1 })
	cancel()
	<-done
}

func TestShutdownFlushesRemaining(t *testing.T) {
	sink := &fakeSink{}
	b := New(cfg(1000, time.Hour, 100), sink, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Run(ctx); close(done) }()
	b.EnqueueHit(store.WebHit{ID: "h"})
	time.Sleep(10 * time.Millisecond) // let worker pick it up
	cancel()
	<-done
	if len(sink.hits) != 1 {
		t.Fatalf("shutdown must flush; got %d hits", len(sink.hits))
	}
}

func TestOverflowDropsOldest(t *testing.T) {
	sink := &fakeSink{}
	b := New(cfg(1000, time.Hour, 2), sink, slog.Default())
	// No worker running: fill beyond capacity.
	b.EnqueueHit(store.WebHit{ID: "1"})
	b.EnqueueHit(store.WebHit{ID: "2"})
	b.EnqueueHit(store.WebHit{ID: "3"}) // drops "1"
	if b.Dropped() != 1 {
		t.Fatalf("Dropped = %d, want 1", b.Dropped())
	}
}

func TestFlushRetriesThenDrops(t *testing.T) {
	old := retryDelays
	retryDelays = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	defer func() { retryDelays = old }()
	sink := &fakeSink{fail: 2} // fails twice, succeeds on 3rd
	b := New(cfg(1, time.Hour, 10), sink, slog.Default())
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { b.Run(ctx); close(done) }()
	b.EnqueueHit(store.WebHit{ID: "h"})
	waitFor(t, func() bool { sink.mu.Lock(); defer sink.mu.Unlock(); return len(sink.hits) == 1 })
	cancel()
	<-done
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatal("condition not met within 2s")
}
```

- [ ] **Step 2: Run — fails.** **Step 3: Implement**

```go
// Package pipeline decouples HTTP ingestion from storage: a bounded
// channel absorbs bursts; a single worker writes batches (spec §5.3).
// Availability over completeness: enqueue never blocks, flush failures
// are retried then dropped.
package pipeline

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/store"
)

type Sink interface {
	WriteWebHits(ctx context.Context, hits []store.WebHit) error
	WriteProductEvents(ctx context.Context, evs []store.ProductEvent) error
}

// item carries exactly one of hit/event.
type item struct {
	hit   *store.WebHit
	event *store.ProductEvent
}

var retryDelays = []time.Duration{time.Second, 5 * time.Second, 25 * time.Second}

type Buffer struct {
	cfg     config.BufferConfig
	sink    Sink
	logger  *slog.Logger
	ch      chan item
	dropped atomic.Uint64
}

func New(cfg config.BufferConfig, sink Sink, logger *slog.Logger) *Buffer {
	return &Buffer{cfg: cfg, sink: sink, logger: logger, ch: make(chan item, cfg.Capacity)}
}

func (b *Buffer) EnqueueHit(h store.WebHit)          { b.enqueue(item{hit: &h}) }
func (b *Buffer) EnqueueEvent(e store.ProductEvent)  { b.enqueue(item{event: &e}) }
func (b *Buffer) Dropped() uint64                    { return b.dropped.Load() }

func (b *Buffer) enqueue(it item) {
	for {
		select {
		case b.ch <- it:
			return
		default:
			// Full: drop the oldest to make room, count it, retry.
			select {
			case <-b.ch:
				b.dropped.Add(1)
			default:
			}
		}
	}
}

func (b *Buffer) Run(ctx context.Context) {
	ticker := time.NewTicker(b.cfg.FlushInterval.Duration)
	defer ticker.Stop()
	var hits []store.WebHit
	var events []store.ProductEvent
	flush := func() {
		if len(hits) > 0 {
			b.write(func(c context.Context) error { return b.sink.WriteWebHits(c, hits) }, len(hits), "web_hits")
			hits = nil
		}
		if len(events) > 0 {
			b.write(func(c context.Context) error { return b.sink.WriteProductEvents(c, events) }, len(events), "product_events")
			events = nil
		}
	}
	for {
		select {
		case <-ctx.Done():
			// Drain whatever is still queued, then final flush.
			for {
				select {
				case it := <-b.ch:
					if it.hit != nil {
						hits = append(hits, *it.hit)
					} else {
						events = append(events, *it.event)
					}
					continue
				default:
				}
				break
			}
			flush()
			return
		case it := <-b.ch:
			if it.hit != nil {
				hits = append(hits, *it.hit)
			} else {
				events = append(events, *it.event)
			}
			if len(hits)+len(events) >= b.cfg.FlushMaxEvents {
				flush()
			}
		case <-ticker.C:
			flush()
		}
	}
}

// write retries per spec §5.3 (3 attempts, then drop with an error log).
// Uses context.Background(): shutdown must still flush.
func (b *Buffer) write(fn func(context.Context) error, n int, kind string) {
	var err error
	for attempt := 0; attempt <= len(retryDelays); attempt++ {
		if attempt > 0 {
			time.Sleep(retryDelays[attempt-1])
		}
		if err = fn(context.Background()); err == nil {
			return
		}
	}
	b.logger.Error("pipeline: batch dropped after retries", "kind", kind, "count", n, "error", err)
}
```

- [ ] **Step 4: Run tests — PASS** (`-race`). **Step 5: Commit** — `git commit -m "feat: bounded ingestion buffer with batch flush, retries, graceful drain"`

---

### Task 13: `internal/server` — HTTP API, origin/CORS, limits

**Files:**
- Create: `internal/server/server.go`, `internal/server/handlers.go`, `internal/server/server_test.go`

**Interfaces:**
- Consumes: `config.Config`, `pipeline.Buffer` (via local interface `type Enqueuer interface { EnqueueHit(store.WebHit); EnqueueEvent(store.ProductEvent) }`), `geo.Provider`, `identity.Salter` (via `type Salt interface { Current(ctx context.Context) (string, error) }`), `enrich`, `uuid.NewV7`.
- Produces:
  - `func New(cfg *config.Config, q Enqueuer, g geo.Provider, salt Salt, logger *slog.Logger) http.Handler`
  - Routes: `POST /api/hit`, `POST /api/event`, `OPTIONS /api/{hit,event}`, `GET /healthz`, `GET /js/script.js` (script route wired in Task 14; return 404 until then)
  - `func clientIP(r *http.Request) string` — `CF-Connecting-IP` → first `X-Forwarded-For` entry → `RemoteAddr` host
  - Behavior contract (spec §5): body limit 16 KB (`http.MaxBytesReader`); JSON parsed regardless of Content-Type (sendBeacon sends text/plain); origin check per project (exact scheme+host match against `allowed_origins`); mismatched Origin → 403; missing Origin allowed for `/api/event` only, 403 for `/api/hit`; unknown project → 204 silent drop; valid → 202 + enqueue; malformed JSON/missing fields → 400; bot UA on /api/hit → 202 but not enqueued; attributes: max 50 keys (excess keys dropped by sorted-key order), keys >64 chars dropped, values marshaled to string then truncated to 512 chars; CORS headers (`Access-Control-Allow-Origin: <origin>`, `Vary: Origin`, `Access-Control-Allow-Headers: Content-Type`, `Access-Control-Max-Age: 86400`) on allowed origins only.

- [ ] **Step 1: Write failing tests**

```go
package server

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/geo"
	"github.com/dmitry/analytics/internal/store"
)

type fakeQueue struct {
	mu     sync.Mutex
	hits   []store.WebHit
	events []store.ProductEvent
}

func (f *fakeQueue) EnqueueHit(h store.WebHit) { f.mu.Lock(); f.hits = append(f.hits, h); f.mu.Unlock() }
func (f *fakeQueue) EnqueueEvent(e store.ProductEvent) {
	f.mu.Lock()
	f.events = append(f.events, e)
	f.mu.Unlock()
}

type fixedSalt struct{}

func (fixedSalt) Current(context.Context) (string, error) { return "test-salt", nil }

const chromeUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

func testServer(t *testing.T) (*fakeQueue, http.Handler) {
	t.Helper()
	cfg, err := config.Parse(strings.NewReader(`{
		"database": "sqlite:///tmp/x.db",
		"projects": [{"id": "app", "name": "App", "allowed_origins": ["https://app.com"]}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	g, _ := geo.New("cloudflare://", t.TempDir(), slog.Default())
	q := &fakeQueue{}
	return q, New(cfg, q, g, fixedSalt{}, slog.Default())
}

func postHit(h http.Handler, origin, body, ua string) *httptest.ResponseRecorder {
	r := httptest.NewRequest("POST", "/api/hit", strings.NewReader(body))
	if origin != "" {
		r.Header.Set("Origin", origin)
	}
	r.Header.Set("User-Agent", ua)
	r.Header.Set("CF-IPCountry", "DE")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	return w
}

func TestHitHappyPath(t *testing.T) {
	q, h := testServer(t)
	w := postHit(h, "https://app.com",
		`{"project_id":"app","url":"https://app.com/pricing?utm_source=hn&secret=x","referrer":"https://news.ycombinator.com/"}`,
		chromeUA)
	if w.Code != 202 {
		t.Fatalf("code = %d body %s", w.Code, w.Body)
	}
	if len(q.hits) != 1 {
		t.Fatalf("hits = %d", len(q.hits))
	}
	hit := q.hits[0]
	if hit.Path != "/pricing" || hit.UTMSource != "hn" || hit.ReferrerSource != "hackernews" ||
		hit.Country != "DE" || hit.Device != "desktop" || hit.Browser != "Chrome" || hit.OS != "Windows" {
		t.Errorf("hit = %+v", hit)
	}
	if hit.ID == "" || hit.VisitorHash == "" || len(hit.VisitorHash) != 16 {
		t.Errorf("id/hash: %+v", hit)
	}
	if w.Header().Get("Access-Control-Allow-Origin") != "https://app.com" {
		t.Error("CORS header missing on allowed origin")
	}
}

func TestHitRejectsBadOrigin(t *testing.T) {
	q, h := testServer(t)
	if w := postHit(h, "https://evil.com", `{"project_id":"app","url":"https://app.com/"}`, chromeUA); w.Code != 403 {
		t.Fatalf("evil origin: code = %d", w.Code)
	}
	if w := postHit(h, "", `{"project_id":"app","url":"https://app.com/"}`, chromeUA); w.Code != 403 {
		t.Fatalf("missing origin on /api/hit: code = %d", w.Code)
	}
	if len(q.hits) != 0 {
		t.Fatal("rejected hits must not enqueue")
	}
}

func TestHitUnknownProjectSilentDrop(t *testing.T) {
	q, h := testServer(t)
	w := postHit(h, "https://app.com", `{"project_id":"nope","url":"https://app.com/"}`, chromeUA)
	if w.Code != 204 {
		t.Fatalf("code = %d, want 204 (no oracle, spec §5.2)", w.Code)
	}
	if len(q.hits) != 0 {
		t.Fatal("unknown project must not enqueue")
	}
}

func TestHitDropsBots(t *testing.T) {
	q, h := testServer(t)
	w := postHit(h, "https://app.com", `{"project_id":"app","url":"https://app.com/"}`,
		"Mozilla/5.0 (compatible; Googlebot/2.1)")
	if w.Code != 202 || len(q.hits) != 0 {
		t.Fatalf("bots: code=%d hits=%d (want 202, 0)", w.Code, len(q.hits))
	}
}

func TestHitBadPayloads(t *testing.T) {
	_, h := testServer(t)
	for name, body := range map[string]string{
		"not json":    "{",
		"no url":      `{"project_id":"app"}`,
		"relative":    `{"project_id":"app","url":"/x"}`,
		"no project":  `{"url":"https://app.com/"}`,
	} {
		if w := postHit(h, "https://app.com", body, chromeUA); w.Code != 400 {
			t.Errorf("%s: code = %d, want 400", name, w.Code)
		}
	}
}

func TestBodyLimit(t *testing.T) {
	_, h := testServer(t)
	big := `{"project_id":"app","url":"https://app.com/","referrer":"` + strings.Repeat("x", 17*1024) + `"}`
	if w := postHit(h, "https://app.com", big, chromeUA); w.Code != 400 && w.Code != 413 {
		t.Fatalf("oversize body: code = %d", w.Code)
	}
}

func TestEventNoOriginAllowed(t *testing.T) {
	q, h := testServer(t)
	r := httptest.NewRequest("POST", "/api/event", strings.NewReader(
		`{"project_id":"app","name":"subscribed","user_id":"u1","attributes":{"plan":"pro","n":7}}`))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 202 || len(q.events) != 1 {
		t.Fatalf("server-side event: code=%d events=%d", w.Code, len(q.events))
	}
	e := q.events[0]
	if e.EventName != "subscribed" || e.UserID != "u1" || e.Attributes["plan"] != "pro" || e.Attributes["n"] != "7" {
		t.Errorf("event = %+v (non-string attr must stringify)", e)
	}
}

func TestEventAttributeLimits(t *testing.T) {
	q, h := testServer(t)
	attrs := map[string]any{strings.Repeat("k", 65): "dropped-key", "ok": strings.Repeat("v", 600)}
	body, _ := json.Marshal(map[string]any{"project_id": "app", "name": "e", "user_id": "u", "attributes": attrs})
	r := httptest.NewRequest("POST", "/api/event", strings.NewReader(string(body)))
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 202 {
		t.Fatalf("code = %d", w.Code)
	}
	e := q.events[0]
	if _, exists := e.Attributes[strings.Repeat("k", 65)]; exists {
		t.Error("over-long key must be dropped")
	}
	if len(e.Attributes["ok"]) != 512 {
		t.Errorf("value must truncate to 512, got %d", len(e.Attributes["ok"]))
	}
}

func TestPreflight(t *testing.T) {
	_, h := testServer(t)
	r := httptest.NewRequest("OPTIONS", "/api/event", nil)
	r.Header.Set("Origin", "https://app.com")
	r.Header.Set("Access-Control-Request-Method", "POST")
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 204 || w.Header().Get("Access-Control-Allow-Origin") != "https://app.com" {
		t.Fatalf("preflight allowed origin: %d %v", w.Code, w.Header())
	}
	r2 := httptest.NewRequest("OPTIONS", "/api/event", nil)
	r2.Header.Set("Origin", "https://evil.com")
	w2 := httptest.NewRecorder()
	h.ServeHTTP(w2, r2)
	if w2.Header().Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("preflight must not allow unknown origins")
	}
}

func TestHealthz(t *testing.T) {
	_, h := testServer(t)
	r := httptest.NewRequest("GET", "/healthz", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("healthz = %d", w.Code)
	}
}
```

- [ ] **Step 2: Run — fails.** **Step 3: Implement**

`server.go`:
```go
// Package server implements the ingestion HTTP API (spec §5). It never
// logs request bodies, IPs, or User-Agents.
package server

import (
	"context"
	"log/slog"
	"net"
	"net/http"
	"strings"

	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/geo"
	"github.com/dmitry/analytics/internal/store"
)

const maxBody = 16 << 10

type Enqueuer interface {
	EnqueueHit(h store.WebHit)
	EnqueueEvent(e store.ProductEvent)
}

type Salt interface {
	Current(ctx context.Context) (string, error)
}

type server struct {
	cfg    *config.Config
	queue  Enqueuer
	geo    geo.Provider
	salt   Salt
	logger *slog.Logger
	// origin -> project index for O(1) allowed-origin checks
	originOK map[string]map[string]bool // projectID -> set of origins
}

func New(cfg *config.Config, q Enqueuer, g geo.Provider, salt Salt, logger *slog.Logger) http.Handler {
	s := &server{cfg: cfg, queue: q, geo: g, salt: salt, logger: logger,
		originOK: map[string]map[string]bool{}}
	for _, p := range cfg.Projects {
		set := map[string]bool{}
		for _, o := range p.AllowedOrigins {
			set[strings.TrimSuffix(o, "/")] = true
		}
		s.originOK[p.ID] = set
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/hit", s.handleHit)
	mux.HandleFunc("POST /api/event", s.handleEvent)
	mux.HandleFunc("OPTIONS /api/hit", s.handlePreflight)
	mux.HandleFunc("OPTIONS /api/event", s.handlePreflight)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	})
	s.registerScript(mux) // Task 14; no-op stub until then
	return mux
}

// originAllowed reports whether the request origin is allowed for the
// project and emits CORS headers when it is.
func (s *server) originAllowed(w http.ResponseWriter, r *http.Request, projectID string) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false
	}
	set, ok := s.originOK[projectID]
	if !ok || !set[strings.TrimSuffix(origin, "/")] {
		return false
	}
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", origin)
	h.Set("Vary", "Origin")
	return true
}

// anyOriginAllowed is used by preflight, where no body (and thus no
// project id) is available: allow iff the origin belongs to any project.
func (s *server) anyOriginAllowed(origin string) bool {
	o := strings.TrimSuffix(origin, "/")
	for _, set := range s.originOK {
		if set[o] {
			return true
		}
	}
	return false
}

func (s *server) handlePreflight(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin != "" && s.anyOriginAllowed(origin) {
		h := w.Header()
		h.Set("Access-Control-Allow-Origin", origin)
		h.Set("Vary", "Origin")
		h.Set("Access-Control-Allow-Methods", "POST, OPTIONS")
		h.Set("Access-Control-Allow-Headers", "Content-Type")
		h.Set("Access-Control-Max-Age", "86400")
	}
	w.WriteHeader(http.StatusNoContent)
}

func clientIP(r *http.Request) string {
	if ip := r.Header.Get("CF-Connecting-IP"); ip != "" {
		return ip
	}
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		return strings.TrimSpace(strings.SplitN(xff, ",", 2)[0])
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
```

`handlers.go`:
```go
package server

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"

	"github.com/dmitry/analytics/internal/enrich"
	"github.com/dmitry/analytics/internal/identity"
	"github.com/dmitry/analytics/internal/store"
	"github.com/google/uuid"
)

type hitPayload struct {
	ProjectID string `json:"project_id"`
	URL       string `json:"url"`
	Referrer  string `json:"referrer"`
}

type eventPayload struct {
	ProjectID  string         `json:"project_id"`
	Name       string         `json:"name"`
	UserID     string         `json:"user_id"`
	Attributes map[string]any `json:"attributes"`
}

func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		http.Error(w, "bad request", http.StatusBadRequest)
		return false
	}
	return true
}

func newID() string {
	id, err := uuid.NewV7()
	if err != nil { // only on entropy exhaustion; fall back to v4
		return uuid.NewString()
	}
	return id.String()
}

func (s *server) handleHit(w http.ResponseWriter, r *http.Request) {
	var p hitPayload
	if !decode(w, r, &p) {
		return
	}
	if p.ProjectID == "" || p.URL == "" {
		http.Error(w, "project_id and url required", http.StatusBadRequest)
		return
	}
	if s.cfg.Project(p.ProjectID) == nil {
		w.WriteHeader(http.StatusNoContent) // silent drop, no oracle
		return
	}
	if !s.originAllowed(w, r, p.ProjectID) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	page, err := enrich.ParsePageURL(p.URL)
	if err != nil {
		http.Error(w, "invalid url", http.StatusBadRequest)
		return
	}
	ua := r.Header.Get("User-Agent")
	if enrich.IsBot(ua) {
		w.WriteHeader(http.StatusAccepted) // accepted, silently ignored
		return
	}
	salt, err := s.salt.Current(r.Context())
	if err != nil {
		s.logger.Error("salt unavailable", "error", err)
		http.Error(w, "internal error", http.StatusInternalServerError)
		return
	}
	device, browser, osName := enrich.ParseUserAgent(ua)
	source := enrich.CleanReferrer(p.Referrer, page.Host)
	if source == "" && page.Ref != "" {
		source = page.Ref // ?ref= fallback (spec §5.4)
	}
	s.queue.EnqueueHit(store.WebHit{
		ID:             newID(),
		ProjectID:      p.ProjectID,
		TS:             time.Now().UTC(),
		VisitorHash:    identity.VisitorHash(salt, clientIP(r), ua, p.ProjectID),
		Path:           page.Path,
		ReferrerSource: source,
		UTMSource:      page.UTMSource,
		UTMMedium:      page.UTMMedium,
		UTMCampaign:    page.UTMCampaign,
		Country:        s.geo.Country(r, clientIP(r)),
		Device:         device,
		Browser:        browser,
		OS:             osName,
	})
	w.WriteHeader(http.StatusAccepted)
}

func (s *server) handleEvent(w http.ResponseWriter, r *http.Request) {
	var p eventPayload
	if !decode(w, r, &p) {
		return
	}
	if p.ProjectID == "" || p.Name == "" || p.UserID == "" {
		http.Error(w, "project_id, name, user_id required", http.StatusBadRequest)
		return
	}
	if s.cfg.Project(p.ProjectID) == nil {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	// Origin, when present, must be allowed; absence is permitted here
	// (server-side SDKs, spec §5.2).
	if origin := r.Header.Get("Origin"); origin != "" && !s.originAllowed(w, r, p.ProjectID) {
		http.Error(w, "origin not allowed", http.StatusForbidden)
		return
	}
	s.queue.EnqueueEvent(store.ProductEvent{
		ID:         newID(),
		ProjectID:  p.ProjectID,
		EventName:  p.Name,
		UserID:     p.UserID,
		TS:         time.Now().UTC(),
		Attributes: sanitizeAttributes(p.Attributes),
	})
	w.WriteHeader(http.StatusAccepted)
}

// sanitizeAttributes applies spec §5.1 limits: ≤50 keys (sorted-key order
// keeps the outcome deterministic), keys ≤64 chars, values stringified and
// truncated to 512 chars.
func sanitizeAttributes(in map[string]any) map[string]string {
	if len(in) == 0 {
		return nil
	}
	keys := make([]string, 0, len(in))
	for k := range in {
		if len(k) > 0 && len(k) <= 64 {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) > 50 {
		keys = keys[:50]
	}
	out := make(map[string]string, len(keys))
	for _, k := range keys {
		var v string
		switch t := in[k].(type) {
		case string:
			v = t
		default:
			b, err := json.Marshal(t)
			if err != nil {
				continue
			}
			v = string(b)
		}
		if len(v) > 512 {
			v = v[:512]
		}
		out[k] = v
	}
	return out
}

var _ = fmt.Sprintf // placeholder to keep import if unused
```

Add to `server.go` a stub `func (s *server) registerScript(mux *http.ServeMux) {}` (Task 14 replaces it).

- [ ] **Step 4: Run tests — PASS.** **Step 5: Commit** — `git commit -m "feat: ingestion HTTP API with origin allowlist, CORS, limits"`

---

### Task 14: tracking script `web/script.js` + serving

**Files:**
- Create: `web/script.js`, `internal/server/script.go`, `internal/server/script_test.go`

**Interfaces:**
- Produces: `GET /js/script.js` serving the embedded snippet with `Content-Type: text/javascript; charset=utf-8`, `Cache-Control: public, max-age=86400`. The script implements spec §5.5 exactly.

- [ ] **Step 1: Write failing tests**

```go
package server

import (
	"net/http/httptest"
	"strings"
	"testing"
)

func TestScriptServed(t *testing.T) {
	_, h := testServer(t)
	r := httptest.NewRequest("GET", "/js/script.js", nil)
	w := httptest.NewRecorder()
	h.ServeHTTP(w, r)
	if w.Code != 200 {
		t.Fatalf("code = %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Errorf("content-type = %q", ct)
	}
	if cc := w.Header().Get("Cache-Control"); !strings.Contains(cc, "max-age") {
		t.Errorf("cache-control = %q", cc)
	}
	body := w.Body.String()
	// Behavioral markers the snippet must contain (spec §5.5 / §5.4a):
	for _, marker := range []string{
		"analytics_ignore",       // opt-out
		"data-project",           // project wiring
		"sendBeacon",             // transport
		"pushState",              // SPA tracking
		"popstate",               // SPA tracking
		"webdriver",              // automation filter
		"/api/hit", "/api/event", // endpoints
		"identify",               // identity API
	} {
		if !strings.Contains(body, marker) {
			t.Errorf("script.js missing %q", marker)
		}
	}
}
```

- [ ] **Step 2: Run — fails.** **Step 3: Implement**

`web/script.js`:
```javascript
/* analytics tracking snippet — cookieless (spec §5.5).
 * <script defer src="https://a.example.com/js/script.js" data-project="myapp"></script>
 * Optional: data-user="uid" to attribute product events.
 */
(function () {
  "use strict";
  var script = document.currentScript;
  if (!script) return;
  var project = script.getAttribute("data-project");
  if (!project) return;
  var endpoint = new URL(script.src).origin;
  var userId = script.getAttribute("data-user") || null;
  var anonId = "anon-" + Math.random().toString(36).slice(2, 12);

  function ignored() {
    try { if (localStorage.analytics_ignore === "true") return true; } catch (e) {}
    if (/^localhost$|^127(\.\d+){3}$|^\[::1\]$/.test(location.hostname)) return true;
    if (location.protocol === "file:") return true;
    if (navigator.webdriver) return true;
    return false;
  }

  function send(path, payload) {
    var body = JSON.stringify(payload);
    // sendBeacon with a string posts text/plain: no CORS preflight.
    if (navigator.sendBeacon && navigator.sendBeacon(endpoint + path, body)) return;
    fetch(endpoint + path, { method: "POST", body: body, keepalive: true });
  }

  var lastPage = null;
  function page() {
    if (ignored()) return;
    var current = location.pathname + location.search;
    if (current === lastPage) return;
    lastPage = current;
    send("/api/hit", { project_id: project, url: location.href, referrer: document.referrer });
  }

  function track(name, attributes) {
    if (ignored() || !name) return;
    send("/api/event", {
      project_id: project,
      name: String(name),
      user_id: userId || anonId,
      attributes: attributes || {}
    });
  }

  var pushState = history.pushState;
  history.pushState = function () {
    pushState.apply(this, arguments);
    page();
  };
  window.addEventListener("popstate", page);

  window.analytics = {
    track: track,
    identify: function (id) { userId = id ? String(id) : null; }
  };

  page();
})();
```

`internal/server/script.go` (replaces the stub):
```go
package server

import (
	_ "embed"
	"net/http"
)

//go:embed script.js
var trackingScript []byte

func (s *server) registerScript(mux *http.ServeMux) {
	mux.HandleFunc("GET /js/script.js", func(w http.ResponseWriter, _ *http.Request) {
		h := w.Header()
		h.Set("Content-Type", "text/javascript; charset=utf-8")
		h.Set("Cache-Control", "public, max-age=86400")
		w.Write(trackingScript)
	})
}
```

`go:embed` cannot reach outside the package dir — keep the canonical file at `internal/server/script.js` and make `web/script.js` a symlink to it (`ln -s ../internal/server/script.js web/script.js`) OR simply keep the single copy at `internal/server/script.js` and drop `web/` (preferred: one copy, no symlink; update the repo-layout doc note in README task accordingly). Delete the Task 13 stub `registerScript`.

- [ ] **Step 4: Run tests — PASS.** **Step 5: Commit** — `git commit -m "feat: embedded cookieless tracking snippet with SPA + opt-out"`

---

### Task 15: sqlite — web aggregation (sessionization) + WebDaysBefore

**Files:**
- Create: `internal/store/sqlite/aggregate_web.go`, `internal/store/sqlite/aggregate_web_test.go`
- Modify: remove corresponding stubs.

**Interfaces:**
- Produces working: `AggregateWebDay(ctx, projectID, day)`, `WebDaysBefore(ctx, projectID, before)`.
- Contract (spec §9): idempotent; skips (no-op) when the day has no raw rows; rollups + raw deletion in ONE transaction; sessions = per-visitor runs split at gaps > 30 min; bounce = single-hit session; duration = per-session `max(t)-min(t)` summed.

- [ ] **Step 1: Write failing tests**

```go
package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/dmitry/analytics/internal/civil"
	"github.com/dmitry/analytics/internal/store"
)

// seedWebDay inserts a deterministic fixture for 2026-08-10, project "app":
//   visitor v1: hits 10:00 /a, 10:10 /b     -> 1 session, 2 pageviews, not bounce, dur 600
//   visitor v1: hit  12:00 /a               -> gap > 30min => 2nd session, bounce, dur 0
//   visitor v2: hit  11:00 /a  (country DE, device mobile, browser Chrome, os Android,
//                               source google, utm hn/social/launch) -> 1 session, bounce
// Totals: visitors 2, pageviews 4, sessions 3, bounces 2, duration 600.
func seedWebDay(t *testing.T, db *DB) {
	t.Helper()
	mk := func(id, vis, path, tsS, source, country, device, browser, osN, us, um, uc string) store.WebHit {
		return store.WebHit{ID: id, ProjectID: "app", TS: ts(tsS), VisitorHash: vis, Path: path,
			ReferrerSource: source, Country: country, Device: device, Browser: browser, OS: osN,
			UTMSource: us, UTMMedium: um, UTMCampaign: uc}
	}
	hits := []store.WebHit{
		mk("1", "v1", "/a", "2026-08-10T10:00:00Z", "", "US", "desktop", "Firefox", "Linux", "", "", ""),
		mk("2", "v1", "/b", "2026-08-10T10:10:00Z", "", "US", "desktop", "Firefox", "Linux", "", "", ""),
		mk("3", "v1", "/a", "2026-08-10T12:00:00Z", "", "US", "desktop", "Firefox", "Linux", "", "", ""),
		mk("4", "v2", "/a", "2026-08-10T11:00:00Z", "google", "DE", "mobile", "Chrome", "Android", "hn", "social", "launch"),
	}
	if err := db.WriteWebHits(context.Background(), hits); err != nil {
		t.Fatal(err)
	}
}

func day(s string) civil.Date {
	d, err := civil.Parse(s)
	if err != nil {
		panic(err)
	}
	return d
}

func TestAggregateWebDay(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedWebDay(t, db)
	if err := db.AggregateWebDay(ctx, "app", day("2026-08-10")); err != nil {
		t.Fatal(err)
	}
	var visitors, pageviews, sessions, bounces, dur int
	err := db.db.QueryRow(`SELECT visitors, pageviews, sessions, bounces, duration_sec
		FROM agg_web_daily WHERE project_id='app' AND day='2026-08-10'`).
		Scan(&visitors, &pageviews, &sessions, &bounces, &dur)
	if err != nil {
		t.Fatal(err)
	}
	if visitors != 2 || pageviews != 4 || sessions != 3 || bounces != 2 || dur != 600 {
		t.Fatalf("daily = v%d pv%d s%d b%d d%d", visitors, pageviews, sessions, bounces, dur)
	}
	// Dimensions
	var pv int
	if err := db.db.QueryRow(`SELECT pageviews FROM agg_web_pages WHERE project_id='app' AND day='2026-08-10' AND path='/a'`).Scan(&pv); err != nil || pv != 3 {
		t.Fatalf("pages /a pv=%d err=%v", pv, err)
	}
	if err := db.db.QueryRow(`SELECT visitors FROM agg_web_countries WHERE project_id='app' AND day='2026-08-10' AND country='DE'`).Scan(&pv); err != nil || pv != 1 {
		t.Fatalf("countries DE v=%d err=%v", pv, err)
	}
	if err := db.db.QueryRow(`SELECT pageviews FROM agg_web_referrers WHERE project_id='app' AND day='2026-08-10' AND source='google'`).Scan(&pv); err != nil || pv != 1 {
		t.Fatalf("referrers google pv=%d err=%v", pv, err)
	}
	if err := db.db.QueryRow(`SELECT pageviews FROM agg_web_utm WHERE project_id='app' AND day='2026-08-10' AND utm_source='hn' AND utm_medium='social' AND utm_campaign='launch'`).Scan(&pv); err != nil || pv != 1 {
		t.Fatalf("utm pv=%d err=%v", pv, err)
	}
	// Empty utm rows are not stored
	var n int
	db.db.QueryRow(`SELECT COUNT(*) FROM agg_web_utm WHERE utm_source='' AND utm_medium='' AND utm_campaign=''`).Scan(&n)
	if n != 0 {
		t.Fatal("all-empty utm combination must not be aggregated")
	}
	// Raw rows deleted in same tx
	db.db.QueryRow(`SELECT COUNT(*) FROM web_hits WHERE project_id='app'`).Scan(&n)
	if n != 0 {
		t.Fatalf("raw rows remaining = %d, want 0", n)
	}
}

func TestAggregateWebDayIdempotent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedWebDay(t, db)
	if err := db.AggregateWebDay(ctx, "app", day("2026-08-10")); err != nil {
		t.Fatal(err)
	}
	// Second run: raw is gone => must be a no-op, not zero out aggregates.
	if err := db.AggregateWebDay(ctx, "app", day("2026-08-10")); err != nil {
		t.Fatal(err)
	}
	var pageviews int
	db.db.QueryRow(`SELECT pageviews FROM agg_web_daily WHERE project_id='app' AND day='2026-08-10'`).Scan(&pageviews)
	if pageviews != 4 {
		t.Fatalf("second run corrupted aggregates: pv=%d", pageviews)
	}
}

func TestAggregateWebDayScopesToProjectAndDay(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedWebDay(t, db)
	other := []store.WebHit{
		{ID: "x1", ProjectID: "other", TS: ts("2026-08-10T10:00:00Z"), VisitorHash: "o1", Path: "/z"},
		{ID: "x2", ProjectID: "app", TS: ts("2026-08-11T10:00:00Z"), VisitorHash: "v9", Path: "/next-day"},
	}
	if err := db.WriteWebHits(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateWebDay(ctx, "app", day("2026-08-10")); err != nil {
		t.Fatal(err)
	}
	var n int
	db.db.QueryRow(`SELECT COUNT(*) FROM web_hits`).Scan(&n)
	if n != 2 {
		t.Fatalf("other project/day raw rows must survive, remaining=%d", n)
	}
}

func TestWebDaysBefore(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedWebDay(t, db) // 2026-08-10
	db.WriteWebHits(ctx, []store.WebHit{
		{ID: "n1", ProjectID: "app", TS: ts("2026-08-15T09:00:00Z"), VisitorHash: "v", Path: "/"},
	})
	days, err := db.WebDaysBefore(ctx, "app", day("2026-08-12"))
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 || days[0].String() != "2026-08-10" {
		t.Fatalf("days = %v", days)
	}
}

var _ = time.Now
```

- [ ] **Step 2: Run — fails.** **Step 3: Implement** `aggregate_web.go`:

```go
package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/dmitry/analytics/internal/civil"
)

// sessionsCTE computes per-session rows for one project+day. A session is
// a per-visitor run of hits with gaps <= 30 min (spec §9).
const sessionsCTE = `
WITH hits AS (
  SELECT visitor_hash, CAST(strftime('%s', ts) AS INTEGER) AS t
  FROM web_hits WHERE project_id = :p AND ts >= :from AND ts < :to
),
marked AS (
  SELECT visitor_hash, t,
         CASE WHEN LAG(t) OVER w IS NULL OR t - LAG(t) OVER w > 1800 THEN 1 ELSE 0 END AS new_session
  FROM hits WINDOW w AS (PARTITION BY visitor_hash ORDER BY t)
),
numbered AS (
  SELECT visitor_hash, t,
         SUM(new_session) OVER (PARTITION BY visitor_hash ORDER BY t) AS session_no
  FROM marked
),
sessions AS (
  SELECT visitor_hash, session_no, COUNT(*) AS hit_count, MAX(t) - MIN(t) AS duration
  FROM numbered GROUP BY visitor_hash, session_no
)`

func dayRange(day civil.Date) (string, string) {
	return day.String() + "T00:00:00Z", day.AddDays(1).String() + "T00:00:00Z"
}

func (d *DB) WebDaysBefore(ctx context.Context, projectID string, before civil.Date) ([]civil.Date, error) {
	return d.daysBefore(ctx, "web_hits", projectID, before)
}

func (d *DB) ProductDaysBefore(ctx context.Context, projectID string, before civil.Date) ([]civil.Date, error) {
	return d.daysBefore(ctx, "product_events", projectID, before)
}

func (d *DB) daysBefore(ctx context.Context, table, projectID string, before civil.Date) ([]civil.Date, error) {
	rows, err := d.db.QueryContext(ctx,
		fmt.Sprintf(`SELECT DISTINCT substr(ts,1,10) FROM %s WHERE project_id=? AND ts < ? ORDER BY 1`, table),
		projectID, before.String()+"T00:00:00Z")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []civil.Date
	for rows.Next() {
		var s string
		if err := rows.Scan(&s); err != nil {
			return nil, err
		}
		day, err := civil.Parse(s)
		if err != nil {
			return nil, err
		}
		out = append(out, day)
	}
	return out, rows.Err()
}

func (d *DB) AggregateWebDay(ctx context.Context, projectID string, day civil.Date) error {
	from, to := dayRange(day)
	return d.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM web_hits WHERE project_id=? AND ts>=? AND ts<?`,
			projectID, from, to).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return nil // already aggregated (or empty day): no-op keeps idempotency
		}
		named := []any{sql.Named("p", projectID), sql.Named("from", from), sql.Named("to", to), sql.Named("day", day.String())}
		if _, err := tx.ExecContext(ctx, sessionsCTE+`
			INSERT OR REPLACE INTO agg_web_daily
			  (project_id, day, visitors, pageviews, sessions, bounces, duration_sec)
			SELECT :p, :day,
			  (SELECT COUNT(DISTINCT visitor_hash) FROM hits),
			  (SELECT COUNT(*) FROM hits),
			  COUNT(*),
			  SUM(CASE WHEN hit_count = 1 THEN 1 ELSE 0 END),
			  COALESCE(SUM(duration), 0)
			FROM sessions`, named...); err != nil {
			return fmt.Errorf("agg_web_daily: %w", err)
		}
		type dim struct{ table, cols, group, where string }
		dims := []dim{
			{"agg_web_pages", "path", "path", ""},
			{"agg_web_referrers", "source", "referrer_source", ""},
			{"agg_web_countries", "country", "country", ""},
			{"agg_web_devices", "device", "device", ""},
			{"agg_web_browsers", "browser", "browser", ""},
			{"agg_web_os", "os", "os", ""},
			{"agg_web_utm", "utm_source, utm_medium, utm_campaign", "utm_source, utm_medium, utm_campaign",
				"AND NOT (utm_source='' AND utm_medium='' AND utm_campaign='')"},
		}
		for _, dm := range dims {
			q := fmt.Sprintf(`INSERT OR REPLACE INTO %s (project_id, day, %s, visitors, pageviews)
				SELECT :p, :day, %s, COUNT(DISTINCT visitor_hash), COUNT(*)
				FROM web_hits
				WHERE project_id = :p AND ts >= :from AND ts < :to %s
				GROUP BY %s`, dm.table, dm.cols, dm.group, dm.where, dm.group)
			if _, err := tx.ExecContext(ctx, q, named...); err != nil {
				return fmt.Errorf("%s: %w", dm.table, err)
			}
		}
		_, err := tx.ExecContext(ctx,
			`DELETE FROM web_hits WHERE project_id=? AND ts>=? AND ts<?`, projectID, from, to)
		return err
	})
}
```

Note the dimension SELECT column list vs group: for the single-column dims `%s` after `:day` selects the *raw column name* (e.g. `referrer_source`) into the differently-named agg column (`source`) — positional INSERT columns make that correct.

- [ ] **Step 4: Run tests — PASS.** **Step 5: Commit** — `git commit -m "feat: web daily aggregation with sessionization, atomic raw pruning"`

---

### Task 16: sqlite — product aggregation (opt-in, attrs top-N)

**Files:**
- Create: `internal/store/sqlite/aggregate_product.go`, `internal/store/sqlite/aggregate_product_test.go`

**Interfaces:**
- Produces working: `AggregateProductDay(ctx, projectID, day, agg store.ProductAggSettings)`.
- Contract (spec §8/§9 + user decision): `agg.Enabled == false` (zero value) ⇒ delete the day's raw rows, write NO aggregates. Enabled ⇒ fill `agg_product_daily` + `agg_product_totals`; for each event's configured attribute keys (union of `agg.Attributes[event]` and `agg.Attributes["*"]`), fill `agg_product_attrs` with top-`agg.TopN` values by count and a `"(other)"` row collapsing the tail (computed with correct distinct-user counts from raw). Raw deletion in the same transaction. Idempotent via the same no-raw-rows no-op guard as web.

- [ ] **Step 1: Write failing tests**

```go
package sqlite

import (
	"context"
	"fmt"
	"testing"

	"github.com/dmitry/analytics/internal/store"
)

// seedProductDay: project "app", day 2026-08-10:
//   subscribed: u1 plan=pro source=ads; u2 plan=free source=ads; u2 plan=free (no source)
//   ping:       u1 (no attrs)
func seedProductDay(t *testing.T, db *DB) {
	t.Helper()
	evs := []store.ProductEvent{
		{ID: "p1", ProjectID: "app", EventName: "subscribed", UserID: "u1", TS: ts("2026-08-10T10:00:00Z"),
			Attributes: map[string]string{"plan": "pro", "source": "ads"}},
		{ID: "p2", ProjectID: "app", EventName: "subscribed", UserID: "u2", TS: ts("2026-08-10T11:00:00Z"),
			Attributes: map[string]string{"plan": "free", "source": "ads"}},
		{ID: "p3", ProjectID: "app", EventName: "subscribed", UserID: "u2", TS: ts("2026-08-10T12:00:00Z"),
			Attributes: map[string]string{"plan": "free"}},
		{ID: "p4", ProjectID: "app", EventName: "ping", UserID: "u1", TS: ts("2026-08-10T13:00:00Z")},
	}
	if err := db.WriteProductEvents(context.Background(), evs); err != nil {
		t.Fatal(err)
	}
}

func TestAggregateProductDisabledDeletesOnly(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedProductDay(t, db)
	if err := db.AggregateProductDay(ctx, "app", day("2026-08-10"), store.ProductAggSettings{}); err != nil {
		t.Fatal(err)
	}
	var n int
	db.db.QueryRow(`SELECT COUNT(*) FROM product_events`).Scan(&n)
	if n != 0 {
		t.Fatalf("raw remaining %d", n)
	}
	db.db.QueryRow(`SELECT COUNT(*) FROM agg_product_daily`).Scan(&n)
	if n != 0 {
		t.Fatal("disabled aggregation must write nothing (spec §4)")
	}
}

func TestAggregateProductEnabled(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedProductDay(t, db)
	agg := store.ProductAggSettings{
		Enabled:    true,
		TopN:       50,
		Attributes: map[string][]string{"subscribed": {"plan"}, "*": {"source"}},
	}
	if err := db.AggregateProductDay(ctx, "app", day("2026-08-10"), agg); err != nil {
		t.Fatal(err)
	}
	var count, uniq int
	if err := db.db.QueryRow(`SELECT count, unique_users FROM agg_product_daily
		WHERE project_id='app' AND day='2026-08-10' AND event_name='subscribed'`).Scan(&count, &uniq); err != nil {
		t.Fatal(err)
	}
	if count != 3 || uniq != 2 {
		t.Fatalf("subscribed: c=%d u=%d", count, uniq)
	}
	if err := db.db.QueryRow(`SELECT total_events, active_users FROM agg_product_totals
		WHERE project_id='app' AND day='2026-08-10'`).Scan(&count, &uniq); err != nil {
		t.Fatal(err)
	}
	if count != 4 || uniq != 2 {
		t.Fatalf("totals: e=%d dau=%d", count, uniq)
	}
	// plan breakdown for subscribed only
	if err := db.db.QueryRow(`SELECT count, unique_users FROM agg_product_attrs
		WHERE project_id='app' AND day='2026-08-10' AND event_name='subscribed'
		AND attr_key='plan' AND attr_value='free'`).Scan(&count, &uniq); err != nil {
		t.Fatal(err)
	}
	if count != 2 || uniq != 1 {
		t.Fatalf("plan=free: c=%d u=%d", count, uniq)
	}
	// wildcard source applies to subscribed (2 with source=ads); ping has no source attr -> no row
	var n int
	db.db.QueryRow(`SELECT COUNT(*) FROM agg_product_attrs WHERE event_name='ping'`).Scan(&n)
	if n != 0 {
		t.Fatal("events without the attribute must produce no attr rows")
	}
	db.db.QueryRow(`SELECT count FROM agg_product_attrs
		WHERE event_name='subscribed' AND attr_key='source' AND attr_value='ads'`).Scan(&count)
	if count != 2 {
		t.Fatalf("source=ads count=%d", count)
	}
	db.db.QueryRow(`SELECT COUNT(*) FROM product_events`).Scan(&n)
	if n != 0 {
		t.Fatal("raw must be deleted after rollup")
	}
}

func TestAggregateProductTopNCollapsesTail(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	var evs []store.ProductEvent
	// 5 distinct values; v0 appears 3x, v1 2x, v2..v4 once each.
	id := 0
	add := func(user, val string) {
		id++
		evs = append(evs, store.ProductEvent{ID: fmt.Sprintf("e%d", id), ProjectID: "app",
			EventName: "clicked", UserID: user, TS: ts("2026-08-10T10:00:00Z"),
			Attributes: map[string]string{"button": val}})
	}
	add("u1", "v0"); add("u2", "v0"); add("u3", "v0")
	add("u1", "v1"); add("u2", "v1")
	add("u1", "v2"); add("u2", "v3"); add("u3", "v4")
	if err := db.WriteProductEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	agg := store.ProductAggSettings{Enabled: true, TopN: 2,
		Attributes: map[string][]string{"clicked": {"button"}}}
	if err := db.AggregateProductDay(ctx, "app", day("2026-08-10"), agg); err != nil {
		t.Fatal(err)
	}
	var n int
	db.db.QueryRow(`SELECT COUNT(*) FROM agg_product_attrs WHERE attr_key='button'`).Scan(&n)
	if n != 3 { // v0, v1, (other)
		t.Fatalf("rows = %d, want 3 (top2 + other)", n)
	}
	var count, uniq int
	if err := db.db.QueryRow(`SELECT count, unique_users FROM agg_product_attrs
		WHERE attr_key='button' AND attr_value='(other)'`).Scan(&count, &uniq); err != nil {
		t.Fatal(err)
	}
	if count != 3 || uniq != 3 { // v2+v3+v4: 3 events by 3 distinct users
		t.Fatalf("(other): c=%d u=%d", count, uniq)
	}
}

func TestAggregateProductIdempotent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedProductDay(t, db)
	agg := store.ProductAggSettings{Enabled: true, TopN: 50}
	must := func(err error) { if err != nil { t.Fatal(err) } }
	must(db.AggregateProductDay(ctx, "app", day("2026-08-10"), agg))
	must(db.AggregateProductDay(ctx, "app", day("2026-08-10"), agg)) // no raw left: no-op
	var c int
	db.db.QueryRow(`SELECT count FROM agg_product_daily WHERE event_name='subscribed'`).Scan(&c)
	if c != 3 {
		t.Fatalf("second run corrupted: %d", c)
	}
}
```

- [ ] **Step 2: Run — fails.** **Step 3: Implement** `aggregate_product.go`:

```go
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/dmitry/analytics/internal/civil"
	"github.com/dmitry/analytics/internal/store"
)

// attrPath builds a JSON path literal for a config-supplied attribute key.
// Keys come from the operator's config, not from clients, but are still
// quoted defensively (spec §8.1 sanitization rules apply to view building;
// here the value is parameter-adjacent SQL, so escape quotes).
func attrPath(key string) string {
	return `$."` + strings.ReplaceAll(key, `"`, `\"`) + `"`
}

func (d *DB) AggregateProductDay(ctx context.Context, projectID string, day civil.Date, agg store.ProductAggSettings) error {
	from, to := dayRange(day)
	return d.tx(ctx, func(tx *sql.Tx) error {
		var n int
		if err := tx.QueryRowContext(ctx,
			`SELECT COUNT(*) FROM product_events WHERE project_id=? AND ts>=? AND ts<?`,
			projectID, from, to).Scan(&n); err != nil {
			return err
		}
		if n == 0 {
			return nil
		}
		if agg.Enabled {
			if err := d.rollupProduct(ctx, tx, projectID, day, from, to, agg); err != nil {
				return err
			}
		}
		_, err := tx.ExecContext(ctx,
			`DELETE FROM product_events WHERE project_id=? AND ts>=? AND ts<?`, projectID, from, to)
		return err
	})
}

func (d *DB) rollupProduct(ctx context.Context, tx *sql.Tx, projectID string, day civil.Date, from, to string, agg store.ProductAggSettings) error {
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO agg_product_daily
		(project_id, day, event_name, count, unique_users)
		SELECT project_id, ?, event_name, COUNT(*), COUNT(DISTINCT user_id)
		FROM product_events WHERE project_id=? AND ts>=? AND ts<?
		GROUP BY event_name`, day.String(), projectID, from, to); err != nil {
		return fmt.Errorf("agg_product_daily: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT OR REPLACE INTO agg_product_totals
		(project_id, day, total_events, active_users)
		SELECT project_id, ?, COUNT(*), COUNT(DISTINCT user_id)
		FROM product_events WHERE project_id=? AND ts>=? AND ts<?
		GROUP BY project_id`, day.String(), projectID, from, to); err != nil {
		return fmt.Errorf("agg_product_totals: %w", err)
	}
	// Attribute breakdowns: resolve per event name present that day.
	rows, err := tx.QueryContext(ctx, `SELECT DISTINCT event_name FROM product_events
		WHERE project_id=? AND ts>=? AND ts<?`, projectID, from, to)
	if err != nil {
		return err
	}
	var events []string
	for rows.Next() {
		var e string
		if err := rows.Scan(&e); err != nil {
			rows.Close()
			return err
		}
		events = append(events, e)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for _, event := range events {
		keys := map[string]bool{}
		for _, k := range agg.Attributes[event] {
			keys[k] = true
		}
		for _, k := range agg.Attributes["*"] {
			keys[k] = true
		}
		for key := range keys {
			if err := d.rollupAttr(ctx, tx, projectID, day, from, to, event, key, agg.TopN); err != nil {
				return fmt.Errorf("attr %s/%s: %w", event, key, err)
			}
		}
	}
	return nil
}

func (d *DB) rollupAttr(ctx context.Context, tx *sql.Tx, projectID string, day civil.Date, from, to, event, key string, topN int) error {
	path := attrPath(key)
	named := []any{
		sql.Named("p", projectID), sql.Named("day", day.String()),
		sql.Named("from", from), sql.Named("to", to),
		sql.Named("event", event), sql.Named("key", key),
		sql.Named("path", path), sql.Named("n", topN),
	}
	// Top-N values by count.
	if _, err := tx.ExecContext(ctx, `
		WITH counted AS (
		  SELECT json_extract(attributes, :path) AS v, COUNT(*) AS c, COUNT(DISTINCT user_id) AS u
		  FROM product_events
		  WHERE project_id=:p AND ts>=:from AND ts<:to AND event_name=:event
		    AND json_extract(attributes, :path) IS NOT NULL
		  GROUP BY v
		),
		ranked AS (SELECT v, c, u, ROW_NUMBER() OVER (ORDER BY c DESC, v) AS rn FROM counted)
		INSERT OR REPLACE INTO agg_product_attrs
		  (project_id, day, event_name, attr_key, attr_value, count, unique_users)
		SELECT :p, :day, :event, :key, v, c, u FROM ranked WHERE rn <= :n`, named...); err != nil {
		return err
	}
	// Tail -> "(other)" with correct distinct users, computed from raw.
	_, err := tx.ExecContext(ctx, `
		WITH counted AS (
		  SELECT json_extract(attributes, :path) AS v, COUNT(*) AS c
		  FROM product_events
		  WHERE project_id=:p AND ts>=:from AND ts<:to AND event_name=:event
		    AND json_extract(attributes, :path) IS NOT NULL
		  GROUP BY v
		),
		ranked AS (SELECT v, ROW_NUMBER() OVER (ORDER BY c DESC, v) AS rn FROM counted),
		keep AS (SELECT v FROM ranked WHERE rn <= :n)
		INSERT OR REPLACE INTO agg_product_attrs
		  (project_id, day, event_name, attr_key, attr_value, count, unique_users)
		SELECT :p, :day, :event, :key, '(other)', COUNT(*), COUNT(DISTINCT user_id)
		FROM product_events
		WHERE project_id=:p AND ts>=:from AND ts<:to AND event_name=:event
		  AND json_extract(attributes, :path) IS NOT NULL
		  AND json_extract(attributes, :path) NOT IN (SELECT v FROM keep)
		HAVING COUNT(*) > 0`, named...)
	return err
}
```

- [ ] **Step 4: Run tests — PASS.** **Step 5: Commit** — `git commit -m "feat: opt-in product aggregation with attr top-N and (other) collapse"`

---

### Task 17: sqlite — aggregate pruning + incremental vacuum

**Files:**
- Create: `internal/store/sqlite/prune.go`, `internal/store/sqlite/prune_test.go`

**Interfaces:**
- Produces working: `PruneAggregates(ctx, projectID, webBefore, productBefore civil.Date)` — deletes rows with `day < webBefore` from all `agg_web_*` tables and `day < productBefore` from all `agg_product_*` tables, scoped to the project; `IncrementalVacuum(ctx)` — `PRAGMA incremental_vacuum(1000)`.

- [ ] **Step 1: Write failing tests**

```go
package sqlite

import (
	"context"
	"testing"
)

func TestPruneAggregates(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	exec := func(q string, args ...any) {
		if _, err := db.db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	// Seed old + new aggregate rows across representative tables.
	exec(`INSERT INTO agg_web_daily VALUES ('app','2025-01-01',1,1,1,0,0), ('app','2026-08-01',2,2,2,0,0)`)
	exec(`INSERT INTO agg_web_pages VALUES ('app','2025-01-01','/',1,1), ('app','2026-08-01','/',2,2)`)
	exec(`INSERT INTO agg_product_daily VALUES ('app','2025-06-01','e',1,1), ('app','2026-08-01','e',2,2)`)
	exec(`INSERT INTO agg_product_attrs VALUES ('app','2025-06-01','e','k','v',1,1), ('app','2026-08-01','e','k','v',2,2)`)
	// Different project must be untouched.
	exec(`INSERT INTO agg_web_daily VALUES ('other','2025-01-01',9,9,9,0,0)`)

	if err := db.PruneAggregates(ctx, "app", day("2026-01-01"), day("2026-01-01")); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, tbl := range []string{"agg_web_daily", "agg_web_pages", "agg_product_daily", "agg_product_attrs"} {
		var n int
		db.db.QueryRow(`SELECT COUNT(*) FROM `+tbl+` WHERE project_id='app'`).Scan(&n)
		counts[tbl] = n
	}
	for tbl, n := range counts {
		if n != 1 {
			t.Errorf("%s: %d rows for app, want 1 (old pruned, new kept)", tbl, n)
		}
	}
	var n int
	db.db.QueryRow(`SELECT COUNT(*) FROM agg_web_daily WHERE project_id='other'`).Scan(&n)
	if n != 1 {
		t.Error("other project must be untouched")
	}
}

func TestIncrementalVacuumRuns(t *testing.T) {
	db := newTestDB(t)
	if err := db.IncrementalVacuum(context.Background()); err != nil {
		t.Fatal(err)
	}
}
```

- [ ] **Step 2: Run — fails.** **Step 3: Implement** `prune.go`:

```go
package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/dmitry/analytics/internal/civil"
)

var webAggTables = []string{
	"agg_web_daily", "agg_web_pages", "agg_web_referrers", "agg_web_countries",
	"agg_web_devices", "agg_web_browsers", "agg_web_os", "agg_web_utm",
}

var productAggTables = []string{"agg_product_daily", "agg_product_totals", "agg_product_attrs"}

func (d *DB) PruneAggregates(ctx context.Context, projectID string, webBefore, productBefore civil.Date) error {
	return d.tx(ctx, func(tx *sql.Tx) error {
		for _, tbl := range webAggTables {
			if _, err := tx.ExecContext(ctx,
				fmt.Sprintf(`DELETE FROM %s WHERE project_id=? AND day < ?`, tbl),
				projectID, webBefore.String()); err != nil {
				return fmt.Errorf("prune %s: %w", tbl, err)
			}
		}
		for _, tbl := range productAggTables {
			if _, err := tx.ExecContext(ctx,
				fmt.Sprintf(`DELETE FROM %s WHERE project_id=? AND day < ?`, tbl),
				projectID, productBefore.String()); err != nil {
				return fmt.Errorf("prune %s: %w", tbl, err)
			}
		}
		return nil
	})
}

func (d *DB) IncrementalVacuum(ctx context.Context) error {
	_, err := d.db.ExecContext(ctx, `PRAGMA incremental_vacuum(1000)`)
	return err
}
```

- [ ] **Step 4: Run tests — PASS.** **Step 5: Commit** — `git commit -m "feat: aggregate retention pruning and incremental vacuum"`

---

### Task 18: sqlite — dynamic `v_events_flat` view

**Files:**
- Create: `internal/store/sqlite/flatview.go`, `internal/store/sqlite/flatview_test.go`

**Interfaces:**
- Produces working: `KnownAttributeKeys(ctx) ([]string, error)` (distinct keys via `json_each` over `product_events`), `RebuildFlatView(ctx, keys []string) error`.
- Sanitization contract (spec §8.1): alias = key with `[^a-zA-Z0-9_]` stripped; prefixed with `attr_` always (avoids clashing with base columns and digit-leading names); collisions after sanitizing get `_2`, `_3`... suffixes; empty-after-sanitizing keys are skipped; the JSON path embeds the ORIGINAL key with `"` escaped. View columns: `id, project_id, event_name, user_id, ts` + one per key. DROP+CREATE in one transaction.

- [ ] **Step 1: Write failing tests**

```go
package sqlite

import (
	"context"
	"sort"
	"testing"

	"github.com/dmitry/analytics/internal/store"
)

func TestKnownAttributeKeys(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	evs := []store.ProductEvent{
		{ID: "1", ProjectID: "app", EventName: "e", UserID: "u", TS: ts("2026-08-10T10:00:00Z"),
			Attributes: map[string]string{"plan": "pro", "weird key!": "x"}},
		{ID: "2", ProjectID: "app", EventName: "e", UserID: "u", TS: ts("2026-08-10T11:00:00Z"),
			Attributes: map[string]string{"plan": "free", "source": "ads"}},
	}
	if err := db.WriteProductEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	keys, err := db.KnownAttributeKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(keys)
	want := []string{"plan", "source", "weird key!"}
	if len(keys) != 3 || keys[0] != want[0] || keys[1] != want[1] || keys[2] != want[2] {
		t.Fatalf("keys = %v, want %v", keys, want)
	}
}

func TestRebuildFlatView(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	evs := []store.ProductEvent{
		{ID: "1", ProjectID: "app", EventName: "e", UserID: "u1", TS: ts("2026-08-10T10:00:00Z"),
			Attributes: map[string]string{"plan": "pro"}},
	}
	if err := db.WriteProductEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFlatView(ctx, []string{"plan"}); err != nil {
		t.Fatal(err)
	}
	var plan string
	if err := db.db.QueryRow(`SELECT attr_plan FROM v_events_flat WHERE id='1'`).Scan(&plan); err != nil {
		t.Fatal(err)
	}
	if plan != "pro" {
		t.Fatalf("attr_plan = %q", plan)
	}
	// Rebuild with more keys replaces the view.
	if err := db.RebuildFlatView(ctx, []string{"plan", "source"}); err != nil {
		t.Fatal(err)
	}
	var src *string
	if err := db.db.QueryRow(`SELECT attr_source FROM v_events_flat WHERE id='1'`).Scan(&src); err != nil {
		t.Fatal(err)
	}
	if src != nil {
		t.Fatalf("missing attr must be NULL, got %v", *src)
	}
}

func TestRebuildFlatViewHostileKeys(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	// Injection attempts and collisions must not break the DDL.
	hostile := []string{
		`x"; DROP TABLE product_events; --`,
		"weird key!",
		"weird-key.", // collides with "weird key!" after sanitizing
		"1starts_with_digit",
		"漢字",         // sanitizes to empty -> skipped
	}
	if err := db.RebuildFlatView(ctx, hostile); err != nil {
		t.Fatal(err)
	}
	// product_events must still exist (no injection).
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM product_events`).Scan(&n); err != nil {
		t.Fatalf("table gone — injection succeeded: %v", err)
	}
	// Collisions produce distinct columns.
	rows, err := db.db.Query(`SELECT name FROM pragma_table_info('v_events_flat')`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	cols := map[string]bool{}
	for rows.Next() {
		var c string
		rows.Scan(&c)
		cols[c] = true
	}
	if !cols["attr_weirdkey"] || !cols["attr_weirdkey_2"] {
		t.Fatalf("collision suffixing failed: %v", cols)
	}
}
```

- [ ] **Step 2: Run — fails.** **Step 3: Implement** `flatview.go`:

```go
package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

func (d *DB) KnownAttributeKeys(ctx context.Context) ([]string, error) {
	rows, err := d.db.QueryContext(ctx,
		`SELECT DISTINCT je.key FROM product_events, json_each(product_events.attributes) je`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var keys []string
	for rows.Next() {
		var k string
		if err := rows.Scan(&k); err != nil {
			return nil, err
		}
		keys = append(keys, k)
	}
	return keys, rows.Err()
}

func sanitizeAlias(key string) string {
	var b strings.Builder
	for _, r := range key {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '_' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (d *DB) RebuildFlatView(ctx context.Context, keys []string) error {
	sorted := append([]string(nil), keys...)
	sort.Strings(sorted) // deterministic column order
	cols := []string{"id", "project_id", "event_name", "user_id", "ts"}
	used := map[string]bool{}
	for _, key := range sorted {
		alias := sanitizeAlias(key)
		if alias == "" {
			continue
		}
		alias = "attr_" + alias
		if used[alias] {
			for i := 2; ; i++ {
				candidate := fmt.Sprintf("%s_%d", alias, i)
				if !used[candidate] {
					alias = candidate
					break
				}
			}
		}
		used[alias] = true
		// Original key goes inside a JSON path literal; only " needs escaping
		// there. Single quotes in the SQL string literal are doubled.
		path := `$."` + strings.ReplaceAll(key, `"`, `\"`) + `"`
		pathLit := strings.ReplaceAll(path, `'`, `''`)
		cols = append(cols, fmt.Sprintf(`json_extract(attributes, '%s') AS %s`, pathLit, alias))
	}
	return d.tx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, `DROP VIEW IF EXISTS v_events_flat`); err != nil {
			return err
		}
		_, err := tx.ExecContext(ctx, fmt.Sprintf(
			`CREATE VIEW v_events_flat AS SELECT %s FROM product_events`,
			strings.Join(cols, ", ")))
		return err
	})
}
```

- [ ] **Step 4: Run tests — PASS.** **Step 5: Commit** — `git commit -m "feat: dynamic v_events_flat with hostile-key-safe DDL"`

---

### Task 19: sqlite — stitch views (migration 002)

**Files:**
- Create: `internal/store/sqlite/migrations/002_views.sql`, `internal/store/sqlite/views_test.go`

**Interfaces:**
- Produces: views `v_web_daily`, `v_web_pages`, `v_web_referrers`, `v_web_countries`, `v_web_devices`, `v_web_browsers`, `v_web_os`, `v_web_utm`, `v_product_daily`, `v_product_totals` — each `agg UNION ALL live-from-raw` (raw and aggregated days are disjoint by construction: aggregation deletes raw in the same tx).

- [ ] **Step 1: Write failing tests**

```go
package sqlite

import (
	"context"
	"testing"
)

// The invariant that makes Evidence dashboards boundary-free (spec §8.1):
// v_* views must return IDENTICAL numbers before and after aggregation.
func TestStitchViewsInvariantWeb(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedWebDay(t, db) // raw only

	read := func() (v, pv, s, b, d int) {
		err := db.db.QueryRow(`SELECT visitors, pageviews, sessions, bounces, duration_sec
			FROM v_web_daily WHERE project_id='app' AND day='2026-08-10'`).Scan(&v, &pv, &s, &b, &d)
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	v1, pv1, s1, b1, d1 := read()
	if err := db.AggregateWebDay(ctx, "app", day("2026-08-10")); err != nil {
		t.Fatal(err)
	}
	v2, pv2, s2, b2, d2 := read()
	if v1 != v2 || pv1 != pv2 || s1 != s2 || b1 != b2 || d1 != d2 {
		t.Fatalf("stitch mismatch: before (%d %d %d %d %d) after (%d %d %d %d %d)",
			v1, pv1, s1, b1, d1, v2, pv2, s2, b2, d2)
	}
	var pages int
	if err := db.db.QueryRow(`SELECT pageviews FROM v_web_pages
		WHERE project_id='app' AND day='2026-08-10' AND path='/a'`).Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if pages != 3 {
		t.Fatalf("v_web_pages /a = %d", pages)
	}
}

func TestStitchViewsInvariantProduct(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedProductDay(t, db)
	read := func() (c, u int) {
		err := db.db.QueryRow(`SELECT count, unique_users FROM v_product_daily
			WHERE project_id='app' AND day='2026-08-10' AND event_name='subscribed'`).Scan(&c, &u)
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	c1, u1 := read()
	agg := store.ProductAggSettings{Enabled: true, TopN: 50}
	if err := db.AggregateProductDay(ctx, "app", day("2026-08-10"), agg); err != nil {
		t.Fatal(err)
	}
	c2, u2 := read()
	if c1 != c2 || u1 != u2 {
		t.Fatalf("product stitch mismatch: (%d,%d) vs (%d,%d)", c1, u1, c2, u2)
	}
	var dau int
	if err := db.db.QueryRow(`SELECT active_users FROM v_product_totals
		WHERE project_id='app' AND day='2026-08-10'`).Scan(&dau); err != nil {
		t.Fatal(err)
	}
	if dau != 2 {
		t.Fatalf("dau = %d", dau)
	}
}
```

(add `"github.com/dmitry/analytics/internal/store"` import)

- [ ] **Step 2: Run — fails (views missing).** **Step 3: Implement** `002_views.sql`:

```sql
-- Stitch views: aggregates UNION ALL live computation over raw rows.
-- Raw and aggregated days are disjoint (aggregation deletes raw in-tx),
-- so no day is double counted.

CREATE VIEW v_web_daily AS
SELECT project_id, day, visitors, pageviews, sessions, bounces, duration_sec FROM agg_web_daily
UNION ALL
SELECT c.project_id, c.day, c.visitors, c.pageviews, p.sessions, p.bounces, p.duration_sec
FROM (
  SELECT project_id, substr(ts,1,10) AS day,
         COUNT(DISTINCT visitor_hash) AS visitors, COUNT(*) AS pageviews
  FROM web_hits GROUP BY project_id, substr(ts,1,10)
) c
JOIN (
  WITH base AS (
    SELECT project_id, substr(ts,1,10) AS day, visitor_hash,
           CAST(strftime('%s', ts) AS INTEGER) AS t
    FROM web_hits
  ),
  marked AS (
    SELECT project_id, day, visitor_hash, t,
           CASE WHEN LAG(t) OVER w IS NULL OR t - LAG(t) OVER w > 1800 THEN 1 ELSE 0 END AS new_session
    FROM base WINDOW w AS (PARTITION BY project_id, day, visitor_hash ORDER BY t)
  ),
  numbered AS (
    SELECT project_id, day, visitor_hash, t,
           SUM(new_session) OVER (PARTITION BY project_id, day, visitor_hash ORDER BY t) AS session_no
    FROM marked
  ),
  per_session AS (
    SELECT project_id, day, visitor_hash, session_no,
           COUNT(*) AS hit_count, MAX(t) - MIN(t) AS duration
    FROM numbered GROUP BY project_id, day, visitor_hash, session_no
  )
  SELECT project_id, day, COUNT(*) AS sessions,
         SUM(CASE WHEN hit_count = 1 THEN 1 ELSE 0 END) AS bounces,
         COALESCE(SUM(duration), 0) AS duration_sec
  FROM per_session GROUP BY project_id, day
) p ON p.project_id = c.project_id AND p.day = c.day;

CREATE VIEW v_web_pages AS
SELECT project_id, day, path, visitors, pageviews FROM agg_web_pages
UNION ALL
SELECT project_id, substr(ts,1,10), path, COUNT(DISTINCT visitor_hash), COUNT(*)
FROM web_hits GROUP BY project_id, substr(ts,1,10), path;

CREATE VIEW v_web_referrers AS
SELECT project_id, day, source, visitors, pageviews FROM agg_web_referrers
UNION ALL
SELECT project_id, substr(ts,1,10), referrer_source, COUNT(DISTINCT visitor_hash), COUNT(*)
FROM web_hits GROUP BY project_id, substr(ts,1,10), referrer_source;

CREATE VIEW v_web_countries AS
SELECT project_id, day, country, visitors, pageviews FROM agg_web_countries
UNION ALL
SELECT project_id, substr(ts,1,10), country, COUNT(DISTINCT visitor_hash), COUNT(*)
FROM web_hits GROUP BY project_id, substr(ts,1,10), country;

CREATE VIEW v_web_devices AS
SELECT project_id, day, device, visitors, pageviews FROM agg_web_devices
UNION ALL
SELECT project_id, substr(ts,1,10), device, COUNT(DISTINCT visitor_hash), COUNT(*)
FROM web_hits GROUP BY project_id, substr(ts,1,10), device;

CREATE VIEW v_web_browsers AS
SELECT project_id, day, browser, visitors, pageviews FROM agg_web_browsers
UNION ALL
SELECT project_id, substr(ts,1,10), browser, COUNT(DISTINCT visitor_hash), COUNT(*)
FROM web_hits GROUP BY project_id, substr(ts,1,10), browser;

CREATE VIEW v_web_os AS
SELECT project_id, day, os, visitors, pageviews FROM agg_web_os
UNION ALL
SELECT project_id, substr(ts,1,10), os, COUNT(DISTINCT visitor_hash), COUNT(*)
FROM web_hits GROUP BY project_id, substr(ts,1,10), os;

CREATE VIEW v_web_utm AS
SELECT project_id, day, utm_source, utm_medium, utm_campaign, visitors, pageviews FROM agg_web_utm
UNION ALL
SELECT project_id, substr(ts,1,10), utm_source, utm_medium, utm_campaign,
       COUNT(DISTINCT visitor_hash), COUNT(*)
FROM web_hits
WHERE NOT (utm_source='' AND utm_medium='' AND utm_campaign='')
GROUP BY project_id, substr(ts,1,10), utm_source, utm_medium, utm_campaign;

CREATE VIEW v_product_daily AS
SELECT project_id, day, event_name, count, unique_users FROM agg_product_daily
UNION ALL
SELECT project_id, substr(ts,1,10), event_name, COUNT(*), COUNT(DISTINCT user_id)
FROM product_events GROUP BY project_id, substr(ts,1,10), event_name;

CREATE VIEW v_product_totals AS
SELECT project_id, day, total_events, active_users FROM agg_product_totals
UNION ALL
SELECT project_id, substr(ts,1,10), COUNT(*), COUNT(DISTINCT user_id)
FROM product_events GROUP BY project_id, substr(ts,1,10);
```

- [ ] **Step 4: Run tests — PASS** (the invariant test is the point: identical numbers pre/post aggregation). **Step 5: Commit** — `git commit -m "feat: stitch views hiding the raw/aggregate boundary"`

---

### Task 20: `internal/jobs` — scheduler (aggregate, prune, salt, flat view)

**Files:**
- Create: `internal/jobs/jobs.go`, `internal/jobs/jobs_test.go`

**Interfaces:**
- Consumes: `store.Store`, `config.Config`, `identity.Salter` (via `type Rotator interface { Rotate(ctx context.Context) error; Current(ctx context.Context) (string, error) }`).
- Produces:
  - `func New(st store.Store, cfg *config.Config, salt Rotator, logger *slog.Logger, now func() time.Time) *Runner`
  - `func (r *Runner) RunDailyPass(ctx context.Context) error` — the whole daily batch, callable directly (tests call this; boot catch-up calls this): for every project in config → aggregate web days `< today - web.raw_days`, aggregate product days `< today - product.raw_days` (with the project's `ProductAggSettings` mapped from config), prune aggregates `< today - aggregate_days`, then `IncrementalVacuum`; also `RebuildFlatView(ctx, KnownAttributeKeys())`. Uses per-project `cfg.RetentionFor`. Projects present only in the DB (archived) use global retention.
  - `func (r *Runner) Run(ctx context.Context)` — loop: on start run `RunDailyPass` (boot catch-up, spec §9) and ensure salt via `Current`; then tick every minute; at 00:00 UTC call `salt.Rotate`; at 03:00 UTC call `RunDailyPass`. Guard so each fires at most once per day (remember last-run day).
  - `func aggSettingsFor(p *config.Project) store.ProductAggSettings` — nil/absent → zero value.

- [ ] **Step 1: Write failing tests** — drive `RunDailyPass` against the real sqlite store:

```go
package jobs

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/identity"
	"github.com/dmitry/analytics/internal/store"
	_ "github.com/dmitry/analytics/internal/store/sqlite"
)

func setup(t *testing.T, cfgJSON string) (store.Store, *config.Config, *Runner) {
	t.Helper()
	cfg, err := config.Parse(strings.NewReader(cfgJSON))
	if err != nil {
		t.Fatal(err)
	}
	st, err := store.Open("sqlite://" + filepath.Join(t.TempDir(), "jobs.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	now := func() time.Time { return time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC) }
	salter := identity.NewSalter(st, now)
	return st, cfg, New(st, cfg, salter, slog.Default(), now)
}

const jobsCfg = `{
  "database": "sqlite:///unused",
  "retention": {"web": {"raw_days": 7, "aggregate_days": 365}, "product": {"raw_days": 7, "aggregate_days": 365}},
  "projects": [{"id": "app", "name": "App", "allowed_origins": ["https://a.com"],
    "product_aggregation": {"enabled": true, "attributes": {"*": ["plan"]}, "top_n": 50}}]
}`

func mustTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func TestRunDailyPassAggregatesOldDays(t *testing.T) {
	st, _, r := setup(t, jobsCfg)
	ctx := context.Background()
	// Old day (beyond 7-day raw window relative to fake now 2026-08-22).
	st.WriteWebHits(ctx, []store.WebHit{
		{ID: "1", ProjectID: "app", TS: mustTime("2026-08-10T10:00:00Z"), VisitorHash: "v", Path: "/"}})
	st.WriteProductEvents(ctx, []store.ProductEvent{
		{ID: "2", ProjectID: "app", EventName: "e", UserID: "u", TS: mustTime("2026-08-10T10:00:00Z"),
			Attributes: map[string]string{"plan": "pro"}}})
	// Recent day (inside window) must survive raw.
	st.WriteWebHits(ctx, []store.WebHit{
		{ID: "3", ProjectID: "app", TS: mustTime("2026-08-21T10:00:00Z"), VisitorHash: "v", Path: "/"}})
	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}
	oldWeb, _ := st.WebDaysBefore(ctx, "app", mustDay("2026-08-20"))
	if len(oldWeb) != 0 {
		t.Fatalf("old web raw must be gone: %v", oldWeb)
	}
	recent, _ := st.WebDaysBefore(ctx, "app", mustDay("2026-08-23"))
	if len(recent) != 1 {
		t.Fatalf("recent raw must survive: %v", recent)
	}
	oldProd, _ := st.ProductDaysBefore(ctx, "app", mustDay("2026-08-20"))
	if len(oldProd) != 0 {
		t.Fatalf("old product raw must be gone: %v", oldProd)
	}
	// Second pass is a no-op (idempotency at the job level).
	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}
}

func mustDay(s string) civil.Date { d, _ := civil.Parse(s); return d }
```

(add `"github.com/dmitry/analytics/internal/civil"` import)

- [ ] **Step 2: Run — fails.** **Step 3: Implement**

```go
// Package jobs schedules the daily maintenance work (spec §9): salt
// rotation at 00:00 UTC, aggregation+prune at 03:00 UTC, with a catch-up
// pass on boot so downtime never skips days.
package jobs

import (
	"context"
	"log/slog"
	"time"

	"github.com/dmitry/analytics/internal/civil"
	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/store"
)

type Rotator interface {
	Rotate(ctx context.Context) error
	Current(ctx context.Context) (string, error)
}

type Runner struct {
	store  store.Store
	cfg    *config.Config
	salt   Rotator
	logger *slog.Logger
	now    func() time.Time
}

func New(st store.Store, cfg *config.Config, salt Rotator, logger *slog.Logger, now func() time.Time) *Runner {
	return &Runner{store: st, cfg: cfg, salt: salt, logger: logger, now: now}
}

func aggSettingsFor(p *config.Project) store.ProductAggSettings {
	if p == nil || p.ProductAggregation == nil {
		return store.ProductAggSettings{}
	}
	return store.ProductAggSettings{
		Enabled:    p.ProductAggregation.Enabled,
		Attributes: p.ProductAggregation.Attributes,
		TopN:       p.ProductAggregation.TopN,
	}
}

func (r *Runner) RunDailyPass(ctx context.Context) error {
	today := civil.Today(r.now())
	ids, err := r.store.ProjectIDs(ctx)
	if err != nil {
		return err
	}
	// Config projects may not be synced yet on first boot; union them in.
	seen := map[string]bool{}
	for _, id := range ids {
		seen[id] = true
	}
	for _, p := range r.cfg.Projects {
		if !seen[p.ID] {
			ids = append(ids, p.ID)
		}
	}
	for _, id := range ids {
		ret := r.cfg.RetentionFor(id) // archived/unknown ids fall back to global
		webCutoff := today.AddDays(-ret.Web.RawDays)
		days, err := r.store.WebDaysBefore(ctx, id, webCutoff)
		if err != nil {
			return err
		}
		for _, day := range days {
			if err := r.store.AggregateWebDay(ctx, id, day); err != nil {
				r.logger.Error("aggregate web failed", "project", id, "day", day.String(), "error", err)
			}
		}
		prodCutoff := today.AddDays(-ret.Product.RawDays)
		prodDays, err := r.store.ProductDaysBefore(ctx, id, prodCutoff)
		if err != nil {
			return err
		}
		settings := aggSettingsFor(r.cfg.Project(id))
		for _, day := range prodDays {
			if err := r.store.AggregateProductDay(ctx, id, day, settings); err != nil {
				r.logger.Error("aggregate product failed", "project", id, "day", day.String(), "error", err)
			}
		}
		if err := r.store.PruneAggregates(ctx, id,
			today.AddDays(-ret.Web.AggregateDays), today.AddDays(-ret.Product.AggregateDays)); err != nil {
			r.logger.Error("prune failed", "project", id, "error", err)
		}
	}
	if keys, err := r.store.KnownAttributeKeys(ctx); err == nil {
		if err := r.store.RebuildFlatView(ctx, keys); err != nil {
			r.logger.Error("flat view rebuild failed", "error", err)
		}
	}
	if err := r.store.IncrementalVacuum(ctx); err != nil {
		r.logger.Error("incremental vacuum failed", "error", err)
	}
	return nil
}

func (r *Runner) Run(ctx context.Context) {
	if _, err := r.salt.Current(ctx); err != nil {
		r.logger.Error("initial salt", "error", err)
	}
	if err := r.RunDailyPass(ctx); err != nil {
		r.logger.Error("boot catch-up pass", "error", err)
	}
	var lastSaltDay, lastAggDay string
	tick := time.NewTicker(time.Minute)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			now := r.now().UTC()
			day := civil.Today(now).String()
			if now.Hour() == 0 && lastSaltDay != day {
				lastSaltDay = day
				if err := r.salt.Rotate(ctx); err != nil {
					r.logger.Error("salt rotation", "error", err)
				}
			}
			if now.Hour() == 3 && lastAggDay != day {
				lastAggDay = day
				if err := r.RunDailyPass(ctx); err != nil {
					r.logger.Error("daily pass", "error", err)
				}
			}
		}
	}
}
```

- [ ] **Step 4: Run tests — PASS.** **Step 5: Commit** — `git commit -m "feat: daily maintenance scheduler with boot catch-up"`

---

### Task 21: `serve` + `migrate` wiring, graceful shutdown, end-to-end test

**Files:**
- Create: `cmd/analytics/serve.go`, `cmd/analytics/migrate.go`, `internal/app/app.go`, `internal/app/app_test.go`

**Interfaces:**
- Produces:
  - `internal/app`: `func Serve(ctx context.Context, cfg *config.Config, logger *slog.Logger) error` — wires store→migrate→SyncProjects→flat-view seed (`RebuildFlatView(KnownAttributeKeys())`)→salter→geo→pipeline→server→jobs; starts `http.Server`; on ctx cancel: shut down HTTP (5s), cancel pipeline (final flush), stop jobs, close store/geo. `func NewLogger(cfg config.LogConfig) *slog.Logger` — JSON/text handler at configured level, to stdout or `cfg.File`.
  - `cmd/analytics/serve.go` registers `serve` (flag `-config`, default `/etc/analytics/config.json`; SIGTERM/SIGINT → cancel). `migrate.go` registers `migrate` (opens store, `Migrate`, exits).
- The e2e test is the acceptance test for the whole VPS side (spec §1–§9): boots `Serve` on a random port with a temp DB, posts real hits/events over HTTP, restarts, verifies aggregation.

- [ ] **Step 1: Write failing e2e test**

```go
package app

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/store"
	_ "github.com/dmitry/analytics/internal/store/sqlite"
)

const chromeUA = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"

func freePort(t *testing.T) string {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

func TestServeEndToEnd(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "e2e.db")
	addr := freePort(t)
	cfg, err := config.Parse(strings.NewReader(fmt.Sprintf(`{
		"listen": %q,
		"database": "sqlite://%s",
		"buffer": {"flush_max_events": 2, "flush_interval": "50ms", "capacity": 100},
		"projects": [{"id": "app", "name": "App", "allowed_origins": ["https://app.com"]}]
	}`, addr, dbPath)))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, cfg, slog.Default()) }()

	base := "http://" + addr
	waitHealthy(t, base)

	post := func(path, origin, body string) *http.Response {
		req, _ := http.NewRequest("POST", base+path, strings.NewReader(body))
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		req.Header.Set("User-Agent", chromeUA)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp
	}
	if r := post("/api/hit", "https://app.com",
		`{"project_id":"app","url":"https://app.com/pricing","referrer":""}`); r.StatusCode != 202 {
		t.Fatalf("hit: %d", r.StatusCode)
	}
	if r := post("/api/event", "",
		`{"project_id":"app","name":"signup","user_id":"u1","attributes":{"plan":"pro"}}`); r.StatusCode != 202 {
		t.Fatalf("event: %d", r.StatusCode)
	}
	if r := post("/api/hit", "https://evil.com",
		`{"project_id":"app","url":"https://app.com/"}`); r.StatusCode != 403 {
		t.Fatalf("evil origin: %d", r.StatusCode)
	}

	// Graceful shutdown must flush the buffer.
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("serve did not shut down")
	}

	st, err := store.Open("sqlite://" + dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	days, err := st.WebDaysBefore(context.Background(), "app", mustDay("2100-01-01"))
	if err != nil || len(days) != 1 {
		t.Fatalf("web hit not persisted: %v %v", days, err)
	}
	pdays, err := st.ProductDaysBefore(context.Background(), "app", mustDay("2100-01-01"))
	if err != nil || len(pdays) != 1 {
		t.Fatalf("event not persisted: %v %v", pdays, err)
	}
}

func waitHealthy(t *testing.T, base string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(base + "/healthz"); err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("server never became healthy")
}

func mustDay(s string) civil.Date { d, _ := civil.Parse(s); return d }
```

(add `"github.com/dmitry/analytics/internal/civil"` import)

- [ ] **Step 2: Run — fails.** **Step 3: Implement**

`internal/app/app.go`:
```go
// Package app wires the serve subcommand (spec §1, §5, §9).
package app

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/geo"
	"github.com/dmitry/analytics/internal/identity"
	"github.com/dmitry/analytics/internal/jobs"
	"github.com/dmitry/analytics/internal/pipeline"
	"github.com/dmitry/analytics/internal/server"
	"github.com/dmitry/analytics/internal/store"
	_ "github.com/dmitry/analytics/internal/store/sqlite"
)

func NewLogger(cfg config.LogConfig) *slog.Logger {
	var level slog.Level
	switch cfg.Level {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	out := os.Stdout
	if cfg.File != "" {
		if f, err := os.OpenFile(cfg.File, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o640); err == nil {
			out = f
		}
	}
	opts := &slog.HandlerOptions{Level: level}
	if cfg.Format == "text" {
		return slog.New(slog.NewTextHandler(out, opts))
	}
	return slog.New(slog.NewJSONHandler(out, opts))
}

func Serve(ctx context.Context, cfg *config.Config, logger *slog.Logger) error {
	st, err := store.Open(cfg.Database)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		return err
	}
	infos := make([]store.ProjectInfo, 0, len(cfg.Projects))
	for _, p := range cfg.Projects {
		infos = append(infos, store.ProjectInfo{ID: p.ID, Name: p.Name})
	}
	if err := st.SyncProjects(ctx, infos); err != nil {
		return err
	}
	if keys, err := st.KnownAttributeKeys(ctx); err == nil {
		if err := st.RebuildFlatView(ctx, keys); err != nil {
			logger.Warn("flat view seed", "error", err)
		}
	}

	salter := identity.NewSalter(st, time.Now)
	dataDir := filepath.Dir(databasePath(cfg.Database))
	geoProvider, err := geo.New(cfg.Geo, dataDir, logger)
	if err != nil {
		return err
	}
	defer geoProvider.Close()

	buf := pipeline.New(cfg.Buffer, st, logger)
	pipeCtx, stopPipe := context.WithCancel(context.Background())
	pipeDone := make(chan struct{})
	go func() { buf.Run(pipeCtx); close(pipeDone) }()

	runner := jobs.New(st, cfg, salter, logger, time.Now)
	jobsCtx, stopJobs := context.WithCancel(context.Background())
	jobsDone := make(chan struct{})
	go func() { runner.Run(jobsCtx); close(jobsDone) }()

	srv := &http.Server{
		Addr:              cfg.Listen,
		Handler:           server.New(cfg, buf, geoProvider, salter, logger),
		ReadHeaderTimeout: 5 * time.Second,
	}
	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()
	logger.Info("serving", "addr", cfg.Listen, "projects", len(cfg.Projects))

	select {
	case <-ctx.Done():
	case err := <-errCh:
		stopPipe()
		stopJobs()
		return err
	}
	shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	srv.Shutdown(shutCtx)
	stopJobs()
	<-jobsDone
	stopPipe() // triggers final flush
	<-pipeDone
	if err := <-errCh; err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}

// databasePath extracts the filesystem path from a sqlite DSN for use as
// the data dir (GeoLite2 DB lives next to the database).
func databasePath(dsn string) string {
	const prefix = "sqlite://"
	if len(dsn) > len(prefix) && dsn[:len(prefix)] == prefix {
		return dsn[len(prefix):]
	}
	return dsn
}
```

`cmd/analytics/serve.go`:
```go
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/dmitry/analytics/internal/app"
	"github.com/dmitry/analytics/internal/config"
)

func init() {
	commands["serve"] = cmdServe
}

func cmdServe(args []string, stdout io.Writer) int {
	fs := flag.NewFlagSet("serve", flag.ContinueOnError)
	cfgPath := fs.String("config", "/etc/analytics/config.json", "path to config.json")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(stdout, err)
		return 1
	}
	logger := app.NewLogger(cfg.Log)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Serve(ctx, cfg, logger); err != nil {
		logger.Error("serve failed", "error", err)
		return 1
	}
	return 0
}
```

`cmd/analytics/migrate.go`:
```go
package main

import (
	"context"
	"flag"
	"fmt"
	"io"

	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/store"
	_ "github.com/dmitry/analytics/internal/store/sqlite"
)

func init() {
	commands["migrate"] = func(args []string, stdout io.Writer) int {
		fs := flag.NewFlagSet("migrate", flag.ContinueOnError)
		cfgPath := fs.String("config", "/etc/analytics/config.json", "path to config.json")
		if err := fs.Parse(args); err != nil {
			return 2
		}
		cfg, err := config.Load(*cfgPath)
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		st, err := store.Open(cfg.Database)
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		defer st.Close()
		if err := st.Migrate(context.Background()); err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		fmt.Fprintln(stdout, "migrations applied")
		return 0
	}
}
```

Note: `server.New` takes the pipeline `*pipeline.Buffer` as its `Enqueuer` and `identity.Salter` as its `Salt` — both already satisfy the interfaces.

- [ ] **Step 4: Run — `go test -race ./internal/app/ -v` PASS; `make build && ./analytics serve -config /nonexistent` exits 1 with a clear error.**
- [ ] **Step 5: Commit** — `git commit -m "feat: serve/migrate commands with graceful shutdown, e2e test"`

---

### Task 22: `sync` subcommand — backoffice replica loop

**Files:**
- Create: `internal/synccmd/sync.go`, `internal/synccmd/sync_test.go`, `cmd/analytics/sync.go`

**Interfaces:**
- Consumes: `config.SyncConfig`.
- Produces:
  - `func RunOnce(ctx context.Context, cfg config.SyncConfig, dbDSN string, logger *slog.Logger) error` — one cycle (spec §10): (1) exec `litestream restore -config <cfg> -o <tmp> <dbPath>` where tmp = `ReplicaPath + ".tmp"` and dbPath = the source path from `dbDSN`; (2) open tmp read-only and run `PRAGMA quick_check` expecting `ok`; (3) `os.Rename(tmp, ReplicaPath)`; (4) touch marker `<dir>/.last_sync`. Any step failing removes tmp and returns the error (existing replica untouched).
  - `func Run(ctx context.Context, cfg config.SyncConfig, dbDSN string, logger *slog.Logger) error` — RunOnce immediately, then every `cfg.Interval` until ctx done; per-cycle errors are logged, not fatal.
  - Exec seam: `var execCommand = exec.CommandContext` (tests point it at a helper that copies a fixture DB to `-o`).
  - `cmd/analytics/sync.go`: registers `sync` (flag `-config`), calls `Run` with `cfg.Sync` and `cfg.Database`.

- [ ] **Step 1: Write failing tests**

```go
package synccmd

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/store"
	_ "github.com/dmitry/analytics/internal/store/sqlite"
)

// makeValidDB creates a real migrated sqlite file to act as the "restored"
// artifact litestream would produce.
func makeValidDB(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "source.db")
	st, err := store.Open("sqlite://" + path)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	st.Close()
	return path
}

func stubLitestream(t *testing.T, fixture string, fail bool) {
	t.Helper()
	old := execCommand
	execCommand = func(ctx context.Context, name string, args ...string) *exec.Cmd {
		// find -o argument
		out := ""
		for i, a := range args {
			if a == "-o" && i+1 < len(args) {
				out = args[i+1]
			}
		}
		if fail {
			return exec.CommandContext(ctx, "false")
		}
		return exec.CommandContext(ctx, "cp", fixture, out)
	}
	t.Cleanup(func() { execCommand = old })
}

func TestRunOnceHappyPath(t *testing.T) {
	fixture := makeValidDB(t)
	dir := t.TempDir()
	replica := filepath.Join(dir, "replica.db")
	stubLitestream(t, fixture, false)
	cfg := config.SyncConfig{ReplicaPath: replica, LitestreamConfig: "/etc/litestream.yml"}
	if err := RunOnce(context.Background(), cfg, "sqlite:///var/lib/analytics/analytics.db", slog.Default()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(replica); err != nil {
		t.Fatal("replica missing")
	}
	if _, err := os.Stat(filepath.Join(dir, ".last_sync")); err != nil {
		t.Fatal("marker missing")
	}
	if _, err := os.Stat(replica + ".tmp"); !os.IsNotExist(err) {
		t.Fatal("tmp must not linger")
	}
}

func TestRunOnceRestoreFailureKeepsReplica(t *testing.T) {
	fixture := makeValidDB(t)
	dir := t.TempDir()
	replica := filepath.Join(dir, "replica.db")
	os.WriteFile(replica, []byte("existing"), 0o644)
	stubLitestream(t, fixture, true)
	cfg := config.SyncConfig{ReplicaPath: replica}
	if err := RunOnce(context.Background(), cfg, "sqlite:///x.db", slog.Default()); err == nil {
		t.Fatal("restore failure must error")
	}
	data, _ := os.ReadFile(replica)
	if string(data) != "existing" {
		t.Fatal("failed cycle must not touch existing replica")
	}
}

func TestRunOnceCorruptRestoreRejected(t *testing.T) {
	dir := t.TempDir()
	garbage := filepath.Join(dir, "garbage")
	os.WriteFile(garbage, []byte("not a database"), 0o644)
	replica := filepath.Join(dir, "replica.db")
	stubLitestream(t, garbage, false)
	cfg := config.SyncConfig{ReplicaPath: replica}
	if err := RunOnce(context.Background(), cfg, "sqlite:///x.db", slog.Default()); err == nil {
		t.Fatal("quick_check must reject a corrupt restore")
	}
	if _, err := os.Stat(replica); !os.IsNotExist(err) {
		t.Fatal("corrupt file must not become the replica")
	}
}

var _ = fmt.Sprintf
```

- [ ] **Step 2: Run — fails.** **Step 3: Implement**

```go
// Package synccmd maintains the backoffice read replica (spec §10, split
// topology): restore from object storage, verify, swap atomically.
package synccmd

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/dmitry/analytics/internal/config"
	_ "modernc.org/sqlite"
)

var execCommand = exec.CommandContext

func sourcePath(dbDSN string) string {
	return strings.TrimPrefix(dbDSN, "sqlite://")
}

func RunOnce(ctx context.Context, cfg config.SyncConfig, dbDSN string, logger *slog.Logger) error {
	tmp := cfg.ReplicaPath + ".tmp"
	defer os.Remove(tmp)

	args := []string{"restore"}
	if cfg.LitestreamConfig != "" {
		args = append(args, "-config", cfg.LitestreamConfig)
	}
	args = append(args, "-o", tmp, sourcePath(dbDSN))
	cmd := execCommand(ctx, "litestream", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("sync: litestream restore: %w (%s)", err, strings.TrimSpace(string(out)))
	}

	if err := quickCheck(ctx, tmp); err != nil {
		return fmt.Errorf("sync: restored file failed verification: %w", err)
	}
	if err := os.Rename(tmp, cfg.ReplicaPath); err != nil {
		return fmt.Errorf("sync: swap replica: %w", err)
	}
	marker := filepath.Join(filepath.Dir(cfg.ReplicaPath), ".last_sync")
	if err := os.WriteFile(marker, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o644); err != nil {
		logger.Warn("sync: marker write failed", "error", err)
	}
	logger.Info("sync: replica updated", "path", cfg.ReplicaPath)
	return nil
}

func quickCheck(ctx context.Context, path string) error {
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		return err
	}
	defer db.Close()
	var result string
	if err := db.QueryRowContext(ctx, "PRAGMA quick_check").Scan(&result); err != nil {
		return err
	}
	if result != "ok" {
		return fmt.Errorf("quick_check: %s", result)
	}
	return nil
}

func Run(ctx context.Context, cfg config.SyncConfig, dbDSN string, logger *slog.Logger) error {
	if err := RunOnce(ctx, cfg, dbDSN, logger); err != nil {
		logger.Error("sync cycle failed", "error", err)
	}
	t := time.NewTicker(cfg.Interval.Duration)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
			if err := RunOnce(ctx, cfg, dbDSN, logger); err != nil {
				logger.Error("sync cycle failed", "error", err)
			}
		}
	}
}
```

`cmd/analytics/sync.go`:
```go
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/dmitry/analytics/internal/app"
	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/synccmd"
)

func init() {
	commands["sync"] = func(args []string, stdout io.Writer) int {
		fs := flag.NewFlagSet("sync", flag.ContinueOnError)
		cfgPath := fs.String("config", "/etc/analytics/config.json", "path to config.json")
		if err := fs.Parse(args); err != nil {
			return 2
		}
		cfg, err := config.Load(*cfgPath)
		if err != nil {
			fmt.Fprintln(stdout, err)
			return 1
		}
		logger := app.NewLogger(cfg.Log)
		ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
		defer stop()
		if err := synccmd.Run(ctx, cfg.Sync, cfg.Database, logger); err != nil {
			logger.Error("sync failed", "error", err)
			return 1
		}
		return 0
	}
}
```

- [ ] **Step 4: Run tests — PASS.** **Step 5: Commit** — `git commit -m "feat: sync subcommand with verified atomic replica swap"`

---

### Task 23: Dockerfile + docker compose (all-in-one and split)

**Files:**
- Create: `Dockerfile`, `.dockerignore`, `backoffice/docker-compose.aio.yml`, `backoffice/docker-compose.yml`, `backoffice/evidence-entrypoint.sh`, `deploy/litestream/litestream.yml`

**Interfaces:**
- Produces: image `analytics` containing `/usr/local/bin/analytics` + `/usr/local/bin/litestream`; compose files per spec §1 topologies. Evidence container details land in Task 24 — the entrypoint script is created here, the Evidence project it builds arrives next task.

- [ ] **Step 1: Write the files**

`Dockerfile`:
```dockerfile
# syntax=docker/dockerfile:1
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
ARG VERSION=dev
RUN CGO_ENABLED=0 go build -trimpath -ldflags "-s -w -X main.version=${VERSION}" \
    -o /out/analytics ./cmd/analytics

FROM litestream/litestream:0.3.13 AS litestream

FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata && adduser -D -H -u 10001 analytics
COPY --from=build /out/analytics /usr/local/bin/analytics
COPY --from=litestream /usr/local/bin/litestream /usr/local/bin/litestream
USER analytics
ENTRYPOINT ["/usr/local/bin/analytics"]
CMD ["serve", "-config", "/etc/analytics/config.json"]
```

`.dockerignore`:
```
.git
dist/
coverage.out
*.db
docs/
backoffice/evidence/node_modules
```

`deploy/litestream/litestream.yml` (used by both topologies; env-driven):
```yaml
# Litestream replication config (spec §4.1 of the original concept).
# R2 credentials come from the environment: LITESTREAM_ACCESS_KEY_ID,
# LITESTREAM_SECRET_ACCESS_KEY.
dbs:
  - path: /var/lib/analytics/analytics.db
    replicas:
      - type: s3
        bucket: ${R2_BUCKET}
        path: litestream
        endpoint: ${R2_ENDPOINT}   # https://<account_id>.r2.cloudflarestorage.com
        sync-interval: 5s
```

`backoffice/docker-compose.aio.yml` — all-in-one (spec §1 topology 2: the analytics server itself runs here):
```yaml
# All-in-one: ingestion + backup + dashboards on one machine.
#   docker compose -f docker-compose.aio.yml up -d
services:
  analytics:
    image: analytics:latest
    build: ..
    restart: unless-stopped
    command: ["serve", "-config", "/etc/analytics/config.json"]
    ports:
      - "8080:8080"          # put Caddy/nginx/Cloudflare in front for TLS
    volumes:
      - ./config.json:/etc/analytics/config.json:ro
      - data:/var/lib/analytics
    environment:
      - GOMEMLIMIT=128MiB

  litestream:
    image: litestream/litestream:0.3.13
    restart: unless-stopped
    command: ["replicate", "-config", "/etc/litestream.yml"]
    volumes:
      - ../deploy/litestream/litestream.yml:/etc/litestream.yml:ro
      - data:/var/lib/analytics
    environment:
      - R2_BUCKET=${R2_BUCKET}
      - R2_ENDPOINT=${R2_ENDPOINT}
      - LITESTREAM_ACCESS_KEY_ID=${R2_ACCESS_KEY}
      - LITESTREAM_SECRET_ACCESS_KEY=${R2_SECRET_KEY}

  evidence:
    image: node:20-alpine
    restart: unless-stopped
    working_dir: /app
    command: ["sh", "/entrypoint.sh"]
    ports:
      - "3000:3000"
    volumes:
      - ./evidence:/app
      - ./evidence-entrypoint.sh:/entrypoint.sh:ro
      - data:/data:ro              # reads the LIVE db read-only (WAL readers ok)
    environment:
      - EVIDENCE_SOURCE__analytics__filename=/data/analytics.db

  # Split-topology variant of this service (uncomment on a backoffice-only
  # machine, remove `analytics`+`litestream` above, and point Evidence at
  # /data/replica.db):
  # sync:
  #   image: analytics:latest
  #   command: ["sync", "-config", "/etc/analytics/config.json"]
  #   volumes:
  #     - ./config.json:/etc/analytics/config.json:ro
  #     - ../deploy/litestream/litestream.yml:/etc/litestream.yml:ro
  #     - data:/data
  #   environment:
  #     - LITESTREAM_ACCESS_KEY_ID=${R2_ACCESS_KEY}
  #     - LITESTREAM_SECRET_ACCESS_KEY=${R2_SECRET_KEY}

volumes:
  data:
```

`backoffice/docker-compose.yml` — split topology backoffice (two services, spec §1 topology 1):
```yaml
# Backoffice for the split topology: replica sync + dashboards only.
# The VPS runs `analytics serve` + litestream via deploy/install.sh.
services:
  sync:
    image: analytics:latest
    build: ..
    restart: unless-stopped
    command: ["sync", "-config", "/etc/analytics/config.json"]
    volumes:
      - ./config.json:/etc/analytics/config.json:ro
      - ../deploy/litestream/litestream.yml:/etc/litestream.yml:ro
      - data:/data
    environment:
      - R2_BUCKET=${R2_BUCKET}
      - R2_ENDPOINT=${R2_ENDPOINT}
      - LITESTREAM_ACCESS_KEY_ID=${R2_ACCESS_KEY}
      - LITESTREAM_SECRET_ACCESS_KEY=${R2_SECRET_KEY}

  evidence:
    image: node:20-alpine
    restart: unless-stopped
    working_dir: /app
    command: ["sh", "/entrypoint.sh"]
    ports:
      - "3000:3000"
    volumes:
      - ./evidence:/app
      - ./evidence-entrypoint.sh:/entrypoint.sh:ro
      - data:/data:ro
    environment:
      - EVIDENCE_SOURCE__analytics__filename=/data/replica.db

volumes:
  data:
```

Note: for the split compose, `config.json`'s `sync` block must use `"replica_path": "/data/replica.db"` and `"litestream_config": "/etc/litestream.yml"`.

`backoffice/evidence-entrypoint.sh`:
```sh
#!/bin/sh
# Build-and-serve loop for Evidence: rebuild whenever the replica marker
# changes (or every 15 min in aio mode where there is no marker).
set -e
cd /app
[ -d node_modules ] || npm ci
last=""
build() {
  npm run sources && npm run build && echo "evidence: rebuilt $(date -u)"
}
build || true
(npx --yes http-server ./build -p 3000 -s &) 
while true; do
  cur=$(cat /data/.last_sync 2>/dev/null || date -u +%Y-%m-%dT%H:%M)
  if [ "$cur" != "$last" ]; then
    last="$cur"
    build || echo "evidence: build failed, serving previous"
  fi
  sleep 60
done
```

- [ ] **Step 2: Verify** — `docker build -t analytics:latest .` succeeds; `docker run --rm analytics:latest version` prints the version; `docker compose -f backoffice/docker-compose.aio.yml config` and `docker compose -f backoffice/docker-compose.yml config` both validate (compose config only — no need to boot the stack; the Evidence loop is exercised in Task 24). If docker is unavailable in the environment, note it in the commit and rely on CI/user verification.
- [ ] **Step 3: Commit** — `git commit -m "feat: docker image (analytics+litestream) and compose topologies"`

---

### Task 24: Evidence project — preconfigured dashboards

**Files:**
- Create: `backoffice/evidence/package.json`, `backoffice/evidence/evidence.config.yaml`, `backoffice/evidence/sources/analytics/connection.yaml`, `backoffice/evidence/pages/index.md`, `backoffice/evidence/pages/web/[project].md`, `backoffice/evidence/pages/product/[project].md`, `backoffice/evidence/.gitignore`

**Interfaces:**
- Consumes: the stitch views (`v_web_*`, `v_product_*`), `projects` table, `v_events_flat`.
- Produces: an Evidence project that works with ONLY the env var `EVIDENCE_SOURCE__analytics__filename` set (spec §10: zero-setup).

- [ ] **Step 1: Write the files**

`package.json`:
```json
{
  "name": "analytics-dashboards",
  "private": true,
  "scripts": {
    "dev": "evidence dev",
    "sources": "evidence sources",
    "build": "evidence build"
  },
  "dependencies": {
    "@evidence-dev/core-components": "^4",
    "@evidence-dev/evidence": "^39",
    "@evidence-dev/sqlite": "^2"
  }
}
```

(Adjust the three version ranges to current majors at implementation time with `npm view <pkg> version`; pin what `npm install` resolves by committing `package-lock.json`.)

`evidence.config.yaml`:
```yaml
title: Analytics
```

`sources/analytics/connection.yaml`:
```yaml
name: analytics
type: sqlite
options:
  filename: /data/analytics.db   # overridden by EVIDENCE_SOURCE__analytics__filename
```

`.gitignore`:
```
node_modules/
.evidence/
build/
```

`pages/index.md`:
````markdown
# Analytics

```sql projects
select id, name, archived_at is not null as archived
from analytics.projects
order by archived, name
```

## Projects

{#each projects.filter(p => !p.archived) as p}

- [{p.name} — web](/web/{p.id}) · [product](/product/{p.id})

{/each}

{#if projects.filter(p => p.archived).length > 0}

### Archived

{#each projects.filter(p => p.archived) as p}

- [{p.name} — web](/web/{p.id}) · [product](/product/{p.id})

{/each}

{/if}
````

`pages/web/[project].md`:
````markdown
# {params.project} — Web

```sql daily
select day, visitors, pageviews, sessions,
       case when sessions > 0 then bounces * 1.0 / sessions else 0 end as bounce_rate,
       case when sessions > 0 then duration_sec * 1.0 / sessions else 0 end as avg_session_sec
from analytics.v_web_daily
where project_id = '${params.project}' and day >= date('now', '-90 days')
order by day
```

<BigValue data={daily} value=visitors sparkline=day comparison=pageviews />

<LineChart data={daily} x=day y={["visitors","pageviews"]} title="Visitors & Pageviews (90d)" />

<LineChart data={daily} x=day y=bounce_rate yFmt=pct title="Bounce rate" />

```sql pages
select path, sum(visitors) as visitors, sum(pageviews) as pageviews
from analytics.v_web_pages
where project_id = '${params.project}' and day >= date('now', '-30 days')
group by path order by pageviews desc limit 20
```

```sql referrers
select source, sum(visitors) as visitors, sum(pageviews) as pageviews
from analytics.v_web_referrers
where project_id = '${params.project}' and day >= date('now', '-30 days') and source != ''
group by source order by visitors desc limit 20
```

```sql countries
select country, sum(visitors) as visitors
from analytics.v_web_countries
where project_id = '${params.project}' and day >= date('now', '-30 days') and country != ''
group by country order by visitors desc limit 20
```

```sql devices
select device, sum(visitors) as visitors from analytics.v_web_devices
where project_id = '${params.project}' and day >= date('now', '-30 days')
group by device order by visitors desc
```

```sql browsers
select browser, sum(visitors) as visitors from analytics.v_web_browsers
where project_id = '${params.project}' and day >= date('now', '-30 days') and browser != ''
group by browser order by visitors desc limit 10
```

```sql oses
select os, sum(visitors) as visitors from analytics.v_web_os
where project_id = '${params.project}' and day >= date('now', '-30 days') and os != ''
group by os order by visitors desc limit 10
```

```sql campaigns
select utm_source, utm_medium, utm_campaign, sum(visitors) as visitors, sum(pageviews) as pageviews
from analytics.v_web_utm
where project_id = '${params.project}' and day >= date('now', '-30 days')
group by utm_source, utm_medium, utm_campaign order by visitors desc limit 20
```

## Top pages (30d)
<DataTable data={pages} rows=10 />

## Referrers (30d)
<DataTable data={referrers} rows=10 />

## Countries (30d)
<BarChart data={countries} x=country y=visitors swapXY=true />

## Devices / Browsers / OS (30d)
<BarChart data={devices} x=device y=visitors />
<BarChart data={browsers} x=browser y=visitors swapXY=true />
<BarChart data={oses} x=os y=visitors swapXY=true />

## Campaigns (30d)
<DataTable data={campaigns} rows=10 />
````

`pages/product/[project].md`:
````markdown
# {params.project} — Product

```sql totals
select day, total_events, active_users
from analytics.v_product_totals
where project_id = '${params.project}' and day >= date('now', '-90 days')
order by day
```

<BigValue data={totals} value=active_users sparkline=day title="DAU" />

<LineChart data={totals} x=day y=active_users title="Daily active users (90d)" />
<LineChart data={totals} x=day y=total_events title="Events per day (90d)" />

```sql events
select day, event_name, count, unique_users
from analytics.v_product_daily
where project_id = '${params.project}' and day >= date('now', '-90 days')
order by day
```

<LineChart data={events} x=day y=count series=event_name title="Events by name" />

```sql event_summary
select event_name, sum(count) as total, max(unique_users) as peak_daily_uniques
from analytics.v_product_daily
where project_id = '${params.project}' and day >= date('now', '-30 days')
group by event_name order by total desc
```

## Events (30d)
<DataTable data={event_summary} />

```sql attr_breakdowns
select day, event_name, attr_key, attr_value, count, unique_users
from analytics.agg_product_attrs
where project_id = '${params.project}' and day >= date('now', '-90 days')
order by day
```

{#if attr_breakdowns.length > 0}

## Attribute breakdowns
<DataTable data={attr_breakdowns} rows=20 groupBy=attr_key />

{/if}
````

- [ ] **Step 2: Verify** — with node available: `cd backoffice/evidence && npm install && EVIDENCE_SOURCE__analytics__filename=/tmp/ev-test.db npm run sources && npm run build`, where `/tmp/ev-test.db` is produced by a small Go helper run (`go run ./cmd/analytics migrate -config <testcfg>` on a temp config) plus a few inserted rows via `sqlite3`-free means (write a tiny `go run` snippet under `/tmp`, or reuse the e2e test DB). Fix Evidence component/prop mismatches per its current docs (`WebFetch https://docs.evidence.dev` if syntax has drifted). Commit `package-lock.json`. If node is unavailable, mark this task's verification as deferred and say so in the commit message.
- [ ] **Step 3: Commit** — `git commit -m "feat: preconfigured Evidence dashboards (web + product + projects index)"`

---

### Task 25: deploy — install.sh, systemd units, logrotate

**Files:**
- Create: `deploy/install.sh`, `deploy/systemd/analytics.service`, `deploy/systemd/litestream.service`, `deploy/logrotate/analytics`, `deploy/config.example.json`

- [ ] **Step 1: Write the files**

`deploy/config.example.json`:
```json
{
  "listen": "127.0.0.1:8080",
  "database": "sqlite:///var/lib/analytics/analytics.db",
  "geo": "cloudflare://",
  "log": { "level": "info", "format": "json" },
  "buffer": { "flush_max_events": 1000, "flush_interval": "5s", "capacity": 10000 },
  "retention": {
    "web": { "raw_days": 7, "aggregate_days": 365 },
    "product": { "raw_days": 30, "aggregate_days": 365 }
  },
  "sync": {
    "interval": "5m",
    "litestream_config": "/etc/litestream.yml",
    "replica_path": "/data/replica.db"
  },
  "projects": [
    {
      "id": "myapp",
      "name": "My App",
      "allowed_origins": ["https://myapp.com"]
    }
  ]
}
```

`deploy/systemd/analytics.service` (hardening per spec §11):
```ini
[Unit]
Description=Ultra-lite analytics collector
After=network-online.target
Wants=network-online.target

[Service]
User=__USER__
Group=__USER__
ExecStart=/usr/local/bin/analytics serve -config /etc/analytics/config.json
Restart=on-failure
RestartSec=2
TimeoutStopSec=30
Environment=GOMEMLIMIT=128MiB

# Hardening (spec §11)
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=/var/lib/analytics
ProtectHome=yes
PrivateTmp=yes
PrivateDevices=yes
ProtectKernelTunables=yes
ProtectKernelModules=yes
ProtectControlGroups=yes
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX
RestrictNamespaces=yes
RestrictRealtime=yes
RestrictSUIDSGID=yes
LockPersonality=yes
MemoryDenyWriteExecute=yes
CapabilityBoundingSet=
AmbientCapabilities=
SystemCallFilter=@system-service
SystemCallErrorNumber=EPERM

[Install]
WantedBy=multi-user.target
```

`deploy/systemd/litestream.service`:
```ini
[Unit]
Description=Litestream replication for analytics
After=network-online.target analytics.service
Wants=network-online.target

[Service]
User=__USER__
Group=__USER__
ExecStart=/usr/local/bin/litestream replicate -config /etc/litestream.yml
EnvironmentFile=/etc/analytics/litestream.env
Restart=on-failure
RestartSec=5
NoNewPrivileges=yes
ProtectSystem=strict
ReadWritePaths=/var/lib/analytics
ProtectHome=yes
PrivateTmp=yes

[Install]
WantedBy=multi-user.target
```

`deploy/logrotate/analytics` (only for the optional file-logging mode, spec §11):
```
/var/log/analytics/*.log {
    weekly
    rotate 8
    compress
    delaycompress
    missingok
    notifempty
    copytruncate
}
```

`deploy/install.sh`:
```bash
#!/usr/bin/env bash
# Installer for the analytics collector (spec §11).
# Usage: sudo ./install.sh [--user NAME] [--yes]
set -euo pipefail

SERVICE_USER=""
ASSUME_YES=0
while [ $# -gt 0 ]; do
  case "$1" in
    --user) SERVICE_USER="$2"; shift 2 ;;
    --yes)  ASSUME_YES=1; shift ;;
    *) echo "unknown flag: $1"; exit 2 ;;
  esac
done

[ "$(id -u)" -eq 0 ] || { echo "run as root (sudo ./install.sh)"; exit 1; }

here="$(cd "$(dirname "$0")" && pwd)"
bin="$here/../analytics"
[ -x "$bin" ] || bin="$here/analytics"
[ -x "$bin" ] || { echo "analytics binary not found next to installer; run 'make build' first"; exit 1; }

if [ -z "$SERVICE_USER" ]; then
  if [ "$ASSUME_YES" -eq 1 ]; then
    SERVICE_USER=analytics
  else
    read -r -p "Service account to run under [analytics]: " SERVICE_USER
    SERVICE_USER=${SERVICE_USER:-analytics}
  fi
fi

if ! id "$SERVICE_USER" >/dev/null 2>&1; then
  echo "Creating system user $SERVICE_USER"
  useradd --system --home-dir /var/lib/analytics --shell /usr/sbin/nologin "$SERVICE_USER"
fi

install -m 0755 "$bin" /usr/local/bin/analytics
install -d -m 0750 -o "$SERVICE_USER" -g "$SERVICE_USER" /var/lib/analytics
install -d -m 0755 /etc/analytics

if [ ! -f /etc/analytics/config.json ]; then
  install -m 0640 -g "$SERVICE_USER" "$here/config.example.json" /etc/analytics/config.json
  echo "Installed /etc/analytics/config.json — EDIT IT (projects, origins)."
fi
if [ ! -f /etc/analytics/litestream.env ]; then
  cat > /etc/analytics/litestream.env <<'EOF_ENV'
# R2/S3 credentials for litestream (referenced by litestream.service)
LITESTREAM_ACCESS_KEY_ID=CHANGE_ME
LITESTREAM_SECRET_ACCESS_KEY=CHANGE_ME
R2_BUCKET=CHANGE_ME
R2_ENDPOINT=https://ACCOUNT_ID.r2.cloudflarestorage.com
EOF_ENV
  chmod 0640 /etc/analytics/litestream.env
  chgrp "$SERVICE_USER" /etc/analytics/litestream.env
  echo "Installed /etc/analytics/litestream.env — EDIT IT (R2 credentials)."
fi
if [ ! -f /etc/litestream.yml ] && [ -f "$here/litestream/litestream.yml" ]; then
  install -m 0644 "$here/litestream/litestream.yml" /etc/litestream.yml
fi

for unit in analytics litestream; do
  sed "s/__USER__/$SERVICE_USER/g" "$here/systemd/$unit.service" \
    > "/etc/systemd/system/$unit.service"
done
systemctl daemon-reload
systemctl enable analytics.service
if command -v litestream >/dev/null 2>&1; then
  systemctl enable litestream.service
else
  echo "NOTE: litestream binary not found; install it (https://litestream.io/install/) then: systemctl enable --now litestream"
fi

cat <<EOF_DONE

Installed. Next steps:
  1. Edit /etc/analytics/config.json (projects, allowed_origins, geo)
  2. Edit /etc/analytics/litestream.env (R2 credentials)
  3. systemctl start analytics   (and litestream once installed)
  4. Put Cloudflare/Caddy/nginx in front of 127.0.0.1:8080 for TLS
  5. Embed: <script defer src="https://YOUR_DOMAIN/js/script.js" data-project="myapp"></script>
EOF_DONE
```

- [ ] **Step 2: Verify** — `bash -n deploy/install.sh` (syntax); `shellcheck deploy/install.sh` if available; `systemd-analyze verify deploy/systemd/analytics.service` if available (expect only the `__USER__` placeholder warning). Manual review of the unit against spec §11's hardening list — every directive present.
- [ ] **Step 3: Commit** — `git commit -m "feat: install.sh with user prompt, hardened systemd units, logrotate"`

---

### Task 26: README, deployment docs, final verification

**Files:**
- Create: `README.md` (replace stub), `docs/deployment.md`
- Modify: none

- [ ] **Step 1: Write README.md** covering, in order: what it is (2 paragraphs: cookieless web analytics + product events, single binary + SQLite, Plausible-model privacy); quickstart all-in-one (docker compose, 5 commands); quickstart split (install.sh on VPS + backoffice compose); embedding the script (`<script defer src=... data-project=...>`), `analytics.track/identify`, `localStorage.analytics_ignore` opt-out; server-side events (`curl` example against `/api/event`); config reference (every key from `deploy/config.example.json` with one line each, including per-project `retention` override and `product_aggregation` with the `"*"` wildcard and `top_n`); privacy/GDPR section (spec §5.4a verbatim points, including the operator-responsibility note about PII in paths and user_id); Pi/low-resource notes (spec §12a: raise `flush_interval`, litestream `sync-interval`, `GOMEMLIMIT`); dashboards (Evidence URLs, rebuild cadence); development (make targets, coverage gate).

- [ ] **Step 2: Write docs/deployment.md** — the runbook: VPS install (install.sh walkthrough), R2 bucket + token creation steps, litestream restore drill (`litestream restore -config /etc/litestream.yml -o /tmp/check.db /var/lib/analytics/analytics.db` — verify backups actually restore, monthly), backoffice setup for both topologies, migrating aio→split (change Evidence env var + enable sync service), disaster recovery (VPS dies: restore from R2 onto new host, start serve).

- [ ] **Step 3: Full verification**

Run, in order, and fix anything that fails:
```bash
make check          # coverage gate: >=80% total, >=85% core packages
make build
make build-all      # arm64/arm/amd64 cross-compilation must succeed
./analytics version
go vet ./...
```

- [ ] **Step 4: Spec sweep** — reread the spec top to bottom; for each section confirm an implementation exists and matches (§4 config keys, §5 endpoints/limits/auth, §5.4a privacy invariants — grep the codebase for `RemoteAddr`/`X-Forwarded-For`/`User-Agent` and confirm none reach a log or the store; §8 schema; §9 jobs; §10 topologies; §11 hardening; §12a defaults; §14 gates). Fix gaps.

- [ ] **Step 5: Commit** — `git commit -m "docs: README and deployment runbook"`

---

## Plan Self-Review (completed)

- **Spec coverage:** §2 dependency budget → Task 1/global constraints (maxminddb flagged); §4 config → Task 3; §5 API/limits/auth/pipeline/enrichment/script → Tasks 8, 9, 12, 13, 14; §5.4/§5.4a privacy → Tasks 7, 9, 13, 14, 26; §6 geo → Tasks 10, 11; §7 store abstraction → Tasks 4, 5; §8 schema/views → Tasks 5, 18, 19; §9 jobs → Task 20; §10 replication/topologies/Evidence → Tasks 22, 23, 24; §11 ops → Task 25; §12 layout → all; §12a low-resource → Tasks 1 (multi-arch), 5 (cache_size), 23/25 (GOMEMLIMIT); §13 future work → none needed (registry seams exist); §14 testing/coverage → Task 1 gate + every task's tests + Task 26 verification.
- **Known deviations from spec (accepted):** (1) `oschwald/maxminddb-golang/v2` instead of a hand-rolled MMDB reader — spec §6 contingency, declared in the header; (2) tracking script lives at `internal/server/script.js` (go:embed constraint) instead of `web/script.js` — repo-layout note updated in README task.
- **Type consistency check:** `store.Store` methods match between Task 4 (definition), Tasks 5–19 (implementations), Task 20 (consumer), Task 21 (wiring): `Migrate`, `SyncProjects`, `WriteWebHits`, `WriteProductEvents`, `WebDaysBefore`, `ProductDaysBefore`, `AggregateWebDay`, `AggregateProductDay`, `PruneAggregates`, `IncrementalVacuum`, `ProjectIDs`, `KnownAttributeKeys`, `RebuildFlatView`, `GetMeta`, `SetMeta`, `Close`. `config.BufferConfig{FlushMaxEvents, FlushInterval, Capacity}` consistent across Tasks 3, 12, 21. `civil.Date` API consistent across Tasks 2, 15–22.
- **Sequencing:** Tasks 1–4 are foundations; 5–6 sqlite base; 7–12 independent of each other (parallelizable); 13–14 need 7–12; 15–19 need 6; 20 needs 15–19; 21 needs everything Go; 22 needs 3+5; 23–25 need 21–22; 24 needs 19; 26 last.
