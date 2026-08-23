package jobs

import (
	"context"
	"database/sql"
	"log/slog"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dmitry/analytics/internal/civil"
	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/config/configtest"
	"github.com/dmitry/analytics/internal/identity"
	"github.com/dmitry/analytics/internal/store"
	_ "github.com/dmitry/analytics/internal/store/sqlite"
	_ "modernc.org/sqlite"
)

const jobsProjects = `[{"alias": "app", "name": "App", "allowed_origins": ["https://a.com"],
  "product_aggregation": {"enabled": true, "attributes": {"*": ["plan"]}, "top_n": 50}}]`

// jobsVars pins the retention windows the assertions below rely on.
var jobsVars = map[string]string{
	"RETENTION_WEB_RAW_DAYS": "7", "RETENTION_WEB_AGGREGATE_DAYS": "365",
	"RETENTION_PRODUCT_RAW_DAYS": "7", "RETENTION_PRODUCT_AGGREGATE_DAYS": "365",
}

// countingStore counts daily passes; IncrementalVacuum runs exactly once per
// pass, which makes it a reliable proxy.
type countingStore struct {
	store.Store
	vacuums atomic.Int64
}

func (c *countingStore) IncrementalVacuum(ctx context.Context) error {
	c.vacuums.Add(1)
	return c.Store.IncrementalVacuum(ctx)
}

type countingRotator struct {
	*identity.Salter
	rotations atomic.Int64
}

func (c *countingRotator) Rotate(ctx context.Context) error {
	c.rotations.Add(1)
	return c.Salter.Rotate(ctx)
}

func openStoreAt(t *testing.T) (store.Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "jobs.db")
	st, err := store.Open("sqlite://" + path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	if err := st.Migrate(context.Background()); err != nil {
		t.Fatal(err)
	}
	return st, path
}

func openStore(t *testing.T) store.Store {
	t.Helper()
	st, _ := openStoreAt(t)
	return st
}

func setup(t *testing.T, projectsJSON string) (store.Store, *config.Config, *Runner) {
	t.Helper()
	cfg := configtest.Load(t, jobsVars, projectsJSON)
	st, path := openStoreAt(t)
	t.Setenv("JOBS_TEST_DB", path)
	now := func() time.Time { return time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC) }
	return st, cfg, New(st, cfg, identity.NewSalter(st, now), slog.Default(), now)
}

func mustTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func mustDay(s string) civil.Date { d, _ := civil.Parse(s); return d }

func TestRunDailyPassAggregatesOldDays(t *testing.T) {
	st, _, r := setup(t, jobsProjects)
	ctx := context.Background()
	// Old day (beyond the 7-day raw window relative to fake now 2026-08-22).
	if err := st.WriteWebHits(ctx, []store.WebHit{
		{ID: "1", Project: "app", TS: mustTime("2026-08-10T10:00:00Z"), VisitorHash: "v", Path: "/"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteProductEvents(ctx, []store.ProductEvent{
		{ID: "2", Project: "app", EventName: "e", UserID: "u", TS: mustTime("2026-08-10T10:00:00Z"),
			Attributes: map[string]string{"plan": "pro"}}}); err != nil {
		t.Fatal(err)
	}
	// Recent day (inside the window) must survive as raw.
	if err := st.WriteWebHits(ctx, []store.WebHit{
		{ID: "3", Project: "app", TS: mustTime("2026-08-21T10:00:00Z"), VisitorHash: "v", Path: "/"}}); err != nil {
		t.Fatal(err)
	}
	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}
	oldWeb, err := st.WebDaysBefore(ctx, "app", mustDay("2026-08-20"))
	if err != nil {
		t.Fatal(err)
	}
	if len(oldWeb) != 0 {
		t.Fatalf("old web raw must be gone: %v", oldWeb)
	}
	recent, err := st.WebDaysBefore(ctx, "app", mustDay("2026-08-23"))
	if err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 {
		t.Fatalf("recent raw must survive: %v", recent)
	}
	oldProd, err := st.ProductDaysBefore(ctx, "app", mustDay("2026-08-20"))
	if err != nil {
		t.Fatal(err)
	}
	if len(oldProd) != 0 {
		t.Fatalf("old product raw must be gone: %v", oldProd)
	}
	// Second pass is a no-op (idempotency at the job level).
	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}
	if len(recent) != 1 {
		t.Fatal("second pass must not disturb in-window raw data")
	}
}

// The pass must rebuild v_events_flat from the keys actually present, so a
// newly seen attribute becomes queryable without a restart.
func TestRunDailyPassRebuildsFlatView(t *testing.T) {
	st, _, r := setup(t, jobsProjects)
	ctx := context.Background()
	// Inside the raw window, so it survives to be discovered.
	if err := st.WriteProductEvents(ctx, []store.ProductEvent{
		{ID: "1", Project: "app", EventName: "e", UserID: "u", TS: mustTime("2026-08-21T10:00:00Z"),
			Attributes: map[string]string{"plan": "pro"}}}); err != nil {
		t.Fatal(err)
	}
	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}
	// Inspect the view through a separate read-only handle: Store deliberately
	// does not expose its *sql.DB.
	raw, err := sql.Open("sqlite", "file:"+os.Getenv("JOBS_TEST_DB"))
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	var plan string
	if err := raw.QueryRow(`SELECT attr_plan FROM v_events_flat WHERE id='1'`).Scan(&plan); err != nil {
		t.Fatalf("v_events_flat not rebuilt with the discovered key: %v", err)
	}
	if plan != "pro" {
		t.Errorf("attr_plan = %q, want pro", plan)
	}
}

