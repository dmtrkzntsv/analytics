// Package config loads infra settings from the environment (12-factor; the
// process reads real env vars — systemd/compose/make load the env *file*)
// and the project list from the JSON file named by PROJECTS_FILE (spec §4).
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
	Alias              string              `json:"alias"`
	Name               string              `json:"name"`
	AllowedOrigins     []string            `json:"allowed_origins"`
	Retention          *RetentionOverride  `json:"retention"`
	ProductAggregation *ProductAggregation `json:"product_aggregation"`
}

type SyncConfig struct {
	Interval         time.Duration
	LitestreamConfig string
	ReplicaPath      string
}

type Config struct {
	Listen    string
	Database  string
	Geo       string
	Log       LogConfig
	Buffer    BufferConfig
	Retention Retention
	Sync      SyncConfig
	Projects  []Project
}

// DefaultProjectsFile is where the installer puts projects.json; override
// with PROJECTS_FILE.
const DefaultProjectsFile = "/etc/analytics/projects.json"

// Load builds the configuration from the process environment and the
// projects file. This is the only entry point the commands use.
func Load() (*Config, error) {
	return FromEnv(os.LookupEnv)
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
// a map lookup in tests) and loads the projects file it names.
func FromEnv(lookup func(string) (string, bool)) (*Config, error) {
	e := &env{lookup: lookup}
	c := &Config{
		Listen:   e.str("LISTEN_ADDR", "127.0.0.1:8080"),
		Database: e.str("DATABASE_URL", ""),
		Geo:      e.str("GEO_URL", "cloudflare://"),
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
		},
		Sync: SyncConfig{
			Interval:         e.dur("SYNC_INTERVAL", 5*time.Minute),
			LitestreamConfig: e.str("SYNC_LITESTREAM_CONFIG", "/etc/litestream.yml"),
			ReplicaPath:      e.str("SYNC_REPLICA_PATH", ""),
		},
	}
	if e.err != nil {
		return nil, e.err
	}
	path := e.str("PROJECTS_FILE", DefaultProjectsFile)
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("config: projects file: %w", err)
	}
	defer f.Close()
	c.Projects, err = ParseProjects(f)
	if err != nil {
		return nil, fmt.Errorf("config: %s: %w", path, err)
	}
	c.applyDefaults()
	if err := c.validate(); err != nil {
		return nil, err
	}
	return c, nil
}

// ParseProjects reads a projects.json: a bare JSON array of projects.
func ParseProjects(r io.Reader) ([]Project, error) {
	var ps []Project
	dec := json.NewDecoder(r)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ps); err != nil {
		return nil, fmt.Errorf("projects: %w", err)
	}
	return ps, nil
}

func (c *Config) applyDefaults() {
	for i := range c.Projects {
		if pa := c.Projects[i].ProductAggregation; pa != nil && pa.TopN == 0 {
			pa.TopN = 50
		}
	}
}

func (c *Config) validate() error {
	if c.Database == "" {
		return fmt.Errorf("config: DATABASE_URL is required")
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
	// Validate global retention (negative values only)
	for _, rc := range []RetentionClass{c.Retention.Web, c.Retention.Product} {
		if rc.RawDays < 0 || rc.AggregateDays < 0 {
			return fmt.Errorf("config: retention days must not be negative: %+v", rc)
		}
	}
	seen := map[string]bool{}
	for _, p := range c.Projects {
		if p.Alias == "" {
			return fmt.Errorf("config: project with empty alias")
		}
		if seen[p.Alias] {
			return fmt.Errorf("config: duplicate project alias %q", p.Alias)
		}
		seen[p.Alias] = true
		for _, o := range p.AllowedOrigins {
			if o == "" {
				return fmt.Errorf("config: project %q has an empty allowed_origin", p.Alias)
			}
		}
		// Validate merged retention for project
		r := c.RetentionFor(p.Alias)
		if r.Web.RawDays < 0 || r.Web.AggregateDays < 0 || r.Product.RawDays < 0 || r.Product.AggregateDays < 0 {
			return fmt.Errorf("config: project %q has invalid merged retention (negative values): web=%+v product=%+v", p.Alias, r.Web, r.Product)
		}
	}
	return nil
}

func (c *Config) Project(alias string) *Project {
	for i := range c.Projects {
		if c.Projects[i].Alias == alias {
			return &c.Projects[i]
		}
	}
	return nil
}

func (c *Config) RetentionFor(project string) Retention {
	r := c.Retention
	p := c.Project(project)
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
