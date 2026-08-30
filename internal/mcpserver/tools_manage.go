package mcpserver

import (
	"context"
	"fmt"

	"github.com/dmtrkzntsv/twillingate/internal/manage"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Management tools (managed-config spec §5). One authorization tier: the
// same token that reads. The guardrails against prompt injection from
// attacker-writable analytics data (spec §6): every write is annotated so
// clients interpose the operator, nothing here is irreversible, and every
// operation lands in audit_log as actor 'mcp'.

type projectIn struct {
	Alias          string   `json:"alias" jsonschema:"project alias (immutable)"`
	Name           string   `json:"name,omitempty" jsonschema:"display name, defaults to alias"`
	Identity       string   `json:"identity,omitempty" jsonschema:"anonymous (default) or identified. identified stores user ids and names as given — a privacy-significant setting; see the GDPR docs"`
	AllowedOrigins []string `json:"allowed_origins,omitempty" jsonschema:"origins allowed to post events"`
	// Attributes declares which product-event attribute keys are broken
	// down (v_events_flat columns, agg_product_attrs rollups). Rollups
	// always run regardless of this list; nil (omitted) keeps the current
	// setting on update_project.
	Attributes []string `json:"attributes,omitempty" jsonschema:"attribute keys to break down and expose as flat-view columns"`
	// SkipKey rather than IssueKey: JSON booleans have no "unset", and
	// the zero value must give the default behaviour (issue a key).
	SkipKey bool `json:"skip_key,omitempty" jsonschema:"create_project only: set true to NOT issue a first ingest key"`
}

type projectToolOut struct {
	Alias    string `json:"alias"`
	Identity string `json:"identity"`
	Key      string `json:"key,omitempty"`
	Snippet  string `json:"snippet,omitempty"`
	Note     string `json:"note,omitempty"`
}

func (h *host) createProject(ctx context.Context, _ *mcp.CallToolRequest, in projectIn) (*mcp.CallToolResult, projectToolOut, error) {
	p, err := h.ops.CreateProject(ctx, "mcp", manage.ProjectSpec{
		Alias: in.Alias, Name: in.Name, Identity: in.Identity,
		AllowedOrigins: in.AllowedOrigins, Attributes: in.Attributes})
	if err != nil {
		return nil, projectToolOut{}, err
	}
	out := projectToolOut{Alias: in.Alias}
	if p != nil {
		out.Alias = p.Alias
		out.Identity = p.Identity
	}
	// by default the quickstart story is one round trip to paste-ready
	if !in.SkipKey {
		key, err := h.ops.IssueIngestKey(ctx, "mcp", in.Alias, "default")
		if err != nil {
			return nil, out, fmt.Errorf("project created but key issue failed: %w", err)
		}
		out.Key = key
		// p should never be nil here (we just created it), but a
		// concurrent delete between CreateProject and this point is not
		// impossible; the key is already issued, so degrade to no
		// snippet/origin enrichment rather than fail the call.
		if p != nil {
			out.Snippet = manage.Snippet(h.publicURL, key, p.Identity)
			if h.publicURL == "" {
				out.Note = "PUBLIC_URL is not configured; the snippet uses the placeholder " + manage.SnippetPlaceholderBase + " — ask the operator for the collector's public URL"
			}
		}
	}
	return nil, out, nil
}

// updateProject merges rather than replaces (binding ruling, Task 9):
// Ops.UpdateProject itself is full-replace, so the tool starts from the
// project's current values — including Retention and Attributes, which
// this tool has no fields for and must not silently clear — and overlays
// only what the caller actually provided. AllowedOrigins is the one field
// that is not field-merged: when the caller supplies a non-empty list it
// replaces the origin list wholesale (a caller cannot add one origin to
// an existing list without repeating the others). An empty list is
// treated the same as omitted (JSON has no way to distinguish "not
// provided" from "explicitly empty" once decoded into a nil-or-empty
// slice via omitempty), so there is no way to clear origins to empty
// through this tool — see the tool description.
func (h *host) updateProject(ctx context.Context, _ *mcp.CallToolRequest, in projectIn) (*mcp.CallToolResult, projectToolOut, error) {
	cur := h.reg.Snapshot(ctx).Project(in.Alias)
	if cur == nil {
		return nil, projectToolOut{}, fmt.Errorf("unknown project %q", in.Alias)
	}
	spec := manage.ProjectSpec{
		Alias:          in.Alias,
		Name:           cur.Name,
		Identity:       cur.Identity,
		AllowedOrigins: cur.AllowedOrigins,
		Retention:      cur.Retention,
		Attributes:     cur.Attributes,
	}
	if in.Attributes != nil {
		spec.Attributes = in.Attributes
	}
	if in.Name != "" {
		spec.Name = in.Name
	}
	if in.Identity != "" {
		spec.Identity = in.Identity
	}
	if len(in.AllowedOrigins) > 0 {
		spec.AllowedOrigins = in.AllowedOrigins
	}
	p, err := h.ops.UpdateProject(ctx, "mcp", spec)
	if err != nil {
		return nil, projectToolOut{}, err
	}
	return nil, projectToolOut{Alias: p.Alias, Identity: p.Identity}, nil
}

type aliasIn struct {
	Alias string `json:"alias" jsonschema:"project alias"`
}
type okOut struct {
	Status string `json:"status"`
}

func (h *host) archiveProject(ctx context.Context, _ *mcp.CallToolRequest, in aliasIn) (*mcp.CallToolResult, okOut, error) {
	if err := h.ops.ArchiveProject(ctx, "mcp", in.Alias); err != nil {
		return nil, okOut{}, err
	}
	return nil, okOut{Status: "archived; ingestion rejected, data kept, reversible with restore_project"}, nil
}

func (h *host) restoreProject(ctx context.Context, _ *mcp.CallToolRequest, in aliasIn) (*mcp.CallToolResult, okOut, error) {
	if err := h.ops.RestoreProject(ctx, "mcp", in.Alias); err != nil {
		return nil, okOut{}, err
	}
	return nil, okOut{Status: "restored"}, nil
}

type keyIn struct {
	Project string `json:"project" jsonschema:"project alias"`
	Label   string `json:"label" jsonschema:"key label, e.g. web, ios; unique per project"`
}
type keyOut struct {
	Key     string `json:"key,omitempty"`
	Snippet string `json:"snippet,omitempty"`
	Status  string `json:"status"`
	Note    string `json:"note,omitempty"`
}

func (h *host) issueKey(ctx context.Context, _ *mcp.CallToolRequest, in keyIn) (*mcp.CallToolResult, keyOut, error) {
	key, err := h.ops.IssueIngestKey(ctx, "mcp", in.Project, in.Label)
	if err != nil {
		return nil, keyOut{}, err
	}
	out := keyOut{Key: key, Status: "issued"}
	// Project should still exist (the key issue above would have failed
	// otherwise), but guard the lookup anyway: don't fail an
	// already-issued key just because enrichment can't find the project.
	if p := h.reg.Snapshot(ctx).Project(in.Project); p != nil {
		out.Snippet = manage.Snippet(h.publicURL, key, p.Identity)
		if h.publicURL == "" {
			out.Note = "PUBLIC_URL is not configured; the snippet uses the placeholder " + manage.SnippetPlaceholderBase + " — ask the operator for the collector's public URL"
		}
	}
	return nil, out, nil
}

func (h *host) disableKey(ctx context.Context, _ *mcp.CallToolRequest, in keyIn) (*mcp.CallToolResult, okOut, error) {
	if err := h.ops.DisableIngestKey(ctx, "mcp", in.Project, in.Label); err != nil {
		return nil, okOut{}, err
	}
	return nil, okOut{Status: "disabled; reversible with enable_ingest_key"}, nil
}

func (h *host) enableKey(ctx context.Context, _ *mcp.CallToolRequest, in keyIn) (*mcp.CallToolResult, okOut, error) {
	if err := h.ops.EnableIngestKey(ctx, "mcp", in.Project, in.Label); err != nil {
		return nil, okOut{}, err
	}
	return nil, okOut{Status: "enabled"}, nil
}

type listKeysIn struct {
	Project string `json:"project,omitempty" jsonschema:"filter to one project"`
}
type listKeysOut struct {
	Keys []keyRow `json:"keys"`
}
type keyRow struct {
	Project, Label, Key, State string
}

func (h *host) listKeys(ctx context.Context, _ *mcp.CallToolRequest, in listKeysIn) (*mcp.CallToolResult, listKeysOut, error) {
	_, ks, err := h.ops.St.LoadRegistry(ctx)
	if err != nil {
		return nil, listKeysOut{}, err
	}
	var out listKeysOut
	for _, k := range ks {
		if in.Project != "" && k.Project != in.Project {
			continue
		}
		state := "active"
		if k.Disabled {
			state = "disabled"
		}
		out.Keys = append(out.Keys, keyRow{k.Project, k.Label, k.Key, state})
	}
	return nil, out, nil
}

func (h *host) registerManage(s *mcp.Server) {
	no := false // DestructiveHint is *bool in the SDK; nothing here destroys
	write := &mcp.ToolAnnotations{DestructiveHint: &no}
	idem := &mcp.ToolAnnotations{DestructiveHint: &no, IdempotentHint: true}
	mcp.AddTool(s, &mcp.Tool{Name: "create_project", Annotations: write,
		Description: "Create a project and (by default) its first ingest key; returns a paste-ready embed snippet. Set skip_key to suppress the key."},
		h.createProject)
	mcp.AddTool(s, &mcp.Tool{Name: "update_project", Annotations: write,
		Description: "Update a project's name, identity mode, allowed origins and/or declared product-event attributes (breakdown keys for flat-view columns and attribute rollups). Fields you omit are left unchanged (this is a merge, not a replace) — except allowed_origins, which if provided non-empty replaces the whole list; origins cannot be cleared to empty via this tool (clear origins via `twillingate config import`, an explicit empty allowed_origins list in the document). Switching to identity=identified starts storing user ids and names as given — privacy-significant, say so to the user before doing it."},
		h.updateProject)
	mcp.AddTool(s, &mcp.Tool{Name: "archive_project", Annotations: idem,
		Description: "Archive a project: ingestion stops, data and dashboards keep working, fully reversible with restore_project. There is no delete over MCP — deletion requires the CLI."},
		h.archiveProject)
	mcp.AddTool(s, &mcp.Tool{Name: "restore_project", Annotations: idem,
		Description: "Restore an archived project."},
		h.restoreProject)
	mcp.AddTool(s, &mcp.Tool{Name: "issue_ingest_key", Annotations: write,
		Description: "Issue a new ingest key for a project. Ingest keys are public identifiers (they ship in page source); retirement is disable, not secrecy."},
		h.issueKey)
	mcp.AddTool(s, &mcp.Tool{Name: "disable_ingest_key", Annotations: idem,
		Description: "Disable an ingest key by project and label; events with it are rejected within a second. Reversible."},
		h.disableKey)
	mcp.AddTool(s, &mcp.Tool{Name: "enable_ingest_key", Annotations: idem,
		Description: "Re-enable a disabled ingest key."},
		h.enableKey)
	mcp.AddTool(s, &mcp.Tool{Name: "list_ingest_keys",
		Annotations: &mcp.ToolAnnotations{ReadOnlyHint: true},
		Description: "List ingest keys with their state, including disabled ones."},
		h.listKeys)
}
