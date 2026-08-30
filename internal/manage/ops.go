package manage

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Ops are the audited registry operations. Every mutation writes its
// audit row and bumps config_version in one transaction (store layer),
// then rebuilds the snapshot synchronously so in-process readers see it
// immediately (spec §3.3).
type Ops struct {
	Reg *Registry
	St  store.Store
}

func NewOps(reg *Registry, st store.Store) *Ops { return &Ops{Reg: reg, St: st} }

type ProjectSpec struct {
	Alias, Name, Identity string
	AllowedOrigins        []string
	Retention             *config.RetentionOverride
	Aggregation           *config.ProductAggregation
}

func (sp *ProjectSpec) validate() error {
	if sp.Alias == "" {
		return fmt.Errorf("project alias must not be empty")
	}
	if sp.Name == "" {
		sp.Name = sp.Alias
	}
	if sp.Identity == "" {
		sp.Identity = config.IdentityAnonymous
	}
	switch sp.Identity {
	case config.IdentityAnonymous, config.IdentityIdentified:
	default:
		return fmt.Errorf("identity must be %q or %q, got %q",
			config.IdentityAnonymous, config.IdentityIdentified, sp.Identity)
	}
	for _, o := range sp.AllowedOrigins {
		if o == "" {
			return fmt.Errorf("allowed_origins must not contain an empty origin")
		}
	}
	return nil
}

func (sp *ProjectSpec) row() (store.RegistryProject, error) {
	origins, err := json.Marshal(sp.AllowedOrigins)
	if sp.AllowedOrigins == nil {
		origins, err = []byte("[]"), nil
	}
	if err != nil {
		return store.RegistryProject{}, err
	}
	row := store.RegistryProject{Alias: sp.Alias, Name: sp.Name,
		Identity: sp.Identity, AllowedOrigins: string(origins)}
	if sp.Retention != nil {
		b, err := json.Marshal(sp.Retention)
		if err != nil {
			return row, err
		}
		row.Retention = string(b)
	}
	if sp.Aggregation != nil {
		b, err := json.Marshal(sp.Aggregation)
		if err != nil {
			return row, err
		}
		row.Aggregation = string(b)
	}
	return row, nil
}

func (o *Ops) CreateProject(ctx context.Context, actor string, spec ProjectSpec) (*Project, error) {
	if err := spec.validate(); err != nil {
		return nil, err
	}
	row, err := spec.row()
	if err != nil {
		return nil, err
	}
	if err := o.St.CreateProject(ctx, row, store.AuditEntry{
		Actor: actor, Action: "project.create", Subject: spec.Alias}); err != nil {
		return nil, err
	}
	if err := o.Reg.Reload(ctx); err != nil {
		return nil, err
	}
	return o.Reg.Snapshot(ctx).Project(spec.Alias), nil
}

func (o *Ops) UpdateProject(ctx context.Context, actor string, spec ProjectSpec) (*Project, error) {
	if err := spec.validate(); err != nil {
		return nil, err
	}
	row, err := spec.row()
	if err != nil {
		return nil, err
	}
	if err := o.St.UpdateProject(ctx, row, store.AuditEntry{
		Actor: actor, Action: "project.update", Subject: spec.Alias}); err != nil {
		return nil, err
	}
	if err := o.Reg.Reload(ctx); err != nil {
		return nil, err
	}
	return o.Reg.Snapshot(ctx).Project(spec.Alias), nil
}

func (o *Ops) ArchiveProject(ctx context.Context, actor, alias string) error {
	if err := o.St.SetProjectArchived(ctx, alias, true, store.AuditEntry{
		Actor: actor, Action: "project.archive", Subject: alias}); err != nil {
		return err
	}
	return o.Reg.Reload(ctx)
}

func (o *Ops) RestoreProject(ctx context.Context, actor, alias string) error {
	if err := o.St.SetProjectArchived(ctx, alias, false, store.AuditEntry{
		Actor: actor, Action: "project.restore", Subject: alias}); err != nil {
		return err
	}
	return o.Reg.Reload(ctx)
}

func (o *Ops) IssueIngestKey(ctx context.Context, actor, project, label string) (string, error) {
	s := o.Reg.Snapshot(ctx)
	p := s.Project(project)
	if p == nil {
		return "", fmt.Errorf("unknown project %q", project)
	}
	key, err := MintIngestKey()
	if err != nil {
		return "", err
	}
	if err := o.St.InsertIngestKey(ctx, store.RegistryKey{
		Key: key, Project: project, Label: label}, store.AuditEntry{
		Actor: actor, Action: "key.issue", Subject: project + "/" + label}); err != nil {
		return "", err
	}
	return key, o.Reg.Reload(ctx)
}

func (o *Ops) DisableIngestKey(ctx context.Context, actor, project, label string) error {
	if err := o.St.SetIngestKeyDisabled(ctx, project, label, true, store.AuditEntry{
		Actor: actor, Action: "key.disable", Subject: project + "/" + label}); err != nil {
		return err
	}
	return o.Reg.Reload(ctx)
}

func (o *Ops) EnableIngestKey(ctx context.Context, actor, project, label string) error {
	if err := o.St.SetIngestKeyDisabled(ctx, project, label, false, store.AuditEntry{
		Actor: actor, Action: "key.enable", Subject: project + "/" + label}); err != nil {
		return err
	}
	return o.Reg.Reload(ctx)
}

// DeleteProject is exposed by the CLI only — never as an MCP tool
// (spec §7.3: irreversible operations require a shell). Reclaims pages
// afterwards; the tx cannot (single connection).
func (o *Ops) DeleteProject(ctx context.Context, actor, alias string) error {
	if err := o.St.DeleteProjectData(ctx, alias, store.AuditEntry{
		Actor: actor, Action: "project.delete", Subject: alias}); err != nil {
		return err
	}
	if err := o.St.IncrementalVacuum(ctx); err != nil {
		return fmt.Errorf("delete succeeded but vacuum failed: %w", err)
	}
	return o.Reg.Reload(ctx)
}

// MintIngestKey mints "ak_" + 128 bits hex. Ingest keys are public by
// design (they ship in page source); 128 bits makes guessing infeasible.
func MintIngestKey() (string, error) { return mint("ak_", 16) }

// MintMCPToken mints "ar_" + 256 bits hex. Unlike ingest keys this is a
// true secret: it reads every project and authorizes management.
func MintMCPToken() (string, error) { return mint("ar_", 32) }

func mint(prefix string, n int) (string, error) {
	buf := make([]byte, n)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("entropy: %w", err)
	}
	return prefix + hex.EncodeToString(buf), nil
}

// SnippetPlaceholderBase is what Snippet falls back to when PUBLIC_URL is
// not configured. Callers that surface a snippet built on it should say so
// (the operator must substitute the real collector URL).
const SnippetPlaceholderBase = "https://analytics.example.com"

// Snippet renders the paste-ready embed tag returned by create_project,
// issue_ingest_key and `twillingate key issue`. base is the COLLECTOR's
// public URL (script.js and /api/events live there) — never the
// customer's site origin.
func Snippet(base, key, identity string) string {
	if base == "" {
		base = SnippetPlaceholderBase
	}
	return fmt.Sprintf(`<script defer src="%s/js/script.js"
        data-key=%q
        data-identity=%q></script>`, base, key, identity)
}
