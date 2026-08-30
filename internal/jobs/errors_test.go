package jobs

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/civil"
	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/config/configtest"
	"github.com/dmtrkzntsv/twillingate/internal/identity"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// errBoom is the sentinel every faultyStore failure returns, so assertions
// can check the log line names the failing operation without depending on
// message wording elsewhere.
var errBoom = errors.New("boom")

// faultyStore wraps a real store.Store and makes named methods fail, either
// every call (failAlways) or after a number of successful calls (skip) —
// the latter is what lets a test fail the SECOND call to a method that
// RunDailyPass also calls once earlier via allRawDays.
type faultyStore struct {
	store.Store
	failAlways map[string]bool
	skip       map[string]int
}

func newFaultyStore(st store.Store) *faultyStore {
	return &faultyStore{Store: st, failAlways: map[string]bool{}, skip: map[string]int{}}
}

func (f *faultyStore) failing(name string) *faultyStore {
	f.failAlways[name] = true
	return f
}

func (f *faultyStore) failingAfter(name string, okCalls int) *faultyStore {
	f.skip[name] = okCalls
	return f
}

func (f *faultyStore) shouldFail(name string) bool {
	if f.failAlways[name] {
		return true
	}
	if n, ok := f.skip[name]; ok {
		if n > 0 {
			f.skip[name] = n - 1
			return false
		}
		return true
	}
	return false
}

func (f *faultyStore) ProjectAliases(ctx context.Context) ([]string, error) {
	if f.shouldFail("ProjectAliases") {
		return nil, errBoom
	}
	return f.Store.ProjectAliases(ctx)
}

func (f *faultyStore) WebDaysBefore(ctx context.Context, project string, before civil.Date) ([]civil.Date, error) {
	if f.shouldFail("WebDaysBefore") {
		return nil, errBoom
	}
	return f.Store.WebDaysBefore(ctx, project, before)
}

func (f *faultyStore) ProductDaysBefore(ctx context.Context, project string, before civil.Date) ([]civil.Date, error) {
	if f.shouldFail("ProductDaysBefore") {
		return nil, errBoom
	}
	return f.Store.ProductDaysBefore(ctx, project, before)
}

func (f *faultyStore) AppDaysBefore(ctx context.Context, project string, before civil.Date) ([]civil.Date, error) {
	if f.shouldFail("AppDaysBefore") {
		return nil, errBoom
	}
	return f.Store.AppDaysBefore(ctx, project, before)
}

func (f *faultyStore) UpsertActors(ctx context.Context, project string, day civil.Date) error {
	if f.shouldFail("UpsertActors") {
		return errBoom
	}
	return f.Store.UpsertActors(ctx, project, day)
}

func (f *faultyStore) AggregateRetentionDay(ctx context.Context, project string, day civil.Date) error {
	if f.shouldFail("AggregateRetentionDay") {
		return errBoom
	}
	return f.Store.AggregateRetentionDay(ctx, project, day)
}

func (f *faultyStore) AggregateIdentityDay(ctx context.Context, project string, day civil.Date) error {
	if f.shouldFail("AggregateIdentityDay") {
		return errBoom
	}
	return f.Store.AggregateIdentityDay(ctx, project, day)
}

func (f *faultyStore) AggregateWebDay(ctx context.Context, project string, day civil.Date) error {
	if f.shouldFail("AggregateWebDay") {
		return errBoom
	}
	return f.Store.AggregateWebDay(ctx, project, day)
}

func (f *faultyStore) AggregateProductDay(ctx context.Context, project string, day civil.Date, attrs []string, topN int) error {
	if f.shouldFail("AggregateProductDay") {
		return errBoom
	}
	return f.Store.AggregateProductDay(ctx, project, day, attrs, topN)
}

func (f *faultyStore) AggregateAppDay(ctx context.Context, project string, day civil.Date) error {
	if f.shouldFail("AggregateAppDay") {
		return errBoom
	}
	return f.Store.AggregateAppDay(ctx, project, day)
}

func (f *faultyStore) PruneAggregates(ctx context.Context, project string, webBefore, productBefore, appBefore civil.Date) error {
	if f.shouldFail("PruneAggregates") {
		return errBoom
	}
	return f.Store.PruneAggregates(ctx, project, webBefore, productBefore, appBefore)
}

func (f *faultyStore) PruneActors(ctx context.Context, project string, before civil.Date) error {
	if f.shouldFail("PruneActors") {
		return errBoom
	}
	return f.Store.PruneActors(ctx, project, before)
}

func (f *faultyStore) PruneIdentities(ctx context.Context, project string, before civil.Date) error {
	if f.shouldFail("PruneIdentities") {
		return errBoom
	}
	return f.Store.PruneIdentities(ctx, project, before)
}

