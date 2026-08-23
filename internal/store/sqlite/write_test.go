package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/dmitry/analytics/internal/store"
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
		VisitorHash: "v1", Path: "/x", ReferrerSource: "google",
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

func TestSyncProjectsArchiving(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	must := func(err error) { if err != nil { t.Fatal(err) } }
	must(db.SyncProjects(ctx, []store.ProjectInfo{{Alias: "a", Name: "A"}, {Alias: "b", Name: "B"}}))
	var idA string
	must(db.db.QueryRow(`SELECT id FROM projects WHERE alias='a'`).Scan(&idA))
	if len(idA) != 36 {
		t.Fatalf("generated id must be a UUID, got %q", idA)
	}
	// b disappears from config -> archived
	must(db.SyncProjects(ctx, []store.ProjectInfo{{Alias: "a", Name: "A2"}}))
	var name string
	var archived *string
	must(db.db.QueryRow(`SELECT name, archived_at FROM projects WHERE alias='a'`).Scan(&name, &archived))
	if name != "A2" || archived != nil {
		t.Fatalf("a: name=%q archived=%v", name, archived)
	}
	var idA2 string
	must(db.db.QueryRow(`SELECT id FROM projects WHERE alias='a'`).Scan(&idA2))
	if idA2 != idA {
		t.Fatal("re-sync must retain the generated id")
	}
	must(db.db.QueryRow(`SELECT name, archived_at FROM projects WHERE alias='b'`).Scan(&name, &archived))
	if archived == nil {
		t.Fatal("b must be archived")
	}
	// b returns -> unarchived
	must(db.SyncProjects(ctx, []store.ProjectInfo{{Alias: "a", Name: "A2"}, {Alias: "b", Name: "B"}}))
	must(db.db.QueryRow(`SELECT archived_at FROM projects WHERE alias='b'`).Scan(&archived))
	if archived != nil {
		t.Fatal("b must be unarchived after reappearing")
	}
	ids, err := db.ProjectAliases(ctx)
	if err != nil || len(ids) != 2 {
		t.Fatalf("ProjectAliases = %v, %v", ids, err)
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
