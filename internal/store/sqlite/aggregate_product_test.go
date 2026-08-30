package sqlite

import (
	"context"
	"fmt"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
	"github.com/google/uuid"
)

// seedProductEvent writes one product event. platform and version are
// the typed system columns; attrs is the custom JSON blob.
func seedProductEvent(t *testing.T, db *DB, project, event, at string,
	attrs map[string]string, platform, version string) {
	t.Helper()
	if err := db.WriteProductEvents(context.Background(), []store.ProductEvent{{
		ID: uuid.NewString(), Project: project, EventName: event,
		ActorID: "u1", TS: ts(at), Attributes: attrs,
		Platform: platform, Version: version,
	}}); err != nil {
		t.Fatal(err)
	}
}

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

// TestAggregateProductRunsWithNoDeclaredAttributes replaces the old
// "disabled" case: rollups are unconditional now (enabled is gone), so a
// project with no declared attributes still gets agg_product_daily /
// agg_product_totals rows and the raw day is still deleted, but no
// agg_product_attrs rows are written since no keys were named.
func TestAggregateProductRunsWithNoDeclaredAttributes(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedProductDay(t, db)
	if err := db.AggregateProductDay(ctx, "app", day("2026-08-10"), nil, 50); err != nil {
		t.Fatal(err)
	}
	var n int
	db.db.QueryRow(`SELECT COUNT(*) FROM product_events`).Scan(&n)
	if n != 0 {
		t.Fatalf("raw remaining %d", n)
	}
	db.db.QueryRow(`SELECT COUNT(*) FROM agg_product_daily`).Scan(&n)
	if n == 0 {
		t.Fatal("rollups are unconditional now; agg_product_daily must be populated even with no declared attributes")
	}
	db.db.QueryRow(`SELECT COUNT(*) FROM agg_product_attrs`).Scan(&n)
	if n != 0 {
		t.Fatal("no declared attributes must produce no attr rows")
	}
}

