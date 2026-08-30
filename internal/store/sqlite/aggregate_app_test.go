package sqlite

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func appDay() civil.Date { return civil.Date{Year: 2026, Month: time.August, Day: 23} }

func at(h, m int) time.Time {
	return time.Date(2026, 8, 23, h, m, 0, 0, time.UTC)
}

func seedViews(t *testing.T, db *DB, views ...store.AppView) {
	t.Helper()
	for i := range views {
		if views[i].ReceivedAt.IsZero() {
			views[i].ReceivedAt = views[i].TS
		}
		if views[i].Project == "" {
			views[i].Project = "p"
		}
	}
	if err := db.WriteAppViews(context.Background(), views); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestAggregateAppDayCounts(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	seedViews(t, db,
		store.AppView{ID: "1", TS: at(10, 0), ActorID: "a", SessionID: "s1",
			Screen: "/home", Platform: "ios", AppVersion: "2.4.1",
			OSVersion: "17.2", DeviceModel: "iPhone15,2", Country: "DE"},
		store.AppView{ID: "2", TS: at(10, 5), ActorID: "a", SessionID: "s1",
			Screen: "/settings", Platform: "ios", AppVersion: "2.4.1",
			OSVersion: "17.2", DeviceModel: "iPhone15,2", Country: "DE"},
		store.AppView{ID: "3", TS: at(11, 0), ActorID: "b", SessionID: "s2",
			Screen: "/home", Platform: "android", AppVersion: "2.4.1",
			OSVersion: "14", DeviceModel: "Pixel 8", Country: "FR"},
	)

	if err := db.AggregateAppDay(ctx, "p", appDay()); err != nil {
		t.Fatalf("aggregate: %v", err)
	}

	var actives, views, sessions, dur int
	if err := db.db.QueryRowContext(ctx,
		`SELECT actives, views, sessions, duration_sec FROM agg_app_daily WHERE project='p' AND day=?`,
		appDay().String()).Scan(&actives, &views, &sessions, &dur); err != nil {
		t.Fatalf("read daily: %v", err)
	}
	if actives != 2 || views != 3 || sessions != 2 || dur != 300 {
		t.Errorf("daily = actives %d views %d sessions %d dur %d; want 2 3 2 300",
			actives, views, sessions, dur)
	}

	var n int
	if err := db.db.QueryRowContext(ctx,
		`SELECT views FROM agg_app_screens WHERE project='p' AND day=? AND screen='/home'`,
		appDay().String()).Scan(&n); err != nil {
		t.Fatalf("read screens: %v", err)
	}
	if n != 2 {
		t.Errorf("/home views = %d, want 2", n)
	}

	// platform is part of the versions key, so one version string across two
	// platforms must not merge into a single row.
	if err := db.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agg_app_versions WHERE project='p' AND day=? AND app_version='2.4.1'`,
		appDay().String()).Scan(&n); err != nil {
		t.Fatalf("read versions: %v", err)
	}
	if n != 2 {
		t.Errorf("version rows = %d, want 2 (one per platform)", n)
	}

	for _, q := range []struct {
		table, where string
		want         int
	}{
		{"agg_app_os", "platform='ios' AND os_version='17.2'", 2},
		{"agg_app_devices", "device_model='Pixel 8'", 1},
		{"agg_app_countries", "country='DE'", 2},
	} {
		if err := db.db.QueryRowContext(ctx, fmt.Sprintf(
			`SELECT views FROM %s WHERE project='p' AND day=? AND %s`, q.table, q.where),
			appDay().String()).Scan(&n); err != nil {
			t.Fatalf("%s: %v", q.table, err)
		}
		if n != q.want {
			t.Errorf("%s views = %d, want %d", q.table, n, q.want)
		}
	}

	// Raw rows are consumed by aggregation.
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM app_views`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("raw app_views left = %d, want 0", n)
	}
}

func TestAggregateAppDayIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	seedViews(t, db, store.AppView{ID: "1", TS: at(10, 0), ActorID: "a",
		Screen: "/home", Platform: "ios"})

	for i := 0; i < 2; i++ {
		if err := db.AggregateAppDay(ctx, "p", appDay()); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}

	var views, rows int
	if err := db.db.QueryRowContext(ctx,
		`SELECT views FROM agg_app_daily WHERE project='p' AND day=?`, appDay().String()).
		Scan(&views); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agg_app_screens`).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if views != 1 || rows != 1 {
		t.Errorf("views=%d screenRows=%d after double aggregation; want 1 and 1", views, rows)
	}
}

func TestAggregateAppDayEmptyDayWritesNothing(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.AggregateAppDay(ctx, "p", appDay()); err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	var n int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agg_app_daily`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Errorf("agg_app_daily rows = %d for an empty day, want 0", n)
	}
}

