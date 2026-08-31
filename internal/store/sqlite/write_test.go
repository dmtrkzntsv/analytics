package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestWriteWebHitsRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	hits := []store.WebHit{{
		ID: "h1", Project: "app", TS: ts("2026-08-22T10:00:00Z"),
		ActorID: "v1", Path: "/x", ReferrerSource: "google",
		UTMSource: "hn", Country: "DE", Device: "desktop", Browser: "Firefox", OS: "Linux",
	}}
	if err := db.WriteWebHits(ctx, hits); err != nil {
		t.Fatal(err)
	}
	var path, tsCol string
	if err := db.db.QueryRow(`SELECT path, ts FROM web_hits WHERE id='h1'`).Scan(&path, &tsCol); err != nil {
		t.Fatal(err)
	}
	if path != "/x" || tsCol != "2026-08-22T10:00:00Z" {
		t.Fatalf("got %q %q", path, tsCol)
	}
	if err := db.WriteWebHits(ctx, nil); err != nil {
		t.Fatal("empty batch must be a no-op")
	}
}

func TestWriteProductEventsAttributesJSON(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	err := db.WriteProductEvents(ctx, []store.ProductEvent{
		{ID: "e1", Project: "app", EventName: "sub", UserID: "u1",
			TS: ts("2026-08-22T10:00:00Z"), Attributes: map[string]string{"plan": "pro"}},
		{ID: "e2", Project: "app", EventName: "sub", UserID: "u2",
			TS: ts("2026-08-22T10:01:00Z")}, // nil attributes
	})
	if err != nil {
		t.Fatal(err)
	}
	var attrs string
	if err := db.db.QueryRow(`SELECT attributes->>'plan' FROM product_events WHERE id='e1'`).Scan(&attrs); err != nil {
		t.Fatal(err)
	}
	if attrs != "pro" {
		t.Fatalf("attrs = %q", attrs)
	}
	if err := db.db.QueryRow(`SELECT attributes FROM product_events WHERE id='e2'`).Scan(&attrs); err != nil {
		t.Fatal(err)
	}
	if attrs != "{}" {
		t.Fatalf("nil attributes must store {}, got %q", attrs)
	}
}

// ProjectAliases returns every registry row including archived ones: the
// daily pass (internal/jobs) relies on this to keep maintaining a project
// after it is archived, using global retention.
func TestProjectAliasesIncludesArchived(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	audit := store.AuditEntry{Actor: "test", Action: "project.create"}
	must(db.CreateProject(ctx, store.RegistryProject{Alias: "a", Name: "A", Identity: "anonymous", AllowedOrigins: "[]"}, audit))
	must(db.CreateProject(ctx, store.RegistryProject{Alias: "b", Name: "B", Identity: "anonymous", AllowedOrigins: "[]"}, audit))
	must(db.SetProjectArchived(ctx, "b", true, store.AuditEntry{Actor: "test", Action: "project.archive"}))

	ids, err := db.ProjectAliases(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 2 || ids[0] != "a" || ids[1] != "b" {
		t.Fatalf("ProjectAliases = %v, want [a b] (archived rows included)", ids)
	}
}

func TestMetaRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if v, err := db.GetMeta(ctx, "missing"); err != nil || v != "" {
		t.Fatalf("missing key: %q %v", v, err)
	}
	if err := db.SetMeta(ctx, "salt", "s1"); err != nil {
		t.Fatal(err)
	}
	if err := db.SetMeta(ctx, "salt", "s2"); err != nil {
		t.Fatal(err) // overwrite
	}
	if v, _ := db.GetMeta(ctx, "salt"); v != "s2" {
		t.Fatalf("got %q", v)
	}
}

// --- app views, identities, idempotent writes ---

func TestWriteAppViewsRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	ts := time.Date(2026, 8, 23, 10, 0, 0, 0, time.UTC)

	in := []store.AppView{{
		ID: "018f-a", Project: "p", TS: ts, ReceivedAt: ts,
		ActorID: "act1", UserID: "u1", GroupID: "org9", SessionID: "s1",
		Screen: "/settings", Platform: "ios", AppVersion: "2.4.1",
		OSVersion: "17.2", DeviceModel: "iPhone15,2", Locale: "en-US", Country: "DE",
	}}
	if err := db.WriteAppViews(ctx, in); err != nil {
		t.Fatalf("write: %v", err)
	}

	var screen, platform, group, session, locale string
	if err := db.db.QueryRowContext(ctx,
		`SELECT screen, platform, group_id, session_id, locale FROM app_views WHERE id=?`, "018f-a").
		Scan(&screen, &platform, &group, &session, &locale); err != nil {
		t.Fatalf("read back: %v", err)
	}
	if screen != "/settings" || platform != "ios" || group != "org9" ||
		session != "s1" || locale != "en-US" {
		t.Errorf("got %q %q %q %q %q", screen, platform, group, session, locale)
	}
}

func TestWriteAppViewsEmptyIsNoop(t *testing.T) {
	db := newTestDB(t)
	if err := db.WriteAppViews(context.Background(), nil); err != nil {
		t.Fatalf("empty write: %v", err)
	}
}

