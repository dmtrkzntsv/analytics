package mcpserver

import (
	"context"
	"database/sql"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/manage"
	"github.com/dmitry/analytics/internal/store"
	_ "github.com/dmitry/analytics/internal/store/sqlite"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var testRetention = config.Retention{
	Web:     config.RetentionClass{RawDays: 7, AggregateDays: 365},
	Product: config.RetentionClass{RawDays: 30, AggregateDays: 365},
	App:     config.RetentionClass{RawDays: 30, AggregateDays: 365},
}

// newTestHost seeds two projects (blog: identified, docs: anonymous),
// two days of web aggregates and one raw hit, and returns a connected
// in-memory MCP client session against the assembled tool host.
func newTestHost(t *testing.T) (*host, *mcp.ClientSession) {
	t.Helper()
	path := t.TempDir() + "/mcp.db"
	st, err := store.Open("sqlite://" + path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	ctx := context.Background()
	if err := st.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	for alias, identity := range map[string]string{"blog": "identified", "docs": "anonymous"} {
		if err := st.CreateProject(ctx, store.RegistryProject{
			Alias: alias, Name: alias, Identity: identity, AllowedOrigins: "[]"},
			store.AuditEntry{Actor: "test", Action: "project.create", Subject: alias}); err != nil {
			t.Fatal(err)
		}
	}
	seed := func(q string, args ...any) {
		t.Helper()
		if _, err := rawExec(st, q, args...); err != nil {
			t.Fatal(err)
		}
	}
	seed(`INSERT INTO agg_web_daily (project, day, visitors, pageviews, sessions, bounces, duration_sec)
	      VALUES ('blog','2026-08-20',10,25,12,3,600), ('blog','2026-08-21',12,30,14,4,720)`)
	seed(`INSERT INTO agg_web_pages (project, day, path, visitors, pageviews)
	      VALUES ('blog','2026-08-20','/post-1',8,15), ('blog','2026-08-20','/post-2',4,10)`)
	seed(`INSERT INTO agg_web_utm (project, day, utm_source, utm_medium, utm_campaign, visitors, pageviews)
	      VALUES ('blog','2026-08-20','newsletter','email','august',6,9)`)
	seed(`INSERT INTO web_hits (id, project, ts, received_at, actor_id, path, referrer_source,
	      utm_source, utm_medium, utm_campaign, country, device, browser, os, user_id, group_id)
	      VALUES ('h1','blog','2026-08-26T10:00:00Z','2026-08-26T10:00:00Z','a1','/live','','','','','','','','','u1','')`)
	seed(`INSERT INTO agg_product_daily (project, day, event_name, count, unique_users)
	      VALUES ('blog','2026-08-20','signup',5,4)`)
	seed(`INSERT INTO agg_product_totals (project, day, total_events, active_users)
	      VALUES ('blog','2026-08-20',5,4)`)
	seed(`INSERT INTO agg_retention (project, surface, cohort_day, day_offset, actors)
	      VALUES ('blog','web','2026-08-01',0,10), ('blog','web','2026-08-01',7,4)`)
	seed(`INSERT INTO agg_identity_daily (project, day, kind, id, actors, users, hits, views, events)
	      VALUES ('blog','2026-08-20','user','u1',1,1,5,0,2)`)
	seed(`INSERT INTO identities (project, kind, id, name) VALUES ('blog','user','u1','Jane Doe')`)
	seed(`INSERT INTO agg_identity_daily (project, day, kind, id, actors, users, hits, views, events)
	      VALUES ('blog','2026-08-20','group','g1',1,0,5,0,2)`)
	seed(`INSERT INTO identities (project, kind, id, name) VALUES ('blog','group','g1','Acme Inc')`)
	// app data, for app_overview / app_breakdown
	seed(`INSERT INTO agg_app_daily (project, day, actives, views, sessions, duration_sec)
	      VALUES ('blog','2026-08-20',6,20,8,480), ('blog','2026-08-21',7,22,9,540)`)
	seed(`INSERT INTO agg_app_screens (project, day, screen, actives, views)
	      VALUES ('blog','2026-08-20','/settings',5,12), ('blog','2026-08-20','/home',3,8)`)
	seed(`INSERT INTO agg_app_versions (project, day, platform, app_version, actives, views)
	      VALUES ('blog','2026-08-20','ios','2.4.1',5,12)`)
	seed(`INSERT INTO agg_app_os (project, day, platform, os_version, actives, views)
	      VALUES ('blog','2026-08-20','ios','17.4',5,12)`)
	seed(`INSERT INTO agg_app_devices (project, day, device_model, actives, views)
	      VALUES ('blog','2026-08-20','iPhone15,3',5,12)`)
	seed(`INSERT INTO agg_app_countries (project, day, country, actives, views)
	      VALUES ('blog','2026-08-20','US',5,12)`)
	// product_aggregation, enabled for blog only: docs stays at the
	// default (aggregation off), which TestProductAttributesExplainsWhenOff
	// depends on.
	seed(`UPDATE projects SET product_aggregation =
	      '{"enabled":true,"attributes":{"*":["plan"]},"top_n":50}' WHERE alias='blog'`)
	seed(`INSERT INTO agg_product_attrs (project, day, event_name, attr_key, attr_value, count, unique_users)
	      VALUES ('blog','2026-08-20','signup','plan','pro',3,3)`)

	db, err := OpenReadDB(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	reg := manage.New(st, testRetention, logger)
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	h := &host{db: db, reg: reg, ops: manage.NewOps(reg, st),
		timeout: 5 * time.Second, maxRows: 1000, logger: logger}

	srv := mcp.NewServer(&mcp.Implementation{Name: "analytics", Version: "test"}, nil)
	h.register(srv)
	h.registerResources(srv)
	ct, stEnd := mcp.NewInMemoryTransports()
	if _, err := srv.Connect(ctx, stEnd, nil); err != nil {
		t.Fatal(err)
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "t", Version: "0"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return h, cs
}

// rawExec reaches the underlying *sql.DB of the sqlite store for seeding.
// The store interface deliberately has no Exec; ExecForTest is a
// test-only accessor added to internal/store/sqlite/sqlite.go.
func rawExec(st store.Store, q string, args ...any) (sql.Result, error) {
	return st.(interface {
		ExecForTest(string, ...any) (sql.Result, error)
	}).ExecForTest(q, args...)
}

// callTool invokes a tool over the session and fails the test on
// protocol errors; tool errors come back in the result.
func callTool(t *testing.T, cs *mcp.ClientSession, name string, args map[string]any) *mcp.CallToolResult {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("CallTool(%s): %v", name, err)
	}
	return res
}

func textOf(res *mcp.CallToolResult) string {
	var out string
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			out += tc.Text
		}
	}
	return out
}
