package config

import (
	"strings"
	"testing"
	"time"
)

// load runs FromEnv against a var map. DATABASE_DSN defaults to an unused
// sqlite DSN so callers that only care about other fields can omit it.
func load(t *testing.T, vars map[string]string) (*Config, error) {
	t.Helper()
	m := map[string]string{"DATABASE_DSN": "sqlite:///tmp/a.db"}
	for k, v := range vars {
		m[k] = v
	}
	return FromEnv(func(k string) (string, bool) { v, ok := m[k]; return v, ok })
}

func TestDefaultsApplied(t *testing.T) {
	c, err := load(t, nil)
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
		"DATABASE_DSN":                     "sqlite:///tmp/a.db",
		"LISTEN_ADDR":                      "0.0.0.0:9999",
		"GEO_DSN":                          "none://",
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
	})
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

func TestValidationErrors(t *testing.T) {
	base := func(over map[string]string) map[string]string {
		vars := map[string]string{"DATABASE_DSN": "sqlite:///tmp/a.db"}
		for k, v := range over {
			vars[k] = v
		}
		return vars
	}
	cases := map[string]map[string]string{
		"no database":       {"DATABASE_DSN": ""},
		"bad geo scheme":    base(map[string]string{"GEO_DSN": "???"}),
		"negative raw_days": base(map[string]string{"RETENTION_WEB_RAW_DAYS": "-1"}),
		"bad integer":       base(map[string]string{"BUFFER_CAPACITY": "many"}),
		"invalid duration":  base(map[string]string{"BUFFER_FLUSH_INTERVAL": "fast"}),
	}
	for name, vars := range cases {
		if _, err := FromEnv(func(k string) (string, bool) { v, ok := vars[k]; return v, ok }); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

// Load reads the real process environment; cover the wrapper itself.
func TestLoadFromProcessEnv(t *testing.T) {
	t.Setenv("DATABASE_DSN", "sqlite:///tmp/a.db")
	t.Setenv("LOG_LEVEL", "warn")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.Log.Level != "warn" {
		t.Errorf("Load() = %+v", c)
	}
}

// mapLookup is the FromEnv* seam: a lookup backed by a plain map.
func mapLookup(vars map[string]string) func(string) (string, bool) {
	return func(k string) (string, bool) { v, ok := vars[k]; return v, ok }
}

func TestDashboardsDefaults(t *testing.T) {
	c, err := FromEnvDashboards(mapLookup(map[string]string{
		"DATABASE_DSN": "sqlite:///var/lib/twillingate/twillingate.db",
	}))
	if err != nil {
		t.Fatalf("FromEnvDashboards: %v", err)
	}
	if c.Dashboards.DBPath != "/var/lib/twillingate/twillingate.db" {
		t.Errorf("DBPath = %q, want the DATABASE_DSN path", c.Dashboards.DBPath)
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
		"DATABASE_DSN":       "sqlite:///var/lib/twillingate/twillingate.db",
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
		t.Fatal("want an error with neither DASHBOARDS_DB_PATH nor DATABASE_DSN")
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

// --- legacy projects.json format (used only by `twillingate config import`) ---

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

func TestParseProjectsRejectsUnknownFields(t *testing.T) {
	if _, err := ParseProjects(strings.NewReader(`[{"alias":"a","name":"A","allowed_origin":["https://a.com"]}]`)); err == nil {
		t.Fatal("want error for unknown field allowed_origin")
	}
}

func TestParseProjectsRejectsNonArray(t *testing.T) {
	if _, err := ParseProjects(strings.NewReader(`{"projects":[]}`)); err == nil {
		t.Fatal("want error when the top level is not an array")
	}
}

func TestAppRetentionDefaultsAndMaxEventAge(t *testing.T) {
	c, err := load(t, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.Retention.App.RawDays != 30 || c.Retention.App.AggregateDays != 365 {
		t.Fatalf("app retention = %+v", c.Retention.App)
	}
	if want := 30 * 24 * time.Hour; c.MaxEventAge() != want {
		t.Errorf("MaxEventAge() = %v, want %v", c.MaxEventAge(), want)
	}
}

func TestAppRetentionFromEnv(t *testing.T) {
	c, err := load(t, map[string]string{
		"RETENTION_APP_RAW_DAYS":       "14",
		"RETENTION_APP_AGGREGATE_DAYS": "90",
	})
	if err != nil {
		t.Fatal(err)
	}
	if c.Retention.App.RawDays != 14 || c.Retention.App.AggregateDays != 90 {
		t.Errorf("app retention = %+v", c.Retention.App)
	}
}

func TestRejectsNegativeAppRetention(t *testing.T) {
	if _, err := load(t, map[string]string{"RETENTION_APP_RAW_DAYS": "-1"}); err == nil {
		t.Fatal("want error for negative app retention")
	}
}

func TestLoadDoesNotRequireProjectsFile(t *testing.T) {
	cfg, err := FromEnv(func(k string) (string, bool) {
		if k == "DATABASE_DSN" {
			return "sqlite:///tmp/x.db", true
		}
		return "", false // PROJECTS_FILE unset, no file anywhere
	})
	if err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	if cfg.Database != "sqlite:///tmp/x.db" {
		t.Fatalf("cfg = %+v", cfg)
	}
}

func mcpEnv(over map[string]string) func(string) (string, bool) {
	base := map[string]string{
		"DATABASE_DSN": "sqlite:///tmp/x.db",
		"MCP_AUTH_DSN": "token://ar_x",
	}
	for k, v := range over {
		if v == "" {
			delete(base, k)
		} else {
			base[k] = v
		}
	}
	return func(k string) (string, bool) { v, ok := base[k]; return v, ok }
}

func TestValidateMCP(t *testing.T) {
	cases := []struct {
		name string
		over map[string]string
		ok   bool
	}{
		{"token ok", nil, true},
		{"no dsn", map[string]string{"MCP_AUTH_DSN": ""}, false},
		{"not a dsn", map[string]string{"MCP_AUTH_DSN": "token"}, false},
		{"unknown scheme", map[string]string{"MCP_AUTH_DSN": "basic://x"}, false},
		{"empty token", map[string]string{"MCP_AUTH_DSN": "token://"}, false},
		{"oauth ok", map[string]string{
			"MCP_AUTH_DSN": "oauth://idp.example.com?resource=https://twillingate.example.com/mcp"}, true},
		{"oauth resource from PUBLIC_URL", map[string]string{
			"MCP_AUTH_DSN": "oauth://idp.example.com",
			"PUBLIC_URL":   "https://twillingate.example.com"}, true},
		{"oauth no resource and no PUBLIC_URL", map[string]string{
			"MCP_AUTH_DSN": "oauth://idp.example.com"}, false},
		{"oauth empty issuer", map[string]string{
			"MCP_AUTH_DSN": "oauth://?resource=https://twillingate.example.com/mcp"}, false},
		{"cloudflare ok", map[string]string{
			"MCP_AUTH_DSN": "cloudflare://team.cloudflareaccess.com?aud=aud123"}, true},
		{"cloudflare missing aud", map[string]string{
			"MCP_AUTH_DSN": "cloudflare://team.cloudflareaccess.com"}, false},
		{"cloudflare missing team", map[string]string{
			"MCP_AUTH_DSN": "cloudflare://?aud=aud123"}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg, err := FromEnv(mcpEnv(tc.over))
			if err != nil {
				t.Fatal(err)
			}
			err = cfg.ValidateMCP()
			if (err == nil) != tc.ok {
				t.Fatalf("ValidateMCP = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}

func TestMCPAuthDSNParsing(t *testing.T) {
	t.Run("token", func(t *testing.T) {
		cfg, err := FromEnv(mcpEnv(nil))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.MCP.AuthMode != "token" || cfg.MCP.Token != "ar_x" {
			t.Errorf("mode = %q token = %q", cfg.MCP.AuthMode, cfg.MCP.Token)
		}
	})
	t.Run("oauth issuer keeps path, resource explicit", func(t *testing.T) {
		cfg, err := FromEnv(mcpEnv(map[string]string{
			"MCP_AUTH_DSN": "oauth://idp.example.com/tenant1?resource=https://t.example.com/mcp&audience=aud9"}))
		if err != nil {
			t.Fatal(err)
		}
		m := cfg.MCP
		if m.AuthMode != "oauth" || m.Issuer != "https://idp.example.com/tenant1" {
			t.Errorf("mode = %q issuer = %q", m.AuthMode, m.Issuer)
		}
		if m.ResourceURL != "https://t.example.com/mcp" || m.Audience != "aud9" {
			t.Errorf("resource = %q audience = %q", m.ResourceURL, m.Audience)
		}
	})
	t.Run("oauth defaults resource to PUBLIC_URL/mcp and audience to resource", func(t *testing.T) {
		cfg, err := FromEnv(mcpEnv(map[string]string{
			"MCP_AUTH_DSN": "oauth://idp.example.com",
			"PUBLIC_URL":   "https://twillingate.example.com"}))
		if err != nil {
			t.Fatal(err)
		}
		m := cfg.MCP
		if m.ResourceURL != "https://twillingate.example.com/mcp" {
			t.Errorf("resource = %q", m.ResourceURL)
		}
		if m.Audience != m.ResourceURL {
			t.Errorf("audience = %q, want the resource URL", m.Audience)
		}
	})
	t.Run("oauth+insecure issuer is http for local IdPs", func(t *testing.T) {
		cfg, err := FromEnv(mcpEnv(map[string]string{
			"MCP_AUTH_DSN": "oauth+insecure://127.0.0.1:9999?resource=https://t.example.com/mcp"}))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.MCP.AuthMode != "oauth" || cfg.MCP.Issuer != "http://127.0.0.1:9999" {
			t.Errorf("mode = %q issuer = %q", cfg.MCP.AuthMode, cfg.MCP.Issuer)
		}
	})
	t.Run("cloudflare", func(t *testing.T) {
		cfg, err := FromEnv(mcpEnv(map[string]string{
			"MCP_AUTH_DSN": "cloudflare://team.cloudflareaccess.com?aud=aud123"}))
		if err != nil {
			t.Fatal(err)
		}
		m := cfg.MCP
		if m.AuthMode != "cloudflare" || m.CFTeamDomain != "team.cloudflareaccess.com" || m.CFAud != "aud123" {
			t.Errorf("mode = %q team = %q aud = %q", m.AuthMode, m.CFTeamDomain, m.CFAud)
		}
	})
	t.Run("cloudflare+insecure team domain carries http scheme", func(t *testing.T) {
		cfg, err := FromEnv(mcpEnv(map[string]string{
			"MCP_AUTH_DSN": "cloudflare+insecure://127.0.0.1:9999?aud=aud123"}))
		if err != nil {
			t.Fatal(err)
		}
		if cfg.MCP.CFTeamDomain != "http://127.0.0.1:9999" {
			t.Errorf("team = %q", cfg.MCP.CFTeamDomain)
		}
	})
	t.Run("malformed DSN does not fail FromEnv, only ValidateMCP", func(t *testing.T) {
		cfg, err := FromEnv(mcpEnv(map[string]string{"MCP_AUTH_DSN": "basic://x"}))
		if err != nil {
			t.Fatalf("FromEnv must stay lenient for bare `serve`: %v", err)
		}
		if err := cfg.ValidateMCP(); err == nil {
			t.Error("ValidateMCP accepted an unknown scheme")
		}
	})
}

func TestMCPDefaults(t *testing.T) {
	cfg, err := FromEnv(mcpEnv(nil))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCP.Addr != cfg.Listen {
		t.Errorf("Addr = %q, want Listen %q", cfg.MCP.Addr, cfg.Listen)
	}
	if cfg.MCP.DBPath != "/tmp/x.db" {
		t.Errorf("DBPath = %q", cfg.MCP.DBPath)
	}
	if cfg.MCP.QueryTimeout != 10*time.Second || cfg.MCP.QueryMaxRows != 1000 {
		t.Errorf("guards = %v %d", cfg.MCP.QueryTimeout, cfg.MCP.QueryMaxRows)
	}
}

func TestMCPAudienceDefaultsToResource(t *testing.T) {
	cfg, err := FromEnv(mcpEnv(map[string]string{
		"MCP_AUTH_DSN": "oauth://idp.example.com?resource=https://twillingate.example.com/mcp"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.MCP.Audience != "https://twillingate.example.com/mcp" {
		t.Errorf("Audience = %q", cfg.MCP.Audience)
	}
}
