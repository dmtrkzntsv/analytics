package mcpserver

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/dmitry/analytics/internal/manage"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type host struct {
	db      *sql.DB
	reg     *manage.Registry
	ops     *manage.Ops
	timeout time.Duration
	maxRows int
	logger  *slog.Logger
}

var dayRe = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}$`)

type rangeIn struct {
	Project string `json:"project" jsonschema:"project alias; call list_projects first"`
	From    string `json:"from" jsonschema:"start day inclusive, YYYY-MM-DD"`
	To      string `json:"to" jsonschema:"end day inclusive, YYYY-MM-DD"`
}

// checkRange validates the shared inputs. Error text is written for a
// model to recover from (endpoint spec §10): an unknown project lists
// the valid aliases instead of just refusing.
func (h *host) checkRange(ctx context.Context, in rangeIn) error {
	if !dayRe.MatchString(in.From) || !dayRe.MatchString(in.To) {
		return fmt.Errorf("from and to must be YYYY-MM-DD, got %q and %q", in.From, in.To)
	}
	s := h.reg.Snapshot(ctx)
	if s.Project(in.Project) == nil {
		var aliases []string
		for _, p := range s.Projects() {
			aliases = append(aliases, p.Alias)
		}
		sort.Strings(aliases)
		return fmt.Errorf("unknown project %q; valid aliases: %s",
			in.Project, strings.Join(aliases, ", "))
	}
	return nil
}

type tableOut struct {
	Columns   []string   `json:"columns"`
	Rows      [][]string `json:"rows"`
	Truncated bool       `json:"truncated,omitempty"`
	Note      string     `json:"note,omitempty"`
}

func (h *host) table(ctx context.Context, q string, args ...any) (tableOut, error) {
	cols, rows, truncated, err := queryRows(ctx, h.db, h.timeout, h.maxRows, q, args...)
	if err != nil {
		if ctx.Err() != nil || strings.Contains(err.Error(), "context deadline") {
			return tableOut{}, fmt.Errorf("query exceeded %s; narrow the date range", h.timeout)
		}
		return tableOut{}, err
	}
	out := tableOut{Columns: cols, Rows: rows, Truncated: truncated}
	if truncated {
		out.Note = fmt.Sprintf("truncated to %d rows; results are PARTIAL — narrow the range or raise the limit", h.maxRows)
	}
	return out, nil
}

// ---- list_projects ----

type projectOut struct {
	Alias       string `json:"alias"`
	Name        string `json:"name"`
	Identity    string `json:"identity"`
	Archived    bool   `json:"archived,omitempty"`
	FirstWebDay string `json:"first_web_day,omitempty"`
	LastWebDay  string `json:"last_web_day,omitempty"`
	FirstAppDay string `json:"first_app_day,omitempty"`
	LastAppDay  string `json:"last_app_day,omitempty"`
}

type listProjectsOut struct {
	Projects []projectOut `json:"projects"`
}

func (h *host) listProjects(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listProjectsOut, error) {
	var out listProjectsOut
	for _, p := range h.reg.Snapshot(ctx).Projects() {
		po := projectOut{Alias: p.Alias, Name: p.Name, Identity: p.Identity, Archived: p.Archived}
		// coverage probe: cheap MIN/MAX over the stitch views
		for _, probe := range []struct {
			view  string
			first *string
			last  *string
		}{
			{"v_web_daily", &po.FirstWebDay, &po.LastWebDay},
			{"v_app_daily", &po.FirstAppDay, &po.LastAppDay},
		} {
			_, rows, _, err := queryRows(ctx, h.db, h.timeout, 1,
				`SELECT COALESCE(MIN(day),''), COALESCE(MAX(day),'') FROM `+probe.view+` WHERE project=?`, p.Alias)
			if err != nil {
				return nil, out, err
			}
			if len(rows) == 1 {
				*probe.first, *probe.last = rows[0][0], rows[0][1]
			}
		}
		out.Projects = append(out.Projects, po)
	}
	return nil, out, nil
}

// ---- web_overview ----

func (h *host) webOverview(ctx context.Context, _ *mcp.CallToolRequest, in rangeIn) (*mcp.CallToolResult, tableOut, error) {
	if err := h.checkRange(ctx, in); err != nil {
		return nil, tableOut{}, err
	}
	out, err := h.table(ctx, `SELECT day, visitors, pageviews, sessions, bounces, duration_sec,
		ROUND(CAST(bounces AS REAL)/MAX(sessions,1), 3) AS bounce_rate,
		CAST(duration_sec/MAX(sessions,1) AS INTEGER) AS avg_session_sec
		FROM v_web_daily WHERE project=? AND day BETWEEN ? AND ? ORDER BY day`,
		in.Project, in.From, in.To)
	return nil, out, err
}

// ---- web_breakdown ----

// webDimensions maps the dimension enum to view + value column. The
// enum in the input schema is generated from this map, so tool text and
// behaviour cannot drift.
var webDimensions = map[string]struct{ view, col string }{
	"pages":     {"v_web_pages", "path"},
	"referrers": {"v_web_referrers", "source"},
	"countries": {"v_web_countries", "country"},
	"devices":   {"v_web_devices", "device"},
	"browsers":  {"v_web_browsers", "browser"},
	"os":        {"v_web_os", "os"},
	"utm":       {"v_web_utm", "utm_source"},
}

// breakdownIn embeds rangeIn. Verified during implementation (a
// tools/list call against web_breakdown) that the SDK's jsonschema
// inference promotes embedded struct fields exactly like encoding/json:
// project/from/to appear as top-level properties, not nested under a
// "rangeIn" key. See task-16-report.md.
type breakdownIn struct {
	rangeIn
	Dimension string `json:"dimension" jsonschema:"one of: pages, referrers, countries, devices, browsers, os, utm"`
	Limit     int    `json:"limit,omitempty" jsonschema:"top-N rows, default 20"`
}

func dimensionKeys(m map[string]struct{ view, col string }) string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ", ")
}

func (h *host) webBreakdown(ctx context.Context, _ *mcp.CallToolRequest, in breakdownIn) (*mcp.CallToolResult, tableOut, error) {
	if err := h.checkRange(ctx, in.rangeIn); err != nil {
		return nil, tableOut{}, err
	}
	dim, ok := webDimensions[in.Dimension]
	if !ok {
		return nil, tableOut{}, fmt.Errorf("unknown dimension %q; valid: %s",
			in.Dimension, dimensionKeys(webDimensions))
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	if in.Dimension == "utm" {
		out, err := h.table(ctx, `SELECT utm_source, utm_medium, utm_campaign,
			SUM(visitors) AS visitors, SUM(pageviews) AS pageviews
			FROM v_web_utm WHERE project=? AND day BETWEEN ? AND ?
			GROUP BY utm_source, utm_medium, utm_campaign
			ORDER BY visitors DESC LIMIT ?`, in.Project, in.From, in.To, limit)
		return nil, out, err
	}
	out, err := h.table(ctx, `SELECT `+dim.col+` AS value,
		SUM(visitors) AS visitors, SUM(pageviews) AS pageviews
		FROM `+dim.view+` WHERE project=? AND day BETWEEN ? AND ?
		GROUP BY `+dim.col+` ORDER BY visitors DESC LIMIT ?`,
		in.Project, in.From, in.To, limit)
	return nil, out, err
}

// ---- app_overview / app_breakdown ----

var appDimensions = map[string]struct{ view, col string }{
	"screens":   {"v_app_screens", "screen"},
	"versions":  {"v_app_versions", "app_version"},
	"os":        {"v_app_os", "os_version"},
	"devices":   {"v_app_devices", "device_model"},
	"countries": {"v_app_countries", "country"},
}

func (h *host) appOverview(ctx context.Context, _ *mcp.CallToolRequest, in rangeIn) (*mcp.CallToolResult, tableOut, error) {
	if err := h.checkRange(ctx, in); err != nil {
		return nil, tableOut{}, err
	}
	out, err := h.table(ctx, `SELECT day, actives, views, sessions, duration_sec
		FROM v_app_daily WHERE project=? AND day BETWEEN ? AND ? ORDER BY day`,
		in.Project, in.From, in.To)
	return nil, out, err
}

func (h *host) appBreakdown(ctx context.Context, _ *mcp.CallToolRequest, in breakdownIn) (*mcp.CallToolResult, tableOut, error) {
	if err := h.checkRange(ctx, in.rangeIn); err != nil {
		return nil, tableOut{}, err
	}
	dim, ok := appDimensions[in.Dimension]
	if !ok {
		return nil, tableOut{}, fmt.Errorf("unknown dimension %q; valid: %s",
			in.Dimension, dimensionKeys(appDimensions))
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 20
	}
	out, err := h.table(ctx, `SELECT `+dim.col+` AS value,
		SUM(actives) AS actives, SUM(views) AS views
		FROM `+dim.view+` WHERE project=? AND day BETWEEN ? AND ?
		GROUP BY `+dim.col+` ORDER BY actives DESC LIMIT ?`,
		in.Project, in.From, in.To, limit)
	return nil, out, err
}

// register adds every tool to the server. Read tools carry
// ReadOnlyHint; management tools (tools_manage.go) do not.
func (h *host) register(s *mcp.Server) {
	ro := &mcp.ToolAnnotations{ReadOnlyHint: true}
	mcp.AddTool(s, &mcp.Tool{Name: "list_projects", Annotations: ro,
		Description: "List projects with identity mode and data coverage. Call this first: every other tool takes a project alias from here. Projects with identity=identified support retention and identities; anonymous ones cannot (their visitor ids rotate daily)."},
		h.listProjects)
	mcp.AddTool(s, &mcp.Tool{Name: "web_overview", Annotations: ro,
		Description: "Daily web traffic for one project: visitors, pageviews, sessions, bounces, duration, with derived bounce_rate and avg_session_sec. Data includes yesterday and today (live)."},
		h.webOverview)
	mcp.AddTool(s, &mcp.Tool{Name: "web_breakdown", Annotations: ro,
		Description: "Top pages, referrers, countries, devices, browsers, os or utm for one project over a date range."},
		h.webBreakdown)
	mcp.AddTool(s, &mcp.Tool{Name: "app_overview", Annotations: ro,
		Description: "Daily app usage for one project: active users, screen views, sessions, duration."},
		h.appOverview)
	mcp.AddTool(s, &mcp.Tool{Name: "app_breakdown", Annotations: ro,
		Description: "Top screens, versions, os, devices or countries for one project's app traffic."},
		h.appBreakdown)
	h.registerProduct(s)
	h.registerQuery(s)
	h.registerManage(s)
}

func (h *host) registerQuery(s *mcp.Server)  {} // Task 18 moves this to query.go
func (h *host) registerManage(s *mcp.Server) {} // Task 19 moves this to tools_manage.go
