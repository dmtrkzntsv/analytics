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
	Alias              string              `json:"alias"`
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
	}
	for _, rc := range []RetentionClass{c.Retention.Web, c.Retention.Product} {
		if rc.RawDays <= 0 || rc.AggregateDays < 0 {
			return fmt.Errorf("config: retention days out of range: %+v", rc)
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
