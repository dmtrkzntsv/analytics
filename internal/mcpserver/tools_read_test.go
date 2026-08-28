package mcpserver

import (
	"strings"
	"testing"
)

func TestListProjects(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "list_projects", nil)
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	for _, want := range []string{"blog", "identified", "docs", "anonymous"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in %s", want, out)
		}
	}
}

func TestWebOverviewStitchesAggregatedAndLive(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "web_overview", map[string]any{
		"project": "blog", "from": "2026-08-01", "to": "2026-08-31"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	// two aggregated days plus the live day from the raw hit
	for _, day := range []string{"2026-08-20", "2026-08-21", "2026-08-26"} {
		if !strings.Contains(out, day) {
			t.Errorf("missing day %s in %s", day, out)
		}
	}
	if !strings.Contains(out, "bounce_rate") {
		t.Errorf("no derived bounce_rate in %s", out)
	}
}

func TestWebBreakdownDimensions(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "web_breakdown", map[string]any{
		"project": "blog", "from": "2026-08-01", "to": "2026-08-31",
		"dimension": "pages", "limit": 1})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	if !strings.Contains(out, "/post-1") {
		t.Errorf("top page missing: %s", out)
	}
	if strings.Contains(out, "/post-2") {
		t.Errorf("limit 1 not applied: %s", out)
	}
	// invalid dimension is a tool error listing the valid ones
	res = callTool(t, cs, "web_breakdown", map[string]any{
		"project": "blog", "from": "2026-08-01", "to": "2026-08-31",
		"dimension": "sandwiches"})
	if !res.IsError || !strings.Contains(textOf(res), "pages") {
		t.Errorf("bad dimension: %v %s", res.IsError, textOf(res))
	}
}

func TestUnknownProjectListsAliases(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "web_overview", map[string]any{
		"project": "nope", "from": "2026-08-01", "to": "2026-08-31"})
	if !res.IsError {
		t.Fatal("unknown project did not error")
	}
	if out := textOf(res); !strings.Contains(out, "blog") || !strings.Contains(out, "docs") {
		t.Errorf("error must list valid aliases: %s", out)
	}
}
