package manage

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"

	"github.com/dmtrkzntsv/twillingate/internal/config"
	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Export writes the full registry as one JSON document that round-trips
// through Import losslessly (spec §8). Deterministic: projects and keys
// come back alias-sorted from LoadRegistry.
func (o *Ops) Export(ctx context.Context, w io.Writer) error {
	ps, ks, err := o.St.LoadRegistry(ctx)
	if err != nil {
		return err
	}
	keysByProject := map[string][]exportKey{}
	for _, k := range ks {
		keysByProject[k.Project] = append(keysByProject[k.Project],
			exportKey{Key: k.Key, Label: k.Label, Disabled: k.Disabled})
	}
	doc := exportDoc{Version: 1}
	for _, rp := range ps {
		ep := exportProject{Alias: rp.Alias, Name: rp.Name, Identity: rp.Identity,
			Archived: rp.Archived, AllowedOrigins: []string{}, Attributes: []string{},
			IngestKeys: keysByProject[rp.Alias]}
		if ep.IngestKeys == nil {
			ep.IngestKeys = []exportKey{}
		}
		if rp.AllowedOrigins != "" {
			if err := json.Unmarshal([]byte(rp.AllowedOrigins), &ep.AllowedOrigins); err != nil {
				return fmt.Errorf("export %q: %w", rp.Alias, err)
			}
		}
		if rp.Retention != "" {
			ep.Retention = new(config.RetentionOverride)
			if err := json.Unmarshal([]byte(rp.Retention), ep.Retention); err != nil {
				return fmt.Errorf("export %q: %w", rp.Alias, err)
			}
		}
		if rp.Attributes != "" {
			if err := json.Unmarshal([]byte(rp.Attributes), &ep.Attributes); err != nil {
				return fmt.Errorf("export %q: %w", rp.Alias, err)
			}
		}
		doc.Projects = append(doc.Projects, ep)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(doc)
}

// Import applies a document declaratively: create what is missing, update
// what is listed, and NEVER archive, disable or delete what is absent
// (spec §8 — the old SyncProjects boot-archiving is not rebuilt here).
// It accepts both the v1 export document and the legacy projects.json
// bare-array format, detected by first non-space byte.
func (o *Ops) Import(ctx context.Context, actor string, r io.Reader) (ImportResult, error) {
	var res ImportResult
	br := bufio.NewReader(r)
	first, err := firstNonSpace(br)
	if err != nil {
		return res, fmt.Errorf("import: %w", err)
	}
	var projects []exportProject
	if first == '[' {
		legacy, err := config.ParseProjects(br)
		if err != nil {
			return res, fmt.Errorf("import legacy projects.json: %w", err)
		}
		for _, lp := range legacy {
			ep := exportProject{Alias: lp.Alias, Name: lp.Name, Identity: lp.Identity,
				AllowedOrigins: lp.AllowedOrigins, Retention: lp.Retention,
				Attributes: lp.DeclaredAttributes()}
			for _, k := range lp.IngestKeys {
				ep.IngestKeys = append(ep.IngestKeys, exportKey{Key: k.Key, Label: k.Label, Disabled: k.Disabled})
			}
			projects = append(projects, ep)
		}
	} else {
		var doc exportDoc
		dec := json.NewDecoder(br)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&doc); err != nil {
			return res, fmt.Errorf("import: %w", err)
		}
		if doc.Version != 1 {
			return res, fmt.Errorf("import: unsupported version %d", doc.Version)
		}
		projects = doc.Projects
	}

	// Validate every alias that would need to be CREATED before applying
	// anything. CreateProject enforces the ^[a-z0-9]+$ charset
	// (validateNew); UpdateProject exempts existing rows from it so a
	// legacy alias already in the registry stays editable. Checking this
	// upfront, across the whole document, keeps import all-or-nothing on
	// validation: without it, a document like [blog, my_app, shop] would
	// create blog, fail on my_app, and never reach shop, leaving the
	// registry half migrated (spec §8 promises a declarative document,
	// not a partial one). Reporting every bad alias in one error also
	// saves the operator from fixing them one failed attempt at a time.
	snap := o.Reg.Snapshot(ctx)
	seen := map[string]bool{}
	var badAliases []string
	for _, ep := range projects {
		if seen[ep.Alias] || snap.Project(ep.Alias) != nil || validAlias(ep.Alias) {
			continue
		}
		seen[ep.Alias] = true
		badAliases = append(badAliases, ep.Alias)
	}
	if len(badAliases) > 0 {
		return res, fmt.Errorf("import: alias(es) %s do not match ^[a-z0-9]+$ and no matching project exists to update; rename them first with `twillingate project rename`",
			strings.Join(badAliases, ", "))
	}

	for _, ep := range projects {
		spec := ProjectSpec{Alias: ep.Alias, Name: ep.Name, Identity: ep.Identity,
			AllowedOrigins: ep.AllowedOrigins, Retention: ep.Retention,
			Attributes: ep.declaredAttributes()}
		// snapshot re-read per iteration: CreateProject reloads it, and a
		// document may (erroneously) repeat an alias — the second pass
		// must see the first.
		if o.Reg.Snapshot(ctx).Project(ep.Alias) == nil {
			if _, err := o.CreateProject(ctx, actor, spec); err != nil {
				return res, fmt.Errorf("import %q: %w", ep.Alias, err)
			}
			res.Created++
		} else {
			if _, err := o.UpdateProject(ctx, actor, spec); err != nil {
				return res, fmt.Errorf("import %q: %w", ep.Alias, err)
			}
			res.Updated++
		}

		// Reconcile archived state for listed projects only. Unlisted
		// projects remain untouched (never-destructive rule).
		p := o.Reg.Snapshot(ctx).Project(ep.Alias)
		if p != nil {
			if ep.Archived && !p.Archived {
				if err := o.ArchiveProject(ctx, actor, ep.Alias); err != nil {
					return res, fmt.Errorf("import archive %q: %w", ep.Alias, err)
				}
			} else if !ep.Archived && p.Archived {
				if err := o.RestoreProject(ctx, actor, ep.Alias); err != nil {
					return res, fmt.Errorf("import restore %q: %w", ep.Alias, err)
				}
			}
		}

		existing := map[string]store.RegistryKey{}
		_, ks, err := o.St.LoadRegistry(ctx)
		if err != nil {
			return res, err
		}
		for _, k := range ks {
			existing[k.Key] = k
		}
		for _, ek := range ep.IngestKeys {
			if ek.Key == "" {
				continue
			}
			if cur, ok := existing[ek.Key]; ok {
				// A key listed in the document that already exists and
				// belongs to this listed project has its disabled state
				// reconciled to what the document says — explicit, so it
				// is not covered by the "unlisted stays untouched" rule.
				// Keys absent from the document, or present but under a
				// different project, are left alone.
				if cur.Project != ep.Alias || cur.Disabled == ek.Disabled {
					continue
				}
				action := "key.enable"
				if ek.Disabled {
					action = "key.disable"
				}
				if err := o.St.SetIngestKeyDisabled(ctx, ep.Alias, ek.Label, ek.Disabled,
					store.AuditEntry{Actor: actor, Action: action,
						Subject: ep.Alias + "/" + ek.Label}); err != nil {
					return res, fmt.Errorf("import key %q/%q: %w", ep.Alias, ek.Label, err)
				}
				continue
			}
			if err := o.St.InsertIngestKey(ctx, store.RegistryKey{
				Key: ek.Key, Project: ep.Alias, Label: ek.Label},
				store.AuditEntry{Actor: actor, Action: "key.import",
					Subject: ep.Alias + "/" + ek.Label}); err != nil {
				return res, fmt.Errorf("import key %q/%q: %w", ep.Alias, ek.Label, err)
			}
			if ek.Disabled {
				if err := o.St.SetIngestKeyDisabled(ctx, ep.Alias, ek.Label, true,
					store.AuditEntry{Actor: actor, Action: "key.disable",
						Subject: ep.Alias + "/" + ek.Label}); err != nil {
					return res, err
				}
			}
			res.KeysAdded++
		}
	}
	if err := o.Reg.Reload(ctx); err != nil {
		return res, err
	}
	o.rebuildFlatView(ctx)
	return res, nil
}