func (f *faultyStore) RebuildFlatView(ctx context.Context, keys []string) error {
	if f.shouldFail("RebuildFlatView") {
		return errBoom
	}
	return f.Store.RebuildFlatView(ctx, keys)
}

func (f *faultyStore) IncrementalVacuum(ctx context.Context) error {
	if f.shouldFail("IncrementalVacuum") {
		return errBoom
	}
	return f.Store.IncrementalVacuum(ctx)
}

// setupFaulty is setup, but returns a *Runner built over a faultyStore so
// the caller can arrange targeted failures before calling RunDailyPass, plus
// a buffer capturing every log line the run produces.
func setupFaulty(t *testing.T, vars map[string]string, specs []manage.ProjectSpec) (store.Store, *faultyStore, *Runner, *bytes.Buffer) {
	t.Helper()
	cfg := configtest.Load(t, vars)
	st, _ := openStoreAt(t)
	reg := newRegistry(t, st, cfg, specs)
	fst := newFaultyStore(st)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	now := func() time.Time { return time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC) }
	r := New(fst, cfg, reg, identity.NewSalter(st, now), logger, now)
	return st, fst, r, &buf
}

func logged(buf *bytes.Buffer, substr string) bool {
	return strings.Contains(buf.String(), substr)
}

// --- fatal: failure to enumerate work at all stops the pass ---

func TestRunDailyPassFailsWhenProjectAliasesErrors(t *testing.T) {
	_, fst, r, _ := setupFaulty(t, jobsVars, jobsProjectSpecs)
	fst.failing("ProjectAliases")
	if err := r.RunDailyPass(context.Background()); !errors.Is(err, errBoom) {
		t.Fatalf("RunDailyPass = %v, want errBoom", err)
	}
}

func TestRunDailyPassFailsWhenAllRawDaysWebQueryErrors(t *testing.T) {
	_, fst, r, _ := setupFaulty(t, jobsVars, jobsProjectSpecs)
	fst.failing("WebDaysBefore")
	if err := r.RunDailyPass(context.Background()); !errors.Is(err, errBoom) {
		t.Fatalf("RunDailyPass = %v, want errBoom", err)
	}
}

func TestRunDailyPassFailsWhenWebRawWindowQueryErrors(t *testing.T) {
	_, fst, r, _ := setupFaulty(t, jobsVars, jobsProjectSpecs)
	// allRawDays makes the first WebDaysBefore call; let that one succeed so
	// the pass reaches the direct raw-window call further down, and fail
	// that one instead.
	fst.failingAfter("WebDaysBefore", 1)
	if err := r.RunDailyPass(context.Background()); !errors.Is(err, errBoom) {
		t.Fatalf("RunDailyPass = %v, want errBoom", err)
	}
}

func TestRunDailyPassFailsWhenProductRawWindowQueryErrors(t *testing.T) {
	_, fst, r, _ := setupFaulty(t, jobsVars, jobsProjectSpecs)
	fst.failingAfter("ProductDaysBefore", 1)
	if err := r.RunDailyPass(context.Background()); !errors.Is(err, errBoom) {
		t.Fatalf("RunDailyPass = %v, want errBoom", err)
	}
}

func TestRunDailyPassFailsWhenAppRawWindowQueryErrors(t *testing.T) {
	_, fst, r, _ := setupFaulty(t, jobsVars, jobsProjectSpecs)
	fst.failingAfter("AppDaysBefore", 1)
	if err := r.RunDailyPass(context.Background()); !errors.Is(err, errBoom) {
		t.Fatalf("RunDailyPass = %v, want errBoom", err)
	}
}

// --- per-project soft failures: logged, pass continues, RunDailyPass returns nil ---

var identifiedJobsSpecs = []manage.ProjectSpec{
	{Alias: "app", Name: "App", Identity: config.IdentityIdentified, AllowedOrigins: []string{"https://a.com"}},
}

func TestRunDailyPassLogsUpsertActorsFailure(t *testing.T) {
	st, fst, r, buf := setupFaulty(t, jobsVars, identifiedJobsSpecs)
	ctx := context.Background()
	if err := st.WriteWebHits(ctx, []store.WebHit{
		{ID: "1", Project: "app", TS: mustTime("2026-08-20T10:00:00Z"), ActorID: "v", Path: "/"}}); err != nil {
		t.Fatal(err)
	}
	fst.failing("UpsertActors")
	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatalf("RunDailyPass = %v, want nil (per-project failure must not be fatal)", err)
	}
	if !logged(buf, "upsert actors failed") || !logged(buf, "boom") {
		t.Errorf("log output = %q, want mention of upsert actors failed and boom", buf.String())
	}
}

