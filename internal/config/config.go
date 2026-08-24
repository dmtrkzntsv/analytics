// Package config loads infra settings from the environment (12-factor; the
// process reads real env vars — systemd/compose/make load the env *file*)
// and the project list from the JSON file named by PROJECTS_FILE (spec §4).
// stdlib only: encoding/json + net/url for DSN scheme checks.
package config

import (
	"crypto/subtle"
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
type IngestKey struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Disabled bool   `json:"disabled"`
}

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

type Config struct {
	Listen     string
	Database   string
	Geo        string
	Log        LogConfig
	Buffer     BufferConfig
	Retention  Retention
	Dashboards DashboardsConfig
	Projects   []Project

	// keys is a flat list of non-disabled ingest keys. A slice rather than
	// a map because lookup uses subtle.ConstantTimeCompare: a map index on
	// a credential is not constant time.
	keys []keyOwner
}

type keyOwner struct {
	key     string
	project *Project
	label   string
}

// DefaultProjectsFile is where the installer puts projects.json; override
// with PROJECTS_FILE.
const DefaultProjectsFile = "/etc/analytics/projects.json"

// Load builds the configuration from the process environment and the
// projects file, for the commands that need the project list.
func Load() (*Config, error) {
	return FromEnv(os.LookupEnv)
}

// LoadDashboards builds the configuration for `analytics dashboards`, which
// renders whatever database it is pointed at and never reads the project list.
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
// a map lookup in tests) and loads the projects file it names.
func FromEnv(lookup func(string) (string, bool)) (*Config, error) {
	return parse(lookup, true)
}

// FromEnvDashboards is FromEnv without the projects file: a machine that only
// reads a replica has no project list to give.
func FromEnvDashboards(lookup func(string) (string, bool)) (*Config, error) {
	return parse(lookup, false)
}

func parse(lookup func(string) (string, bool), withProjects bool) (*Config, error) {
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
	if e.err != nil {
		return nil, e.err
	}
	if !withProjects {
		if c.Dashboards.DBPath == "" {
			if c.Database == "" {
				return nil, fmt.Errorf("config: DASHBOARDS_DB_PATH or DATABASE_URL is required")
			}
			c.Dashboards.DBPath = strings.TrimPrefix(c.Database, "sqlite://")
		}
		return c, nil
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
	c.keys = nil
	for i := range c.Projects {
		p := &c.Projects[i]
		if pa := p.ProductAggregation; pa != nil && pa.TopN == 0 {
			pa.TopN = 50
		}
		if p.Identity == "" {
			p.Identity = IdentityAnonymous
		}
		for _, k := range p.IngestKeys {
			if !k.Disabled && k.Key != "" {
				c.keys = append(c.keys, keyOwner{key: k.Key, project: p, label: k.Label})
			}
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
	for _, rc := range []RetentionClass{c.Retention.Web, c.Retention.Product, c.Retention.App} {
		if rc.RawDays < 0 || rc.AggregateDays < 0 {
			return fmt.Errorf("config: retention days must not be negative: %+v", rc)
		}
	}
	seen := map[string]bool{}
	seenKey := map[string]string{}
	for _, p := range c.Projects {
		if p.Alias == "" {
			return fmt.Errorf("config: project with empty alias")
		}
		if seen[p.Alias] {
			return fmt.Errorf("config: duplicate project alias %q", p.Alias)
		}
		seen[p.Alias] = true
		switch p.Identity {
		case IdentityAnonymous, IdentityIdentified:
		default:
			return fmt.Errorf("config: project %q: identity must be %q or %q, got %q",
				p.Alias, IdentityAnonymous, IdentityIdentified, p.Identity)
		}
		if len(p.IngestKeys) == 0 {
			return fmt.Errorf("config: project %q has no ingest_keys; run `analytics keygen`", p.Alias)
		}
		for _, k := range p.IngestKeys {
			if k.Key == "" {
				return fmt.Errorf("config: project %q has an empty ingest key", p.Alias)
			}
			if owner, dup := seenKey[k.Key]; dup {
				return fmt.Errorf("config: an ingest key of project %q is already used by project %q; keys must be globally unique", p.Alias, owner)
			}
			seenKey[k.Key] = p.Alias
		}
		for _, o := range p.AllowedOrigins {
			if o == "" {
				return fmt.Errorf("config: project %q has an empty allowed_origin", p.Alias)
			}
		}
		// Validate merged retention for project
		r := c.RetentionFor(p.Alias)
		if r.Web.RawDays < 0 || r.Web.AggregateDays < 0 ||
			r.Product.RawDays < 0 || r.Product.AggregateDays < 0 ||
			r.App.RawDays < 0 || r.App.AggregateDays < 0 {
			return fmt.Errorf("config: project %q has invalid merged retention (negative values): web=%+v product=%+v app=%+v", p.Alias, r.Web, r.Product, r.App)
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
	apply(&r.App, p.Retention.App)
	return r
}

// ProjectByKey resolves a project from an ingest key. Disabled keys never
// resolve. This is the only auth outcome: the key resolves or it does not,
// so there is no unknown-project oracle to keep indistinguishable.
//
// Every candidate is compared with no early return on match, so the loop's
// timing does not vary with key content. Length differences do leak through
// ConstantTimeCompare's zero return; that is unavoidable and harmless for
// 128-bit random keys.
func (c *Config) ProjectByKey(key string) (*Project, string, bool) {
	if key == "" {
		return nil, "", false
	}
	match := -1
	kb := []byte(key)
	for i := range c.keys {
		if subtle.ConstantTimeCompare([]byte(c.keys[i].key), kb) == 1 {
			match = i
		}
	}
	if match < 0 {
		return nil, "", false
	}
	return c.keys[match].project, c.keys[match].label, true
}

// MaxEventAge is derived from the app raw window rather than separately
// configurable: the two must agree or a clamped timestamp could land in an
// already-aggregated day.
func (c *Config) MaxEventAge() time.Duration {
	return time.Duration(c.Retention.App.RawDays) * 24 * time.Hour
}

// DisabledKeyProjects lists projects that can receive nothing because all
// their keys are disabled. That is a legitimate retired state, so callers
// warn rather than fail.
func (c *Config) DisabledKeyProjects() []string {
	var out []string
	for i := range c.Projects {
		p := &c.Projects[i]
		active := false
		for _, k := range p.IngestKeys {
			if !k.Disabled {
				active = true
				break
			}
		}
		if !active {
			out = append(out, p.Alias)
		}
	}
	return out
}
