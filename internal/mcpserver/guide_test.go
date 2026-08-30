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
	if !strings.Contains(out, "https://collector.test/js/twillingate.js") {
		t.Errorf("snippet must point at the collector, got: %s", out)
	}
	if strings.Contains(out, "blog.example.com/js/twillingate.js") {
		t.Error("snippet points at the customer origin (the old bug)")
	}
	for _, want := range []string{"IDENTIFIED", "consent", "twillingate.reset"} {
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
	// no-attributes-declared pointer
	res = callTool(t, cs, "integration_guide", map[string]any{
		"project": "docs", "platform": "web"})
	if out := textOf(res); !strings.Contains(out, "No product attributes declared") {
		t.Errorf("no-attributes-declared guidance missing: %s", out)
	}
}

func TestDocsResourcesReadable(t *testing.T) {
	_, cs := newTestHost(t)
	res, err := cs.ReadResource(context.Background(),
		&mcp.ReadResourceParams{URI: "docs://twillingate"})
	if err != nil {
		t.Fatalf("docs://twillingate: %v", err)
	}
	body := res.Contents[0].Text
	// One document now covers what three resources used to: the event
	// model, the SDK and the wire format.
	for _, want := range []string{
		"$screen_view", "twillingate.track", "POST /api/events",
		"$host", "$path", "data-mask-url",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("docs://twillingate missing %q", want)
		}
	}
}

// The three resources it replaced must be gone, so a client cannot read a
// stale contract from a URI that still resolves.
func TestSupersededDocsResourcesRemoved(t *testing.T) {
	_, cs := newTestHost(t)
	for _, uri := range []string{"docs://events", "docs://js-sdk", "docs://ingest-api"} {
		if _, err := cs.ReadResource(context.Background(),
			&mcp.ReadResourceParams{URI: uri}); err == nil {
			t.Errorf("%s still resolves", uri)
		}
	}
}

func TestUpdateProjectSetsAttributes(t *testing.T) {
	h, cs := newTestHost(t)
	res := callTool(t, cs, "update_project", map[string]any{
		"alias": "blog", "attributes": []string{"plan", "tier"},
	})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	p := h.reg.Snapshot(context.Background()).Project("blog")
	if len(p.Attributes) != 2 || p.Attributes[0] != "plan" || p.Attributes[1] != "tier" {
		t.Fatalf("attributes not set: %+v", p.Attributes)
	}
	// omitted on a later update -> preserved
	if res := callTool(t, cs, "update_project", map[string]any{
		"alias": "blog", "name": "renamed"}); res.IsError {
		t.Fatalf("rename: %s", textOf(res))
	}
	p = h.reg.Snapshot(context.Background()).Project("blog")
	if len(p.Attributes) != 2 {
		t.Fatal("attributes lost on unrelated update")
	}
}