// A project that exists in the database but has been removed from config
// (archived) must still be maintained, using global retention.
func TestRunDailyPassCoversArchivedProjects(t *testing.T) {
	st, _, r := setup(t, jobsProjects)
	ctx := context.Background()
	if err := st.SyncProjects(ctx, []store.ProjectInfo{{Alias: "app", Name: "App"}, {Alias: "gone", Name: "Gone"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteWebHits(ctx, []store.WebHit{
		{ID: "1", Project: "gone", TS: mustTime("2026-08-10T10:00:00Z"), VisitorHash: "v", Path: "/"}}); err != nil {
		t.Fatal(err)
	}
	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}
	left, err := st.WebDaysBefore(ctx, "gone", mustDay("2026-08-20"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("archived project not aggregated: %v", left)
	}
}

// A per-project retention override must win over the global window.
func TestRunDailyPassHonoursProjectRetention(t *testing.T) {
	const projectsJSON = `[
        {"alias": "app", "name": "App", "allowed_origins": ["https://a.com"]},
        {"alias": "keep", "name": "Keep", "allowed_origins": ["https://b.com"],
         "retention": {"web": {"raw_days": 90}}}]`
	st, _, r := setup(t, projectsJSON)
	ctx := context.Background()
	hits := []store.WebHit{
		{ID: "1", Project: "app", TS: mustTime("2026-08-10T10:00:00Z"), VisitorHash: "v", Path: "/"},
		{ID: "2", Project: "keep", TS: mustTime("2026-08-10T10:00:00Z"), VisitorHash: "v", Path: "/"},
	}
	if err := st.WriteWebHits(ctx, hits); err != nil {
		t.Fatal(err)
	}
	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}
	appLeft, err := st.WebDaysBefore(ctx, "app", mustDay("2026-08-22"))
	if err != nil {
		t.Fatal(err)
	}
	if len(appLeft) != 0 {
		t.Fatalf("app raw should be aggregated under the 7-day window: %v", appLeft)
	}
	keepLeft, err := st.WebDaysBefore(ctx, "keep", mustDay("2026-08-22"))
	if err != nil {
		t.Fatal(err)
	}
	if len(keepLeft) != 1 {
		t.Fatalf("keep raw should survive its 90-day window: %v", keepLeft)
	}
}

func TestAggSettingsFor(t *testing.T) {
	if got := aggSettingsFor(nil); got.Enabled || got.TopN != 0 {
		t.Errorf("nil project = %+v, want zero value", got)
	}
	if got := aggSettingsFor(&config.Project{}); got.Enabled {
		t.Errorf("project without product_aggregation = %+v, want disabled", got)
	}
	p := &config.Project{ProductAggregation: &config.ProductAggregation{
		Enabled: true, Attributes: map[string][]string{"*": {"plan"}}, TopN: 25}}
	got := aggSettingsFor(p)
	if !got.Enabled || got.TopN != 25 || len(got.Attributes["*"]) != 1 {
		t.Errorf("aggSettingsFor = %+v", got)
	}
}

// The scheduler must fire salt rotation at 00:00 and the daily pass at 03:00,
// each at most once per day, and neither at other hours.
func TestScheduleFiresOncePerDay(t *testing.T) {
	cfg := configtest.Load(t, jobsVars, jobsProjects)
	clock := time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	st := &countingStore{Store: openStore(t)}
	rot := &countingRotator{Salter: identity.NewSalter(st, now)}
	r := New(st, cfg, rot, slog.Default(), now)
	ctx := context.Background()

	at := func(h, m int) {
		t.Helper()
		clock = time.Date(2026, 8, 22, h, m, 0, 0, time.UTC)
		r.runScheduled(ctx)
	}
	at(0, 0)
	at(0, 30) // same hour, same day: must not rotate twice
	if got := rot.rotations.Load(); got != 1 {
		t.Errorf("rotations = %d, want 1", got)
	}
	at(1, 0)
	if got := st.vacuums.Load(); got != 0 {
		t.Errorf("daily pass ran at 01:00 (%d vacuums)", got)
	}
	at(3, 0)
	at(3, 45) // same hour, same day: must not run twice
	if got := st.vacuums.Load(); got != 1 {
		t.Errorf("daily passes = %d, want 1", got)
	}

	// Next day: both fire again.
	clock = time.Date(2026, 8, 23, 0, 0, 0, 0, time.UTC)
	r.runScheduled(ctx)
	clock = time.Date(2026, 8, 23, 3, 0, 0, 0, time.UTC)
	r.runScheduled(ctx)
	if got := rot.rotations.Load(); got != 2 {
		t.Errorf("rotations = %d, want 2 after a day boundary", got)
	}
	if got := st.vacuums.Load(); got != 2 {
		t.Errorf("daily passes = %d, want 2 after a day boundary", got)
	}
}

// Boot must run a catch-up pass immediately so downtime never skips a day,
// and Run must return when the context is cancelled.
func TestRunBootCatchUpAndCancel(t *testing.T) {
	cfg := configtest.Load(t, jobsVars, jobsProjects)
	now := func() time.Time { return time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC) }
	st := &countingStore{Store: openStore(t)}
	rot := &countingRotator{Salter: identity.NewSalter(st, now)}
	r := New(st, cfg, rot, slog.Default(), now)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()

	deadline := time.After(5 * time.Second)
	for st.vacuums.Load() == 0 {
		select {
		case <-deadline:
			t.Fatal("boot catch-up pass never ran")
		case <-time.After(time.Millisecond):
		}
	}
	// Boot must also ensure a salt exists.
	salt, err := rot.Current(context.Background())
	if err != nil || salt == "" {
		t.Fatalf("salt after boot = %q, err %v", salt, err)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}
}
