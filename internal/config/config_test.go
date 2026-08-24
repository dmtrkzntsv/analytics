package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const minimalProjects = `[{"alias": "app", "name": "App", "allowed_origins": ["https://app.com"]}]`

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
	if c.Dashboards.Addr != "0.0.0.0:3000" || c.Dashboards.Interval != 15*time.Minute {
		t.Errorf("Dashboards = %+v", c.Dashboards)
	}
	if c.Dashboards.ProjectDir != "/opt/evidence" || c.Dashboards.WorkDir != "/var/lib/dashboards" {
		t.Errorf("Dashboards dirs = %+v", c.Dashboards)
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
		"DASHBOARDS_ADDR":                  "127.0.0.1:4000",
		"DASHBOARDS_INTERVAL":              "1m",
		"DASHBOARDS_PROJECT_DIR":           "/tmp/evidence",
		"DASHBOARDS_WORK_DIR":              "/tmp/work",
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
	if c.Dashboards.Addr != "127.0.0.1:4000" || c.Dashboards.Interval != time.Minute ||
		c.Dashboards.ProjectDir != "/tmp/evidence" || c.Dashboards.WorkDir != "/tmp/work" {
		t.Errorf("Dashboards = %+v", c.Dashboards)
	}
}

func TestRetentionOverrideMerge(t *testing.T) {
	c, err := load(t, map[string]string{"DATABASE_URL": "sqlite:///tmp/a.db"}, `[{
	  "alias": "app", "name": "App", "allowed_origins": ["https://app.com"],
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
	  {"alias": "a", "name": "A", "allowed_origins": ["https://a.com"]},
	  {"alias": "b", "name": "B", "allowed_origins": ["https://b.com"],
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

// mapLookup is the FromEnv* seam: a lookup backed by a plain map.
func mapLookup(vars map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := vars[k]; return v, ok }
}

func TestDashboardsDefaults(t *testing.T) {
	c, err := FromEnvDashboards(mapLookup(map[string]string{
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
	c, err := FromEnvDashboards(mapLookup(map[string]string{
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
	if _, err := FromEnvDashboards(mapLookup(map[string]string{})); err == nil {
		t.Fatal("want an error with neither DASHBOARDS_DB_PATH nor DATABASE_URL")
	}
}

func TestDashboardsIgnoresProjectsFile(t *testing.T) {
	// A replica-only host has no project list; dashboards must not demand one.
	c, err := FromEnvDashboards(mapLookup(map[string]string{
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

func TestDashboardsReportsBadDurations(t *testing.T) {
	if _, err := FromEnvDashboards(mapLookup(map[string]string{
		"DASHBOARDS_DB_PATH":  "/data/replica.db",
		"DASHBOARDS_INTERVAL": "soon",
	})); err == nil {
		t.Fatal("want an error for an unparseable DASHBOARDS_INTERVAL")
	}
}