func TestRunDailyPassLogsAggregateRetentionDayFailure(t *testing.T) {
	st, fst, r, buf := setupFaulty(t, jobsVars, identifiedJobsSpecs)
	ctx := context.Background()
	if err := st.WriteWebHits(ctx, []store.WebHit{
		{ID: "1", Project: "app", TS: mustTime("2026-08-20T10:00:00Z"), ActorID: "v", Path: "/"}}); err != nil {
		t.Fatal(err)
	}
	fst.failing("AggregateRetentionDay")
	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatalf("RunDailyPass = %v, want nil", err)
	}
	if !logged(buf, "aggregate retention failed") {
		t.Errorf("log output = %q, want mention of aggregate retention failed", buf.String())
	}
}

func TestRunDailyPassLogsAggregateIdentityDayFailure(t *testing.T) {
	st, fst, r, buf := setupFaulty(t, jobsVars, jobsProjectSpecs)
	ctx := context.Background()
	if err := st.WriteWebHits(ctx, []store.WebHit{
		{ID: "1", Project: "app", TS: mustTime("2026-08-20T10:00:00Z"), ActorID: "v", Path: "/"}}); err != nil {
		t.Fatal(err)
	}
	fst.failing("AggregateIdentityDay")
	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatalf("RunDailyPass = %v, want nil", err)
	}
	if !logged(buf, "aggregate identity failed") {
		t.Errorf("log output = %q, want mention of aggregate identity failed", buf.String())
	}
}

func TestRunDailyPassLogsAggregateWebDayFailure(t *testing.T) {
	st, fst, r, buf := setupFaulty(t, jobsVars, jobsProjectSpecs)
	ctx := context.Background()
	if err := st.WriteWebHits(ctx, []store.WebHit{
		{ID: "1", Project: "app", TS: mustTime("2026-08-10T10:00:00Z"), ActorID: "v", Path: "/"}}); err != nil {
		t.Fatal(err)
	}
	fst.failing("AggregateWebDay")
	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatalf("RunDailyPass = %v, want nil", err)
	}
	if !logged(buf, "aggregate web failed") {
		t.Errorf("log output = %q, want mention of aggregate web failed", buf.String())
	}
	// The raw row must survive: the failed aggregation must not have
	// deleted it (AggregateWebDay itself is what would delete it, and it
	// never got to run for real).
	left, err := st.WebDaysBefore(ctx, "app", mustDay("2026-08-22"))
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 1 {
		t.Errorf("raw web days left = %v, want the failed day to survive", left)
	}
}

func TestRunDailyPassLogsAggregateProductDayFailure(t *testing.T) {
	st, fst, r, buf := setupFaulty(t, jobsVars, jobsProjectSpecs)
	ctx := context.Background()
	if err := st.WriteProductEvents(ctx, []store.ProductEvent{
		{ID: "1", Project: "app", EventName: "e", ActorID: "u", TS: mustTime("2026-08-10T10:00:00Z")}}); err != nil {
		t.Fatal(err)
	}
	fst.failing("AggregateProductDay")
	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatalf("RunDailyPass = %v, want nil", err)
	}
	if !logged(buf, "aggregate product failed") {
		t.Errorf("log output = %q, want mention of aggregate product failed", buf.String())
	}
}

func TestRunDailyPassLogsAggregateAppDayFailure(t *testing.T) {
	st, fst, r, buf := setupFaulty(t, appVars, jobsProjectSpecs)
	ctx := context.Background()
	if err := st.WriteAppViews(ctx, []store.AppView{
		{ID: "1", Project: "app", TS: mustTime("2026-08-10T10:00:00Z"), ReceivedAt: mustTime("2026-08-10T10:00:00Z"),
			ActorID: "a", Screen: "/home"}}); err != nil {
		t.Fatal(err)
	}
	fst.failing("AggregateAppDay")
	if err := r.RunDailyPass(ctx); err != nil {
		t.Fatalf("RunDailyPass = %v, want nil", err)
	}
	if !logged(buf, "aggregate app failed") {
		t.Errorf("log output = %q, want mention of aggregate app failed", buf.String())
	}
}

func TestRunDailyPassLogsPruneAggregatesFailure(t *testing.T) {
	_, fst, r, buf := setupFaulty(t, jobsVars, jobsProjectSpecs)
	fst.failing("PruneAggregates")
	if err := r.RunDailyPass(context.Background()); err != nil {
		t.Fatalf("RunDailyPass = %v, want nil", err)
	}
	if !logged(buf, "prune failed") {
		t.Errorf("log output = %q, want mention of prune failed", buf.String())
	}
}

func TestRunDailyPassLogsPruneActorsFailure(t *testing.T) {
	_, fst, r, buf := setupFaulty(t, jobsVars, jobsProjectSpecs)
	fst.failing("PruneActors")
	if err := r.RunDailyPass(context.Background()); err != nil {
		t.Fatalf("RunDailyPass = %v, want nil", err)
	}
	if !logged(buf, "prune actors failed") {
		t.Errorf("log output = %q, want mention of prune actors failed", buf.String())
	}
}

