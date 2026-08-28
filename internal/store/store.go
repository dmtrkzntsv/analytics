package store

import (
	"context"
	"fmt"
	"net/url"
	"time"

	"github.com/dmitry/analytics/internal/civil"
)

// WebHit represents a single web pageview ($pageview).
type WebHit struct {
	ID, Project                       string
	TS, ReceivedAt                    time.Time
	ActorID, UserID, GroupID          string
	Path, ReferrerSource              string
	UTMSource, UTMMedium, UTMCampaign string
	Country, Device, Browser, OS      string
}

// ProductEvent represents a custom event from any surface.
type ProductEvent struct {
	ID, Project, EventName   string
	TS, ReceivedAt           time.Time
	ActorID, UserID, GroupID string
	Platform, AppVersion     string
	Attributes               map[string]string
}

// AppView represents a single app screen view ($screen_view). Note what is
// absent: no browser, no device class, no referrer, no utm — and no
// User-Agent parsing anywhere. Apps declare their context, which is why the
// Electron-reports-as-Chrome and OkHttp-reports-as-desktop problems do not
// arise here rather than being patched around.
type AppView struct {
	ID, Project                         string
	TS, ReceivedAt                      time.Time
	ActorID, UserID, GroupID, SessionID string
	Screen                              string
	Platform, AppVersion                string
	OSVersion, DeviceModel, Locale      string
	Country                             string
}

// Identity is a display name for a user or group. Names live in their own
// table rather than on event rows: a name repeated on every row could never
// be updated, and names change.
type Identity struct {
	Project, Kind, ID, Name string
}

// Identity kinds.
const (
	KindUser  = "user"
	KindGroup = "group"
)

// ProjectInfo represents basic information about a project.
type ProjectInfo struct {
	Alias, Name string
}

// RegistryProject represents a project configuration from the registry.
type RegistryProject struct {
	Alias, Name, Identity string
	AllowedOrigins        string // JSON array, "[]" if none
	Retention             string // JSON object or ""
	Aggregation           string // JSON object or ""
	Archived              bool
}

// RegistryKey represents an API key in the registry.
type RegistryKey struct {
	Key, Project, Label string
	Disabled            bool
}

// AuditEntry represents a single audit log entry.
type AuditEntry struct {
	Actor, Action, Subject, Detail string
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
	WriteAppViews(ctx context.Context, views []AppView) error
	UpsertIdentities(ctx context.Context, ids []Identity) error
	WebDaysBefore(ctx context.Context, project string, before civil.Date) ([]civil.Date, error)
	ProductDaysBefore(ctx context.Context, project string, before civil.Date) ([]civil.Date, error)
	AggregateWebDay(ctx context.Context, project string, day civil.Date) error
	AggregateProductDay(ctx context.Context, project string, day civil.Date, agg ProductAggSettings) error
	AppDaysBefore(ctx context.Context, project string, before civil.Date) ([]civil.Date, error)
	AggregateAppDay(ctx context.Context, project string, day civil.Date) error
	UpsertActors(ctx context.Context, project string, day civil.Date) error
	AggregateRetentionDay(ctx context.Context, project string, day civil.Date) error
	PruneActors(ctx context.Context, project string, before civil.Date) error
	AggregateIdentityDay(ctx context.Context, project string, day civil.Date) error
	PruneIdentities(ctx context.Context, project string, before civil.Date) error
	PruneAggregates(ctx context.Context, project string, webBefore, productBefore, appBefore civil.Date) error
	IncrementalVacuum(ctx context.Context) error
	ProjectAliases(ctx context.Context) ([]string, error) // all rows incl. archived
	KnownAttributeKeys(ctx context.Context) ([]string, error)
	RebuildFlatView(ctx context.Context, keys []string) error
	GetMeta(ctx context.Context, key string) (string, error) // "" if absent
	SetMeta(ctx context.Context, key, value string) error
	LoadRegistry(ctx context.Context) ([]RegistryProject, []RegistryKey, error)
	ConfigVersion(ctx context.Context) (int64, error)
	CreateProject(ctx context.Context, p RegistryProject, a AuditEntry) error
	UpdateProject(ctx context.Context, p RegistryProject, a AuditEntry) error
	SetProjectArchived(ctx context.Context, alias string, archived bool, a AuditEntry) error
	InsertIngestKey(ctx context.Context, k RegistryKey, a AuditEntry) error
	SetIngestKeyDisabled(ctx context.Context, project, label string, disabled bool, a AuditEntry) error
	DeleteProjectData(ctx context.Context, alias string, a AuditEntry) error
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