func firstNonSpace(br *bufio.Reader) (byte, error) {
	for {
		bs, err := br.Peek(1)
		if err != nil {
			return 0, err
		}
		switch bs[0] {
		case ' ', '\t', '\n', '\r':
			br.ReadByte()
		default:
			return bs[0], nil
		}
	}
}

type ImportResult struct{ Created, Updated, KeysAdded int }

type exportDoc struct {
	Version  int             `json:"version"`
	Projects []exportProject `json:"projects"`
}

type exportProject struct {
	Alias          string                    `json:"alias"`
	Name           string                    `json:"name"`
	Identity       string                    `json:"identity"`
	AllowedOrigins []string                  `json:"allowed_origins"`
	Retention      *config.RetentionOverride `json:"retention,omitempty"`
	Attributes     []string                  `json:"attributes,omitempty"`
	// ProductAggregation is the pre-2026-08 shape. Import still accepts it
	// (DisallowUnknownFields would otherwise reject every previously
	// exported document); Export never sets it, so omitempty drops it from
	// round-tripped documents.
	ProductAggregation *config.LegacyAggregation `json:"product_aggregation,omitempty"`
	Archived           bool                      `json:"archived,omitempty"`
	IngestKeys         []exportKey               `json:"ingest_keys"`
}

// declaredAttributes resolves Attributes and the legacy ProductAggregation
// block the same way config.Project.DeclaredAttributes does, preferring
// Attributes when both are present.
func (ep *exportProject) declaredAttributes() []string {
	if len(ep.Attributes) > 0 || ep.ProductAggregation == nil {
		return ep.Attributes
	}
	seen := map[string]bool{}
	var out []string
	for _, keys := range ep.ProductAggregation.Attributes {
		for _, k := range keys {
			if !seen[k] {
				seen[k] = true
				out = append(out, k)
			}
		}
	}
	sort.Strings(out)
	return out
}

type exportKey struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Disabled bool   `json:"disabled,omitempty"`
}