func TestRunDailyPassLogsPruneIdentitiesFailure(t *testing.T) {
	_, fst, r, buf := setupFaulty(t, jobsVars, jobsProjectSpecs)
	fst.failing("PruneIdentities")
	if err := r.RunDailyPass(context.Background()); err != nil {
		t.Fatalf("RunDailyPass = %v, want nil", err)
	}
	if !logged(buf, "prune identities failed") {
		t.Errorf("log output = %q, want mention of prune identities failed", buf.String())
	}
}

func TestRunDailyPassLogsFlatViewRebuildFailure(t *testing.T) {
	_, fst, r, buf := setupFaulty(t, jobsVars, jobsProjectSpecs)
	fst.failing("RebuildFlatView")
	if err := r.RunDailyPass(context.Background()); err != nil {
		t.Fatalf("RunDailyPass = %v, want nil", err)
	}
	if !logged(buf, "flat view rebuild failed") {
		t.Errorf("log output = %q, want mention of flat view rebuild failed", buf.String())
	}
}

func TestRunDailyPassLogsIncrementalVacuumFailure(t *testing.T) {
	_, fst, r, buf := setupFaulty(t, jobsVars, jobsProjectSpecs)
	fst.failing("IncrementalVacuum")
	if err := r.RunDailyPass(context.Background()); err != nil {
		t.Fatalf("RunDailyPass = %v, want nil", err)
	}
	if !logged(buf, "incremental vacuum failed") {
		t.Errorf("log output = %q, want mention of incremental vacuum failed", buf.String())
	}
}

// --- runScheduled / Run: scheduler-level failures are logged, not fatal ---

// failingRotator lets a test fail Rotate and/or Current independently of
// the real identity.Salter, which always succeeds against a healthy store.
type failingRotator struct {
	rotateErr  error
	currentErr error
}

func (f *failingRotator) Rotate(context.Context) error            { return f.rotateErr }
func (f *failingRotator) Current(context.Context) (string, error) { return "", f.currentErr }

func TestRunScheduledLogsSaltRotationFailure(t *testing.T) {
	cfg := configtest.Load(t, jobsVars)
	st := openStore(t)
	reg := newRegistry(t, st, cfg, jobsProjectSpecs)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	now := func() time.Time { return time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC) }
	r := New(st, cfg, reg, &failingRotator{rotateErr: errBoom}, logger, now)

	r.runScheduled(context.Background())

	if !logged(&buf, "salt rotation") || !logged(&buf, "boom") {
		t.Errorf("log output = %q, want mention of salt rotation and boom", buf.String())
	}
}

func TestRunScheduledLogsDailyPassFailure(t *testing.T) {
	cfg := configtest.Load(t, jobsVars)
	st, _ := openStoreAt(t)
	reg := newRegistry(t, st, cfg, jobsProjectSpecs)
	fst := newFaultyStore(st)
	fst.failing("ProjectAliases")
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	now := func() time.Time { return time.Date(2026, 8, 22, 3, 0, 0, 0, time.UTC) }
	r := New(fst, cfg, reg, identity.NewSalter(st, now), logger, now)

	r.runScheduled(context.Background())

	if !logged(&buf, "daily pass") || !logged(&buf, "boom") {
		t.Errorf("log output = %q, want mention of daily pass and boom", buf.String())
	}
}

func TestRunLogsInitialSaltFailure(t *testing.T) {
	cfg := configtest.Load(t, jobsVars)
	st := openStore(t)
	reg := newRegistry(t, st, cfg, jobsProjectSpecs)
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	now := func() time.Time { return time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC) }
	r := New(st, cfg, reg, &failingRotator{currentErr: errBoom}, logger, now)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

	if !logged(&buf, "initial salt") || !logged(&buf, "boom") {
		t.Errorf("log output = %q, want mention of initial salt and boom", buf.String())
	}
}

func TestRunLogsBootCatchUpFailure(t *testing.T) {
	cfg := configtest.Load(t, jobsVars)
	st, _ := openStoreAt(t)
	reg := newRegistry(t, st, cfg, jobsProjectSpecs)
	fst := newFaultyStore(st)
	fst.failing("ProjectAliases")
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	now := func() time.Time { return time.Date(2026, 8, 22, 4, 0, 0, 0, time.UTC) }
	r := New(fst, cfg, reg, identity.NewSalter(fst, now), logger, now)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); r.Run(ctx) }()
	cancel()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return after cancel")
	}

	if !logged(&buf, "boot catch-up pass") || !logged(&buf, "boom") {
		t.Errorf("log output = %q, want mention of boot catch-up pass and boom", buf.String())
	}
}
