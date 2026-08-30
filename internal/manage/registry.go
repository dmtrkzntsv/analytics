// Package manage owns the project registry (managed-config spec §3–§4):
// an immutable snapshot behind an atomic pointer for the ingest hot path,
// and the audited operations that mutate it. MCP tools, CLI subcommands
// and the importer are thin frontends over this package.
package manage

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

type Project struct {
	Alias, Name, Identity string
	AllowedOrigins        []string
	Retention             *config.RetentionOverride
	Aggregation           *config.ProductAggregation
	Archived              bool
}

type keyOwner struct {
	key     string
	project *Project
	label   string
}

// Snapshot is an immutable view of the registry. Readers pay one atomic
// load; every mutation builds a fresh one.
type Snapshot struct {
	byAlias  map[string]*Project
	ordered  []*Project
	keys     []keyOwner // active keys of non-archived projects only
	origins  map[string]map[string]bool
	defaults config.Retention
}

// pollInterval bounds how often the hot path re-reads config_version to
// notice out-of-process writes (CLI, second serve process). Spec §3.3.
const pollInterval = time.Second

type Registry struct {
	st       store.Store
	defaults config.Retention
	logger   *slog.Logger

	snap      atomic.Pointer[Snapshot]
	version   atomic.Int64
	lastCheck atomic.Int64 // UnixNano of the last version poll
}

func New(st store.Store, defaults config.Retention, logger *slog.Logger) *Registry {
	r := &Registry{st: st, defaults: defaults, logger: logger}
	r.snap.Store(&Snapshot{byAlias: map[string]*Project{}, origins: map[string]map[string]bool{}, defaults: defaults})
	return r
}

// Reload rebuilds the snapshot from the database. Called at boot, after
// every in-process write, and by the poll when config_version moves.
func (r *Registry) Reload(ctx context.Context) error {
	ps, ks, err := r.st.LoadRegistry(ctx)
	if err != nil {
		return fmt.Errorf("manage: load registry: %w", err)
	}
	v, err := r.st.ConfigVersion(ctx)
	if err != nil {
		return fmt.Errorf("manage: config version: %w", err)
	}
	s := &Snapshot{
		byAlias:  make(map[string]*Project, len(ps)),
		origins:  make(map[string]map[string]bool, len(ps)),
		defaults: r.defaults,
	}
	for _, rp := range ps {
		p := &Project{Alias: rp.Alias, Name: rp.Name, Identity: rp.Identity, Archived: rp.Archived}
		if rp.AllowedOrigins != "" {
			if err := json.Unmarshal([]byte(rp.AllowedOrigins), &p.AllowedOrigins); err != nil {
				return fmt.Errorf("manage: project %q allowed_origins: %w", rp.Alias, err)
			}
		}
		if rp.Retention != "" {
			p.Retention = new(config.RetentionOverride)
			if err := json.Unmarshal([]byte(rp.Retention), p.Retention); err != nil {
				return fmt.Errorf("manage: project %q retention: %w", rp.Alias, err)
			}
		}
		if rp.Aggregation != "" {
			p.Aggregation = new(config.ProductAggregation)
			if err := json.Unmarshal([]byte(rp.Aggregation), p.Aggregation); err != nil {
				return fmt.Errorf("manage: project %q product_aggregation: %w", rp.Alias, err)
			}
			if p.Aggregation.TopN == 0 {
				p.Aggregation.TopN = 50
			}
		}
		s.byAlias[p.Alias] = p
		s.ordered = append(s.ordered, p)
		set := map[string]bool{}
		for _, o := range p.AllowedOrigins {
			set[trimSlash(o)] = true
		}
		s.origins[p.Alias] = set
	}
	for _, k := range ks {
		p := s.byAlias[k.Project]
		if p == nil || p.Archived || k.Disabled {
			continue // archived projects reject events (001_init.sql comment)
		}
		s.keys = append(s.keys, keyOwner{key: k.Key, project: p, label: k.Label})
	}
	r.snap.Store(s)
	r.version.Store(v)
	r.lastCheck.Store(time.Now().UnixNano())
	return nil
}

// Snapshot returns the current registry view, polling config_version at
// most once per pollInterval to notice out-of-process writes. On poll
// failure the previous snapshot keeps serving: a transient read error
// must not take down ingestion.
func (r *Registry) Snapshot(ctx context.Context) *Snapshot {
	last := r.lastCheck.Load()
	now := time.Now().UnixNano()
	if now-last >= int64(pollInterval) && r.lastCheck.CompareAndSwap(last, now) {
		if v, err := r.st.ConfigVersion(ctx); err != nil {
			r.logger.Warn("registry version poll failed", "error", err)
		} else if v != r.version.Load() {
			if err := r.Reload(ctx); err != nil {
				r.logger.Warn("registry reload failed", "error", err)
			}
		}
	}
	return r.snap.Load()
}

func trimSlash(o string) string {
	if len(o) > 0 && o[len(o)-1] == '/' {
		return o[:len(o)-1]
	}
	return o
}

func (s *Snapshot) Project(alias string) *Project { return s.byAlias[alias] }

func (s *Snapshot) Projects() []*Project { return s.ordered }

// ProjectByKey preserves the constant-time contract of the old
// config.ProjectByKey: every candidate compared, no early return.
func (s *Snapshot) ProjectByKey(key string) (*Project, string, bool) {
	if key == "" {
		return nil, "", false
	}
	match := -1
	kb := []byte(key)
	for i := range s.keys {
		if subtle.ConstantTimeCompare([]byte(s.keys[i].key), kb) == 1 {
			match = i
		}
	}
	if match < 0 {
		return nil, "", false
	}
	return s.keys[match].project, s.keys[match].label, true
}

func (s *Snapshot) OriginAllowed(alias, origin string) bool {
	set, ok := s.origins[alias]
	return ok && set[trimSlash(origin)]
}

func (s *Snapshot) AnyOriginAllowed(origin string) bool {
	o := trimSlash(origin)
	for _, set := range s.origins {
		if set[o] {
			return true
		}
	}
	return false
}

// RetentionFor merges the project's override over the global defaults,
// byte-for-byte the same semantics as the old config.RetentionFor.
func (s *Snapshot) RetentionFor(alias string) config.Retention {
	r := s.defaults
	p := s.byAlias[alias]
	if p == nil || p.Retention == nil {
		return r
	}
	apply := func(dst *config.RetentionClass, o *config.RetentionClassOverride) {
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

// KeylessProjects lists active projects with no active key: a legitimate
// retired state, so callers warn rather than fail.
func (s *Snapshot) KeylessProjects() []string {
	withKey := map[string]bool{}
	for _, k := range s.keys {
		withKey[k.project.Alias] = true
	}
	var out []string
	for _, p := range s.ordered {
		if !p.Archived && !withKey[p.Alias] {
			out = append(out, p.Alias)
		}
	}
	return out
}

func (s *Snapshot) AggregationFor(alias string) store.ProductAggSettings {
	p := s.byAlias[alias]
	if p == nil || p.Aggregation == nil {
		return store.ProductAggSettings{}
	}
	return store.ProductAggSettings{
		Enabled:    p.Aggregation.Enabled,
		Attributes: p.Aggregation.Attributes,
		TopN:       p.Aggregation.TopN,
	}
}
