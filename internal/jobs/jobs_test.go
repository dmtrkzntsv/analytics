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

	"github.com/dmtrkzntsv/twillingate/internal/civil"
	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/config/configtest"
	"github.com/dmtrkzntsv/twillingate/internal/identity"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	_ "github.com/dmtrkzntsv/twillingate/internal/store/sqlite"
	_ "modernc.org/sqlite"
)

func intPtr(n int) *int { return &n }

var jobsProjectSpecs = []manage.ProjectSpec{
	{Alias: "app", Name: "App", AllowedOrigins: []string{"https://a.com"},
		Attributes: []string{"plan"}},
}

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

// newRegistry builds a registry over st, seeded with specs via the audited
// Ops path (mirrors production: registry rows come from CreateProject, not
// a direct insert).
func newRegistry(t *testing.T, st store.Store, cfg *config.Config, specs []manage.ProjectSpec) *manage.Registry {
	t.Helper()
	ctx := context.Background()
	reg := manage.New(st, cfg.Retention, slog.Default())
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	ops := manage.NewOps(reg, st)
	for _, spec := range specs {
		if _, err := ops.CreateProject(ctx, "test", spec); err != nil {
			t.Fatal(err)
		}
	}
	return reg
}

// setup seeds a fresh store and registry from specs and returns a Runner
// wired to both, using a fixed clock (2026-08-22T04:00:00Z).
func setup(t *testing.T, vars map[string]string, specs []manage.ProjectSpec) (store.Store, *manage.Registry, *Runner) {
	t.Helper()
	cfg := configtest.Load(t, vars)
	st, path := openStoreAt(t)
	t.Setenv("JOBS_TEST_DB", path)
	reg := newRegistry(t, st, cfg, specs)
	now := func() time.Time { return time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC) }
	return st, reg, New(st, cfg, reg, identity.NewSalter(st, now), slog.Default(), now)
}

