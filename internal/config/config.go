// Package config loads infra settings from the process environment
// (12-factor; systemd/compose/make load the env *file*, the process reads
// real env vars). It no longer reads any file: the project list lives in
// the registry (internal/manage), seeded once via `twillingate config
// import` from the legacy projects.json format.
// stdlib only: encoding/json + net/url for DSN scheme checks.
package config

import (
	"encoding/json"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"
)

// Identity modes. anonymous salts and rotates whatever identifier the
// client supplies; identified stores it as given. The server is always the
// enforcement point: a client hint never overrides this.
const (
	IdentityAnonymous  = "anonymous"
	IdentityIdentified = "identified"
)

type LogConfig struct {
	Level  string
	Format string
	File   string
}

type BufferConfig struct {
	FlushMaxEvents int
	FlushInterval  time.Duration
	Capacity       int
}

type RetentionClass struct {
	RawDays       int `json:"raw_days"`
	AggregateDays int `json:"aggregate_days"`
}

type Retention struct {
	Web     RetentionClass `json:"web"`
	Product RetentionClass `json:"product"`
	App     RetentionClass `json:"app"`
}

type RetentionClassOverride struct {
	RawDays       *int `json:"raw_days"`
	AggregateDays *int `json:"aggregate_days"`
}

type RetentionOverride struct {
	Web     *RetentionClassOverride `json:"web"`
	Product *RetentionClassOverride `json:"product"`
	App     *RetentionClassOverride `json:"app"`
}

type ProductAggregation struct {
	Enabled    bool                `json:"enabled"`
	Attributes map[string][]string `json:"attributes"`
	TopN       int                 `json:"top_n"`
}

// IngestKey is one client credential. Multiple keys per project let a
// website, an iOS app and a desktop app be retired independently. Disabled
// rather than deleted: retirement is reversible during a botched rollout
// without regenerating and redistributing.
//
// Legacy projects.json format, used only by `twillingate config import`.
type IngestKey struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Disabled bool   `json:"disabled"`
}

// Project is the legacy projects.json format, used only by `analytics
// config import` to seed the registry (internal/manage) from a pre-upgrade
// install. The running server never reads this type from a file.
type Project struct {
	Alias              string              `json:"alias"`
	Name               string              `json:"name"`
	Identity           string              `json:"identity"`
	IngestKeys         []IngestKey         `json:"ingest_keys"`
	AllowedOrigins     []string            `json:"allowed_origins"`
	Retention          *RetentionOverride  `json:"retention"`
	ProductAggregation *ProductAggregation `json:"product_aggregation"`
}

// DashboardsConfig configures `analytics dashboards`: which database to
// render, where the Evidence project lives, and how often to rebuild it.
type DashboardsConfig struct {
	DBPath     string
	Addr       string
	Interval   time.Duration
	ProjectDir string
	WorkDir    string
}

type MCPConfig struct {
	Addr         string        // MCP_ADDR, defaults to Listen
	DBPath       string        // MCP_DB_PATH, defaults to DATABASE_DSN path
	AuthMode     string        // "oauth" | "cloudflare" | "token"; no default
	ResourceURL  string        // MCP_RESOURCE_URL
	Issuer       string        // MCP_AUTH_ISSUER
	Audience     string        // MCP_AUTH_AUDIENCE, defaults to ResourceURL
	CFTeamDomain string        // MCP_CF_TEAM_DOMAIN
	CFAud        string        // MCP_CF_AUD
	Token        string        // MCP_TOKEN
	QueryTimeout time.Duration // MCP_QUERY_TIMEOUT, default 10s
	QueryMaxRows int           // MCP_QUERY_MAX_ROWS, default 1000
}

type Config struct {
	Listen     string
	Database   string
	Geo        string
	PublicURL  string
	Log        LogConfig
	Buffer     BufferConfig
	Retention  Retention
	Dashboards DashboardsConfig
	MCP        MCPConfig
}

// Load builds the configuration from the process environment.
func Load() (*Config, error) {
	return FromEnv(os.LookupEnv)
}

// LoadDashboards builds the configuration for `analytics dashboards`, which
// renders whatever database it is pointed at.
func LoadDashboards() (*Config, error) {
	return FromEnvDashboards(os.LookupEnv)
}

