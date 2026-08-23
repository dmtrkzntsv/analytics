package config

import (
	"os"
	"strings"
	"testing"
	"time"
)

const minimal = `{
  "database": "sqlite:///tmp/a.db",
  "projects": [{"alias": "app", "name": "App", "allowed_origins": ["https://app.com"]}]
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
	    "alias": "app", "name": "App", "allowed_origins": ["https://app.com"],
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
	    {"alias": "a", "name": "A", "allowed_origins": ["https://a.com"]},
	    {"alias": "b", "name": "B", "allowed_origins": ["https://b.com"],
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
		"no database":        `{"projects":[{"alias":"a","name":"A","allowed_origins":["https://a.com"]}]}`,
		"no projects":        `{"database":"sqlite:///tmp/a.db"}`,
		"dup alias":          `{"database":"sqlite:///tmp/a.db","projects":[{"alias":"a","name":"A","allowed_origins":["https://a.com"]},{"alias":"a","name":"A2","allowed_origins":["https://b.com"]}]}`,
		"bad geo scheme":     `{"database":"sqlite:///tmp/a.db","geo":"???","projects":[{"alias":"a","name":"A","allowed_origins":["https://a.com"]}]}`,
		"negative raw_days":  `{"database":"sqlite:///tmp/a.db","retention":{"web":{"raw_days":-1,"aggregate_days":365}},"projects":[{"alias":"a","name":"A","allowed_origins":["https://a.com"]}]}`,
		"empty origin":       `{"database":"sqlite:///tmp/a.db","projects":[{"alias":"a","name":"A","allowed_origins":[""]}]}`,
		"invalid duration":   `{"database":"sqlite:///tmp/a.db","buffer":{"flush_interval":"fast"},"projects":[{"alias":"a","name":"A","allowed_origins":["https://a.com"]}]}`,
		"project negative override": `{"database":"sqlite:///tmp/a.db","projects":[{"alias":"a","name":"A","allowed_origins":["https://a.com"],"retention":{"web":{"raw_days":-2}}}]}`,
	}
	for name, in := range cases {
		if _, err := Parse(strings.NewReader(in)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
}

func TestPartialRetentionOverride(t *testing.T) {
	c, err := Parse(strings.NewReader(`{
	  "database": "sqlite:///tmp/a.db",
	  "retention": {"web": {"raw_days": 5}},
	  "projects": [{"alias": "a", "name": "A", "allowed_origins": ["https://a.com"]}]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	// Partial override: web.raw_days=5 set, but web.aggregate_days should default to 365
	if c.Retention.Web.RawDays != 5 || c.Retention.Web.AggregateDays != 365 {
		t.Errorf("Partial override failed: Web=%+v, want {5, 365}", c.Retention.Web)
	}
	// Product should use all defaults
	if c.Retention.Product.RawDays != 30 || c.Retention.Product.AggregateDays != 365 {
		t.Errorf("Product defaults failed: %+v, want {30, 365}", c.Retention.Product)
	}
}

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/config.json"
	if err := os.WriteFile(path, []byte(minimal), 0644); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Database != "sqlite:///tmp/a.db" {
		t.Errorf("Database = %q", c.Database)
	}
}
