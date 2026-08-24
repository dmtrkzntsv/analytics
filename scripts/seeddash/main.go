package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/dmitry/analytics/internal/civil"
	"github.com/dmitry/analytics/internal/store"
	_ "github.com/dmitry/analytics/internal/store/sqlite"
)

func main() {
	ctx := context.Background()
	st, err := store.Open("sqlite://" + os.Args[1])
	if err != nil {
		panic(err)
	}
	defer st.Close()
	if err := st.Migrate(ctx); err != nil {
		panic(err)
	}
	if err := st.SyncProjects(ctx, []store.ProjectInfo{{Alias: "app", Name: "App"}}); err != nil {
		panic(err)
	}
	now := time.Now().UTC()
	var views []store.AppView
	var hits []store.WebHit
	var events []store.ProductEvent
	for d := 40; d >= 1; d-- {
		day := now.AddDate(0, 0, -d)
		for u := 0; u < 5; u++ {
			actor := fmt.Sprintf("actor-%d", u)
			views = append(views, store.AppView{
				ID: fmt.Sprintf("v-%d-%d", d, u), Project: "app", TS: day, ReceivedAt: day,
				ActorID: actor, UserID: fmt.Sprintf("u%d", u), GroupID: "org9",
				SessionID: fmt.Sprintf("s-%d-%d", d, u),
				Screen:    "/home", Platform: "ios", AppVersion: "2.4.1",
				OSVersion: "17.2", DeviceModel: "iPhone15,2", Locale: "en-US", Country: "DE",
			})
			hits = append(hits, store.WebHit{
				ID: fmt.Sprintf("h-%d-%d", d, u), Project: "app", TS: day, ReceivedAt: day,
				ActorID: actor, UserID: fmt.Sprintf("u%d", u), GroupID: "org9",
				Path: "/pricing", Country: "DE", Device: "desktop", Browser: "Chrome", OS: "macOS",
				UTMSource: "hn", UTMMedium: "referral", UTMCampaign: "launch",
			})
			events = append(events, store.ProductEvent{
				ID: fmt.Sprintf("e-%d-%d", d, u), Project: "app", EventName: "subscribed",
				TS: day, ReceivedAt: day, ActorID: actor, UserID: fmt.Sprintf("u%d", u),
				GroupID: "org9", Platform: "ios", AppVersion: "2.4.1",
				Attributes: map[string]string{"plan": "pro"},
			})
		}
	}
	must(st.WriteAppViews(ctx, views))
	must(st.WriteWebHits(ctx, hits))
	must(st.WriteProductEvents(ctx, events))
	must(st.UpsertIdentities(ctx, []store.Identity{
		{Project: "app", Kind: store.KindUser, ID: "u0", Name: "Ada Lovelace"},
		{Project: "app", Kind: store.KindGroup, ID: "org9", Name: "Acme Corp"},
	}))
	for d := 40; d >= 1; d-- {
		day := civil.Today(now.AddDate(0, 0, -d))
		must(st.UpsertActors(ctx, "app", day))
		must(st.AggregateRetentionDay(ctx, "app", day))
		must(st.AggregateIdentityDay(ctx, "app", day))
		must(st.AggregateAppDay(ctx, "app", day))
		must(st.AggregateWebDay(ctx, "app", day))
		must(st.AggregateProductDay(ctx, "app", day, store.ProductAggSettings{Enabled: true, TopN: 50,
			Attributes: map[string][]string{"*": {"plan"}}}))
	}
	fmt.Println("seeded")
}

func must(err error) {
	if err != nil {
		panic(err)
	}
}
