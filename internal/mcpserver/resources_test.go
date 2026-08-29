package mcpserver

import (
	"context"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestSchemaResources(t *testing.T) {
	_, cs := newTestHost(t)
	res, err := cs.ReadResource(context.Background(),
		&mcp.ReadResourceParams{URI: "schema://views"})
	if err != nil {
		t.Fatal(err)
	}
	text := res.Contents[0].Text
	for _, want := range []string{
		"v_web_daily", "v_retention", "YYYY-MM-DD",
		"03:00 UTC", "identified", "includes yesterday",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("schema://views missing %q", want)
		}
	}
	pres, err := cs.ReadResource(context.Background(),
		&mcp.ReadResourceParams{URI: "schema://projects"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pres.Contents[0].Text, "blog") {
		t.Errorf("schema://projects missing project: %s", pres.Contents[0].Text)
	}
}
