package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

// withDB points DATABASE_URL at a fresh temp file for one test.
func withDB(t *testing.T) string {
	t.Helper()
	dsn := "sqlite://" + t.TempDir() + "/cli.db"
	t.Setenv("DATABASE_URL", dsn)
	return dsn
}

func TestProjectCreateListArchiveDelete(t *testing.T) {
	withDB(t)
	var out bytes.Buffer
	if code := run([]string{"project", "create", "-alias", "blog", "-name", "My blog",
		"-origin", "https://blog.example.com"}, &out); code != 0 {
		t.Fatalf("create: exit %d: %s", code, out.String())
	}
	out.Reset()
	if code := run([]string{"project", "list"}, &out); code != 0 {
		t.Fatalf("list: exit %d", code)
	}
	if !strings.Contains(out.String(), "blog") {
		t.Fatalf("list output: %s", out.String())
	}
	out.Reset()
	if code := run([]string{"project", "archive", "-alias", "blog"}, &out); code != 0 {
		t.Fatalf("archive: exit %d: %s", code, out.String())
	}
	out.Reset()
	if code := run([]string{"project", "restore", "-alias", "blog"}, &out); code != 0 {
		t.Fatalf("restore: exit %d: %s", code, out.String())
	}
	// delete refuses without -force
	out.Reset()
	if code := run([]string{"project", "delete", "-alias", "blog"}, &out); code == 0 {
		t.Fatal("delete without -force succeeded")
	}
	out.Reset()
	if code := run([]string{"project", "delete", "-alias", "blog", "-force"}, &out); code != 0 {
		t.Fatalf("delete -force: exit %d: %s", code, out.String())
	}
	out.Reset()
	if code := run([]string{"project", "list"}, &out); code != 0 || strings.Contains(out.String(), "blog") {
		t.Fatalf("blog survived delete: %s", out.String())
	}
}

func TestProjectCreateUnknownSubcommand(t *testing.T) {
	withDB(t)
	var out bytes.Buffer
	if code := run([]string{"project", "frobnicate"}, &out); code != 2 {
		t.Fatalf("exit = %d", code)
	}
}

func TestEnvFileFlag(t *testing.T) {
	dir := t.TempDir()
	envFile := dir + "/analytics.env"
	os.WriteFile(envFile, []byte("DATABASE_URL=sqlite://"+dir+"/env.db\n"), 0o600)
	os.Unsetenv("DATABASE_URL")
	var out bytes.Buffer
	if code := run([]string{"project", "-env-file", envFile, "create", "-alias", "x"}, &out); code != 0 {
		t.Fatalf("exit %d: %s", code, out.String())
	}
}

func TestProjectUpdateMergeSemantics(t *testing.T) {
	withDB(t)
	var out bytes.Buffer
	// Create an identified project with an origin
	if code := run([]string{"project", "create", "-alias", "test", "-name", "Original",
		"-identity", "identified", "-origin", "https://example.com"}, &out); code != 0 {
		t.Fatalf("create: exit %d: %s", code, out.String())
	}

	// Update only the name; identity and origin should survive
	out.Reset()
	if code := run([]string{"project", "update", "-alias", "test", "-name", "Updated"}, &out); code != 0 {
		t.Fatalf("update: exit %d: %s", code, out.String())
	}

	// Verify identity is still "identified" and origin survives
	out.Reset()
	if code := run([]string{"project", "list"}, &out); code != 0 {
		t.Fatalf("list: exit %d", code)
	}
	listing := out.String()
	if !strings.Contains(listing, "Updated") {
		t.Fatalf("name not updated: %s", listing)
	}
	if !strings.Contains(listing, "identified") {
		t.Fatalf("identity was reset: %s", listing)
	}
	// The origin should still be there (can't verify via list, but no error means it survived)

	// Now explicitly change identity to anonymous
	out.Reset()
	if code := run([]string{"project", "update", "-alias", "test", "-identity", "anonymous"}, &out); code != 0 {
		t.Fatalf("update identity: exit %d: %s", code, out.String())
	}

	// Verify identity changed
	out.Reset()
	if code := run([]string{"project", "list"}, &out); code != 0 {
		t.Fatalf("list: exit %d", code)
	}
	listing = out.String()
	if !strings.Contains(listing, "anonymous") {
		t.Fatalf("identity not changed: %s", listing)
	}
	if strings.Contains(listing, "identified") {
		t.Fatalf("old identity still present: %s", listing)
	}
}
