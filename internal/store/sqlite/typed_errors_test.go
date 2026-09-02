package sqlite

import (
	"context"
	"errors"
	"testing"

	"github.com/dmtrkzntsv/twillingate/internal/store"
)

// Every registry write that rejects a call because of what the caller
// named — a missing alias or key, a taken alias or label — must be
// classifiable with errors.Is, so the MCP and CLI edges can map it
// without matching message text.
func TestRegistryWritesReturnTypedOutcomes(t *testing.T) {
	d := openRegistryDB(t)
	ctx := context.Background()
	audit := store.AuditEntry{Actor: "test", Action: "test", Subject: "test"}
	blog := store.RegistryProject{Alias: "blog", Name: "blog", Identity: "anonymous", AllowedOrigins: "[]", Attributes: "[]"}
	if err := d.CreateProject(ctx, blog, audit); err != nil {
		t.Fatal(err)
	}
	if err := d.InsertIngestKey(ctx, store.RegistryKey{Key: "ak_1", Project: "blog", Label: "web"}, audit); err != nil {
		t.Fatal(err)
	}

	notFound := map[string]error{
		"update unknown alias":   d.UpdateProject(ctx, store.RegistryProject{Alias: "ghost", AllowedOrigins: "[]", Attributes: "[]"}, audit),
		"archive unknown alias":  d.SetProjectArchived(ctx, "ghost", true, audit),
		"disable unknown key":    d.SetIngestKeyDisabled(ctx, "blog", "ghost", true, audit),
		"disable key of unknown": d.SetIngestKeyDisabled(ctx, "ghost", "web", true, audit),
		"delete unknown alias":   d.DeleteProjectData(ctx, "ghost", audit),
		"rename unknown alias":   d.RenameProject(ctx, "ghost", "journal", audit),
	}
	for name, err := range notFound {
		if !errors.Is(err, store.ErrNotFound) {
			t.Errorf("%s: err = %v, want errors.Is(err, store.ErrNotFound)", name, err)
		}
	}

	conflict := map[string]error{
		"create taken alias": d.CreateProject(ctx, blog, audit),
		"issue taken label":  d.InsertIngestKey(ctx, store.RegistryKey{Key: "ak_2", Project: "blog", Label: "web"}, audit),
		"rename onto taken alias": func() error {
			other := blog
			other.Alias = "journal"
			if err := d.CreateProject(ctx, other, audit); err != nil {
				t.Fatal(err)
			}
			return d.RenameProject(ctx, "blog", "journal", audit)
		}(),
	}
	for name, err := range conflict {
		if !errors.Is(err, store.ErrConflict) {
			t.Errorf("%s: err = %v, want errors.Is(err, store.ErrConflict)", name, err)
		}
	}

	// A rename to the alias the project already has is neither outcome:
	// nothing is missing and nothing collides. It keeps its own message
	// (see TestRenameProjectSameAliasIsRejectedWithItsOwnMessage).
	err := d.RenameProject(ctx, "blog", "blog", audit)
	if err == nil || errors.Is(err, store.ErrConflict) || errors.Is(err, store.ErrNotFound) {
		t.Errorf("rename to own alias: err = %v, want a plain error", err)
	}
}
