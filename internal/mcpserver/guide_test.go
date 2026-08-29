package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestIntegrationGuideWebUsesCollectorURLAndIdentity(t *testing.T) {
	_, cs := newTestHost(t)
	// blog is identified and (fixture) has an allowed origin but no key yet
	res := callTool(t, cs, "integration_guide", map[string]any{
		"project": "blog", "platform": "web"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	if !strings.Contains(out, "https://collector.test/js/script.js") {
		t.Errorf("snippet must point at the collector, got: %s", out)
	}
	if strings.Contains(out, "blog.example.com/js/script.js") {
		t.Error("snippet points at the customer origin (the old bug)")
	}
	for _, want := range []string{"IDENTIFIED", "consent", "analytics.reset"} {
		if !strings.Contains(out, want) {
			t.Errorf("identified-mode guidance missing %q", want)
		}
	}
	if !strings.Contains(out, "NO active ingest key") {
		t.Errorf("keyless project must be told to issue a key: %s", out)
	}
}

func TestIntegrationGuideAnonymousAndPlatforms(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "integration_guide", map[string]any{
		"project": "docs", "platform": "server"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	for _, want := range []string{"https://collector.test/api/events", "X-Analytics-Key", "UUIDv7"} {
		if !strings.Contains(out, want) {
			t.Errorf("server guide missing %q", want)
		}
	}
	res = callTool(t, cs, "integration_guide", map[string]any{
		"project": "docs", "platform": "mobile"})
	if out := textOf(res); !strings.Contains(out, "$install_id") || !strings.Contains(out, "$screen_view") {
		t.Errorf("mobile guide missing app context: %s", out)
	}
	// bad platform lists the valid ones
	res = callTool(t, cs, "integration_guide", map[string]any{
		"project": "docs", "platform": "fax"})
	if !res.IsError || !strings.Contains(textOf(res), "web") {
		t.Errorf("bad platform: %v %s", res.IsError, textOf(res))
	}
	// aggregation-off pointer
	res = callTool(t, cs, "integration_guide", map[string]any{
		"project": "docs", "platform": "web"})
	if out := textOf(res); !strings.Contains(out, "product_aggregation") {
		t.Errorf("aggregation-off guidance missing: %s", out)
	}
}

func TestDocsResourcesReadable(t *testing.T) {
	_, cs := newTestHost(t)
	for uri, want := range map[string]string{
		"docs://events":     "$screen_view",
		"docs://js-sdk":     "analytics.track",
		"docs://ingest-api": "POST /api/events",
	} {
		res, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: uri})
		if err != nil {
			t.Fatalf("%s: %v", uri, err)
		}
		if !strings.Contains(res.Contents[0].Text, want) {
			t.Errorf("%s missing %q", uri, want)
		}
	}
}

func TestUpdateProjectSetsAggregation(t *testing.T) {
	h, cs := newTestHost(t)
	res := callTool(t, cs, "update_project", map[string]any{
		"alias": "blog",
		"product_aggregation": map[string]any{
			"enabled": true, "attributes": map[string]any{"*": []string{"plan"}}},
	})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	p := h.reg.Snapshot(context.Background()).Project("blog")
	if p.Aggregation == nil || !p.Aggregation.Enabled || p.Aggregation.TopN != 50 {
		t.Fatalf("aggregation not set (TopN default expected): %+v", p.Aggregation)
	}
	// omitted on a later update -> preserved
	if res := callTool(t, cs, "update_project", map[string]any{
		"alias": "blog", "name": "renamed"}); res.IsError {
		t.Fatalf("rename: %s", textOf(res))
	}
	p = h.reg.Snapshot(context.Background()).Project("blog")
	if p.Aggregation == nil || !p.Aggregation.Enabled {
		t.Fatal("aggregation lost on unrelated update")
	}
}
