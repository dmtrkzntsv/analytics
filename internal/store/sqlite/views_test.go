package sqlite

import (
	"context"
	"fmt"
	"testing"

	"github.com/dmitry/analytics/internal/store"
)

// The invariant that makes Evidence dashboards boundary-free (spec §8.1):
// v_* views must return IDENTICAL numbers before and after aggregation.
func TestStitchViewsInvariantWeb(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedWebDay(t, db) // raw only

	read := func() (v, pv, s, b, d int) {
		t.Helper()
		err := db.db.QueryRow(`SELECT visitors, pageviews, sessions, bounces, duration_sec
			FROM v_web_daily WHERE project='app' AND day='2026-08-10'`).Scan(&v, &pv, &s, &b, &d)
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	v1, pv1, s1, b1, d1 := read()
	// Sanity: the view must actually see the seeded day, or the comparison
	// below would be vacuous.
	if v1 != 2 || pv1 != 4 || s1 != 3 || b1 != 2 || d1 != 600 {
		t.Fatalf("live v_web_daily = (%d %d %d %d %d), want (2 4 3 2 600)", v1, pv1, s1, b1, d1)
	}
	if err := db.AggregateWebDay(ctx, "app", day("2026-08-10")); err != nil {
		t.Fatal(err)
	}
	v2, pv2, s2, b2, d2 := read()
	if v1 != v2 || pv1 != pv2 || s1 != s2 || b1 != b2 || d1 != d2 {
		t.Fatalf("stitch mismatch: before (%d %d %d %d %d) after (%d %d %d %d %d)",
			v1, pv1, s1, b1, d1, v2, pv2, s2, b2, d2)
	}
	var pages int
	if err := db.db.QueryRow(`SELECT pageviews FROM v_web_pages
		WHERE project='app' AND day='2026-08-10' AND path='/a'`).Scan(&pages); err != nil {
		t.Fatal(err)
	}
	if pages != 3 {
		t.Fatalf("v_web_pages /a = %d", pages)
	}
}

// Every dimension view must hold the invariant, not just the daily rollup —
// a mismatch between a view's live SQL and its aggregation SQL would make one
// dimension jump the moment aggregation runs.
func TestStitchViewsInvariantAllWebDimensions(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedWebDay(t, db)

	type dim struct{ view, key string }
	dims := []dim{
		{"v_web_pages", "path"},
		{"v_web_referrers", "source"},
		{"v_web_countries", "country"},
		{"v_web_devices", "device"},
		{"v_web_browsers", "browser"},
		{"v_web_os", "os"},
		{"v_web_utm", "utm_source || '|' || utm_medium || '|' || utm_campaign"},
	}
	snapshot := func(d dim) map[string][2]int {
		t.Helper()
		rows, err := db.db.Query(fmt.Sprintf(
			`SELECT %s, visitors, pageviews FROM %s WHERE project='app' AND day='2026-08-10'`,
			d.key, d.view))
		if err != nil {
			t.Fatalf("%s: %v", d.view, err)
		}
		defer rows.Close()
		out := map[string][2]int{}
		for rows.Next() {
			var k string
			var v, pv int
			if err := rows.Scan(&k, &v, &pv); err != nil {
				t.Fatal(err)
			}
			out[k] = [2]int{v, pv}
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}

	before := map[string]map[string][2]int{}
	for _, d := range dims {
		before[d.view] = snapshot(d)
		if len(before[d.view]) == 0 {
			t.Fatalf("%s returned no live rows; invariant check would be vacuous", d.view)
		}
	}
	if err := db.AggregateWebDay(ctx, "app", day("2026-08-10")); err != nil {
		t.Fatal(err)
	}
	for _, d := range dims {
		after := snapshot(d)
		if len(after) != len(before[d.view]) {
			t.Errorf("%s: %d rows before, %d after", d.view, len(before[d.view]), len(after))
			continue
		}
		for k, wantVals := range before[d.view] {
			if after[k] != wantVals {
				t.Errorf("%s[%q]: before %v, after %v", d.view, k, wantVals, after[k])
			}
		}
	}
}

// Hits carrying no UTM parameters must not create an all-empty UTM row on
// either side of the boundary.
func TestStitchViewUTMExcludesEmpty(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedWebDay(t, db)
	count := func() int {
		t.Helper()
		var n int
		if err := db.db.QueryRow(`SELECT COUNT(*) FROM v_web_utm
			WHERE project='app' AND day='2026-08-10'`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// Only visitor v2 carries UTM tags in the fixture.
	if n := count(); n != 1 {
		t.Fatalf("live v_web_utm rows = %d, want 1", n)
	}
	if err := db.AggregateWebDay(ctx, "app", day("2026-08-10")); err != nil {
		t.Fatal(err)
	}
	if n := count(); n != 1 {
		t.Fatalf("aggregated v_web_utm rows = %d, want 1", n)
	}
}

func TestStitchViewsInvariantProduct(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedProductDay(t, db)
	read := func() (c, u int) {
		t.Helper()
		err := db.db.QueryRow(`SELECT count, unique_users FROM v_product_daily
			WHERE project='app' AND day='2026-08-10' AND event_name='subscribed'`).Scan(&c, &u)
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	c1, u1 := read()
	if c1 != 3 || u1 != 2 {
		t.Fatalf("live v_product_daily subscribed = (%d,%d), want (3,2)", c1, u1)
	}
	agg := store.ProductAggSettings{Enabled: true, TopN: 50}
	if err := db.AggregateProductDay(ctx, "app", day("2026-08-10"), agg); err != nil {
		t.Fatal(err)
	}
	c2, u2 := read()
	if c1 != c2 || u1 != u2 {
		t.Fatalf("product stitch mismatch: (%d,%d) vs (%d,%d)", c1, u1, c2, u2)
	}
	var dau int
	if err := db.db.QueryRow(`SELECT active_users FROM v_product_totals
		WHERE project='app' AND day='2026-08-10'`).Scan(&dau); err != nil {
		t.Fatal(err)
	}
	if dau != 2 {
		t.Fatalf("dau = %d", dau)
	}
}

// v_product_totals must report true DAU, not the sum of per-event uniques:
// u1 and u2 both appear under more than one event name in the fixture.
func TestStitchViewProductTotalsIsTrueDAU(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedProductDay(t, db)
	read := func() (events, users int) {
		t.Helper()
		if err := db.db.QueryRow(`SELECT total_events, active_users FROM v_product_totals
			WHERE project='app' AND day='2026-08-10'`).Scan(&events, &users); err != nil {
			t.Fatal(err)
		}
		return
	}
	e1, u1 := read()
	if e1 != 4 || u1 != 2 {
		t.Fatalf("live v_product_totals = (%d,%d), want (4,2)", e1, u1)
	}
	if err := db.AggregateProductDay(ctx, "app", day("2026-08-10"),
		store.ProductAggSettings{Enabled: true, TopN: 50}); err != nil {
		t.Fatal(err)
	}
	if e2, u2 := read(); e1 != e2 || u1 != u2 {
		t.Fatalf("totals stitch mismatch: (%d,%d) vs (%d,%d)", e1, u1, e2, u2)
	}
}

// Days on either side of the boundary must coexist without double counting:
// one aggregated day plus one still-raw day yields exactly two rows.
func TestStitchViewsMixedAggregatedAndRawDays(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedWebDay(t, db) // 2026-08-10
	if err := db.WriteWebHits(ctx, []store.WebHit{
		{ID: "9", Project: "app", TS: ts("2026-08-11T10:00:00Z"), VisitorHash: "v9", Path: "/a"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateWebDay(ctx, "app", day("2026-08-10")); err != nil {
		t.Fatal(err)
	}
	rows, err := db.db.Query(`SELECT day, pageviews FROM v_web_daily
		WHERE project='app' ORDER BY day`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]int{}
	for rows.Next() {
		var d string
		var pv int
		if err := rows.Scan(&d, &pv); err != nil {
			t.Fatal(err)
		}
		if _, dup := got[d]; dup {
			t.Fatalf("day %s appears twice: raw and aggregate are both contributing", d)
		}
		got[d] = pv
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got["2026-08-10"] != 4 || got["2026-08-11"] != 1 {
		t.Fatalf("v_web_daily = %v, want {2026-08-10:4 2026-08-11:1}", got)
	}
}
