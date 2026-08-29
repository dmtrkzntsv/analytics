package mcpserver

import (
	"strings"
	"testing"
)

func TestProductEvents(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "product_events", map[string]any{
		"project": "blog", "from": "2026-08-01", "to": "2026-08-31"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	if !strings.Contains(out, "signup") {
		t.Errorf("missing event: %s", out)
	}
	if !strings.Contains(out, "total_events") {
		t.Errorf("missing daily totals from v_product_totals: %s", out)
	}
}

// docs is seeded with product_aggregation left at its default (off); blog
// has it enabled (see seed_test.go) for TestProductAttributesReturnsRows.
func TestProductAttributesExplainsWhenOff(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "product_attributes", map[string]any{
		"project": "docs", "from": "2026-08-01", "to": "2026-08-31"})
	if !res.IsError {
		t.Fatal("aggregation-off project did not error")
	}
	if out := textOf(res); !strings.Contains(out, "product_aggregation") {
		t.Errorf("error must name the setting: %s", out)
	}
}

func TestProductAttributesReturnsRows(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "product_attributes", map[string]any{
		"project": "blog", "from": "2026-08-01", "to": "2026-08-31"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	if out := textOf(res); !strings.Contains(out, "plan") || !strings.Contains(out, "pro") {
		t.Errorf("missing attribute row: %s", out)
	}
	// event filter branch
	res = callTool(t, cs, "product_attributes", map[string]any{
		"project": "blog", "from": "2026-08-01", "to": "2026-08-31", "event": "signup"})
	if res.IsError {
		t.Fatalf("error with event filter: %s", textOf(res))
	}
	if out := textOf(res); !strings.Contains(out, "pro") {
		t.Errorf("event-filtered attributes missing row: %s", out)
	}
}

func TestRetentionReturnsCurveAndAggregatedThrough(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "retention", map[string]any{
		"project": "blog", "surface": "web", "from": "2026-07-01", "to": "2026-08-31"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	for _, want := range []string{"2026-08-01", "cohort_size", "aggregated_through"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q: %s", want, out)
		}
	}
}

func TestRetentionOnAnonymousProjectExplains(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "retention", map[string]any{
		"project": "docs", "surface": "web", "from": "2026-07-01", "to": "2026-08-31"})
	if !res.IsError {
		t.Fatal("anonymous project retention did not error")
	}
	if out := textOf(res); !strings.Contains(out, "identified") {
		t.Errorf("error must explain the identity requirement: %s", out)
	}
}

func TestIdentities(t *testing.T) {
	_, cs := newTestHost(t)
	res := callTool(t, cs, "identities", map[string]any{
		"project": "blog", "kind": "user", "from": "2026-08-01", "to": "2026-08-31"})
	if res.IsError {
		t.Fatalf("error: %s", textOf(res))
	}
	out := textOf(res)
	if !strings.Contains(out, "u1") || !strings.Contains(out, "Jane Doe") {
		t.Errorf("identities missing id or name: %s", out)
	}
}