func TestAggregateAppDaySessionFallsBackToGapInference(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// No session_id: a gap over 30 minutes splits sessions.
	seedViews(t, db,
		store.AppView{ID: "1", TS: at(10, 0), ActorID: "a", Screen: "/a"},
		store.AppView{ID: "2", TS: at(11, 0), ActorID: "a", Screen: "/b"},
	)
	if err := db.AggregateAppDay(ctx, "p", appDay()); err != nil {
		t.Fatal(err)
	}
	var sessions int
	if err := db.db.QueryRowContext(ctx,
		`SELECT sessions FROM agg_app_daily WHERE project='p' AND day=?`, appDay().String()).
		Scan(&sessions); err != nil {
		t.Fatal(err)
	}
	if sessions != 2 {
		t.Errorf("sessions = %d, want 2 from gap inference", sessions)
	}
}

func TestAggregateAppDayClientSessionBeatsGap(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// A declared session spanning a two-hour gap stays one session: the app
	// knows its own foreground/background transitions.
	seedViews(t, db,
		store.AppView{ID: "1", TS: at(10, 0), ActorID: "a", SessionID: "s1", Screen: "/a"},
		store.AppView{ID: "2", TS: at(12, 0), ActorID: "a", SessionID: "s1", Screen: "/b"},
	)
	if err := db.AggregateAppDay(ctx, "p", appDay()); err != nil {
		t.Fatal(err)
	}
	var sessions, dur int
	if err := db.db.QueryRowContext(ctx,
		`SELECT sessions, duration_sec FROM agg_app_daily WHERE project='p' AND day=?`,
		appDay().String()).Scan(&sessions, &dur); err != nil {
		t.Fatal(err)
	}
	if sessions != 1 || dur != 7200 {
		t.Errorf("sessions=%d dur=%d; want 1 and 7200", sessions, dur)
	}
}

func TestAggregateAppDayCollapsesTailIntoOther(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// One screen well above the cap plus enough distinct screens to push
	// some past it. Use a small stand-in for the 500 cap by generating
	// topNDimension+3 screens, each seen once, plus one seen twice.
	var views []store.AppView
	views = append(views,
		store.AppView{ID: "hot-1", TS: at(9, 0), ActorID: "hot", Screen: "/hot"},
		store.AppView{ID: "hot-2", TS: at(9, 1), ActorID: "hot", Screen: "/hot"})
	for i := 0; i < topNDimension+3; i++ {
		views = append(views, store.AppView{
			ID: fmt.Sprintf("s-%d", i), TS: at(10, 0),
			ActorID: "tailactor", Screen: fmt.Sprintf("/s%04d", i)})
	}
	seedViews(t, db, views...)

	if err := db.AggregateAppDay(ctx, "p", appDay()); err != nil {
		t.Fatal(err)
	}

	var rows, otherViews, otherActives int
	if err := db.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM agg_app_screens WHERE project='p' AND day=?`,
		appDay().String()).Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRowContext(ctx,
		`SELECT views, actives FROM agg_app_screens WHERE project='p' AND day=? AND screen=?`,
		appDay().String(), otherBucket).Scan(&otherViews, &otherActives); err != nil {
		t.Fatalf("no (other) row: %v", err)
	}
	if rows != topNDimension+1 {
		t.Errorf("screen rows = %d, want %d (cap plus the (other) bucket)", rows, topNDimension+1)
	}
	// 4 screens fall past the cap (503 distinct tail screens, 500 kept
	// including /hot, so 503+1-500 = 4).
	if otherViews != 4 {
		t.Errorf("(other) views = %d, want 4", otherViews)
	}
	// actives is a distinct actor count, not a sum: one actor saw them all.
	if otherActives != 1 {
		t.Errorf("(other) actives = %d, want 1 (distinct actors, not a sum)", otherActives)
	}
}

func TestAppDaysBefore(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	old := time.Date(2026, 8, 1, 9, 0, 0, 0, time.UTC)

	seedViews(t, db,
		store.AppView{ID: "1", TS: old, ActorID: "a", Screen: "/x"},
		store.AppView{ID: "2", TS: at(10, 0), ActorID: "a", Screen: "/x"},
	)

	days, err := db.AppDaysBefore(ctx, "p", appDay())
	if err != nil {
		t.Fatalf("AppDaysBefore: %v", err)
	}
	if len(days) != 1 || days[0].String() != "2026-08-01" {
		t.Fatalf("days = %v, want [2026-08-01]", days)
	}
}
