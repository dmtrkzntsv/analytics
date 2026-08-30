package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
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
	if err := db.AggregateProductDay(ctx, "app", day("2026-08-10"), nil, 50); err != nil {
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
	if err := db.AggregateProductDay(ctx, "app", day("2026-08-10"), nil, 50); err != nil {
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
		{ID: "9", Project: "app", TS: ts("2026-08-11T10:00:00Z"), ActorID: "v9", Path: "/a"},
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

// The daily pass only rolls up days that have aged out of the raw window, so
// the identity stitch view must serve recent days from raw or the users and
// groups pages would be blank for the whole window.
func TestStitchViewIdentityDailyCoversRawDays(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	ts := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)

	if err := db.WriteAppViews(ctx, []store.AppView{
		{ID: "1", Project: "p", TS: ts, ReceivedAt: ts, ActorID: "a",
			UserID: "u1", GroupID: "org9", Screen: "/x"},
		{ID: "2", Project: "p", TS: ts, ReceivedAt: ts, ActorID: "b",
			UserID: "u2", GroupID: "org9", Screen: "/x"},
	}); err != nil {
		t.Fatal(err)
	}

	var actors, users, views int
	if err := db.db.QueryRowContext(ctx,
		`SELECT actors, users, views FROM v_identity_daily
		 WHERE project='p' AND kind='group' AND id='org9'`).
		Scan(&actors, &users, &views); err != nil {
		t.Fatalf("group row before aggregation: %v", err)
	}
	if actors != 2 || users != 2 || views != 2 {
		t.Errorf("live group row = actors %d users %d views %d; want 2 2 2", actors, users, views)
	}

	// After aggregation the same figures must come from the aggregate half,
	// with no double counting from the raw rows the pass deletes.
	if err := db.AggregateIdentityDay(ctx, "p", appDay()); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateAppDay(ctx, "p", appDay()); err != nil {
		t.Fatal(err)
	}
	var rows int
	if err := db.db.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM v_identity_daily WHERE project='p' AND kind='group' AND id='org9'`).
		Scan(&rows); err != nil {
		t.Fatal(err)
	}
	if rows != 1 {
		t.Errorf("group rows after aggregation = %d, want exactly 1 (no double count)", rows)
	}
	if err := db.db.QueryRowContext(ctx,
		`SELECT actors, users, views FROM v_identity_daily
		 WHERE project='p' AND kind='group' AND id='org9'`).
		Scan(&actors, &users, &views); err != nil {
		t.Fatal(err)
	}
	if actors != 2 || users != 2 || views != 2 {
		t.Errorf("aggregated group row = actors %d users %d views %d; want 2 2 2", actors, users, views)
	}
}

// seedDeclaredProject registers a project row with a declared attribute
// list. v_product_attrs' live half reads projects.attributes, so the row
// must exist or the declared half of the view is empty.
func seedDeclaredProject(t *testing.T, db *DB, alias string, attrs []string) {
	t.Helper()
	if attrs == nil {
		attrs = []string{}
	}
	b, err := json.Marshal(attrs)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.db.Exec(
		`INSERT INTO projects (id, alias, name, attributes) VALUES (?,?,?,?)`,
		alias, alias, alias, string(b)); err != nil {
		t.Fatal(err)
	}
}

// attrRow mirrors one v_product_attrs row for before/after comparison.
type attrRow struct {
	Event, Key, Value string
	Count, Uniques    int
}

// readAttrs drains every v_product_attrs row for one project/day into a
// slice. It fully drains and closes the cursor before returning: the pool
// is SetMaxOpenConns(1), so holding rows open while the caller issues the
// next query would deadlock.
func readAttrs(t *testing.T, db *DB, project, day string) []attrRow {
	t.Helper()
	rows, err := db.db.Query(`SELECT event_name, attr_key, attr_value, count, unique_users
		FROM v_product_attrs WHERE project=? AND day=?
		ORDER BY event_name, attr_key, attr_value`, project, day)
	if err != nil {
		t.Fatal(err)
	}
	var out []attrRow
	for rows.Next() {
		var r attrRow
		if err := rows.Scan(&r.Event, &r.Key, &r.Value, &r.Count, &r.Uniques); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		out = append(out, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

// seedAttrDay writes 60 distinct "plan" values for one event on one day --
// more than the 50 cap, so the top-N cutoff and the "(other)" tail are both
// exercised. Counts vary (1..3) so the ranking is not a pure alphabetical
// tiebreak, and the four actors repeat across values so the tail's
// unique_users is strictly less than the sum of its per-value uniques --
// the exact case a summed "(other)" row would get wrong.
func seedAttrDay(t *testing.T, db *DB, project string) {
	t.Helper()
	var evs []store.ProductEvent
	id := 0
	for i := 0; i < 60; i++ {
		for n := 0; n <= i%3; n++ {
			id++
			evs = append(evs, store.ProductEvent{
				ID: fmt.Sprintf("e%04d", id), Project: project, EventName: "signup",
				ActorID: fmt.Sprintf("a%d", (i+n)%4), TS: ts("2026-08-01T10:00:00Z"),
				Attributes: map[string]string{"plan": fmt.Sprintf("p%02d", i)},
				Platform:   []string{"ios", "android"}[i%2],
				Version:    []string{"1.0", "2.0", "3.0"}[i%3],
			})
		}
	}
	// A second event name, so the per-event partitioning is exercised too.
	evs = append(evs, store.ProductEvent{
		ID: "ping1", Project: project, EventName: "ping", ActorID: "a9",
		TS: ts("2026-08-01T11:00:00Z"), Attributes: map[string]string{"plan": "pro"},
		Platform: "web", Version: "1.0",
	})
	if err := db.WriteProductEvents(context.Background(), evs); err != nil {
		t.Fatal(err)
	}
}

// The binding invariant (002_views.sql:5-8) for the attribute breakdown:
// v_product_attrs must return identical rows before and after the day is
// aggregated, including the top-N cutoff, its count-desc/value-asc
// tiebreak, and the recomputed "(other)" tail.
func TestProductAttrsViewInvariant(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedDeclaredProject(t, db, "blog", []string{"plan"})
	seedAttrDay(t, db, "blog")

	before := readAttrs(t, db, "blog", "2026-08-01")
	if len(before) == 0 {
		t.Fatal("live v_product_attrs returned no rows; the comparison would be vacuous")
	}
	// Sanity: the cap and the tail must actually be in play, or the
	// interesting half of the invariant is untested.
	var plans, other int
	for _, r := range before {
		if r.Event == "signup" && r.Key == "plan" {
			plans++
			if r.Value == "(other)" {
				other = r.Uniques
			}
		}
	}
	if plans != 51 {
		t.Fatalf("live signup/plan rows = %d, want 51 (50 capped + one (other))", plans)
	}
	if other == 0 {
		t.Fatal("no (other) row: the tail path is untested")
	}

	if err := db.AggregateProductDay(ctx, "blog",
		civil.DateOf(ts("2026-08-01T00:00:00Z")), []string{"plan"}, 50); err != nil {
		t.Fatal(err)
	}
	after := readAttrs(t, db, "blog", "2026-08-01")
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("view changed when the day aggregated:\nbefore %v\nafter  %v", before, after)
	}
}

// The "(other)" row's unique_users must be a fresh COUNT(DISTINCT actor_id)
// over the tail, not a sum of the per-value uniques: an actor appearing
// under several tail values would otherwise be counted once per value.
func TestProductAttrsViewOtherRecomputesUniques(t *testing.T) {
	db := newTestDB(t)
	seedDeclaredProject(t, db, "blog", []string{"plan"})
	seedAttrDay(t, db, "blog")
	var count, uniques int
	if err := db.db.QueryRow(`SELECT count, unique_users FROM v_product_attrs
		WHERE project='blog' AND day='2026-08-01' AND event_name='signup'
		  AND attr_key='plan' AND attr_value='(other)'`).Scan(&count, &uniques); err != nil {
		t.Fatal(err)
	}
	if uniques >= count {
		t.Fatalf("(other) = count %d uniques %d; the fixture repeats actors across "+
			"tail values, so uniques must be strictly smaller than a summed count",
			count, uniques)
	}
}

// System dimensions roll up unconditionally (task 2), so the live half must
// produce them for a project that declares no attributes at all -- and the
// invariant must hold for them too.
func TestProductAttrsViewSystemDimensionsWithoutDeclaredKeys(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedDeclaredProject(t, db, "blog", nil)
	seedAttrDay(t, db, "blog")

	before := readAttrs(t, db, "blog", "2026-08-01")
	var sys, custom int
	for _, r := range before {
		switch r.Key {
		case "$platform", "$version":
			sys++
		default:
			custom++
		}
	}
	if sys == 0 {
		t.Fatal("no $platform/$version rows for an undeclared project")
	}
	if custom != 0 {
		t.Fatalf("%d rows for undeclared custom keys; only system dimensions were expected", custom)
	}
	if err := db.AggregateProductDay(ctx, "blog",
		civil.DateOf(ts("2026-08-01T00:00:00Z")), nil, 50); err != nil {
		t.Fatal(err)
	}
	if after := readAttrs(t, db, "blog", "2026-08-01"); !reflect.DeepEqual(before, after) {
		t.Fatalf("system dimensions changed when the day aggregated:\nbefore %v\nafter  %v",
			before, after)
	}
}

// The cap comes from the meta row the app writes at boot, so a non-default
// value must move the cutoff in the live half exactly as it moves it in the
// aggregation.
func TestProductAttrsViewHonoursMetaCap(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.SetMeta(ctx, "product_attributes_top_n", "3"); err != nil {
		t.Fatal(err)
	}
	seedDeclaredProject(t, db, "blog", []string{"plan"})
	seedAttrDay(t, db, "blog")
	before := readAttrs(t, db, "blog", "2026-08-01")
	var plans int
	for _, r := range before {
		if r.Event == "signup" && r.Key == "plan" {
			plans++
		}
	}
	if plans != 4 {
		t.Fatalf("live signup/plan rows = %d, want 4 (3 capped + one (other))", plans)
	}
	if err := db.AggregateProductDay(ctx, "blog",
		civil.DateOf(ts("2026-08-01T00:00:00Z")), []string{"plan"}, 3); err != nil {
		t.Fatal(err)
	}
	if after := readAttrs(t, db, "blog", "2026-08-01"); !reflect.DeepEqual(before, after) {
		t.Fatalf("capped view changed when the day aggregated:\nbefore %v\nafter  %v",
			before, after)
	}
}

// A missing meta row must fall back to the same default the aggregation
// clamps to (defaultAttrsTopN), not silently return zero live rows -- which
// would make every invariant assertion above pass vacuously.
func TestProductAttrsViewDefaultsCapWhenMetaMissing(t *testing.T) {
	db := newTestDB(t)
	seedDeclaredProject(t, db, "blog", []string{"plan"})
	seedAttrDay(t, db, "blog")
	var n int
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM meta
		WHERE key='product_attributes_top_n'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("fixture unexpectedly has a cap row (%d)", n)
	}
	if err := db.db.QueryRow(`SELECT COUNT(*) FROM v_product_attrs
		WHERE project='blog' AND day='2026-08-01' AND event_name='signup'
		  AND attr_key='plan'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != defaultAttrsTopN+1 {
		t.Fatalf("signup/plan rows with no cap row = %d, want %d", n, defaultAttrsTopN+1)
	}
}

// aggregate_product.go:29-31 clamps a non-positive topN to defaultAttrsTopN
// precisely so breakdowns are not silently lost -- `rn <= 0` keeps nothing,
// which would sweep every value into "(other)". The view's cap must clamp
// identically, or PRODUCT_ATTRIBUTES_TOP_N=0 (or a hand-edited meta row)
// makes the current day collapse to a single "(other)" row while the same
// day after rollup shows the full top-N: exactly the jump the invariant
// forbids.
func TestProductAttrsViewClampsBadMetaCap(t *testing.T) {
	for _, tc := range []struct {
		name, meta string
		goTopN     int // what the Go side is handed for the same setting
	}{
		{"zero", "0", 0},
		{"negative", "-7", -7},
		// A non-numeric value casts to 0 in SQL. No env value produces it,
		// so the Go side is handed the configured default while meta has
		// been hand-edited to garbage; both must still agree.
		{"non numeric", "banana", defaultAttrsTopN},
		{"empty", "", defaultAttrsTopN},
	} {
		t.Run(tc.name, func(t *testing.T) {
			db := newTestDB(t)
			ctx := context.Background()
			if err := db.SetMeta(ctx, "product_attributes_top_n", tc.meta); err != nil {
				t.Fatal(err)
			}
			seedDeclaredProject(t, db, "blog", []string{"plan"})
			seedAttrDay(t, db, "blog")

			before := readAttrs(t, db, "blog", "2026-08-01")
			var plans int
			for _, r := range before {
				if r.Event == "signup" && r.Key == "plan" {
					plans++
				}
			}
			// The clamp must land on defaultAttrsTopN, not on "keep
			// nothing": one row per kept value plus the tail.
			if plans != defaultAttrsTopN+1 {
				t.Fatalf("live signup/plan rows with meta=%q = %d, want %d "+
					"(the cap must clamp to defaultAttrsTopN, not collapse to (other))",
					tc.meta, plans, defaultAttrsTopN+1)
			}
			if err := db.AggregateProductDay(ctx, "blog",
				civil.DateOf(ts("2026-08-01T00:00:00Z")), []string{"plan"}, tc.goTopN); err != nil {
				t.Fatal(err)
			}
			if after := readAttrs(t, db, "blog", "2026-08-01"); !reflect.DeepEqual(before, after) {
				t.Fatalf("view changed when the day aggregated with meta=%q:\nbefore %v\nafter  %v",
					tc.meta, before, after)
			}
		})
	}
}