// env reads typed values from a lookup function, remembering the first error.
type env struct {
	lookup func(string) (string, bool)
	err    error
}

func (e *env) str(key, def string) string {
	if v, ok := e.lookup(key); ok && v != "" {
		return v
	}
	return def
}

func (e *env) num(key string, def int) int {
	v, ok := e.lookup(key)
	if !ok || v == "" {
		return def
	}
	var n int
	if _, err := fmt.Sscanf(v, "%d", &n); err != nil {
		if e.err == nil {
			e.err = fmt.Errorf("config: %s: invalid integer %q", key, v)
		}
		return def
	}
	return n
}

func (e *env) dur(key string, def time.Duration) time.Duration {
	v, ok := e.lookup(key)
	if !ok || v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		if e.err == nil {
			e.err = fmt.Errorf("config: %s: invalid duration %q", key, v)
		}
		return def
	}
	return d
}

// FromEnv parses the environment via lookup (os.LookupEnv in production,
// a map lookup in tests).
func FromEnv(lookup func(string) (string, bool)) (*Config, error) {
	return parse(lookup, false)
}

// FromEnvDashboards is FromEnv, plus the DASHBOARDS_DB_PATH fallback to
// DATABASE_DSN that only the dashboards renderer needs.
func FromEnvDashboards(lookup func(string) (string, bool)) (*Config, error) {
	return parse(lookup, true)
}

func parse(lookup func(string) (string, bool), dashboards bool) (*Config, error) {
	e := &env{lookup: lookup}
	c := &Config{
		Listen:   e.str("LISTEN_ADDR", "127.0.0.1:8080"),
		Database: e.str("DATABASE_DSN", ""),
		Geo:      e.str("GEO_DSN", "cloudflare://"),
		// The collector's public base URL (https://analytics.example.com).
		// Embed snippets and MCP integration guidance are built from it;
		// unset, they carry a placeholder and tell the model to ask.
		PublicURL: strings.TrimSuffix(e.str("PUBLIC_URL", ""), "/"),
		Log: LogConfig{
			Level:  e.str("LOG_LEVEL", "info"),
			Format: e.str("LOG_FORMAT", "json"),
			File:   e.str("LOG_FILE", ""),
		},
		Buffer: BufferConfig{
			FlushMaxEvents: e.num("BUFFER_FLUSH_MAX_EVENTS", 1000),
			FlushInterval:  e.dur("BUFFER_FLUSH_INTERVAL", 5*time.Second),
			Capacity:       e.num("BUFFER_CAPACITY", 10000),
		},
		Retention: Retention{
			Web: RetentionClass{
				RawDays:       e.num("RETENTION_WEB_RAW_DAYS", 7),
				AggregateDays: e.num("RETENTION_WEB_AGGREGATE_DAYS", 365),
			},
			Product: RetentionClass{
				RawDays:       e.num("RETENTION_PRODUCT_RAW_DAYS", 30),
				AggregateDays: e.num("RETENTION_PRODUCT_AGGREGATE_DAYS", 365),
			},
			App: RetentionClass{
				RawDays:       e.num("RETENTION_APP_RAW_DAYS", 30),
				AggregateDays: e.num("RETENTION_APP_AGGREGATE_DAYS", 365),
			},
		},
		Dashboards: DashboardsConfig{
			DBPath:     e.str("DASHBOARDS_DB_PATH", ""),
			Addr:       e.str("DASHBOARDS_ADDR", "0.0.0.0:3000"),
			Interval:   e.dur("DASHBOARDS_INTERVAL", 15*time.Minute),
			ProjectDir: e.str("DASHBOARDS_PROJECT_DIR", "/opt/evidence"),
			WorkDir:    e.str("DASHBOARDS_WORK_DIR", "/var/lib/dashboards"),
		},
	}
	c.MCP = MCPConfig{
		Addr:         e.str("MCP_ADDR", c.Listen),
		DBPath:       e.str("MCP_DB_PATH", strings.TrimPrefix(c.Database, "sqlite://")),
		AuthMode:     e.str("MCP_AUTH_MODE", ""),
		ResourceURL:  e.str("MCP_RESOURCE_URL", ""),
		Issuer:       e.str("MCP_AUTH_ISSUER", ""),
		CFTeamDomain: e.str("MCP_CF_TEAM_DOMAIN", ""),
		CFAud:        e.str("MCP_CF_AUD", ""),
		Token:        e.str("MCP_TOKEN", ""),
		QueryTimeout: e.dur("MCP_QUERY_TIMEOUT", 10*time.Second),
		QueryMaxRows: e.num("MCP_QUERY_MAX_ROWS", 1000),
	}
	c.MCP.Audience = e.str("MCP_AUTH_AUDIENCE", c.MCP.ResourceURL)
	if e.err != nil {
		return nil, e.err
	}
	if dashboards {
		if c.Dashboards.DBPath == "" {
			if c.Database == "" {
				return nil, fmt.Errorf("config: DASHBOARDS_DB_PATH or DATABASE_DSN is required")
			}
			c.Dashboards.DBPath = strings.TrimPrefix(c.Database, "sqlite://")
		}
		return c, nil
	}
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// ParseProjects reads a projects.json: a bare JSON array of projects.
//
// Legacy projects.json format, used only by `twillingate config import`.
func ParseProjects(r io.Reader) ([]Project, error) {
	var ps []Project
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ps); err != nil {
		return nil, fmt.Errorf("projects: %w", err)
	}
	return ps, nil
}

