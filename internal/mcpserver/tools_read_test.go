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
	for _, want := range []string{"blog", "identified", "docs", "anonymous", "https://blog.example.com"} {
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

func TestWebBreakdownUTMDimension(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "web_breakdown", map[string]any{
		"project": "blog", "from": "2026-08-01", "to": "2026-08-31",
		"dimension": "utm"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	for _, want := range []string{"newsletter", "email", "august"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q: %s", want, out)
		}
	}
}

func TestAppOverviewStitchesAggregatedAndLive(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "app_overview", map[string]any{
		"project": "blog", "from": "2026-08-01", "to": "2026-08-31"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	for _, day := range []string{"2026-08-20", "2026-08-21"} {
		if !strings.Contains(out, day) {
			t.Errorf("missing day %s in %s", day, out)
		}
	}
}

func TestAppBreakdownDimensions(t *testing.T) {
	_, cs := newTestHost(t)
	for dim, want := range map[string]string{
		"screens":   "/settings",
		"versions":  "2.4.1",
		"os":        "17.4",
		"devices":   "iPhone15,3",
		"countries": "US",
	} {
		res := callTool(t, cs, "app_breakdown", map[string]any{
			"project": "blog", "from": "2026-08-01", "to": "2026-08-31",
			"dimension": dim, "limit": 1})
		if res.IsError {
			t.Fatalf("dimension %s: error: %s", dim, textOf(res))
		}
		if out := textOf(res); !strings.Contains(out, want) {
			t.Errorf("dimension %s: missing %q in %s", dim, want, out)
		}
	}
	// invalid dimension is a tool error listing the valid ones
	res := callTool(t, cs, "app_breakdown", map[string]any{
		"project": "blog", "from": "2026-08-01", "to": "2026-08-31",
		"dimension": "sandwiches"})
	if !res.IsError || !strings.Contains(textOf(res), "screens") {
		t.Errorf("bad dimension: %v %s", res.IsError, textOf(res))
	}
}

func TestAppOverviewUnknownProject(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "app_overview", map[string]any{
		"project": "nope", "from": "2026-08-01", "to": "2026-08-31"})
	if !res.IsError {
		t.Fatal("unknown project did not error")
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

// A project spanning two hosts must be able to tell them apart -- the
// reason host is stored at all.
func TestWebBreakdownHostsDimension(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "web_breakdown", map[string]any{
		"project": "blog", "from": "2026-08-01", "to": "2026-08-31",
		"dimension": "hosts"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	for _, want := range []string{"blog.example.com", "shop.example.com"} {
		if !strings.Contains(out, want) {
			t.Errorf("hosts breakdown missing %q: %s", want, out)
		}
	}
}
