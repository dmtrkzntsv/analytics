package sqlite

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/dmtrkzntsv/twillingate/internal/civil"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// TestSeedEvidenceFixture builds a populated database for the Evidence build
// check. Skipped unless SEED_DB is set.
func TestSeedEvidenceFixture(t *testing.T) {
	path := os.Getenv("SEED_DB")
	if path == "" {
		t.Skip("SEED_DB not set")
	}
	db, err := openAt(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	ctx := context.Background()
	if err := db.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	audit := store.AuditEntry{Actor: "seed", Action: "project.create"}
	for _, alias := range []string{"app", "blog"} {
		if err := db.CreateProject(ctx, store.RegistryProject{
			Alias: alias, Name: alias, Identity: "anonymous", AllowedOrigins: "[]"}, audit); err != nil {
			t.Fatal(err)
		}
	}
	var hits []store.WebHit
	var evs []store.ProductEvent
	base := time.Now().UTC().AddDate(0, 0, -10)
	for d := 0; d < 10; d++ {
		day := base.AddDate(0, 0, d)
		for i := 0; i < 5; i++ {
			ts := day.Add(time.Duration(i) * time.Hour)
			hits = append(hits, store.WebHit{
				ID: fmt.Sprintf("h%d-%d", d, i), Project: "app", TS: ts,
				ActorID: fmt.Sprintf("v%d", i%3), Path: []string{"/", "/pricing", "/docs"}[i%3],
				ReferrerSource: []string{"google", "", "hn"}[i%3],
				Country:        []string{"US", "DE", "FR"}[i%3], Device: []string{"desktop", "mobile"}[i%2],
				Browser: "Chrome", OS: "Linux",
				UTMSource: []string{"hn", "", ""}[i%3], UTMMedium: []string{"social", "", ""}[i%3],
				UTMCampaign: []string{"launch", "", ""}[i%3],
			})
			evs = append(evs, store.ProductEvent{
				ID: fmt.Sprintf("e%d-%d", d, i), Project: "app",
				EventName: []string{"signup", "subscribed"}[i%2],
				ActorID:   fmt.Sprintf("u%d", i%3), TS: ts,
				Attributes: map[string]string{"plan": []string{"pro", "free"}[i%2]},
			})
		}
	}
	if err := db.WriteWebHits(ctx, hits); err != nil {
		t.Fatal(err)
	}
	if err := db.WriteProductEvents(ctx, evs); err != nil {
		t.Fatal(err)
	}
	// Aggregate the oldest days so both sides of the stitch views have rows.
	for d := 0; d < 4; d++ {
		day := civilOf(base.AddDate(0, 0, d))
		if err := db.AggregateWebDay(ctx, "app", day); err != nil {
			t.Fatal(err)
		}
		if err := db.AggregateProductDay(ctx, "app", day, store.ProductAggSettings{
			Enabled: true, Attributes: map[string][]string{"*": {"plan"}}, TopN: 10}); err != nil {
			t.Fatal(err)
		}
	}
	keys, err := db.KnownAttributeKeys(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.RebuildFlatView(ctx, keys); err != nil {
		t.Fatal(err)
	}
	t.Logf("seeded %s: %d hits, %d events", path, len(hits), len(evs))
}

func civilOf(t time.Time) civil.Date { return civil.DateOf(t) }
