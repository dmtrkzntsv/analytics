package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const minimalProjects = `[{"alias": "app", "name": "App",
	"ingest_keys": [{"key": "ak_test", "label": "web"}],
	"allowed_origins": ["https://app.com"]}]`

// load runs FromEnv against a var map, writing projects to a temp file and
// pointing PROJECTS_FILE at it unless the map already sets one.
func load(t *testing.T, vars map[string]string, projects string) (*Config, error) {
	t.Helper()
	if _, ok := vars["PROJECTS_FILE"]; !ok && projects != "" {
		path := filepath.Join(t.TempDir(), "projects.json")
		if err := os.WriteFile(path, []byte(projects), 0o644); err != nil {
			t.Fatal(err)
		}
		vars["PROJECTS_FILE"] = path
	}
	return FromEnv(func(k string) (string, bool) { v, ok := vars[k]; return v, ok })
}

func TestDefaultsApplied(t *testing.T) {
	c, err := load(t, map[string]string{"DATABASE_URL": "sqlite:///tmp/a.db"}, minimalProjects)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:8080" {
		t.Errorf("Listen = %q", c.Listen)
	}
	if c.Geo != "cloudflare://" {
		t.Errorf("Geo = %q", c.Geo)
	}
	if c.Log.Level != "info" || c.Log.Format != "json" {
		t.Errorf("Log = %+v", c.Log)
	}
	if c.Buffer.FlushMaxEvents != 1000 || c.Buffer.FlushInterval != 5*time.Second || c.Buffer.Capacity != 10000 {
		t.Errorf("Buffer = %+v", c.Buffer)
	}
	if c.Retention.Web.RawDays != 7 || c.Retention.Product.RawDays != 30 ||
		c.Retention.Web.AggregateDays != 365 || c.Retention.Product.AggregateDays != 365 {
		t.Errorf("Retention = %+v", c.Retention)
	}
	if c.Sync.Interval != 5*time.Minute || c.Sync.LitestreamConfig != "/etc/litestream.yml" {
		t.Errorf("Sync = %+v", c.Sync)
	}
}

func TestEnvOverrides(t *testing.T) {
	c, err := load(t, map[string]string{
		"DATABASE_URL":                     "sqlite:///tmp/a.db",
		"LISTEN_ADDR":                      "0.0.0.0:9999",
		"GEO_URL":                          "none://",
		"LOG_LEVEL":                        "debug",
		"LOG_FORMAT":                       "text",
		"LOG_FILE":                         "/tmp/a.log",
		"BUFFER_FLUSH_MAX_EVENTS":          "5",
		"BUFFER_FLUSH_INTERVAL":            "250ms",
		"BUFFER_CAPACITY":                  "42",
		"RETENTION_WEB_RAW_DAYS":           "3",
		"RETENTION_WEB_AGGREGATE_DAYS":     "30",
		"RETENTION_PRODUCT_RAW_DAYS":       "10",
		"RETENTION_PRODUCT_AGGREGATE_DAYS": "60",
		"SYNC_INTERVAL":                    "1m",
		"SYNC_LITESTREAM_CONFIG":           "/tmp/ls.yml",
		"SYNC_REPLICA_PATH":                "/tmp/replica.db",
	}, minimalProjects)
	if err != nil {
		t.Fatal(err)
	}
	if c.Listen != "0.0.0.0:9999" || c.Geo != "none://" {
		t.Errorf("Listen/Geo = %q/%q", c.Listen, c.Geo)
	}
	if c.Log.Level != "debug" || c.Log.Format != "text" || c.Log.File != "/tmp/a.log" {
		t.Errorf("Log = %+v", c.Log)
	}
	if c.Buffer.FlushMaxEvents != 5 || c.Buffer.FlushInterval != 250*time.Millisecond || c.Buffer.Capacity != 42 {
		t.Errorf("Buffer = %+v", c.Buffer)
	}
	if c.Retention.Web.RawDays != 3 || c.Retention.Web.AggregateDays != 30 ||
		c.Retention.Product.RawDays != 10 || c.Retention.Product.AggregateDays != 60 {
		t.Errorf("Retention = %+v", c.Retention)
	}
	if c.Sync.Interval != time.Minute || c.Sync.LitestreamConfig != "/tmp/ls.yml" || c.Sync.ReplicaPath != "/tmp/replica.db" {
		t.Errorf("Sync = %+v", c.Sync)
	}
}

