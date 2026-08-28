package manage

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"

	"github.com/dmitry/analytics/internal/config"
	"github.com/dmitry/analytics/internal/store"
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
			Archived: rp.Archived, AllowedOrigins: []string{},
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
		if rp.Aggregation != "" {
			ep.ProductAggregation = new(config.ProductAggregation)
			if err := json.Unmarshal([]byte(rp.Aggregation), ep.ProductAggregation); err != nil {
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
				ProductAggregation: lp.ProductAggregation}
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

	for _, ep := range projects {
		spec := ProjectSpec{Alias: ep.Alias, Name: ep.Name, Identity: ep.Identity,
			AllowedOrigins: ep.AllowedOrigins, Retention: ep.Retention,
			Aggregation: ep.ProductAggregation}
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
		existing := map[string]bool{}
		_, ks, err := o.St.LoadRegistry(ctx)
		if err != nil {
			return res, err
		}
		for _, k := range ks {
			existing[k.Key] = true
		}
		for _, ek := range ep.IngestKeys {
			if ek.Key == "" || existing[ek.Key] {
				continue // present keys are left as they are; explicit only
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
	return res, o.Reg.Reload(ctx)
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
	Alias              string                     `json:"alias"`
	Name               string                     `json:"name"`
	Identity           string                     `json:"identity"`
	AllowedOrigins     []string                   `json:"allowed_origins"`
	Retention          *config.RetentionOverride  `json:"retention,omitempty"`
	ProductAggregation *config.ProductAggregation `json:"product_aggregation,omitempty"`
	Archived           bool                       `json:"archived,omitempty"`
	IngestKeys         []exportKey                `json:"ingest_keys"`
}

type exportKey struct {
	Key      string `json:"key"`
	Label    string `json:"label"`
	Disabled bool   `json:"disabled,omitempty"`
}