func (c *Config) validate() error {
	if c.Database == "" {
		return fmt.Errorf("config: DATABASE_DSN is required")
	}
	for _, dsn := range []string{c.Database, c.Geo} {
		u, err := url.Parse(dsn)
		if err != nil || u.Scheme == "" {
			return fmt.Errorf("config: invalid DSN %q", dsn)
		}
	}
	// Validate global retention (negative values only)
	for _, rc := range []RetentionClass{c.Retention.Web, c.Retention.Product, c.Retention.App} {
		if rc.RawDays < 0 || rc.AggregateDays < 0 {
			return fmt.Errorf("config: retention days must not be negative: %+v", rc)
		}
	}
	return nil
}

// MaxEventAge is derived from the app raw window rather than separately
// configurable: the two must agree or a clamped timestamp could land in an
// already-aggregated day.
func (c *Config) MaxEventAge() time.Duration {
	return time.Duration(c.Retention.App.RawDays) * 24 * time.Hour
}

// ValidateMCP fail-fasts the -mcp surface (endpoint spec §4): there is no
// unauthenticated mode and no way to reach one by omission.
func (c *Config) ValidateMCP() error {
	m := c.MCP
	switch m.AuthMode {
	case "token":
		if m.Token == "" {
			return fmt.Errorf("config: MCP_AUTH_MODE=token requires MCP_TOKEN (mint with `twillingate keygen -mcp`)")
		}
	case "oauth":
		if m.Issuer == "" {
			return fmt.Errorf("config: MCP_AUTH_MODE=oauth requires MCP_AUTH_ISSUER")
		}
		if m.ResourceURL == "" {
			return fmt.Errorf("config: MCP_AUTH_MODE=oauth requires MCP_RESOURCE_URL")
		}
	case "cloudflare":
		if m.CFTeamDomain == "" {
			return fmt.Errorf("config: MCP_AUTH_MODE=cloudflare requires MCP_CF_TEAM_DOMAIN")
		}
		if m.CFAud == "" {
			return fmt.Errorf("config: MCP_AUTH_MODE=cloudflare requires MCP_CF_AUD")
		}
	case "":
		return fmt.Errorf("config: -mcp requires MCP_AUTH_MODE (oauth, cloudflare or token)")
	default:
		return fmt.Errorf("config: unknown MCP_AUTH_MODE %q (oauth, cloudflare or token)", m.AuthMode)
	}
	// An issuer with no resource URL produces a spec-broken RFC 9728
	// surface regardless of mode: a relative resource_metadata pointer
	// in WWW-Authenticate and a PRM document with an empty `resource`.
	// oauth mode already requires both above; this catches token mode
	// with MCP_AUTH_ISSUER set (which opts into the PRM route).
	if m.Issuer != "" && m.ResourceURL == "" {
		return fmt.Errorf("config: MCP_AUTH_ISSUER requires MCP_RESOURCE_URL")
	}
	return nil
}
