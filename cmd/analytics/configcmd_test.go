package main

import (
	"bytes"
	"os"
	"strings"
	"testing"
)

func TestConfigImportExportRoundTrip(t *testing.T) {
	withDB(t)
	dir := t.TempDir()
	legacy := dir + "/projects.json"
	os.WriteFile(legacy, []byte(`[{"alias":"blog","name":"My blog",
	  "ingest_keys":[{"key":"ak_legacy_cli","label":"web"}]}]`), 0o600)
	var out bytes.Buffer
	if code := run([]string{"config", "import", legacy}, &out); code != 0 {
		t.Fatalf("import: exit %d: %s", code, out.String())
	}
	if !strings.Contains(out.String(), "1 created") {
		t.Fatalf("import output: %s", out.String())
	}
	out.Reset()
	if code := run([]string{"config", "export"}, &out); code != 0 {
		t.Fatalf("export: exit %d", code)
	}
	if !strings.Contains(out.String(), `"alias": "blog"`) ||
		!strings.Contains(out.String(), "ak_legacy_cli") {
		t.Fatalf("export output: %s", out.String())
	}
}