func TestWritesAreIdempotentOnID(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	ts := time.Now().UTC()

	view := store.AppView{ID: "dup", Project: "p", TS: ts, ReceivedAt: ts,
		ActorID: "a", Screen: "/x"}
	hit := store.WebHit{ID: "duph", Project: "p", TS: ts, ReceivedAt: ts,
		ActorID: "a", Path: "/x"}
	ev := store.ProductEvent{ID: "dupe", Project: "p", EventName: "n",
		TS: ts, ReceivedAt: ts, ActorID: "a"}

	for i := 0; i < 2; i++ {
		if err := db.WriteAppViews(ctx, []store.AppView{view}); err != nil {
			t.Fatalf("app write %d: %v", i, err)
		}
		if err := db.WriteWebHits(ctx, []store.WebHit{hit}); err != nil {
			t.Fatalf("hit write %d: %v", i, err)
		}
		if err := db.WriteProductEvents(ctx, []store.ProductEvent{ev}); err != nil {
			t.Fatalf("event write %d: %v", i, err)
		}
	}

	for _, table := range []string{"app_views", "web_hits", "product_events"} {
		var n int
		if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM `+table).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n != 1 {
			t.Errorf("%s has %d rows after replay, want 1", table, n)
		}
	}
}

func TestWriteCarriesIdentityAndAppContext(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	ts := time.Now().UTC()

	if err := db.WriteWebHits(ctx, []store.WebHit{{ID: "h", Project: "p", TS: ts,
		ReceivedAt: ts, ActorID: "a", UserID: "u1", GroupID: "org9", Path: "/x"}}); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteProductEvents(ctx, []store.ProductEvent{{ID: "e", Project: "p",
		EventName: "n", TS: ts, ReceivedAt: ts, ActorID: "a", UserID: "u1",
		GroupID: "org9", Platform: "ios", AppVersion: "2.4.1"}}); err != nil {
		t.Fatal(err)
	}

	var hu, hg string
	if err := db.db.QueryRowContext(ctx,
		`SELECT user_id, group_id FROM web_hits WHERE id='h'`).Scan(&hu, &hg); err != nil {
		t.Fatal(err)
	}
	if hu != "u1" || hg != "org9" {
		t.Errorf("web hit identity = %q %q", hu, hg)
	}

	var plat, ver string
	if err := db.db.QueryRowContext(ctx,
		`SELECT platform, app_version FROM product_events WHERE id='e'`).Scan(&plat, &ver); err != nil {
		t.Fatal(err)
	}
	if plat != "ios" || ver != "2.4.1" {
		t.Errorf("event context = %q %q", plat, ver)
	}
}

func TestUpsertIdentitiesLatestNameWins(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()

	if err := db.UpsertIdentities(ctx, []store.Identity{
		{Project: "p", Kind: store.KindUser, ID: "u1", Name: "Ada"},
	}); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	if err := db.UpsertIdentities(ctx, []store.Identity{
		{Project: "p", Kind: store.KindUser, ID: "u1", Name: "Ada Lovelace"},
		{Project: "p", Kind: store.KindGroup, ID: "org9", Name: "Acme"},
		{Project: "p", Kind: store.KindUser, ID: "", Name: "skipped"},
		{Project: "p", Kind: store.KindUser, ID: "u2", Name: ""},
	}); err != nil {
		t.Fatalf("second upsert: %v", err)
	}

	var n int
	if err := db.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM identities`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	var name string
	if err := db.db.QueryRowContext(ctx,
		`SELECT name FROM identities WHERE project='p' AND kind='user' AND id='u1'`).
		Scan(&name); err != nil {
		t.Fatal(err)
	}
	if n != 2 || name != "Ada Lovelace" {
		t.Errorf("rows=%d name=%q; want 2 rows and the latest name", n, name)
	}
}

func TestUpsertIdentitiesEmptyIsNoop(t *testing.T) {
	db := newTestDB(t)
	if err := db.UpsertIdentities(context.Background(), nil); err != nil {
		t.Fatalf("empty upsert: %v", err)
	}
}

func TestWriteWebHitsStoresHost(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	hits := []store.WebHit{{
		ID: "h1", Project: "app", TS: ts("2026-08-10T10:00:00Z"),
		ActorID: "v1", Host: "shop.example.com", Path: "/pricing",
	}}
	if err := db.WriteWebHits(ctx, hits); err != nil {
		t.Fatal(err)
	}
	var host string
	if err := db.db.QueryRow(
		`SELECT host FROM web_hits WHERE id='h1'`).Scan(&host); err != nil {
		t.Fatal(err)
	}
	if host != "shop.example.com" {
		t.Errorf("host = %q, want %q", host, "shop.example.com")
	}
}

// A hit written without a host must read back as the empty string, not
// NULL: every consumer scans into a string and the column is NOT NULL.
func TestWriteWebHitsHostDefaultsEmpty(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	if err := db.WriteWebHits(ctx, []store.WebHit{{
		ID: "h2", Project: "app", TS: ts("2026-08-10T10:00:00Z"),
		ActorID: "v1", Path: "/pricing",
	}}); err != nil {
		t.Fatal(err)
	}
	var host string
	if err := db.db.QueryRow(
		`SELECT host FROM web_hits WHERE id='h2'`).Scan(&host); err != nil {
		t.Fatal(err)
	}
	if host != "" {
		t.Errorf("host = %q, want empty", host)
	}
}
