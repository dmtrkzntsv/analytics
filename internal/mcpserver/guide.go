package mcpserver

import (
	"context"
	"fmt"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// integration_guide stitches the project's live registry state (keys,
// identity mode, aggregation, PUBLIC_URL) together with the right slice of
// the static docs, so a model can integrate a site or app in one read.

type guideIn struct {
	Project  string `json:"project" jsonschema:"project alias; call list_projects first"`
	Platform string `json:"platform" jsonschema:"one of: web (script tag), spa (single-page app), server (backend HTTP API), mobile (native app HTTP API)"`
}

type guideOut struct {
	Markdown string `json:"markdown"`
}

var guidePlatforms = map[string]bool{"web": true, "spa": true, "server": true, "mobile": true}

func (h *host) integrationGuide(ctx context.Context, _ *mcp.CallToolRequest, in guideIn) (*mcp.CallToolResult, guideOut, error) {
	if !guidePlatforms[in.Platform] {
		return nil, guideOut{}, fmt.Errorf("unknown platform %q; valid: mobile, server, spa, web", in.Platform)
	}
	s := h.reg.Snapshot(ctx)
	p := s.Project(in.Project)
	if p == nil {
		return nil, guideOut{}, h.unknownProjectErr(ctx, in.Project)
	}

	base := h.publicURL
	baseNote := ""
	if base == "" {
		base = manage.SnippetPlaceholderBase
		baseNote = "\n> PUBLIC_URL is not configured on the server, so the URLs below use the\n> placeholder " + manage.SnippetPlaceholderBase + ". Ask the operator for the collector's\n> real public URL and substitute it.\n"
	}

	// An active key, if the project has one.
	key := ""
	if _, ks, err := h.ops.St.LoadRegistry(ctx); err == nil {
		for _, k := range ks {
			if k.Project == p.Alias && !k.Disabled {
				key = k.Key
				break
			}
		}
	}
	keyLine := ""
	if key == "" {
		key = "ak_ISSUE_A_KEY_FIRST"
		keyLine = "\n> This project has NO active ingest key. Call issue_ingest_key first and\n> substitute the returned key below.\n"
	}

	var b strings.Builder
	fmt.Fprintf(&b, "# Integrating %q (%s, identity=%s)\n%s%s\n", p.Alias, in.Platform, p.Identity, baseNote, keyLine)

	switch in.Platform {
	case "web", "spa":
		fmt.Fprintf(&b, "Add to every page (before </head>):\n\n    %s\n\n",
			strings.ReplaceAll(manage.Snippet(h.publicURL, key, p.Identity), "\n", "\n    "))
		b.WriteString("Pageviews are automatic")
		if in.Platform == "spa" {
			b.WriteString(", including SPA route changes (pushState/popstate are hooked) — no router integration needed")
		}
		b.WriteString(". The snippet is silent on localhost, so test on a real or staged domain.\n\n")
		fmt.Fprintf(&b, "Product events from the page:\n\n    twillingate.track(\"signup\", { plan: \"pro\" });\n\n")
		if p.Identity == config.IdentityIdentified {
			b.WriteString("This project is IDENTIFIED: the snippet persists a visitor id in\nlocalStorage (consent-relevant, ePrivacy — same category as a cookie).\nGate the tag on consent, call twillingate.identify(userId, groupId) after\nlogin and twillingate.reset() on logout.\n\n")
		} else {
			b.WriteString("This project is ANONYMOUS: no cookies, no localStorage, no consent\nbanner needed for pageviews alone; $user_name is ignored and retention\ncurves are unavailable by design.\n\n")
		}
		origins := s.Project(p.Alias).AllowedOrigins
		if len(origins) == 0 {
			b.WriteString("WARNING: this project has NO allowed_origins — browser requests send an\nOrigin header and will be rejected. Add the site's origin with\nupdate_project before deploying the snippet.\n\n")
		} else {
			fmt.Fprintf(&b, "Allowed origins (the page must be served from one of these): %s\n\n", strings.Join(origins, ", "))
		}
	case "server":
		fmt.Fprintf(&b, "POST product events from your backend (no origin/CORS constraints;\nnative and server clients send no Origin header):\n\n"+
			"    curl -X POST %s/api/events \\\n      -H 'Content-Type: application/json' \\\n      -H 'X-Analytics-Key: %s' \\\n      -d '{\"events\":[{\"id\":\"<uuidv7>\",\"ts\":\"2026-08-28T10:00:00Z\",\n            \"name\":\"subscribed\",\"attributes\":{\"plan\":\"pro\",\"$user_id\":\"u_123\"}}]}'\n\n", base, key)
		b.WriteString("- Supply a UUIDv7 id per event: a batch retried after a timeout then\n  dedupes server-side. Omit it and a replay double-counts.\n- Batch up to 500 events per request (256 KiB body cap); rejection is\n  per event, never per batch.\n- Client ts is honoured and clamped to the raw-retention window.\n\n")
	case "mobile":
		fmt.Fprintf(&b, "Apps use the same HTTP API with app context as batch attributes:\n\n"+
			"    POST %s/api/events\n    X-Analytics-Key: %s\n\n"+
			"    {\"attributes\":{\"$install_id\":\"<stable-uuid-per-install>\",\n"+
			"                   \"$platform\":\"ios\",\"$app_version\":\"2.4.1\",\n"+
			"                   \"$os_version\":\"17.2\",\"$device_model\":\"iPhone15,2\"},\n"+
			"     \"events\":[{\"id\":\"<uuidv7>\",\"ts\":\"<event-time-utc>\",\n"+
			"                \"name\":\"$screen_view\",\"attributes\":{\"$screen\":\"/settings\"}}]}\n\n", base, key)
		b.WriteString("- $install_id: generate once per install, store locally, send on every\n  batch. Under anonymous identity it is salted and rotated daily.\n- Send $screen_view per screen; custom names for product events.\n- Queue offline, replay with original ts and stable UUIDv7 ids —\n  docs://ingest-api has a worked offline-queue design.\n\n")
	}

	agg := p.Aggregation != nil && p.Aggregation.Enabled
	if agg {
		fmt.Fprintf(&b, "Product aggregation is ENABLED (attribute breakdowns per: %v).\n", p.Aggregation.Attributes)
	} else {
		b.WriteString("Product aggregation is OFF: product events are counted per day, but\nattribute breakdowns need opting in — call update_project with e.g.\n{\"product_aggregation\":{\"enabled\":true,\"attributes\":{\"*\":[\"plan\"]}}}.\n")
	}
	b.WriteString("\nDeeper reference: docs://events (semantics), docs://js-sdk (snippet API),\ndocs://ingest-api (wire format, batching, retries, offline replay).\n")
	return nil, guideOut{Markdown: b.String()}, nil
}

func (h *host) registerGuide(s *mcp.Server) {
	mcp.AddTool(s, &mcp.Tool{Name: "integration_guide",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: "Tailored integration instructions for one project and platform (web, spa, server, mobile), with the project's real ingest key, collector URL, identity-mode guidance and event examples baked in. Call after create_project; read docs://events, docs://js-sdk and docs://ingest-api for depth."},
		h.integrationGuide)
}
