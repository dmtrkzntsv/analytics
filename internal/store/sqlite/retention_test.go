package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/dmitry/analytics/internal/civil"
	"github.com/dmitry/analytics/internal/store"
)

func onDay(y int, m time.Month, d int) civil.Date { return civil.Date{Year: y, Month: m, Day: d} }

func viewAt(id, actor string, t time.Time) store.AppView {
	return store.AppView{ID: id, Project: "p", TS: t, ReceivedAt: t,
		ActorID: actor, Screen: "/x", Platform: "ios"}
}

func TestUpsertActorsTracksFirstAndLastSeen(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	d1 := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	d2 := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)

	if err := db.WriteAppViews(ctx, []store.AppView{viewAt("1", "a", d1)}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertActors(ctx, "p", onDay(2026, 8, 1)); err != nil {
		t.Fatalf("upsert day 1: %v", err)
	}
	if err := db.WriteAppViews(ctx, []store.AppView{viewAt("2", "a", d2)}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertActors(ctx, "p", onDay(2026, 8, 8)); err != nil {
		t.Fatalf("upsert day 8: %v", err)
	}

	var first, last, surface string
	if err := db.db.QueryRowContext(ctx,
		`SELECT first_seen_day, last_seen_day, surface FROM actors WHERE project='p' AND actor_id='a'`).
		Scan(&first, &last, &surface); err != nil {
		t.Fatalf("read actor: %v", err)
	}
	if first != "2026-08-01" || last != "2026-08-08" || surface != surfaceApp {
		t.Errorf("actor = %q %q %q; want 2026-08-01 2026-08-08 app", first, last, surface)
	}
}

func TestUpsertActorsRecordsWebSurface(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	ts := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	if err := db.WriteWebHits(ctx, []store.WebHit{
		{ID: "1", Project: "p", TS: ts, ReceivedAt: ts, ActorID: "w", Path: "/"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertActors(ctx, "p", onDay(2026, 8, 1)); err != nil {
		t.Fatal(err)
	}

	var surface string
	if err := db.db.QueryRowContext(ctx,
		`SELECT surface FROM actors WHERE project='p' AND actor_id='w'`).Scan(&surface); err != nil {
		t.Fatal(err)
	}
	if surface != surfaceWeb {
		t.Errorf("surface = %q, want web", surface)
	}
}

func TestUpsertActorsIgnoresEmptyActor(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	ts := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	if err := db.WriteAppViews(ctx, []store.AppView{
		{ID: "1", Project: "p", TS: ts, ReceivedAt: ts, ActorID: "", Screen: "/x"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertActors(ctx, "p", onDay(2026, 8, 1)); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM actors`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("actors rows = %d, want 0 for an empty actor id", n)
	}
}

func TestAggregateRetentionDayComputesOffsets(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	cohort := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	later := time.Date(2026, 8, 8, 10, 0, 0, 0, time.UTC)

	// Two actors on day 0; one returns on day 7.
	if err := db.WriteAppViews(ctx, []store.AppView{
		viewAt("1", "a", cohort), viewAt("2", "b", cohort),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertActors(ctx, "p", onDay(2026, 8, 1)); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateRetentionDay(ctx, "p", onDay(2026, 8, 1)); err != nil {
		t.Fatal(err)
	}

	if err := db.WriteAppViews(ctx, []store.AppView{viewAt("3", "a", later)}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertActors(ctx, "p", onDay(2026, 8, 8)); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateRetentionDay(ctx, "p", onDay(2026, 8, 8)); err != nil {
		t.Fatal(err)
	}

	var d0, d7 int
	if err := db.db.QueryRowContext(ctx,
		`SELECT actors FROM agg_retention WHERE project='p' AND surface='app'
		   AND cohort_day='2026-08-01' AND day_offset=0`).Scan(&d0); err != nil {
		t.Fatalf("offset 0: %v", err)
	}
	if err := db.db.QueryRowContext(ctx,
		`SELECT actors FROM agg_retention WHERE project='p' AND surface='app'
		   AND cohort_day='2026-08-01' AND day_offset=7`).Scan(&d7); err != nil {
		t.Fatalf("offset 7: %v", err)
	}
	if d0 != 2 || d7 != 1 {
		t.Errorf("cohort = d0 %d d7 %d; want 2 1", d0, d7)
	}
}

func TestRetentionViewExposesCohortSize(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	cohort := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	later := time.Date(2026, 8, 2, 10, 0, 0, 0, time.UTC)

	if err := db.WriteAppViews(ctx, []store.AppView{
		viewAt("1", "a", cohort), viewAt("2", "b", cohort),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertActors(ctx, "p", onDay(2026, 8, 1)); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateRetentionDay(ctx, "p", onDay(2026, 8, 1)); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteAppViews(ctx, []store.AppView{viewAt("3", "a", later)}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertActors(ctx, "p", onDay(2026, 8, 2)); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateRetentionDay(ctx, "p", onDay(2026, 8, 2)); err != nil {
		t.Fatal(err)
	}

	var actors, size int
	if err := db.db.QueryRowContext(ctx,
		`SELECT actors, cohort_size FROM v_retention
		 WHERE project='p' AND cohort_day='2026-08-01' AND day_offset=1`).
		Scan(&actors, &size); err != nil {
		t.Fatalf("v_retention: %v", err)
	}
	if actors != 1 || size != 2 {
		t.Errorf("v_retention d1 = actors %d of %d; want 1 of 2", actors, size)
	}
}

func TestAggregateRetentionDayIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	cohort := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	if err := db.WriteAppViews(ctx, []store.AppView{viewAt("1", "a", cohort)}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertActors(ctx, "p", onDay(2026, 8, 1)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := db.AggregateRetentionDay(ctx, "p", onDay(2026, 8, 1)); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}

	var n, actors int
	if err := db.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(MAX(actors),0) FROM agg_retention WHERE project='p'`).
		Scan(&n, &actors); err != nil {
		t.Fatal(err)
	}
	if n != 1 || actors != 1 {
		t.Errorf("rows=%d actors=%d after replay; want 1 and 1", n, actors)
	}
}

func TestUpsertActorsIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	ts := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)

	if err := db.WriteAppViews(ctx, []store.AppView{viewAt("1", "a", ts)}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := db.UpsertActors(ctx, "p", onDay(2026, 8, 1)); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	var n int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM actors`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("actors rows = %d, want 1", n)
	}
}

func TestPruneActorsEvictsStale(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	old := time.Date(2025, 1, 1, 10, 0, 0, 0, time.UTC)
	recent := time.Date(2026, 8, 20, 10, 0, 0, 0, time.UTC)

	if err := db.WriteAppViews(ctx, []store.AppView{
		viewAt("1", "stale", old), viewAt("2", "fresh", recent),
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertActors(ctx, "p", onDay(2025, 1, 1)); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertActors(ctx, "p", onDay(2026, 8, 20)); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateRetentionDay(ctx, "p", onDay(2025, 1, 1)); err != nil {
		t.Fatal(err)
	}
	if err := db.PruneActors(ctx, "p", onDay(2026, 1, 1)); err != nil {
		t.Fatalf("prune: %v", err)
	}

	var actors, cohorts int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM actors`).Scan(&actors); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agg_retention`).Scan(&cohorts); err != nil {
		t.Fatal(err)
	}
	if actors != 1 || cohorts != 0 {
		t.Errorf("after prune: actors=%d cohorts=%d; want 1 and 0", actors, cohorts)
	}
}
