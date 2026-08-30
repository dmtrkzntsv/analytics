package sqlite

import (
	"context"
	"fmt"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// seedProductDay: project "app", day 2026-08-10:
//
//	subscribed: u1 plan=pro source=ads; u2 plan=free source=ads; u2 plan=free (no source)
//	ping:       u1 (no attrs)
func seedProductDay(t *testing.T, db *DB) {
	t.Helper()
	evs := []store.ProductEvent{
		{ID: "p1", Project: "app", EventName: "subscribed", ActorID: "u1", TS: ts("2026-08-10T10:00:00Z"),
			Attributes: map[string]string{"plan": "pro", "source": "ads"}},
		{ID: "p2", Project: "app", EventName: "subscribed", ActorID: "u2", TS: ts("2026-08-10T11:00:00Z"),
			Attributes: map[string]string{"plan": "free", "source": "ads"}},
		{ID: "p3", Project: "app", EventName: "subscribed", ActorID: "u2", TS: ts("2026-08-10T12:00:00Z"),
			Attributes: map[string]string{"plan": "free"}},
		{ID: "p4", Project: "app", EventName: "ping", ActorID: "u1", TS: ts("2026-08-10T13:00:00Z")},
	}
	if err := db.WriteProductEvents(context.Background(), evs); err != nil {
		t.Fatal(err)
	}
}

func TestAggregateProductDisabledDeletesOnly(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedProductDay(t, db)
	if err := db.AggregateProductDay(ctx, "app", day("2026-08-10"), store.ProductAggSettings{}); err != nil {
		t.Fatal(err)
	}
	var n int
	db.db.QueryRow(`SELECT COUNT(*) FROM product_events`).Scan(&n)
	if n != 0 {
		t.Fatalf("raw remaining %d", n)
	}
	db.db.QueryRow(`SELECT COUNT(*) FROM agg_product_daily`).Scan(&n)
	if n != 0 {
		t.Fatal("disabled aggregation must write nothing (spec §4)")
	}
}

func TestAggregateProductEnabled(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedProductDay(t, db)
	agg := store.ProductAggSettings{
		Enabled:    true,
		TopN:       50,
		Attributes: map[string][]string{"subscribed": {"plan"}, "*": {"source"}},
	}
	if err := db.AggregateProductDay(ctx, "app", day("2026-08-10"), agg); err != nil {
		t.Fatal(err)
	}
	var count, uniq int
	if err := db.db.QueryRow(`SELECT count, unique_users FROM agg_product_daily
		WHERE project='app' AND day='2026-08-10' AND event_name='subscribed'`).Scan(&count, &uniq); err != nil {
		t.Fatal(err)
	}
	if count != 3 || uniq != 2 {
		t.Fatalf("subscribed: c=%d u=%d", count, uniq)
	}
	if err := db.db.QueryRow(`SELECT total_events, active_users FROM agg_product_totals
		WHERE project='app' AND day='2026-08-10'`).Scan(&count, &uniq); err != nil {
		t.Fatal(err)
	}
	if count != 4 || uniq != 2 {
		t.Fatalf("totals: e=%d dau=%d", count, uniq)
	}
	// plan breakdown for subscribed only
	if err := db.db.QueryRow(`SELECT count, unique_users FROM agg_product_attrs
		WHERE project='app' AND day='2026-08-10' AND event_name='subscribed'
		AND attr_key='plan' AND attr_value='free'`).Scan(&count, &uniq); err != nil {
		t.Fatal(err)
	}
	if count != 2 || uniq != 1 {
		t.Fatalf("plan=free: c=%d u=%d", count, uniq)
	}
	// wildcard source applies to subscribed (2 with source=ads); ping has no source attr -> no row
	var n int
	db.db.QueryRow(`SELECT COUNT(*) FROM agg_product_attrs WHERE event_name='ping'`).Scan(&n)
	if n != 0 {
		t.Fatal("events without the attribute must produce no attr rows")
	}
	db.db.QueryRow(`SELECT count FROM agg_product_attrs
		WHERE event_name='subscribed' AND attr_key='source' AND attr_value='ads'`).Scan(&count)
	if count != 2 {
		t.Fatalf("source=ads count=%d", count)
	}
	db.db.QueryRow(`SELECT COUNT(*) FROM product_events`).Scan(&n)
	if n != 0 {
		t.Fatal("raw must be deleted after rollup")
	}
}

func TestAggregateProductTopNCollapsesTail(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	var evs []store.ProductEvent
	// 5 distinct values; v0 appears 3x, v1 2x, v2..v4 once each.
	id := 0
	add := func(user, val string) {
		id++
		evs = append(evs, store.ProductEvent{ID: fmt.Sprintf("e%d", id), Project: "app",
			EventName: "clicked", ActorID: user, TS: ts("2026-08-10T10:00:00Z"),
			Attributes: map[string]string{"button": val}})
	}
	add("u1", "v0")
	add("u2", "v0")
	add("u3", "v0")
	add("u1", "v1")
	add("u2", "v1")
	add("u1", "v2")
	add("u2", "v3")
	add("u3", "v4")
	if err := db.WriteProductEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	agg := store.ProductAggSettings{Enabled: true, TopN: 2,
		Attributes: map[string][]string{"clicked": {"button"}}}
	if err := db.AggregateProductDay(ctx, "app", day("2026-08-10"), agg); err != nil {
		t.Fatal(err)
	}
	var n int
	db.db.QueryRow(`SELECT COUNT(*) FROM agg_product_attrs WHERE attr_key='button'`).Scan(&n)
	if n != 3 { // v0, v1, (other)
		t.Fatalf("rows = %d, want 3 (top2 + other)", n)
	}
	var count, uniq int
	if err := db.db.QueryRow(`SELECT count, unique_users FROM agg_product_attrs
		WHERE attr_key='button' AND attr_value='(other)'`).Scan(&count, &uniq); err != nil {
		t.Fatal(err)
	}
	if count != 3 || uniq != 3 { // v2+v3+v4: 3 events by 3 distinct users
		t.Fatalf("(other): c=%d u=%d", count, uniq)
	}
}

func TestAggregateProductIdempotent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedProductDay(t, db)
	agg := store.ProductAggSettings{Enabled: true, TopN: 50}
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(db.AggregateProductDay(ctx, "app", day("2026-08-10"), agg))
	must(db.AggregateProductDay(ctx, "app", day("2026-08-10"), agg)) // no raw left: no-op
	var c int
	db.db.QueryRow(`SELECT count FROM agg_product_daily WHERE event_name='subscribed'`).Scan(&c)
	if c != 3 {
		t.Fatalf("second run corrupted: %d", c)
	}
}
