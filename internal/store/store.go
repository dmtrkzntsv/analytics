package store

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/dmitry/analytics/internal/civil"
)

// WebHit represents a single web analytics event.
type WebHit struct {
	ID, Project                       string
	TS                                time.Time
	VisitorHash, Path, ReferrerSource string
	UTMSource, UTMMedium, UTMCampaign string
	Country, Device, Browser, OS      string
}

// ProductEvent represents a product analytics event.
type ProductEvent struct {
	ID, Project, EventName, UserID string
	TS                             time.Time
	Attributes                     map[string]string
}

// ProjectInfo represents basic information about a project.
type ProjectInfo struct {
	Alias, Name string
}

// ProductAggSettings mirrors config.ProductAggregation; zero value =
// aggregation disabled (raw rows deleted without rollup, spec §9).
type ProductAggSettings struct {
	Enabled    bool
	Attributes map[string][]string // event name (or "*") -> attr keys
	TopN       int
}

// Store defines the interface for analytics data storage.
type Store interface {
	Migrate(ctx context.Context) error
	SyncProjects(ctx context.Context, ps []ProjectInfo) error
	WriteWebHits(ctx context.Context, hits []WebHit) error
	WriteProductEvents(ctx context.Context, evs []ProductEvent) error
	WebDaysBefore(ctx context.Context, project string, before civil.Date) ([]civil.Date, error)
	ProductDaysBefore(ctx context.Context, project string, before civil.Date) ([]civil.Date, error)
	AggregateWebDay(ctx context.Context, project string, day civil.Date) error
	AggregateProductDay(ctx context.Context, project string, day civil.Date, agg ProductAggSettings) error
	PruneAggregates(ctx context.Context, project string, webBefore, productBefore, appBefore civil.Date) error
	IncrementalVacuum(ctx context.Context) error
	ProjectAliases(ctx context.Context) ([]string, error) // all rows incl. archived
	KnownAttributeKeys(ctx context.Context) ([]string, error)
	RebuildFlatView(ctx context.Context, keys []string) error
	GetMeta(ctx context.Context, key string) (string, error) // "" if absent
	SetMeta(ctx context.Context, key, value string) error
	Close() error
}

var registry = map[string]func(string) (Store, error){}

// Register registers a backend factory for the given DSN scheme.
func Register(scheme string, fn func(string) (Store, error)) {
	registry[scheme] = fn
}

// Open selects a backend by DSN scheme. sqlite:// is registered by the
// sqlite package's init via Register.
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