func mustTime(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func mustDay(s string) civil.Date { d, _ := civil.Parse(s); return d }

func TestRunDailyPassAggregatesOldDays(t *testing.T) {
	st, _, r := setup(t, jobsVars, jobsProjectSpecs)
	ctx := context.Background()
	// Old day (beyond the 7-day raw window relative to fake now 2026-08-22).
	if err := st.WriteWebHits(ctx, []store.WebHit{
		{ID: "1", Project: "app", TS: mustTime("2026-08-10T10:00:00Z"), ActorID: "v", Path: "/"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteProductEvents(ctx, []store.ProductEvent{
		{ID: "2", Project: "app", EventName: "e", UserID: "u", TS: mustTime("2026-08-10T10:00:00Z"),
			Attributes: map[string]string{"plan": "pro"}}}); err != nil {
		t.Fatal(err)
	}
	// Recent day (inside the window) must survive as raw.
	if err := st.WriteWebHits(ctx, []store.WebHit{
		{ID: "3", Project: "app", TS: mustTime("2026-08-21T10:00:00Z"), ActorID: "v", Path: "/"}}); err != nil {
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
	st, _, r := setup(t, jobsVars, jobsProjectSpecs)
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

// A project that has been archived in the registry must still be
// maintained, using global retention: it is still a registry row, so
// store.ProjectAliases (all rows, including archived) still returns it.
func TestRunDailyPassCoversArchivedProjects(t *testing.T) {
	st, reg, r := setup(t, jobsVars, jobsProjectSpecs)
	ctx := context.Background()
	ops := manage.NewOps(reg, st)
	if _, err := ops.CreateProject(ctx, "test", manage.ProjectSpec{Alias: "gone", Name: "Gone"}); err != nil {
		t.Fatal(err)
	}
	if err := ops.ArchiveProject(ctx, "test", "gone"); err != nil {
		t.Fatal(err)
	}
	if err := st.WriteWebHits(ctx, []store.WebHit{
		{ID: "1", Project: "gone", TS: mustTime("2026-08-10T10:00:00Z"), ActorID: "v", Path: "/"}}); err != nil {
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
	specs := []manage.ProjectSpec{
		{Alias: "app", Name: "App", AllowedOrigins: []string{"https://a.com"}},
		{Alias: "keep", Name: "Keep", AllowedOrigins: []string{"https://b.com"},
			Retention: &config.RetentionOverride{Web: &config.RetentionClassOverride{RawDays: intPtr(90)}}},
	}
	st, _, r := setup(t, jobsVars, specs)
	ctx := context.Background()
	hits := []store.WebHit{
		{ID: "1", Project: "app", TS: mustTime("2026-08-10T10:00:00Z"), ActorID: "v", Path: "/"},
		{ID: "2", Project: "keep", TS: mustTime("2026-08-10T10:00:00Z"), ActorID: "v", Path: "/"},
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

// The scheduler must fire salt rotation at 00:00 and the daily pass at 03:00,
// each at most once per day, and neither at other hours.
func TestScheduleFiresOncePerDay(t *testing.T) {
	cfg := configtest.Load(t, jobsVars)
	clock := time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	st := &countingStore{Store: openStore(t)}
	reg := newRegistry(t, st, cfg, jobsProjectSpecs)
	rot := &countingRotator{Salter: identity.NewSalter(st, now)}
	r := New(st, cfg, reg, rot, slog.Default(), now)
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
	cfg := configtest.Load(t, jobsVars)
	now := func() time.Time { return time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC) }
	st := &countingStore{Store: openStore(t)}
	reg := newRegistry(t, st, cfg, jobsProjectSpecs)
	rot := &countingRotator{Salter: identity.NewSalter(st, now)}
	r := New(st, cfg, reg, rot, slog.Default(), now)

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

// --- app class, cohorts and identities ---

var identifiedProjectSpecs = []manage.ProjectSpec{
	{Alias: "app", Name: "App", Identity: "identified", AllowedOrigins: []string{"https://a.com"}},
}

var anonymousProjectSpecs = []manage.ProjectSpec{
	{Alias: "app", Name: "App", Identity: "anonymous", AllowedOrigins: []string{"https://a.com"}},
}

// appVars uses a 7-day app raw window so the fixed 2026-08-10 fixture day is
// already outside it relative to the fake now of 2026-08-22.
var appVars = map[string]string{
	"RETENTION_WEB_RAW_DAYS": "7", "RETENTION_WEB_AGGREGATE_DAYS": "365",
	"RETENTION_PRODUCT_RAW_DAYS": "7", "RETENTION_PRODUCT_AGGREGATE_DAYS": "365",
	"RETENTION_APP_RAW_DAYS": "7", "RETENTION_APP_AGGREGATE_DAYS": "365",
}

func setupApp(t *testing.T, specs []manage.ProjectSpec) (store.Store, *Runner, *sql.DB) {
	t.Helper()
	cfg := configtest.Load(t, appVars)
	st, path := openStoreAt(t)
	reg := newRegistry(t, st, cfg, specs)
	now := func() time.Time { return time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC) }
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { raw.Close() })
	return st, New(st, cfg, reg, identity.NewSalter(st, now), slog.Default(), now), raw
}

func seedAppDay(t *testing.T, st store.Store, actors ...string) {
	t.Helper()
	var views []store.AppView
	for i, a := range actors {
		views = append(views, store.AppView{
			ID: "v" + a + string(rune('a'+i)), Project: "app",
			TS:         mustTime("2026-08-10T10:00:00Z"),
			ReceivedAt: mustTime("2026-08-10T10:00:00Z"),
			ActorID:    a, UserID: "u-" + a, GroupID: "org9",
			Screen: "/home", Platform: "ios", Version: "2.4.1",
		})
	}
	if err := st.WriteAppViews(context.Background(), views); err != nil {
		t.Fatal(err)
	}
}

func count(t *testing.T, db *sql.DB, query string) int {
	t.Helper()
	var n int
	if err := db.QueryRow(query).Scan(&n); err != nil {
		t.Fatalf("%s: %v", query, err)
	}
	return n
}

func TestRunDailyPassAggregatesAppDays(t *testing.T) {
	st, r, db := setupApp(t, identifiedProjectSpecs)
	ctx := context.Background()
	seedAppDay(t, st, "a", "b")

	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}

	if n := count(t, db, `SELECT COUNT(*) FROM agg_app_daily`); n != 1 {
		t.Errorf("agg_app_daily rows = %d, want 1", n)
	}
	if n := count(t, db, `SELECT actives FROM agg_app_daily`); n != 2 {
		t.Errorf("actives = %d, want 2", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM app_views`); n != 0 {
		t.Errorf("raw app_views left = %d, want 0", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM actors`); n != 2 {
		t.Errorf("actors = %d, want 2", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM agg_retention WHERE day_offset=0`); n != 1 {
		t.Errorf("cohort rows = %d, want 1", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM agg_identity_daily WHERE kind='group'`); n != 1 {
		t.Errorf("group aggregate rows = %d, want 1", n)
	}
	if n := count(t, db, `SELECT users FROM agg_identity_daily WHERE kind='group'`); n != 2 {
		t.Errorf("users in group = %d, want 2", n)
	}
}

func TestRunDailyPassSkipsCohortsForAnonymousProjects(t *testing.T) {
	st, r, db := setupApp(t, anonymousProjectSpecs)
	ctx := context.Background()
	seedAppDay(t, st, "a")

	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}

	if n := count(t, db, `SELECT COUNT(*) FROM actors`); n != 0 {
		t.Errorf("actors = %d; retention is undefined under daily rotation", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM agg_retention`); n != 0 {
		t.Errorf("agg_retention rows = %d, want 0", n)
	}
	// Identity aggregates and app rollups still run: only cohorts are skipped.
	if n := count(t, db, `SELECT COUNT(*) FROM agg_app_daily`); n != 1 {
		t.Errorf("agg_app_daily rows = %d, want 1", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM agg_identity_daily`); n == 0 {
		t.Error("identity aggregates must still run for anonymous projects")
	}
}

func TestRunDailyPassIsIdempotentAcrossAppSteps(t *testing.T) {
	st, r, db := setupApp(t, identifiedProjectSpecs)
	ctx := context.Background()
	seedAppDay(t, st, "a", "b")

	for i := 0; i < 2; i++ {
		if err := r.RunDailyPass(ctx); err != nil {
			t.Fatalf("pass %d: %v", i, err)
		}
	}
	if n := count(t, db, `SELECT views FROM agg_app_daily`); n != 2 {
		t.Errorf("views = %d after two passes, want 2", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM agg_retention`); n != 1 {
		t.Errorf("agg_retention rows = %d after two passes, want 1", n)
	}
}

func TestRunDailyPassPrunesActorsAndIdentities(t *testing.T) {
	st, r, db := setupApp(t, identifiedProjectSpecs)
	ctx := context.Background()

	if err := st.UpsertIdentities(ctx, []store.Identity{
		{Project: "app", Kind: store.KindUser, ID: "old", Name: "Gone"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE identities SET last_seen_day='2020-01-01'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(
		`INSERT INTO actors VALUES ('app','stale','app','2020-01-01','2020-01-01')`); err != nil {
		t.Fatal(err)
	}

	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM actors`); n != 0 {
		t.Errorf("stale actors left = %d, want 0", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM identities`); n != 0 {
		t.Errorf("stale identities left = %d, want 0", n)
	}
}

// A web-only identified project must still get cohorts and identity rollups:
// they used to be driven off app_views alone, which meant a project with no
// app never got either.
func TestRunDailyPassCoversWebOnlyProjectsForCohorts(t *testing.T) {
	st, r, db := setupApp(t, identifiedProjectSpecs)
	ctx := context.Background()
	ts := mustTime("2026-08-10T10:00:00Z")

	if err := st.WriteWebHits(ctx, []store.WebHit{
		{ID: "w1", Project: "app", TS: ts, ReceivedAt: ts, ActorID: "a",
			UserID: "u1", GroupID: "org9", Path: "/"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM actors`); n != 1 {
		t.Errorf("actors = %d for a web-only project, want 1", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM agg_retention`); n != 1 {
		t.Errorf("agg_retention rows = %d, want 1", n)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM agg_identity_daily WHERE kind='user'`); n != 1 {
		t.Errorf("user aggregate rows = %d, want 1", n)
	}
}

// Cohorts must not lag the raw-retention window. They read raw rows without
// deleting them, so they cover days still inside the window too — otherwise
// the retention page would be a whole window stale.
func TestRunDailyPassComputesCohortsForRecentDays(t *testing.T) {
	st, r, db := setupApp(t, identifiedProjectSpecs)
	ctx := context.Background()
	// Two days before the fake now of 2026-08-22, well inside the 7-day
	// app raw window used by appVars.
	recent := mustTime("2026-08-20T10:00:00Z")

	if err := st.WriteAppViews(ctx, []store.AppView{
		{ID: "r1", Project: "app", TS: recent, ReceivedAt: recent, ActorID: "a",
			UserID: "u1", Screen: "/home", Platform: "ios"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT COUNT(*) FROM agg_retention WHERE cohort_day='2026-08-20'`); n != 1 {
		t.Errorf("cohort rows for an in-window day = %d, want 1", n)
	}
	// The raw row itself must survive: it is inside the retention window.
	if n := count(t, db, `SELECT COUNT(*) FROM app_views`); n != 1 {
		t.Errorf("raw app_views = %d; an in-window day must not be aggregated away", n)
	}
}
