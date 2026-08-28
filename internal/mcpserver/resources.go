package mcpserver

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// schemaViews is the reference a model needs to write correct SQL. The
// three caveats at the top are the ones it cannot infer (endpoint spec §9).
const schemaViews = `# Query schema

Facts you cannot infer from the DDL:

1. day columns are TEXT 'YYYY-MM-DD' (UTC). Compare and BETWEEN as strings.
2. Every v_* view includes yesterday and today: each stitches aggregated
   history (agg_* tables) with a live half computed from raw rows.
3. EXCEPTION: v_retention has no live half. It refreshes at the 03:00 UTC
   daily pass; cohort days after that are ABSENT, not zero. It is populated
   only for projects with identity=identified (anonymous visitor ids rotate
   daily, so cohorts are undefined). Check list_projects for identity.

Views (all carry a 'project' column — always filter on it):

  v_web_daily(project, day, visitors, pageviews, sessions, bounces, duration_sec)
  v_web_pages(project, day, path, visitors, pageviews)
  v_web_referrers(project, day, source, visitors, pageviews)
  v_web_countries(project, day, country, visitors, pageviews)
  v_web_devices(project, day, device, visitors, pageviews)
  v_web_browsers(project, day, browser, visitors, pageviews)
  v_web_os(project, day, os, visitors, pageviews)
  v_web_utm(project, day, utm_source, utm_medium, utm_campaign, visitors, pageviews)
  v_product_daily(project, day, event_name, count, unique_users)
  v_product_totals(project, day, total_events, active_users)
  v_app_daily(project, day, actives, views, sessions, duration_sec)
  v_app_screens(project, day, screen, actives, views)
  v_app_versions(project, day, platform, app_version, actives, views)
  v_app_os(project, day, platform, os_version, actives, views)
  v_app_devices(project, day, device_model, actives, views)
  v_app_countries(project, day, country, actives, views)
  v_identity_daily(project, day, kind, id, actors, users, hits, views, events)  -- kind: 'user'|'group'
  v_retention(project, surface, cohort_day, day_offset, actors, cohort_size)    -- surface: 'web'|'app'
  agg_product_attrs(project, day, event_name, attr_key, attr_value, count, unique_users)
  identities(project, kind, id, name)  -- display names, joinable to v_identity_daily

Cost note: the views' live halves sessionize raw rows with window
functions; a WHERE on day may not prune that work. Narrow ranges and the
agg_* tables are cheaper. Queries are row-capped and time-limited.`

func (h *host) registerResources(s *mcp.Server) {
	s.AddResource(&mcp.Resource{
		URI: "schema://views", Name: "views",
		Description: "Queryable views, their columns, and the caveats needed to write correct SQL. Read before using the query tool.",
		MIMEType:    "text/plain",
	}, func(_ context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI: "schema://views", MIMEType: "text/plain", Text: schemaViews}}}, nil
	})
	s.AddResource(&mcp.Resource{
		URI: "schema://projects", Name: "projects",
		Description: "Current projects with identity modes and settings.",
		MIMEType:    "application/json",
	}, func(ctx context.Context, _ *mcp.ReadResourceRequest) (*mcp.ReadResourceResult, error) {
		type pj struct {
			Alias, Name, Identity string
			Archived              bool `json:",omitempty"`
		}
		var out []pj
		for _, p := range h.reg.Snapshot(ctx).Projects() {
			out = append(out, pj{p.Alias, p.Name, p.Identity, p.Archived})
		}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return nil, fmt.Errorf("schema://projects: %w", err)
		}
		return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{
			URI: "schema://projects", MIMEType: "application/json", Text: string(b)}}}, nil
	})
}