func TestAggregateProductDeclaredAttributes(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedProductDay(t, db)
	attrs := []string{"plan", "source"}
	if err := db.AggregateProductDay(ctx, "app", day("2026-08-10"), attrs, 50); err != nil {
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
	// plan breakdown for subscribed
	if err := db.db.QueryRow(`SELECT count, unique_users FROM agg_product_attrs
		WHERE project='app' AND day='2026-08-10' AND event_name='subscribed'
		AND attr_key='plan' AND attr_value='free'`).Scan(&count, &uniq); err != nil {
		t.Fatal(err)
	}
	if count != 2 || uniq != 1 {
		t.Fatalf("plan=free: c=%d u=%d", count, uniq)
	}
	// declared attributes apply across every event name; ping has neither
	// plan nor source -> no rows for it.
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
	if err := db.AggregateProductDay(ctx, "app", day("2026-08-10"), []string{"button"}, 2); err != nil {
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
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(db.AggregateProductDay(ctx, "app", day("2026-08-10"), nil, 50))
	must(db.AggregateProductDay(ctx, "app", day("2026-08-10"), nil, 50)) // no raw left: no-op
	var c int
	db.db.QueryRow(`SELECT count FROM agg_product_daily WHERE event_name='subscribed'`).Scan(&c)
	if c != 3 {
		t.Fatalf("second run corrupted: %d", c)
	}
}

// TestAggregateProductClampsNonPositiveTopN guards the destructive failure
// mode a topN<=0 would otherwise cause: rollupAttr's `rn <= topN` filter
// keeps nothing, so every distinct value silently collapses into
// "(other)" instead of erroring. Not reachable from production callers
// today (jobs.Runner always sets topN from
// config.Config.ProductAttributesTopN, which defaults to 50), but
// AggregateProductDay clamps it anyway as a last line of defense.
func TestAggregateProductClampsNonPositiveTopN(t *testing.T) {
	for _, topN := range []int{0, -5} {
		t.Run(fmt.Sprintf("topN=%d", topN), func(t *testing.T) {
			db := newTestDB(t)
			ctx := context.Background()
			seedProductDay(t, db) // subscribed: plan in {pro, free} -- 2 distinct values
			if err := db.AggregateProductDay(ctx, "app", day("2026-08-10"), []string{"plan"}, topN); err != nil {
				t.Fatal(err)
			}
			var other int
			db.db.QueryRow(`SELECT COUNT(*) FROM agg_product_attrs
				WHERE attr_key='plan' AND attr_value='(other)'`).Scan(&other)
			if other != 0 {
				t.Fatal("non-positive topN collapsed every value into (other) instead of clamping to the default")
			}
			var kept int
			db.db.QueryRow(`SELECT COUNT(*) FROM agg_product_attrs
				WHERE attr_key='plan' AND attr_value IN ('pro','free')`).Scan(&kept)
			if kept != 2 {
				t.Fatalf("kept = %d, want 2 (both real values, clamped topN=%d must behave like the default)", kept, defaultAttrsTopN)
			}
		})
	}
}

func TestRollupWritesSystemDimensions(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedProductEvent(t, db, "blog", "signup", "2026-08-01T10:00:00Z",
		map[string]string{}, "ios", "1.2.0") // platform, version columns
	if err := db.AggregateProductDay(ctx, "blog",
		civil.DateOf(ts("2026-08-01T00:00:00Z")), nil, 50); err != nil {
		t.Fatal(err)
	}
	var v string
	if err := db.db.QueryRow(`SELECT attr_value FROM agg_product_attrs
		WHERE project='blog' AND attr_key='$platform'`).Scan(&v); err != nil {
		t.Fatal(err)
	}
	if v != "ios" {
		t.Fatalf("$platform = %q, want ios", v)
	}
}

// TestRollupSystemDimensionsDoNotCollideWithCustomKeys guards the $ prefix
// invariant from the design doc: an event carrying both the typed
// platform column and a custom "platform" attribute must produce two
// distinct attr_key rows ($platform and platform), never merged.
func TestRollupSystemDimensionsDoNotCollideWithCustomKeys(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedProductEvent(t, db, "blog", "signup", "2026-08-01T10:00:00Z",
		map[string]string{"platform": "custom-value"}, "ios", "1.2.0")
	if err := db.AggregateProductDay(ctx, "blog",
		civil.DateOf(ts("2026-08-01T00:00:00Z")), []string{"platform"}, 50); err != nil {
		t.Fatal(err)
	}
	var sysVal, customVal string
	if err := db.db.QueryRow(`SELECT attr_value FROM agg_product_attrs
		WHERE project='blog' AND attr_key='$platform'`).Scan(&sysVal); err != nil {
		t.Fatal(err)
	}
	if err := db.db.QueryRow(`SELECT attr_value FROM agg_product_attrs
		WHERE project='blog' AND attr_key='platform'`).Scan(&customVal); err != nil {
		t.Fatal(err)
	}
	if sysVal != "ios" || customVal != "custom-value" {
		t.Fatalf("$platform=%q platform=%q, want ios / custom-value", sysVal, customVal)
	}
}

// TestRollupSystemDimensionsSurviveRawDeletion checks the retention story
// this task closes: $platform/$version rows must exist after the raw
// day is deleted, for a project declaring no custom attributes at all.
func TestRollupSystemDimensionsSurviveRawDeletion(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedProductEvent(t, db, "blog", "signup", "2026-08-01T10:00:00Z",
		map[string]string{}, "android", "3.0.0")
	if err := db.AggregateProductDay(ctx, "blog",
		civil.DateOf(ts("2026-08-01T00:00:00Z")), nil, 50); err != nil {
		t.Fatal(err)
	}
	var n int
	db.db.QueryRow(`SELECT COUNT(*) FROM product_events`).Scan(&n)
	if n != 0 {
		t.Fatalf("raw remaining %d", n)
	}
	db.db.QueryRow(`SELECT COUNT(*) FROM agg_product_attrs
		WHERE attr_key IN ('$platform','$version')`).Scan(&n)
	if n != 2 {
		t.Fatalf("system dimension rows = %d, want 2", n)
	}
}