func TestRetentionOverrideMerge(t *testing.T) {
	c, err := load(t, map[string]string{"DATABASE_URL": "sqlite:///tmp/a.db"}, `[{
	  "alias": "app", "name": "App", "allowed_origins": ["https://app.com"],
	  "ingest_keys": [{"key": "ak_1", "label": "web"}],
	  "retention": {"product": {"raw_days": 60}}
	}]`)
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
	c, err := load(t, map[string]string{"DATABASE_URL": "sqlite:///tmp/a.db"}, `[
	  {"alias": "a", "name": "A", "allowed_origins": ["https://a.com"],
	   "ingest_keys": [{"key": "ak_a", "label": "web"}]},
	  {"alias": "b", "name": "B", "allowed_origins": ["https://b.com"],
	   "ingest_keys": [{"key": "ak_b", "label": "web"}],
	   "product_aggregation": {"enabled": true, "attributes": {"subscribed": ["plan"]}}}
	]`)
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
	base := func(over map[string]string) map[string]string {
		vars := map[string]string{"DATABASE_URL": "sqlite:///tmp/a.db"}
		for k, v := range over {
			vars[k] = v
		}
		return vars
	}
	cases := map[string]struct {
		vars     map[string]string
		projects string
	}{
		"no database":               {map[string]string{}, minimalProjects},
		"no projects":               {base(nil), `[]`},
		"dup alias":                 {base(nil), `[{"alias":"a","name":"A","allowed_origins":["https://a.com"]},{"alias":"a","name":"A2","allowed_origins":["https://b.com"]}]`},
		"bad geo scheme":            {base(map[string]string{"GEO_URL": "???"}), minimalProjects},
		"negative raw_days":         {base(map[string]string{"RETENTION_WEB_RAW_DAYS": "-1"}), minimalProjects},
		"bad integer":               {base(map[string]string{"BUFFER_CAPACITY": "many"}), minimalProjects},
		"empty origin":              {base(nil), `[{"alias":"a","name":"A","allowed_origins":[""]}]`},
		"empty alias":               {base(nil), `[{"alias":"","name":"A","allowed_origins":["https://a.com"]}]`},
		"invalid duration":          {base(map[string]string{"BUFFER_FLUSH_INTERVAL": "fast"}), minimalProjects},
		"unknown project key":       {base(nil), `[{"alias":"a","name":"A","allowed_origin":["https://a.com"]}]`},
		"not an array":              {base(nil), `{"projects":[]}`},
		"project negative override": {base(nil), `[{"alias":"a","name":"A","allowed_origins":["https://a.com"],"retention":{"web":{"raw_days":-2}}}]`},
	}
	for name, tc := range cases {
		if _, err := load(t, tc.vars, tc.projects); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestMissingProjectsFile(t *testing.T) {
	_, err := load(t, map[string]string{
		"DATABASE_URL":  "sqlite:///tmp/a.db",
		"PROJECTS_FILE": filepath.Join(t.TempDir(), "absent.json"),
	}, "")
	if err == nil || !strings.Contains(err.Error(), "projects file") {
		t.Errorf("expected projects-file error, got %v", err)
	}
}

// Load reads the real process environment; cover the wrapper itself.
func TestLoadFromProcessEnv(t *testing.T) {
	projects := filepath.Join(t.TempDir(), "projects.json")
	if err := os.WriteFile(projects, []byte(minimalProjects), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("DATABASE_URL", "sqlite:///tmp/a.db")
	t.Setenv("PROJECTS_FILE", projects)
	t.Setenv("LOG_LEVEL", "warn")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Log.Level != "warn" || len(c.Projects) != 1 {
		t.Errorf("Load() = %+v", c)
	}
}

// --- ingest keys, identity mode, app retention (app analytics spec §4/§5/§9) ---

func loadProjects(t *testing.T, projects string) (*Config, error) {
	t.Helper()
	return load(t, map[string]string{"DATABASE_URL": "sqlite:///tmp/a.db"}, projects)
}

func mustLoadProjects(t *testing.T, projects string) *Config {
	t.Helper()
	c, err := loadProjects(t, projects)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return c
}

func TestParseProjectsIngestKeys(t *testing.T) {
	ps, err := ParseProjects(strings.NewReader(`[
	  {"alias":"a","name":"A","identity":"identified",
	   "ingest_keys":[{"key":"ak_1","label":"web"},{"key":"ak_2","label":"ios","disabled":true}]}
	]`))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(ps) != 1 || len(ps[0].IngestKeys) != 2 {
		t.Fatalf("got %+v", ps)
	}
	if ps[0].Identity != "identified" {
		t.Errorf("identity = %q", ps[0].Identity)
	}
	if !ps[0].IngestKeys[1].Disabled {
		t.Error("second key should be disabled")
	}
}

func TestIdentityDefaultsToAnonymous(t *testing.T) {
	c := mustLoadProjects(t, `[{"alias":"a","name":"A","ingest_keys":[{"key":"ak_1","label":"w"}]}]`)
	if got := c.Project("a").Identity; got != IdentityAnonymous {
		t.Errorf("identity = %q, want anonymous", got)
	}
}

func TestRejectsUnknownIdentity(t *testing.T) {
	if _, err := loadProjects(t, `[{"alias":"a","name":"A","identity":"pseudonymous",
	  "ingest_keys":[{"key":"ak_1","label":"w"}]}]`); err == nil {
		t.Fatal("want error for unknown identity mode")
	}
}

func TestRejectsProjectWithoutKeys(t *testing.T) {
	if _, err := loadProjects(t, `[{"alias":"a","name":"A"}]`); err == nil {
		t.Fatal("want error when ingest_keys is missing")
	}
}

func TestRejectsEmptyIngestKey(t *testing.T) {
	if _, err := loadProjects(t, `[{"alias":"a","name":"A","ingest_keys":[{"key":"","label":"w"}]}]`); err == nil {
		t.Fatal("want error for empty ingest key")
	}
}

func TestRejectsDuplicateKeyAcrossProjects(t *testing.T) {
	if _, err := loadProjects(t, `[
	  {"alias":"a","name":"A","ingest_keys":[{"key":"dup","label":"w"}]},
	  {"alias":"b","name":"B","ingest_keys":[{"key":"dup","label":"w"}]}]`); err == nil {
		t.Fatal("want error for duplicate key across projects")
	}
}

func TestProjectByKey(t *testing.T) {
	c := mustLoadProjects(t, `[
	  {"alias":"a","name":"A","ingest_keys":[{"key":"ak_1","label":"web"},{"key":"ak_off","label":"old","disabled":true}]},
	  {"alias":"b","name":"B","ingest_keys":[{"key":"ak_2","label":"ios"}]}]`)

	p, label, ok := c.ProjectByKey("ak_1")
	if !ok || p.Alias != "a" || label != "web" {
		t.Fatalf("ak_1 -> %v %q %v", p, label, ok)
	}
	if p, _, ok := c.ProjectByKey("ak_2"); !ok || p.Alias != "b" {
		t.Errorf("ak_2 -> %v %v", p, ok)
	}
	if _, _, ok := c.ProjectByKey("ak_off"); ok {
		t.Error("disabled key must not resolve")
	}
	if _, _, ok := c.ProjectByKey("nope"); ok {
		t.Error("unknown key must not resolve")
	}
	if _, _, ok := c.ProjectByKey(""); ok {
		t.Error("empty key must not resolve")
	}
}

func TestDisabledKeyProjects(t *testing.T) {
	c := mustLoadProjects(t, `[
	  {"alias":"a","name":"A","ingest_keys":[{"key":"ak_1","label":"w","disabled":true}]},
	  {"alias":"b","name":"B","ingest_keys":[{"key":"ak_2","label":"w"}]}]`)
	got := c.DisabledKeyProjects()
	if len(got) != 1 || got[0] != "a" {
		t.Fatalf("DisabledKeyProjects() = %v, want [a]", got)
	}
}

func TestAppRetentionDefaultsAndMaxEventAge(t *testing.T) {
	c := mustLoadProjects(t, `[{"alias":"a","name":"A","ingest_keys":[{"key":"k","label":"w"}]}]`)
	if c.Retention.App.RawDays != 30 || c.Retention.App.AggregateDays != 365 {
		t.Fatalf("app retention = %+v", c.Retention.App)
	}
	if want := 30 * 24 * time.Hour; c.MaxEventAge() != want {
		t.Errorf("MaxEventAge() = %v, want %v", c.MaxEventAge(), want)
	}
}

func TestAppRetentionFromEnv(t *testing.T) {
	c, err := load(t, map[string]string{
		"DATABASE_URL":                 "sqlite:///tmp/a.db",
		"RETENTION_APP_RAW_DAYS":       "14",
		"RETENTION_APP_AGGREGATE_DAYS": "90",
	}, minimalProjects)
	if err != nil {
		t.Fatal(err)
	}
	if c.Retention.App.RawDays != 14 || c.Retention.App.AggregateDays != 90 {
		t.Errorf("app retention = %+v", c.Retention.App)
	}
}

func TestRetentionForAppOverride(t *testing.T) {
	c := mustLoadProjects(t, `[{"alias":"a","name":"A",
	  "ingest_keys":[{"key":"k","label":"w"}],
	  "retention":{"app":{"raw_days":14}}}]`)
	r := c.RetentionFor("a")
	if r.App.RawDays != 14 || r.App.AggregateDays != 365 {
		t.Fatalf("merged app retention = %+v", r.App)
	}
}

func TestRejectsNegativeAppRetention(t *testing.T) {
	if _, err := loadProjects(t, `[{"alias":"a","name":"A",
	  "ingest_keys":[{"key":"k","label":"w"}],
	  "retention":{"app":{"raw_days":-1}}}]`); err == nil {
		t.Fatal("want error for negative app retention")
	}
}
