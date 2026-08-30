package main

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
)

func TestKeyIssueListDisableEnable(t *testing.T) {
	withDB(t)
	var out bytes.Buffer
	var code int
	code = run([]string{"project", "create", "-alias", "blog"}, &out)
	if code != 0 {
		t.Fatalf("create: %s", out.String())
	}
	out.Reset()
	code = run([]string{"key", "issue", "-project", "blog", "-label", "web"}, &out)
	if code != 0 {
		t.Fatalf("issue: exit %d: %s", code, out.String())
	}
	if !regexp.MustCompile(`ak_[0-9a-f]{32}`).MatchString(out.String()) {
		t.Fatalf("no key in output: %s", out.String())
	}
	if !strings.Contains(out.String(), "script.js") {
		t.Fatalf("no snippet in output: %s", out.String())
	}
	out.Reset()
	code = run([]string{"key", "list", "-project", "blog"}, &out)
	if code != 0 || !strings.Contains(out.String(), "web") {
		t.Fatalf("list: %s", out.String())
	}
	out.Reset()
	code = run([]string{"key", "disable", "-project", "blog", "-label", "web"}, &out)
	if code != 0 {
		t.Fatalf("disable: %s", out.String())
	}
	out.Reset()
	code = run([]string{"key", "list", "-project", "blog"}, &out)
	if !strings.Contains(out.String(), "disabled") {
		t.Fatalf("list after disable: %s", out.String())
	}
	out.Reset()
	code = run([]string{"key", "enable", "-project", "blog", "-label", "web"}, &out)
	if code != 0 {
		t.Fatalf("enable: %s", out.String())
	}
}

func TestKeygenMCP(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"keygen", "-mcp"}, &out); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !regexp.MustCompile(`MCP_TOKEN=ar_[0-9a-f]{64}`).MatchString(out.String()) {
		t.Fatalf("output: %s", out.String())
	}
	if strings.Contains(out.String(), "ingest_keys") {
		t.Fatal("-mcp must not print the JSON block")
	}
}

func TestKeygenDeprecationNotice(t *testing.T) {
	var out bytes.Buffer
	if code := run([]string{"keygen"}, &out); code != 0 {
		t.Fatalf("exit %d", code)
	}
	if !strings.Contains(out.String(), "key issue") {
		t.Fatalf("no deprecation pointer: %s", out.String())
	}
}
