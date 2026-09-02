package manage

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestOpsRefusalsAreTyped(t *testing.T) {
	st := testStore(t)
	reg := New(st, defaults, discard())
	ctx := context.Background()
	if err := reg.Reload(ctx); err != nil {
		t.Fatal(err)
	}
	ops := NewOps(reg, st)
	if _, err := ops.CreateProject(ctx, "cli", ProjectSpec{Alias: "blog"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ops.IssueIngestKey(ctx, "cli", "blog", "web"); err != nil {
		t.Fatal(err)
	}

	invalid := map[string]error{
		"empty alias": func() error {
			_, err := ops.CreateProject(ctx, "cli", ProjectSpec{})
			return err
		}(),
		"bad identity": func() error {
			_, err := ops.CreateProject(ctx, "cli", ProjectSpec{Alias: "x", Identity: "pseudonymous"})
			return err
		}(),
		"empty origin": func() error {
			_, err := ops.CreateProject(ctx, "cli", ProjectSpec{Alias: "x", AllowedOrigins: []string{""}})
			return err
		}(),
		"alias charset": func() error {
			_, err := ops.CreateProject(ctx, "cli", ProjectSpec{Alias: "My App"})
			return err
		}(),
		"rename to bad alias": ops.RenameProject(ctx, "cli", "blog", "My_App"),
	}
	for name, err := range invalid {
		if !errors.Is(err, ErrInvalid) {
			t.Errorf("%s: err = %v, want errors.Is(err, ErrInvalid)", name, err)
		}
		// The sentinel leads so the CLI line reads as one sentence.
		if err != nil && !strings.HasPrefix(err.Error(), "invalid project spec: ") {
			t.Errorf("%s: err = %q, want the 'invalid project spec: ' prefix", name, err)
		}
	}

	notFound := map[string]error{
		"issue key for unknown project": func() error {
			_, err := ops.IssueIngestKey(ctx, "cli", "ghost", "web")
			return err
		}(),
		"update unknown project": func() error {
			_, err := ops.UpdateProject(ctx, "cli", ProjectSpec{Alias: "ghost"})
			return err
		}(),
		"archive unknown project": ops.ArchiveProject(ctx, "cli", "ghost"),
		"disable unknown key":     ops.DisableIngestKey(ctx, "cli", "blog", "ghost"),
		"delete unknown project":  ops.DeleteProject(ctx, "cli", "ghost"),
		"rename unknown project":  ops.RenameProject(ctx, "cli", "ghost", "journal"),
	}
	for name, err := range notFound {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: err = %v, want errors.Is(err, ErrNotFound)", name, err)
		}
	}

	conflict := map[string]error{
		"create taken alias": func() error {
			_, err := ops.CreateProject(ctx, "cli", ProjectSpec{Alias: "blog"})
			return err
		}(),
		"issue taken label": func() error {
			_, err := ops.IssueIngestKey(ctx, "cli", "blog", "web")
			return err
		}(),
	}
	for name, err := range conflict {
		if !errors.Is(err, ErrConflict) {
			t.Errorf("%s: err = %v, want errors.Is(err, ErrConflict)", name, err)
		}
	}
}
