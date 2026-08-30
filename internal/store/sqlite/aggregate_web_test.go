package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// seedWebDay inserts a deterministic fixture for 2026-08-10, project "app":
//
//	visitor v1: hits 10:00 /a, 10:10 /b     -> 1 session, 2 pageviews, not bounce, dur 600
//	visitor v1: hit  12:00 /a               -> gap > 30min => 2nd session, bounce, dur 0
//	visitor v2: hit  11:00 /a  (country DE, device mobile, browser Chrome, os Android,
//	                            source google, utm hn/social/launch) -> 1 session, bounce
//
// Totals: visitors 2, pageviews 4, sessions 3, bounces 2, duration 600.
func seedWebDay(t *testing.T, db *DB) {
	t.Helper()
	mk := func(id, vis, path, tsS, source, country, device, browser, osN, us, um, uc string) store.WebHit {
		return store.WebHit{ID: id, Project: "app", TS: ts(tsS), ActorID: vis, Path: path,
			ReferrerSource: source, Country: country, Device: device, Browser: browser, OS: osN,
			UTMSource: us, UTMMedium: um, UTMCampaign: uc}
	}
	hits := []store.WebHit{
		mk("1", "v1", "/a", "2026-08-10T10:00:00Z", "", "US", "desktop", "Firefox", "Linux", "", "", ""),
		mk("2", "v1", "/b", "2026-08-10T10:10:00Z", "", "US", "desktop", "Firefox", "Linux", "", "", ""),
		mk("3", "v1", "/a", "2026-08-10T12:00:00Z", "", "US", "desktop", "Firefox", "Linux", "", "", ""),
		mk("4", "v2", "/a", "2026-08-10T11:00:00Z", "google", "DE", "mobile", "Chrome", "Android", "hn", "social", "launch"),
	}
	if err := db.WriteWebHits(context.Background(), hits); err != nil {
		t.Fatal(err)
	}
}

func day(s string) civil.Date {
	d, err := civil.Parse(s)
	if err != nil {
		panic(err)
	}
	return d
}

func TestAggregateWebDay(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedWebDay(t, db)
	if err := db.AggregateWebDay(ctx, "app", day("2026-08-10")); err != nil {
		t.Fatal(err)
	}
	var visitors, pageviews, sessions, bounces, dur int
	err := db.db.QueryRow(`SELECT visitors, pageviews, sessions, bounces, duration_sec
		FROM agg_web_daily WHERE project='app' AND day='2026-08-10'`).
		Scan(&visitors, &pageviews, &sessions, &bounces, &dur)
	if err != nil {
		t.Fatal(err)
	}
	if visitors != 2 || pageviews != 4 || sessions != 3 || bounces != 2 || dur != 600 {
		t.Fatalf("daily = v%d pv%d s%d b%d d%d", visitors, pageviews, sessions, bounces, dur)
	}
	// Dimensions
	var pv int
	if err := db.db.QueryRow(`SELECT pageviews FROM agg_web_pages WHERE project='app' AND day='2026-08-10' AND path='/a'`).Scan(&pv); err != nil || pv != 3 {
		t.Fatalf("pages /a pv=%d err=%v", pv, err)
	}
	if err := db.db.QueryRow(`SELECT visitors FROM agg_web_countries WHERE project='app' AND day='2026-08-10' AND country='DE'`).Scan(&pv); err != nil || pv != 1 {
		t.Fatalf("countries DE v=%d err=%v", pv, err)
	}
	if err := db.db.QueryRow(`SELECT pageviews FROM agg_web_referrers WHERE project='app' AND day='2026-08-10' AND source='google'`).Scan(&pv); err != nil || pv != 1 {
		t.Fatalf("referrers google pv=%d err=%v", pv, err)
	}
	if err := db.db.QueryRow(`SELECT pageviews FROM agg_web_utm WHERE project='app' AND day='2026-08-10' AND utm_source='hn' AND utm_medium='social' AND utm_campaign='launch'`).Scan(&pv); err != nil || pv != 1 {
		t.Fatalf("utm pv=%d err=%v", pv, err)
	}
	// Empty utm rows are not stored
	var n int
	db.db.QueryRow(`SELECT COUNT(*) FROM agg_web_utm WHERE utm_source='' AND utm_medium='' AND utm_campaign=''`).Scan(&n)
	if n != 0 {
		t.Fatal("all-empty utm combination must not be aggregated")
	}
	// Raw rows deleted in same tx
	db.db.QueryRow(`SELECT COUNT(*) FROM web_hits WHERE project='app'`).Scan(&n)
	if n != 0 {
		t.Fatalf("raw rows remaining = %d, want 0", n)
	}
}

func TestAggregateWebDayIdempotent(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedWebDay(t, db)
	if err := db.AggregateWebDay(ctx, "app", day("2026-08-10")); err != nil {
		t.Fatal(err)
	}
	// Second run: raw is gone => must be a no-op, not zero out aggregates.
	if err := db.AggregateWebDay(ctx, "app", day("2026-08-10")); err != nil {
		t.Fatal(err)
	}
	var pageviews int
	db.db.QueryRow(`SELECT pageviews FROM agg_web_daily WHERE project='app' AND day='2026-08-10'`).Scan(&pageviews)
	if pageviews != 4 {
		t.Fatalf("second run corrupted aggregates: pv=%d", pageviews)
	}
}

func TestAggregateWebDayScopesToProjectAndDay(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedWebDay(t, db)
	other := []store.WebHit{
		{ID: "x1", Project: "other", TS: ts("2026-08-10T10:00:00Z"), ActorID: "o1", Path: "/z"},
		{ID: "x2", Project: "app", TS: ts("2026-08-11T10:00:00Z"), ActorID: "v9", Path: "/next-day"},
	}
	if err := db.WriteWebHits(ctx, other); err != nil {
		t.Fatal(err)
	}
	if err := db.AggregateWebDay(ctx, "app", day("2026-08-10")); err != nil {
		t.Fatal(err)
	}
	var n int
	db.db.QueryRow(`SELECT COUNT(*) FROM web_hits`).Scan(&n)
	if n != 2 {
		t.Fatalf("other project/day raw rows must survive, remaining=%d", n)
	}
}

func TestWebDaysBefore(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	seedWebDay(t, db) // 2026-08-10
	db.WriteWebHits(ctx, []store.WebHit{
		{ID: "n1", Project: "app", TS: ts("2026-08-15T09:00:00Z"), ActorID: "v", Path: "/"},
	})
	days, err := db.WebDaysBefore(ctx, "app", day("2026-08-12"))
	if err != nil {
		t.Fatal(err)
	}
	if len(days) != 1 || days[0].String() != "2026-08-10" {
		t.Fatalf("days = %v", days)
	}
}

var _ = time.Now
