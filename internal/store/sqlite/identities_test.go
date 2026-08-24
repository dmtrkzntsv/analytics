package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/dmitry/analytics/internal/store"
)

func TestAggregateIdentityDayCountsUsersAndGroups(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tstamp := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	d := appDay()

	if err := db.WriteAppViews(ctx, []store.AppView{
		{ID: "1", Project: "p", TS: tstamp, ReceivedAt: tstamp, ActorID: "a",
			UserID: "u1", GroupID: "org9", Screen: "/x"},
		{ID: "2", Project: "p", TS: tstamp, ReceivedAt: tstamp, ActorID: "b",
			UserID: "u2", GroupID: "org9", Screen: "/x"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteProductEvents(ctx, []store.ProductEvent{
		{ID: "3", Project: "p", EventName: "subscribed", TS: tstamp, ReceivedAt: tstamp,
			ActorID: "a", UserID: "u1", GroupID: "org9"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteWebHits(ctx, []store.WebHit{
		{ID: "4", Project: "p", TS: tstamp, ReceivedAt: tstamp, ActorID: "a",
			UserID: "u1", GroupID: "org9", Path: "/"},
	}); err != nil {
		t.Fatal(err)
	}

	if err := db.AggregateIdentityDay(ctx, "p", d); err != nil {
		t.Fatalf("aggregate: %v", err)
	}

	var actors, users, hits, views, events int
	if err := db.db.QueryRowContext(ctx,
		`SELECT actors, users, hits, views, events FROM agg_identity_daily
		 WHERE project='p' AND day=? AND kind='group' AND id='org9'`, d.String()).
		Scan(&actors, &users, &hits, &views, &events); err != nil {
		t.Fatalf("read group row: %v", err)
	}
	if actors != 2 || users != 2 || hits != 1 || views != 2 || events != 1 {
		t.Errorf("group row = actors %d users %d hits %d views %d events %d; want 2 2 1 2 1",
			actors, users, hits, views, events)
	}

	if err := db.db.QueryRowContext(ctx,
		`SELECT actors, users, views, events FROM agg_identity_daily
		 WHERE project='p' AND day=? AND kind='user' AND id='u1'`, d.String()).
		Scan(&actors, &users, &views, &events); err != nil {
		t.Fatalf("read user row: %v", err)
	}
	if actors != 1 || users != 1 || views != 1 || events != 1 {
		t.Errorf("user row = actors %d users %d views %d events %d; want 1 1 1 1",
			actors, users, views, events)
	}
}

func TestAggregateIdentityDayIsIdempotent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tstamp := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)

	if err := db.WriteAppViews(ctx, []store.AppView{
		{ID: "1", Project: "p", TS: tstamp, ReceivedAt: tstamp, ActorID: "a",
			UserID: "u1", Screen: "/x"},
	}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := db.AggregateIdentityDay(ctx, "p", appDay()); err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
	}
	var rows, views int
	if err := db.db.QueryRowContext(ctx,
		`SELECT COUNT(*), COALESCE(MAX(views),0) FROM agg_identity_daily`).Scan(&rows, &views); err != nil {
		t.Fatal(err)
	}
	if rows != 1 || views != 1 {
		t.Errorf("rows=%d views=%d after replay; want 1 and 1", rows, views)
	}
}

func TestAggregateIdentityDayUpdatesLastSeen(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tstamp := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)
	d := appDay()

	if err := db.UpsertIdentities(ctx, []store.Identity{
		{Project: "p", Kind: store.KindUser, ID: "u1", Name: "Ada"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteAppViews(ctx, []store.AppView{
		{ID: "1", Project: "p", TS: tstamp, ReceivedAt: tstamp, ActorID: "a",
			UserID: "u1", Screen: "/x"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateIdentityDay(ctx, "p", d); err != nil {
		t.Fatal(err)
	}

	var last string
	if err := db.db.QueryRowContext(ctx,
		`SELECT last_seen_day FROM identities WHERE project='p' AND kind='user' AND id='u1'`).
		Scan(&last); err != nil {
		t.Fatal(err)
	}
	if last != d.String() {
		t.Errorf("last_seen_day = %q, want %q", last, d.String())
	}
}

func TestIdentityDailyViewReadsAggregates(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	tstamp := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)

	if err := db.WriteAppViews(ctx, []store.AppView{
		{ID: "1", Project: "p", TS: tstamp, ReceivedAt: tstamp, ActorID: "a",
			GroupID: "org9", Screen: "/x"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateIdentityDay(ctx, "p", appDay()); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM v_identity_daily WHERE project='p' AND kind='group'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("v_identity_daily rows = %d, want 1", n)
	}
}

func TestPruneIdentitiesEvictsStale(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := db.UpsertIdentities(ctx, []store.Identity{
		{Project: "p", Kind: store.KindUser, ID: "old", Name: "Gone"},
		{Project: "p", Kind: store.KindUser, ID: "new", Name: "Here"},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx,
		`UPDATE identities SET last_seen_day='2024-01-01' WHERE id='old'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx,
		`UPDATE identities SET last_seen_day='2026-08-20' WHERE id='new'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.ExecContext(ctx,
		`INSERT INTO agg_identity_daily VALUES ('p','2024-01-01','user','old',1,1,0,1,0)`); err != nil {
		t.Fatal(err)
	}

	if err := db.PruneIdentities(ctx, "p", onDay(2026, 1, 1)); err != nil {
		t.Fatal(err)
	}

	var names, aggs int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM identities`).Scan(&names); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM agg_identity_daily`).Scan(&aggs); err != nil {
		t.Fatal(err)
	}
	if names != 1 || aggs != 0 {
		t.Errorf("after prune: names=%d aggs=%d; want 1 and 0", names, aggs)
	}
}

func TestPruneIdentitiesKeepsNeverSeenNames(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	// last_seen_day is '' until the first daily pass; such a row must not be
	// evicted before it has ever had the chance to be counted.
	if err := db.UpsertIdentities(ctx, []store.Identity{
		{Project: "p", Kind: store.KindGroup, ID: "org9", Name: "Acme"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.PruneIdentities(ctx, "p", onDay(2026, 8, 23)); err != nil {
		t.Fatal(err)
	}
	var n int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM identities`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("identities rows = %d, want 1", n)
	}
}
